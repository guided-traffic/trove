package formats

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// ExposureDimensions say what exposure rules may narrow for the format
// (R-EP-012, R-EP-018).
type ExposureDimensions struct{ Packages, Versions, Tags bool }

// ExposureView answers what an endpoint exposes, to filter a document.
type ExposureView interface {
	Package(name string) bool
	Version(name, version string) bool
	Tag(name, tag, version string) bool
}

// ExposureAdapter is a format's part of endpoint exposure (R-EP-018). The part
// the decision point compiles rules with is [catalog.Versioning]: Register puts
// it, with Dimensions, into the catalog, and authz reads it there, so no bridge
// between the two exists (C-13). FilterDocument removes what the endpoint hides
// from a rendered document.
type ExposureAdapter interface {
	catalog.Versioning
	Dimensions() ExposureDimensions
	FilterDocument(ctx context.Context, p Path, doc []byte, view ExposureView) ([]byte, error)
}

// IndexBuilder recomputes derived metadata of a local repository from stored
// rows (R-ARCH-008). No format of the MVP has one (Q-ARCH-12).
type IndexBuilder interface {
	Rebuild(ctx context.Context, repo RepoRef, scope Path, o RebuildOptions) (RebuildResult, error)
}

// RebuildOptions configure a rebuild.
type RebuildOptions struct{ DryRun bool }

// RebuildResult counts what a rebuild touched.
type RebuildResult struct{ Packages, Versions int }
