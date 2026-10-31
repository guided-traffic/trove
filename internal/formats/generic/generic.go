// Package generic is the generic format (R-GENERIC-001 to R-GENERIC-008):
// repositories that store files of any kind at free-form paths. It has no
// metadata documents, no package names and no client protocol beyond PUT, GET
// and DELETE on /files/. Only the local class is implemented (Q-ROAD-1); the
// remote and virtual classes arrive with R-GENERIC-009 and R-GENERIC-010.
//
// The format implements the contract without routes: its protocol is /files/,
// which the generic layer (internal/files, internal/api/rawfiles) serves for
// every repository, so the format dispatcher registers no /api/generic/ prefix
// (Q-GENERIC-7). What the contract still asks of it is the classification of a
// path, which gives a /files/ request its coordinate (R-EP-014), its exposure
// facts, and the setup snippets and the guide of its one client, curl
// (R-GENERIC-016).
package generic

import (
	"context"
	"errors"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// ID is the format name.
const ID formats.ID = "generic"

// Layout is the name of the flat layout (R-GENERIC-003).
const Layout = "generic"

// Format implements formats.Format for the generic format.
type Format struct{ formats.Base }

var _ formats.Format = Format{}

// Descriptor returns the catalog entry of R-GENERIC-001.
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
		Clients:         []formats.Client{{ID: ClientCurl, DisplayName: "curl"}},
		ContractVersion: formats.ContractVersion,
	}
}

// Routes is empty: /files/ is the protocol of the format, served by the generic
// layer for every repository (Q-GENERIC-7).
func (Format) Routes() []formats.Route { return nil }

// Match finds no route.
func (Format) Match(method, escapedPath string) (*formats.Route, formats.Params, error) {
	return formats.MatchRoutes(nil, method, escapedPath)
}

// Classify describes a /files/ path (R-GENERIC-003). Every stored file is
// immutable content, so no path is metadata; a folder or the root is the
// directory index trove renders. The format has no package names, so the
// identity is empty.
func (Format) Classify(p formats.Path) formats.PathInfo {
	if p.Folder || p.Path == "" {
		return formats.PathInfo{Kind: formats.PathComputed}
	}
	return formats.PathInfo{Kind: formats.Content}
}

// PackageDocumentPath: the format has no package names.
func (Format) PackageDocumentPath(string) (formats.Path, bool) { return formats.Path{}, false }

// Exposure returns the paths-only adapter: generic repositories are exposed on an
// endpoint through paths alone (R-EP-014), as they have no package names,
// versions or tags to narrow.
func (Format) Exposure() formats.ExposureAdapter { return pathsOnly{} }

// pathsOnly is the exposure adapter of a format without dimensions.
type pathsOnly struct{}

var errNoVersions = errors.New("the generic format has no versions")

func (pathsOnly) ValidateRange(string) error             { return errNoVersions }
func (pathsOnly) Prerelease(string) (bool, bool)         { return false, false }
func (pathsOnly) InRange(string, string) bool            { return false }
func (pathsOnly) Dimensions() formats.ExposureDimensions { return formats.ExposureDimensions{} }
func (pathsOnly) FilterDocument(_ context.Context, _ formats.Path, doc []byte, _ formats.ExposureView) ([]byte, error) {
	return doc, nil
}
