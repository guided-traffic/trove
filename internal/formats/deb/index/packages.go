// Package index renders the indexes of a local Debian repository: the Packages files of a suite, component and architecture, their
// compressed variants, the Release document that lists them, and the retention of the by-hash files of earlier generations
// (R-DEB-011, R-DEB-012). It is pure: it reads no clock, no database and no key, and equal inputs give equal bytes. It is a leaf.
package index

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"slices"
	"strconv"

	"github.com/ulikunitz/xz"

	"github.com/guided-traffic/trove/internal/formats/deb/control"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/formats/deb/version"
)

// computed are the fields of a Packages paragraph that trove writes itself; whatever an uploaded paragraph held of them is dropped
// before (R-DEB-007, R-DEB-034).
func computed() []string { return []string{"Filename", "Size", "MD5sum", "SHA256"} }

// Packages renders the Packages file of the entries: the normalized paragraph of each file followed by Filename, Size, MD5sum and
// SHA256 (R-DEB-011). The entries are ordered by package name, version in dpkg order and architecture, so equal rows give equal
// bytes whatever order the database returned them in. The caller passes the entries of one suite, component and architecture.
func Packages(entries []rows.IndexEntry) ([]byte, error) {
	sorted := slices.Clone(entries)
	slices.SortStableFunc(sorted, func(a, b rows.IndexEntry) int {
		if c := compareStrings(a.Package, b.Package); c != 0 {
			return c
		}
		if c := version.Compare(a.Version, b.Version); c != 0 {
			return c
		}
		if c := compareStrings(a.Architecture, b.Architecture); c != 0 {
			return c
		}
		return compareStrings(a.Filename, b.Filename)
	})
	var out bytes.Buffer
	for i, e := range sorted {
		p, err := control.Parse([]byte(e.Control))
		if err != nil {
			return nil, fmt.Errorf("index: the stored control data of %s %s is not a paragraph: %w", e.Package, e.Version, err)
		}
		p = p.Without(computed()...)
		p.Fields = append(p.Fields,
			control.Field{Name: "Filename", Value: e.Filename},
			control.Field{Name: "Size", Value: strconv.FormatInt(e.Size, 10)},
			control.Field{Name: "MD5sum", Value: e.MD5},
			control.Field{Name: "SHA256", Value: e.SHA256},
		)
		b, err := p.Bytes()
		if err != nil {
			return nil, fmt.Errorf("index: the paragraph of %s %s: %w", e.Package, e.Version, err)
		}
		if i > 0 {
			out.WriteByte('\n')
		}
		out.Write(b)
	}
	return out.Bytes(), nil
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Gzip compresses data with no file name and a modification time of 0, so equal content gives equal bytes (R-DEB-011).
func Gzip(data []byte) []byte {
	var out bytes.Buffer
	w, _ := gzip.NewWriterLevel(&out, gzip.BestCompression) // the level is valid
	_, _ = w.Write(data)
	_ = w.Close()
	return out.Bytes()
}

// XZ compresses data as an xz stream; equal content gives equal bytes.
func XZ(data []byte) ([]byte, error) {
	var out bytes.Buffer
	w, err := xz.NewWriter(&out)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
