package nuspec

import (
	"errors"

	"github.com/guided-traffic/trove/internal/errs"
)

// CodeInvalidPackage is the code of every refusal of a package's content: the
// archive, its names, its .nuspec and the bounds on them (R-NUGET-005, 400).
const CodeInvalidPackage = "nuget_invalid_package"

// The reasons a refusal carries as its cause, so that a caller and a test can
// tell the cases apart with errors.Is without reading the message.
var (
	ErrNotXML        = errors.New("the .nuspec is not well-formed XML")
	ErrEncoding      = errors.New("the .nuspec is neither UTF-8 nor UTF-16 with a byte order mark")
	ErrDoctype       = errors.New("the .nuspec holds a document type declaration")
	ErrTooLarge      = errors.New("a bound on the .nuspec is crossed")
	ErrShape         = errors.New("the .nuspec does not have the structure of a package manifest")
	ErrDuplicate     = errors.New("an element that must occur once occurs several times")
	ErrMixedDeps     = errors.New("the .nuspec mixes dependency groups and plain dependencies")
	ErrMissingFields = errors.New("the .nuspec lacks a required element")
)

// Invalid builds the 400 error of a refused package. reason is one of the Err
// values of this package or of nupkg and is kept as the cause.
func Invalid(reason error, detail string) *errs.Error {
	return errs.E(errs.Invalid, CodeInvalidPackage, detail, errs.WithCause(reason))
}
