package untrusted

import (
	"errors"

	"github.com/guided-traffic/trove/internal/errs"
)

// CodeRefused is the code of content that the helpers refuse because of its
// structure: ambiguous or forbidden (R-ARCH-028). A bound that is crossed answers
// with errs.CodeTooLarge.
const CodeRefused = errs.CodeContentRefused

// TooLarge is the error of a bound crossed: a Limits field, a declared size, an
// entry count or a ratio (413).
func TooLarge(detail string) *errs.Error {
	return errs.E(errs.TooLarge, errs.CodeTooLarge, detail)
}

// Refused is the error of a structure the helpers will not read: duplicate names,
// link entries, a document type declaration, names that two readers resolve
// differently, a document that is not well-formed (422).
func Refused(detail string, opts ...errs.Option) *errs.Error {
	return errs.E(errs.Unprocessable, CodeRefused, detail, opts...)
}

// IsTooLarge reports whether err is a bound crossed (413).
func IsTooLarge(err error) bool {
	var e *errs.Error
	return errors.As(err, &e) && e.Kind == errs.TooLarge
}

// IsRefused reports whether err is a refused structure (422).
func IsRefused(err error) bool {
	var e *errs.Error
	return errors.As(err, &e) && e.Kind == errs.Unprocessable
}
