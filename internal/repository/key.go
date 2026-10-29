package repository

import (
	"regexp"
	"slices"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
)

var keyRe = regexp.MustCompile(KeyPattern)

// ValidKeyShape reports whether key can be a repository key at all: the pattern
// and the two extra rules of R-REPO-002. The request path uses it to answer 404
// for an impossible key without a query (R-ARCH-011 step 2). It does not look at
// reserved names, which are valid shapes.
func ValidKeyShape(key string) bool {
	return keyRe.MatchString(key) && !strings.Contains(key, "..") && !strings.HasSuffix(key, ".")
}

// IsReservedKey reports whether key is one of the literal sub-paths of
// /api/v1/repositories.
func IsReservedKey(key string) bool {
	return slices.Contains(ReservedKeys(), key)
}

// checkKey returns the violation of a new repository key, or nil.
func checkKey(key string) *errs.FieldError {
	switch {
	case !ValidKeyShape(key):
		return &errs.FieldError{
			Pointer: "/key", Code: errs.CodeInvalidKey,
			Detail: "must match " + KeyPattern + ", contain no \"..\" and not end in \".\"",
		}
	case IsReservedKey(key):
		return &errs.FieldError{
			Pointer: "/key", Code: codeReservedName,
			Detail: "this name is a sub-path of /api/v1/repositories and cannot be a key",
		}
	}
	return nil
}
