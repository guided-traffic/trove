package ocistub

import (
	"encoding/json"
	"strings"
)

// manifestDoc is the part of a manifest or an index that describing one needs.
type manifestDoc struct {
	MediaType    string       `json:"mediaType"`
	ArtifactType string       `json:"artifactType"`
	Config       *Descriptor  `json:"config"`
	Layers       []Descriptor `json:"layers"`
	Manifests    []Descriptor `json:"manifests"`
	Subject      *Descriptor  `json:"subject"`
}

func (p *Platform) String() string {
	if p == nil {
		return ""
	}
	s := p.OS + "/" + p.Architecture
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}

// describe reads a stored manifest into an ImageInfo, recursively for an index.
func (c *Corpus) describe(name, digest string) ImageInfo {
	o := c.manifests[digest]
	info := ImageInfo{Name: name, Digest: digest, MediaType: o.mediaType, Size: int64(len(o.data))}
	var doc manifestDoc
	if err := json.Unmarshal(o.data, &doc); err != nil {
		return info
	}
	info.ArtifactType = doc.ArtifactType
	if doc.Subject != nil {
		info.Subject = doc.Subject.Digest
	}
	if doc.Config != nil {
		info.Config = &BlobInfo{MediaType: doc.Config.MediaType, Digest: doc.Config.Digest, Size: doc.Config.Size}
	}
	for _, l := range doc.Layers {
		info.Layers = append(info.Layers, BlobInfo{MediaType: l.MediaType, Digest: l.Digest, Size: l.Size})
	}
	for _, d := range doc.Manifests {
		child := c.describe(name, d.Digest)
		child.Platform = d.Platform.String()
		if d.Annotations[AnnotationReferenceType] == ReferenceTypeAttestation {
			child.AttestationFor = d.Annotations[AnnotationReferenceDigest]
		}
		info.Manifests = append(info.Manifests, child)
	}
	return info
}

// Blobs lists every blob digest the image behind a digest names, recursively: the configuration and the layers of every manifest
// below an index, in order, without repeats.
func (i ImageInfo) Blobs() []string {
	var out []string
	seen := map[string]bool{}
	var walk func(ImageInfo)
	walk = func(n ImageInfo) {
		add := func(d string) {
			if !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
		if n.Config != nil {
			add(n.Config.Digest)
		}
		for _, l := range n.Layers {
			add(l.Digest)
		}
		for _, m := range n.Manifests {
			walk(m)
		}
	}
	walk(i)
	return out
}

// ForPlatform returns the platform manifest of an index for "os/arch[/variant]", and false when there is none; for a single image
// it returns the image itself.
func (i ImageInfo) ForPlatform(platform string) (ImageInfo, bool) {
	if len(i.Manifests) == 0 {
		return i, true
	}
	for _, m := range i.Manifests {
		if m.AttestationFor == "" && (m.Platform == platform || strings.HasPrefix(m.Platform, platform+"/")) {
			return m, true
		}
	}
	return ImageInfo{}, false
}
