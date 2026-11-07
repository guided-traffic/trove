package repository

import (
	"slices"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/qname"
)

// IsReservedKey reports whether key is one of the literal sub-paths of
// /api/v1/repositories.
func IsReservedKey(key string) bool {
	return slices.Contains(ReservedKeys(), key)
}

// checkKey returns the violation of a new repository key, or nil.
func checkKey(key string) *errs.FieldError {
	switch {
	case !qname.ValidKey(key):
		return &errs.FieldError{
			Pointer: "/key", Code: errs.CodeInvalidKey,
			Detail: "must be " + qname.KeyRule,
		}
	case IsReservedKey(key):
		return &errs.FieldError{
			Pointer: "/key", Code: codeReservedName,
			Detail: "this name is a sub-path of /api/v1/repositories and cannot be a key",
		}
	}
	return nil
}
