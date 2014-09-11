/*
	This file collects types and functions usable from both labelmap and labels64 datatypes.
*/

package labels64

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sync"

	"github.com/janelia-flyem/dvid/datastore"
	"github.com/janelia-flyem/dvid/datatype/voxels"
	"github.com/janelia-flyem/dvid/dvid"
	"github.com/janelia-flyem/dvid/server"
	"github.com/janelia-flyem/dvid/storage"
)

var (
	zeroLabelBytes = make([]byte, 8, 8)
)

// ZeroBytes returns a slice of bytes that represents the zero label.
func ZeroBytes() []byte {
	return zeroLabelBytes
}

// Store the KeyLabelSpatialMap keys (index = b + s) with slice of runs for value.
func StoreKeyLabelSpatialMap(versionID dvid.VersionID, data dvid.Data, batcher storage.KeyValueBatcher,
	zyxBytes []byte, labelRLEs map[uint64]dvid.RLEs) {

	ctx := datastore.NewVersionedContext(data, versionID)
	batch := batcher.NewBatch(ctx)
	defer func() {
		if err := batch.Commit(); err != nil {
			dvid.Infof("Error on batch PUT of KeyLabelSpatialMap: %s\n", err.Error())
		}
	}()
	bsIndex := make([]byte, 1+8+dvid.IndexZYXSize)
	bsIndex[0] = byte(voxels.KeyLabelSpatialMap)
	copy(bsIndex[9:9+dvid.IndexZYXSize], zyxBytes)
	for b, rles := range labelRLEs {
		binary.BigEndian.PutUint64(bsIndex[1:9], b)
		key := dvid.IndexBytes(bsIndex)
		runsBytes, err := rles.MarshalBinary()
		if err != nil {
			dvid.Infof("Error encoding KeyLabelSpatialMap keys for mapped label %d: %s\n", b, err.Error())
			return
		}
		batch.Put(key, runsBytes)
	}
}

// ComputeSurface computes and stores a label surface.
// Runs asynchronously and assumes that sparse volumes per spatial indices are ordered
// by mapped label, i.e., we will get all data for body N before body N+1.  Exits when
// receives a nil in channel.
func ComputeSurface(ctx storage.Context, data *Data, ch chan *storage.Chunk, wg *sync.WaitGroup) {
	defer func() {
		wg.Done()
		server.HandlerToken <- 1
	}()

	// Sequentially process all the sparse volume data for each label coming down channel.
	var curVol dvid.SparseVol
	var curLabel uint64
	notFirst := false
	for {
		chunk := <-ch
		if chunk == nil {
			if notFirst {
				if err := data.computeAndSaveSurface(ctx, &curVol); err != nil {
					dvid.Infof("Error on computing surface and normals: %s\n", err.Error())
					return
				}
			}
			return
		}
		label := chunk.ChunkOp.Op.(uint64)
		if label != curLabel || label == 0 {
			if notFirst {
				if err := data.computeAndSaveSurface(ctx, &curVol); err != nil {
					dvid.Infof("Error on computing surface and normals: %s\n", err.Error())
					return
				}
			}
			curVol.Clear()
			curVol.SetLabel(label)
		}

		if err := curVol.AddRLEs(chunk.V); err != nil {
			dvid.Infof("Error adding RLE for label %d: %s\n", label, err.Error())
			return
		}
		curLabel = label
		notFirst = true
	}
}

func (d *Data) computeAndSaveSurface(ctx storage.Context, vol *dvid.SparseVol) error {
	surfaceBytes, err := vol.SurfaceSerialization(d.BlockSize().Value(2), d.Resolution.VoxelSize)
	if err != nil {
		return err
	}
	store, err := storage.BigDataStore()
	if err != nil {
		return err
	}

	// Surface blobs are always stored using gzip with best compression, trading off time
	// during the store for speed during interactive GETs.
	compression, _ := dvid.NewCompression(dvid.Gzip, dvid.DefaultCompression)
	serialization, err := dvid.SerializeData(surfaceBytes, compression, dvid.NoChecksum)
	if err != nil {
		return fmt.Errorf("Unable to serialize data in surface computation: %s\n", err.Error())
	}
	key := voxels.NewLabelSurfaceIndex(vol.Label())
	return store.Put(ctx, key, serialization)
}

