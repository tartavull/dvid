package dvid

import (
	"bufio"
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	Kilo = 1 << 10
	Mega = 1 << 20
	Giga = 1 << 30
	Tera = 1 << 40
)

type ModeFlag uint

const (
	Normal ModeFlag = iota
	Debug
	Benchmark
)

// DefaultJPEGQuality is the quality of images returned if requesting JPEG images
// and an explicit Quality amount is omitted.
const DefaultJPEGQuality = 80

// Mode is a global variable set to the run modes of this DVID process.
var Mode ModeFlag

// The global, unexported error logger for DVID.
var errorLogger *log.Logger

// Log prints a message via log.Print() depending on the Mode of DVID.
func Log(mode ModeFlag, p ...interface{}) {
	if mode == Mode {
		if len(p) == 0 {
			log.Println("No message")
		} else {
			log.Printf(p[0].(string), p[1:]...)
		}
	}
}

// Fmt prints a message via fmt.Print() depending on the Mode of DVID.
func Fmt(mode ModeFlag, p ...interface{}) {
	if mode == Mode {
		if len(p) == 0 {
			fmt.Println("No message")
		} else {
			fmt.Printf(p[0].(string), p[1:]...)
		}
	}
}

// Error prints a message to the Error Log File, which is useful to mark potential issues
// but not ones that should crash the DVID server.  Basically, you should opt to crash
// the server if a mistake can propagate and corrupt data.  If not, you can use this function.
// Note that Error logging to a file only occurs if DVID is running as a server, otherwise
// this function will print to stdout.
func Error(p ...interface{}) {
	if len(p) == 0 {
		log.Println("No message")
	} else {
		log.Printf(p[0].(string), p[1:]...)
	}
	if errorLogger != nil {
		if len(p) == 0 {
			errorLogger.Println("No message")
		} else {
			errorLogger.Printf(p[0].(string), p[1:]...)
		}
	}
}

// SetErrorLoggingFile creates an error logger to the given file for this DVID process.
func SetErrorLoggingFile(out io.Writer) {
	errorLogger = log.New(out, "", log.Ldate|log.Ltime|log.Llongfile)
	errorLogger.Println("Starting error logging for DVID")
}

// Wait for WaitGroup then print message including time for operation.
// The last arguments are fmt.Printf arguments and should not include the
// newline since one is added in this function.
func WaitToComplete(wg *sync.WaitGroup, startTime time.Time, p ...interface{}) {
	wg.Wait()
	ElapsedTime(Normal, startTime, p...)
}

// ElapsedTime prints the time elapsed from the start time with Printf arguments afterwards.
// Example:  ElapsedTime(dvid.Debug, startTime, "Time since launch of %s", funcName)
func ElapsedTime(modes ModeFlag, startTime time.Time, p ...interface{}) {
	var args []interface{}
	if len(p) == 0 {
		args = append(args, "%s\n")
	} else {
		format := p[0].(string) + ": %s\n"
		args = append(args, format)
		args = append(args, p[1:]...)
	}
	args = append(args, time.Since(startTime))
	Fmt(modes, args...)
}

// Prompt returns a string entered by the user after displaying message.
// If the user just hits ENTER (or enters an empty string), then the
// defaultValue is returned.
func Prompt(message, defaultValue string) string {
	fmt.Print(message + " [" + defaultValue + "]: ")
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultValue
	}
	return line
}

/***** Image Utilities ******/

// ImageData returns the underlying pixel data for an image or an error if
// the image doesn't have the requisite []uint8 pixel data.
func ImageData(img image.Image) (data []uint8, stride int32, err error) {
	switch typedImg := img.(type) {
	case *image.Alpha:
		data = typedImg.Pix
		stride = int32(typedImg.Stride)
	case *image.Alpha16:
		data = typedImg.Pix
		stride = int32(typedImg.Stride)
	case *image.Gray:
		data = typedImg.Pix
		stride = int32(typedImg.Stride)
		Fmt(Debug, "Gray Image: stride %d, Pix len %d, bounds %s\n", 
			stride, len(data), img.Bounds())
	case *image.Gray16:
		data = typedImg.Pix
		stride = int32(typedImg.Stride)
		Fmt(Debug, "Gray16 Image: stride %d, Pix len %d, bounds %s\n", 
			stride, len(data), img.Bounds())
	case *image.NRGBA:
		data = typedImg.Pix
		stride = int32(typedImg.Stride)
	case *image.NRGBA64:
		data = typedImg.Pix
		stride = int32(typedImg.Stride)
	case *image.Paletted:
		data = typedImg.Pix
		stride = int32(typedImg.Stride)
		Fmt(Debug, "Paletted Image: stride %d, Pix len %d, bounds %s\n", 
			stride, len(data), img.Bounds())
	case *image.RGBA:
		data = typedImg.Pix
		stride = int32(typedImg.Stride)
		Fmt(Debug, "RGBA Image: stride %d, Pix len %d, bounds %s\n", 
			stride, len(data), img.Bounds())
	case *image.RGBA64:
		data = typedImg.Pix
		stride = int32(typedImg.Stride)
		Fmt(Debug, "RGBA64 Image: stride %d, Pix len %d, bounds %s\n", 
			stride, len(data), img.Bounds())
	default:
		err = fmt.Errorf("Illegal image type called ImageData(): %T", typedImg)
	}
	return
}

// WriteImageHttp writes an image to a HTTP response writer using a format and optional
// compression strength specified in a string, e.g., "png", "jpg:80".
func WriteImageHttp(w http.ResponseWriter, img image.Image, formatStr string) (err error) {
	format := strings.Split(formatStr, ":")
	var compression int = DefaultJPEGQuality
	if len(format) > 1 {
		compression, err = strconv.Atoi(format[1])
		if err != nil {
			return err
		}
	}
	switch format[0] {
	case "", "png":
		w.Header().Set("Content-type", "image/png")
		png.Encode(w, img)
	case "jpg", "jpeg":
		w.Header().Set("Content-type", "image/jpeg")
		jpeg.Encode(w, img, &jpeg.Options{Quality: compression})
	default:
		err = fmt.Errorf("Illegal image format requested: %s", format[0])
	}
	return
}

// ImageFromFile returns an image and its format name given a file name.
func ImageFromFile(filename string) (img image.Image, format string, err error) {
	var file *os.File
	file, err = os.Open(filename)
	if err != nil {
		err = fmt.Errorf("Unable to open image (%s).  Is this visible to server process?",
			filename)
		return
	}
	img, format, err = image.Decode(file)
	if err != nil {
		return
	}
	err = file.Close()
	return
}

// ImageFromPost returns and image and its format name given a key to a POST request.
// The image should be the first file in a POSTed form.
func ImageFromPost(r *http.Request, key string) (img image.Image, format string, err error) {
	f, _, err := r.FormFile(key)
	if err != nil {
		return
	}
	defer f.Close()

	var buf bytes.Buffer
	io.Copy(&buf, f)
	img, format, err = image.Decode(&buf)
	return
}

func PrintNonZero(message string, value []byte) {
	nonzero := 0
	for _, b := range value {
		if b != 0 {
			nonzero++
		}
	}
	fmt.Printf("%s> non-zero voxels: %d of %d bytes\n", message, nonzero, len(value))
}
