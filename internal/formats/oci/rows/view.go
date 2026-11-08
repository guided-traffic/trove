package rows

// TagView is a tag with the facts of the manifest it points at, for the package view (R-OCI-031).
type TagView struct {
	// VersionID is the id of the version row of the tag.
	VersionID int64
	Tag       string
	Digest    [32]byte
	MediaType string
	// ArtifactType is the manifest's own artifactType, else its config's media type; "" for none.
	ArtifactType string
	// Size is the size of the manifest document; ContentSize the size of the image: the summed size of the config and layers of an image manifest, the sum of its
	// children's ContentSize for an index (Q-OCI-130).
	Size        int64
	ContentSize int64
	// Children are what an index names, in platform order; empty for an image manifest.
	Children []Child
}

// Child is a manifest an index names: the platform of the edge and the child's own facts.
type Child struct {
	// Platform is "os/arch[/variant]", "" when the index names none for the child.
	Platform    string
	Digest      [32]byte
	MediaType   string
	Size        int64
	ContentSize int64
}
