package formats

import (
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// ExposureDimensions say what exposure rules may narrow for the format
// (R-EP-012, R-EP-018).
type ExposureDimensions struct{ Packages, Versions, Tags bool }

// ExposureView answers what an endpoint exposes of the addressed repository. It is
// the shared half of a view: the package and the version. A format whose rules also
// narrow tags (Dimensions.Tags) reads them from the same value through an interface
// of its own, because what a tag is belongs to the format (Q-ARCH-257).
type ExposureView interface {
	Package(name string) bool
	Version(name, version string) bool
}

// ExposureAdapter is a format's part of endpoint exposure (R-EP-018): the version
// syntax the decision point compiles rules with, [catalog.Versioning], and the
// dimensions the rules may narrow. Register puts both into the catalog, and authz
// reads them there, so no bridge between the two exists (C-13). What an endpoint
// hides from a document is the format's own filter over [Request.Endpoint]: no
// core code calls a filter, so the contract has none (Q-ARCH-257).
type ExposureAdapter interface {
	catalog.Versioning
	Dimensions() ExposureDimensions
}
