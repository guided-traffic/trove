package registry

import (
	"errors"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formats"
)

// Documents is how the registry decides on an image name: on the path of its package document, which the format places in the
// image (Q-AUTHZ-250, Q-OCI-140). A coordinate that names a package and no path fails every path set, so a decision about a name
// alone (a catalog row, the grant of a token request) is asked on this path.
type Documents struct {
	// Path returns the canonical path of the package document of an image name (the format's PackageDocumentPath); ok is
	// false for a string that is no name.
	Path func(name string) (string, bool)
	// Classify completes a path with the package and version the format gives it (the format's Classify).
	Classify authz.Classifier
}

// NewDocuments takes the package document and the classification from the format.
func NewDocuments(f formats.Format) Documents {
	return Documents{
		Path: func(name string) (string, bool) {
			p, ok := f.PackageDocumentPath(name)
			return p.Path, ok
		},
		Classify: func(path string) (string, string) {
			info := f.Classify(formats.Path{Path: path})
			return info.Package, info.Version
		},
	}
}

func (d Documents) check() error {
	if d.Path == nil || d.Classify == nil {
		return errors.New("registry: the registry needs the package document path and the classification of the format")
	}
	return nil
}
