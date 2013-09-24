/*
	Package grayscale8 tailors the voxels data type for 8-bit grayscale images.  It simply
	wraps the voxels package, setting ChannelsInterleaved (1) and BytesPerVoxel(1).
*/
package grayscale8

import (
	"github.com/janelia-flyem/dvid/datastore"
	"github.com/janelia-flyem/dvid/datatype/voxels"
)

const Version = "0.6"

const RepoUrl = "github.com/janelia-flyem/dvid/datatype/grayscale8"

// Grayscale8 Datatype simply embeds voxels.Datatype to create a unique type
// (grayscale8.Datatype) with grayscale functions.
type Datatype struct {
	voxels.Datatype
}

// DefaultBlockMax specifies the default size for each block of this data type.
var DefaultBlockMax voxels.Point3d = voxels.Point3d{16, 16, 16}

func init() {
	grayscale := voxels.NewDatatype()
	grayscale.DatatypeID = datastore.MakeDatatypeID("grayscale8", RepoUrl, Version)
	grayscale.ChannelsInterleaved = 1
	grayscale.BytesPerVoxel = 1

	// Data types must be registered with the datastore to be used.
	datastore.RegisterDatatype(grayscale)
}
