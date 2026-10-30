// Package manifest is the manifest half of the npm publish validation
// (R-NPM-007): bounded reading of a package.json, the union of the clients'
// normalization steps that touch the install-relevant fields, the comparison of
// the request manifest with the tarball's package.json, and the stored manifest
// with its server-controlled fields.
//
// It is a leaf: standard library and internal/errs only. A manifest is held as
// a generic JSON tree; documents arrive from untrusted publishers, so every
// read is bounded in bytes and depth first (R-ARCH-028).
package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/guided-traffic/trove/internal/errs"
)

// The native npm error codes of R-NPM-007. They are part of the API contract of
// the npm protocol (a client prints them) and are registered with the other npm
// codes by the protocol layer.
const (
	// CodeBadPublish is a 400: the publish request or its tarball is malformed.
	CodeBadPublish = "npm_bad_publish"
	// CodeManifestMismatch is a 400: the request manifest and the tarball's
	// package.json disagree.
	CodeManifestMismatch = "npm_manifest_mismatch"
	// CodeTooLarge is a 413: a limit of R-NPM-007 or R-ARCH-028 is exceeded.
	CodeTooLarge = "npm_too_large"
)

// Manifest is a decoded package.json: a generic JSON object. Numbers are
// json.Number, so a manifest marshals back to the digits it arrived with.
type Manifest map[string]any

// Limits bound what a manifest may cost (R-ARCH-028).
type Limits struct {
	// MaxBytes is the size of the document.
	MaxBytes int64
	// MaxDepth is the nesting depth of arrays and objects; the top-level object
	// has depth 1.
	MaxDepth int
}

// Default limits of a manifest (Q-NPM-17).
const (
	DefaultMaxBytes = 1 << 20 // 1 MiB per manifest entry, R-ARCH-028
	DefaultMaxDepth = 64
)

// DefaultLimits returns the limits of R-ARCH-028 for a manifest.
func DefaultLimits() Limits {
	return Limits{MaxBytes: DefaultMaxBytes, MaxDepth: DefaultMaxDepth}
}

// WithDefaults replaces zero fields by the defaults.
func (l Limits) WithDefaults() Limits {
	if l.MaxBytes <= 0 {
		l.MaxBytes = DefaultMaxBytes
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = DefaultMaxDepth
	}
	return l
}

// BadPublish builds the 400 npm_bad_publish error.
func BadPublish(detail string, opts ...errs.Option) *errs.Error {
	return errs.E(errs.Invalid, CodeBadPublish, detail, opts...)
}

// TooLarge builds the 413 npm_too_large error.
func TooLarge(detail string) *errs.Error {
	return errs.E(errs.TooLarge, CodeTooLarge, detail)
}

// Mismatch builds the 400 npm_manifest_mismatch error.
func Mismatch(detail string) *errs.Error {
	return errs.E(errs.Invalid, CodeManifestMismatch, detail)
}

// Read reads a manifest document of at most max bytes. A longer document is
// refused after max+1 bytes were read, never held whole.
func Read(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	if int64(len(data)) > max {
		return nil, TooLarge(fmt.Sprintf("the manifest is larger than %d bytes", max))
	}
	return data, nil
}

// Parse decodes one JSON object within the limits. The size and the depth are
// checked before the decoder allocates anything.
func Parse(data []byte, lim Limits) (Manifest, error) {
	lim = lim.WithDefaults()
	if int64(len(data)) > lim.MaxBytes {
		return nil, TooLarge(fmt.Sprintf("the manifest is larger than %d bytes", lim.MaxBytes))
	}
	if err := checkDepth(data, lim.MaxDepth); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, BadPublish("the manifest is not a JSON object", errs.WithCause(err))
	}
	if m == nil {
		return nil, BadPublish("the manifest is not a JSON object")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, BadPublish("the manifest holds data after the JSON object")
	}
	return m, nil
}

// Decode reads and parses a manifest within the limits.
func Decode(r io.Reader, lim Limits) (Manifest, error) {
	lim = lim.WithDefaults()
	data, err := Read(r, lim.MaxBytes)
	if err != nil {
		return nil, err
	}
	return Parse(data, lim)
}

// checkDepth refuses a document nested deeper than max. It follows string
// literals, so brackets inside strings do not count, and it never builds a tree.
func checkDepth(data []byte, max int) error {
	depth := 0
	inString, escaped := false, false
	for _, c := range data {
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth > max {
				return BadPublish(fmt.Sprintf("the manifest is nested deeper than %d levels", max))
			}
		case '}', ']':
			depth--
		}
	}
	return nil
}

// Clone returns a deep copy.
func (m Manifest) Clone() Manifest {
	if m == nil {
		return nil
	}
	return Manifest(cloneValue(map[string]any(m)).(map[string]any))
}

func cloneValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = cloneValue(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = cloneValue(e)
		}
		return out
	default:
		return v
	}
}

// Marshal renders the manifest as JSON.
func (m Manifest) Marshal() ([]byte, error) { return json.Marshal(map[string]any(m)) }