// GetSurface returns a gzipped byte array with # voxels and float32 arrays for vertices and
// normals.
func GetSurface(ctx storage.Context, label uint64) ([]byte, bool, error) {
	bigdata, err := storage.BigDataStore()
	if err != nil {
		return nil, false, fmt.Errorf("Cannot get datastore that handles big data: %s\n", err.Error())
	}

	// Retrieve the precomputed surface or that it's not available.
	data, err := bigdata.Get(ctx, voxels.NewLabelSurfaceIndex(label))
	if err != nil {
		return nil, false, fmt.Errorf("Error in retrieving surface for label %d: %s", label, err.Error())
	}
	if data == nil {
		return []byte{}, false, nil
	}
	uncompress := false
	surfaceBytes, _, err := dvid.DeserializeData(data, uncompress)
	if err != nil {
		return nil, false, fmt.Errorf("Unable to deserialize surface for label %d: %s\n", label, err.Error())
	}
	return surfaceBytes, true, nil
}

type sparseOp struct {
	versionID dvid.VersionID
	encoding  []byte
	numBlocks uint32
	numRuns   uint32
	//numVoxels int32
}

// GetSparseVol returns an encoded sparse volume given a label.  The encoding has the
// following format where integers are little endian:
//    byte     Payload descriptor:
//               Bit 0 (LSB) - 8-bit grayscale
//               Bit 1 - 16-bit grayscale
//               Bit 2 - 16-bit normal
//               ...
//    uint8    Number of dimensions
//    uint8    Dimension of run (typically 0 = X)
//    byte     Reserved (to be used later)
//    uint32    # Voxels
//    uint32    # Spans
//    Repeating unit of:
//        int32   Coordinate of run start (dimension 0)
//        int32   Coordinate of run start (dimension 1)
//        int32   Coordinate of run start (dimension 2)
//		  ...
//        int32   Length of run
//        bytes   Optional payload dependent on first byte descriptor
//
func GetSparseVol(ctx storage.Context, label uint64) ([]byte, error) {
	bigdata, err := storage.SmallDataStore()
	if err != nil {
		return nil, fmt.Errorf("Cannot get datastore that handles big data: %s\n", err.Error())
	}

	// Create the sparse volume header
	buf := new(bytes.Buffer)
	buf.WriteByte(dvid.EncodingBinary)
	binary.Write(buf, binary.LittleEndian, uint8(3))
	binary.Write(buf, binary.LittleEndian, byte(0))
	buf.WriteByte(byte(0))
	binary.Write(buf, binary.LittleEndian, uint32(0)) // Placeholder for # voxels
	binary.Write(buf, binary.LittleEndian, uint32(0)) // Placeholder for # spans

	// Get the start/end indices for this body's KeyLabelSpatialMap (b + s) keys.
	begIndex := voxels.NewLabelSpatialMapIndex(label, &dvid.MinIndexZYX)
	endIndex := voxels.NewLabelSpatialMapIndex(label, &dvid.MaxIndexZYX)

	// Process all the b+s keys and their values, which contain RLE runs for that label.
	wg := new(sync.WaitGroup)
	op := &sparseOp{versionID: ctx.VersionID(), encoding: buf.Bytes()}
	err = bigdata.ProcessRange(ctx, begIndex, endIndex, &storage.ChunkOp{op, wg}, func(chunk *storage.Chunk) {
		op := chunk.Op.(*sparseOp)
		op.numBlocks++
		op.encoding = append(op.encoding, chunk.V...)
		op.numRuns += uint32(len(chunk.V) / 16)
		chunk.Wg.Done()
	})
	if err != nil {
		return nil, err
	}
	wg.Wait()

	binary.LittleEndian.PutUint32(op.encoding[8:12], op.numRuns)

	dvid.Debugf("[%s] label %d: found %d blocks, %d runs\n", ctx, label, op.numBlocks, op.numRuns)
	return op.encoding, nil
}

