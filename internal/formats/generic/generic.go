// Package generic is the generic format (R-GENERIC-001 to R-GENERIC-008):
// repositories that store files of any kind at free-form paths. It has no
// metadata documents, no package names and no client protocol beyond PUT, GET
// and DELETE on /files/. Only the local class is implemented (Q-ROAD-1); the
// remote and virtual classes arrive with R-GENERIC-009 and R-GENERIC-010.
package generic

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// ID is the format name.
const ID formats.ID = "generic"

// Layout is the name of the flat layout (R-GENERIC-003).
const Layout = "generic"

// Format implements formats.Format for the generic format.
type Format struct{}

var _ formats.Format = Format{}

// Descriptor returns the catalog entry of R-GENERIC-001. Generic repositories are
// exposed on an endpoint through paths alone (R-EP-014): they have no package
// names, versions or tags to narrow.
func (Format) Descriptor() formats.Descriptor {
	notFound := 1800
	return formats.Descriptor{
		Facts: catalog.Facts{
			ID:                        ID,
			DisplayName:               "Generic",
			Classes:                   []formats.Class{formats.Local},
			Layout:                    Layout,
			ProtocolBasePath:          "/files/{key}/",
			DefaultNotFoundTTLSeconds: &notFound,
			PatternSubject:            formats.SubjectPath,
		},
		Exposure: catalog.Exposure{Exposable: true},
	}
}

// Describe classifies a /files/ request (R-GENERIC-003). Every stored file is
// immutable content, so no path is metadata: reads of a file are artifacts, a
// folder or the root is the directory index trove renders, and any method that
// changes state is a write.
func (Format) Describe(method, path string, folder bool) formats.RequestDescriptor {
	d := formats.RequestDescriptor{Path: path}
	switch method {
	case http.MethodGet, http.MethodHead:
		if folder || path == "" {
			d.Kind = formats.KindOther
		} else {
			d.Kind = formats.KindArtifact
		}
	default:
		d.Kind = formats.KindWrite
	}
	return d
}
