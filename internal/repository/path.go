package repository

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/errs"
)

// Limits of a canonical path (Q-GENERIC-2): the total and every segment, in
// bytes of UTF-8.
const (
	MaxPathBytes    = 4096
	MaxSegmentBytes = 255
)

// Cleaned is a canonical repository path.
type Cleaned struct {
	// Path is the canonical path: segments separated by "/", no leading or
	// trailing slash, "" for the repository root.
	Path string
	// Folder reports that the request path ended in a slash (or was the root),
	// which marks a folder (R-STOR-007, R-STOR-017).
	Folder bool
}

// CleanPath canonicalises the repository-relative part of a request path
// (R-ARCH-005). The argument is the path exactly as received, still
// percent-encoded (URL.EscapedPath minus the prefix); a leading slash is
// ignored. The escapes are decoded exactly once, and only then are the
// segments looked at, so %2e%2e and ..%2f are as dead as "..". It answers 400
// invalid_path when
//
//   - an escape is malformed, or the decoded bytes are not valid UTF-8;
//   - a segment is empty, "." or "..";
//   - the path contains NUL, a backslash or any control character;
//   - the path exceeds MaxPathBytes or a segment exceeds MaxSegmentBytes.
//
// One trailing slash is not part of the path; it is reported as Folder.
func CleanPath(escaped string) (Cleaned, error) {
	s := strings.TrimPrefix(escaped, "/")
	if s == "" {
		return Cleaned{Folder: true}, nil
	}
	dec, err := url.PathUnescape(s)
	if err != nil {
		return Cleaned{}, invalidPath("the path contains a malformed percent-escape")
	}
	if !utf8.ValidString(dec) {
		return Cleaned{}, invalidPath("the path is not valid UTF-8")
	}
	folder := strings.HasSuffix(dec, "/")
	if folder {
		dec = dec[:len(dec)-1]
	}
	if len(dec) > MaxPathBytes {
		return Cleaned{}, invalidPath("the path is longer than 4096 bytes")
	}
	for _, r := range dec {
		if r == '\\' || unicode.IsControl(r) {
			return Cleaned{}, invalidPath("the path contains a backslash or a control character")
		}
	}
	for seg := range strings.SplitSeq(dec, "/") {
		switch seg {
		case "":
			return Cleaned{}, invalidPath("the path has an empty segment")
		case ".", "..":
			return Cleaned{}, invalidPath("the path has a . or .. segment")
		}
		if len(seg) > MaxSegmentBytes {
			return Cleaned{}, invalidPath("a path segment is longer than 255 bytes")
		}
	}
	return Cleaned{Path: dec, Folder: folder}, nil
}

func invalidPath(detail string) error {
	return errs.E(errs.Invalid, errs.CodeInvalidPath, detail)
}