// Runs asynchronously and assumes that sparse volumes per spatial indices are ordered
// by mapped label, i.e., we will get all data for body N before body N+1.  Exits when
// receives a nil in channel.
func ComputeSizes(ctx storage.Context, sizeCh chan *storage.Chunk, smalldata storage.SmallDataStorer,
	wg *sync.WaitGroup) {

	// Make sure our small data store can do batching.
	batcher, ok := smalldata.(storage.KeyValueBatcher)
	if !ok {
		dvid.Criticalf("Unable to compute label sizes: small data store can't do batching!")
		return
	}

	const BATCH_SIZE = 10000
	batch := batcher.NewBatch(ctx)

	defer func() {
		wg.Done()
	}()

	// Sequentially process all the sparse volume data for each label
	var curLabel, curSize uint64
	putsInBatch := 0
	notFirst := false
	for {
		chunk := <-sizeCh
		if chunk == nil {
			key := voxels.NewLabelSizesIndex(curSize, curLabel)
			batch.Put(key, dvid.EmptyValue())
			if err := batch.Commit(); err != nil {
				dvid.Infof("Error on batch PUT of label sizes: %s\n", err.Error())
			}
			return
		}
		label := chunk.ChunkOp.Op.(uint64)

		// Compute the size
		var rles dvid.RLEs
		if err := rles.UnmarshalBinary(chunk.V); err != nil {
			dvid.Infof("Error deserializing RLEs: %s\n", err.Error())
			return
		}
		numVoxels, _ := rles.Stats()

		// If we are a new label, store size
		if notFirst && label != curLabel {
			key := voxels.NewLabelSizesIndex(curSize, curLabel)
			curSize = 0
			batch.Put(key, dvid.EmptyValue())
			putsInBatch++
			if putsInBatch%BATCH_SIZE == 0 {
				if err := batch.Commit(); err != nil {
					dvid.Infof("Error on batch PUT of label sizes: %s\n", err.Error())
					return
				}
				batch = batcher.NewBatch(ctx)
			}
		}
		curLabel = label
		curSize += uint64(numVoxels)
		notFirst = true
	}
}

// GetSizeRange returns a JSON list of mapped labels that have volumes within the given range.
// If maxSize is 0, all mapped labels are returned >= minSize.
func GetSizeRange(data dvid.Data, versionID dvid.VersionID, minSize, maxSize uint64) (string, error) {
	store, err := storage.SmallDataStore()
	if err != nil {
		return "{}", err
	}
	ctx := datastore.NewVersionedContext(data, versionID)

	// Get the start/end keys for the size range.
	firstKey := voxels.NewLabelSizesIndex(minSize, 0)
	var upperBound uint64
	if maxSize != 0 {
		upperBound = maxSize
	} else {
		upperBound = math.MaxUint64
	}
	lastKey := voxels.NewLabelSizesIndex(upperBound, math.MaxUint64)

	// Grab all keys for this range in one sequential read.
	keys, err := store.KeysInRange(ctx, firstKey, lastKey)
	if err != nil {
		return "{}", err
	}

	// Convert them to a JSON compatible structure.
	labels := make([]uint64, len(keys))
	for i, key := range keys {
		labels[i], err = voxels.LabelFromLabelSizesKey(key)
		if err != nil {
			return "{}", err
		}
	}
	m, err := json.Marshal(labels)
	if err != nil {
		return "{}", nil
	}
	return string(m), nil
}