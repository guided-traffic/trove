// Package rows holds the values that cross the data port of the OCI format (Q-ARCH-193, D-1, R-OCI-007): the format, an edge
// package that runs no SQL, passes them to the store, and internal/formatdata/oci, a domain package that may not import the
// format, receives them. Both import this leaf, which holds plain data and imports nothing of the module.
//
// The store persists what the format decided: the manifest parser of the format reads a manifest, and the rows arrive as the
// facts a referrers answer and a package screen need and the references the manifest names.
package rows

import (
	"encoding/json"
	"time"
)

// Reference kinds of oci_references.kind.
const (
	// KindManifest is a reference from an index to a manifest, or from a manifest to the manifest it points at.
	KindManifest = "manifest"
	// KindBlob is a reference from an image manifest to its config or a layer.
	KindBlob = "blob"
)

// MaxAnnotationBytes bounds the annotations a manifest row keeps; the rest is cut and the cut is recorded (R-OCI-007).
const MaxAnnotationBytes = 64 << 10

// Reference is one edge of the graph: a manifest names a manifest or a blob of the same image name.
type Reference struct {
	Target [32]byte
	// Kind is KindManifest or KindBlob.
	Kind string
	// Platform is "os/arch[/variant]" of the index entry that names the child manifest the edge points at; "" for every other
	// edge. The edge is where the fact lives, because only the index knows the platform of its child.
	Platform string
}

// Manifest is the row of a manifest of an image name (oci_manifests).
type Manifest struct {
	RepoID int64
	// Name is the image name below the repository.
	Name   string
	Digest [32]byte
	// MediaType is the manifest's media type without parameters.
	MediaType    string
	ArtifactType string
	// Subject is the digest the manifest names as its subject; nil for none.
	Subject *[32]byte
	// Size is the size of the manifest's own bytes.
	Size int64
	// ContentSize is the summed size of the config and the layers of an image manifest.
	ContentSize int64
	// Annotations is the JSON object of the annotations a referrers answer needs, at most MaxAnnotationBytes.
	Annotations json.RawMessage
	// AnnotationsCut says the annotations were cut at MaxAnnotationBytes, which a referrers answer reports.
	AnnotationsCut bool
	CreatedAt      time.Time
}

// ManifestInput is a manifest to record in the transaction that writes its item, with the references it names.
type ManifestInput struct {
	Manifest
	References []Reference
}

// TagPage is a page of a tags list (R-OCI-015): the tags in the tag order of the format, after the cursor.
type TagPage struct {
	Tags []string
	// Next is the last tag of a full page and "" on the last page.
	Next string
}

// Blob is a blob item of an image name that no manifest references (R-OCI-019).
type Blob struct {
	RepoID int64
	Name   string
	Digest [32]byte
	// Path is the canonical item path, "<name>/_blobs/sha256:<hex>".
	Path string
	// Since is when the blob item was created.
	Since time.Time
}

// ManifestDeleted is the deletion of a manifest to record in the transaction that removes its item (R-OCI-018, R-OCI-033): the
// audit event oci.manifest.deleted with its journal entry.
type ManifestDeleted struct {
	RepoID int64
	// RepoKey is "<projectKey>/<repoKey>".
	RepoKey string
	Name    string
	Digest  [32]byte
	// Deleted is the number of items the cascade of the deletion removed besides the manifest itself (its tags, its referrers and
	// the children only it named).
	Deleted int
	// Actor is the principal id behind the removal, "" for a removal that no principal asked for (a retention run, a job).
	Actor string
}

// Tombstone is the mark of a manifest the journal replay withheld after a restore (R-OCI-033, Q-OCI-33): the manifest keeps its items
// and its bytes, and is neither served nor accepted again until an administrator reinstates it.
type Tombstone struct {
	RepoID int64
	Name   string
	Digest [32]byte
	// WithheldAt is when the replay withheld the manifest.
	WithheldAt time.Time
	// DeletedBy is the principal id behind the journaled deletion, "" for none (a retention run).
	DeletedBy string
}
