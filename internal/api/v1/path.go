package v1

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
)

// Item paths and reserved names (R-API-004).

// codeInvalidPath is the problem code of a path that violates R-API-004. The
// files area registers it in the code registry (docs/developer/m1-contracts.md);
// this package only uses the string.
const codeInvalidPath = "invalid_path"

// PathOptions say which shapes of an item path an operation accepts.
type PathOptions struct {
	// AllowRoot accepts the empty path, the root of a repository.
	AllowRoot bool
	// AllowTrailingSlash accepts one final "/", which names a folder.
	AllowTrailingSlash bool
}

// CheckItemPath validates an item path, which the caller has percent-decoded
// exactly once (net/http does that for a path value and a query parameter).
// It applies the rules of repository.CleanPath to the decoded path, so an item
// path means the same in the items API as under /files/ (R-API-004,
// R-GENERIC-002): empty segments, "." and "..", NUL, a backslash, control
// characters, invalid UTF-8, more than 4096 bytes and a segment of more than
// 255 bytes answer 400 invalid_path. The length is checked first, so an
// oversized path costs no further work. It returns the path as it was given.
func CheckItemPath(p string, o PathOptions) (string, error) {
	bad := func(why string) error {
		return errs.E(errs.Invalid, codeInvalidPath, fmt.Sprintf("The path is not valid: %s.", why))
	}
	if p == "" {
		if o.AllowRoot {
			return p, nil
		}
		return "", bad("it is empty")
	}
	body := p
	if o.AllowTrailingSlash {
		body = strings.TrimSuffix(body, "/")
		if body == "" {
			return "", bad("it has an empty segment")
		}
	}
	if len(body) > repository.MaxPathBytes {
		return "", bad("it is longer than 4096 bytes")
	}
	if !utf8.ValidString(p) {
		return "", bad("it is not valid UTF-8")
	}
	for _, r := range p {
		if r == 0 || r == '\\' || unicode.IsControl(r) {
			return "", bad("it contains a backslash or a control character")
		}
	}
	for seg := range strings.SplitSeq(body, "/") {
		switch seg {
		case "":
			return "", bad("it has an empty segment")
		case ".", "..":
			return "", bad("it has a segment " + seg)
		}
		if len(seg) > repository.MaxSegmentBytes {
			return "", bad("a segment is longer than 255 bytes")
		}
	}
	return p, nil
}

// CheckReservedName answers 422 reserved_name when a create uses a name that a
// route of the collection reserves (R-API-004), with the JSON pointer of the
// member that holds it. reserved is the list the key schema carries as
// x-trove-reserved-names.
func CheckReservedName(pointer, name string, reserved ...string) error {
	for _, r := range reserved {
		if name == r {
			return errs.E(errs.Unprocessable, errs.CodeReservedName,
				fmt.Sprintf("The name %q is reserved by a route of this collection.", name),
				errs.WithFields(errs.FieldError{Pointer: pointer, Code: "reserved", Detail: "the name is reserved"}))
		}
	}
	return nil
}
