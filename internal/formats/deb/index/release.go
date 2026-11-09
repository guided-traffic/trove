package index

import (
	"crypto/md5" //nolint:gosec // the MD5Sum field of a Release is a compatibility field, not a security boundary (R-DEB-011)
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/formats/deb/control"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
)

// MaxArchitectures is the bound on the architectures of a suite (R-DEB-009).
const MaxArchitectures = 32

// All is the architecture of the files that every architecture lists.
const All = "all"

// Settings are what a suite's Release and indexes take from the repository (R-DEB-008).
type Settings struct {
	// Origin and Label are the Release fields of the same name; Description is the Description field.
	Origin, Label, Description string
	// Architectures are the architectures the repository lists besides those of its files (deb.architectures).
	Architectures []string
	// XZ asks for Packages.xz besides Packages.gz (deb.compression lists xz).
	XZ bool
}

// File is one index file of a suite: its path relative to dists/<suite>/, its bytes and their digests.
type File struct {
	Path   string
	Body   []byte
	Size   int64
	SHA256 string
	MD5    string
}

// Suite is the rendered content of a suite's generation, before it is signed: the index files and the text of the Release.
type Suite struct {
	Files []File
	// Listed are the entries of the Release's hash lists: the files and, for each Packages, the checksums of its uncompressed content
	// (Q-DEB-56). The uncompressed file itself is not stored or served.
	Listed  []File
	Release []byte
	// Architectures and Components are the lists the Release carries.
	Architectures []string
	Components    []string
}

// ReleaseDate formats a Release Date the way APT reads it.
func ReleaseDate(t time.Time) string { return t.UTC().Format("Mon, 02 Jan 2006 15:04:05 UTC") }

// Build renders the indexes of a suite from its entries and the Release that lists them, dated date. The components are those of the
// entries and the extra ones (the components of the previous generation, so a suite whose last file of a component went keeps the
// component's empty indexes until a reindex drops them, R-DEB-014). The result depends on the arguments alone (R-DEB-011).
func Build(suite string, entries []rows.IndexEntry, extraComponents []string, s Settings, date time.Time) (*Suite, error) {
	archs, comps := suiteLists(entries, extraComponents, s)
	if len(archs) > MaxArchitectures {
		return nil, fmt.Errorf("index: the suite %s lists %d architectures, at most %d", suite, len(archs), MaxArchitectures)
	}
	out := &Suite{Architectures: archs, Components: comps}
	for _, c := range comps {
		for _, a := range append(slices.Clone(archs), All) {
			var part []rows.IndexEntry
			for _, e := range entries {
				if e.Component == c && (e.Architecture == a || (a != All && e.Architecture == All)) {
					part = append(part, e)
				}
			}
			plain, err := Packages(part)
			if err != nil {
				return nil, err
			}
			base := c + "/binary-" + a + "/Packages"
			out.Listed = append(out.Listed, newFile(base, plain))
			out.Files = append(out.Files, newFile(base+".gz", Gzip(plain)))
			if s.XZ {
				z, err := XZ(plain)
				if err != nil {
					return nil, err
				}
				out.Files = append(out.Files, newFile(base+".xz", z))
			}
		}
	}
	slices.SortFunc(out.Files, func(a, b File) int { return compareStrings(a.Path, b.Path) })
	out.Listed = append(out.Listed, out.Files...)
	slices.SortFunc(out.Listed, func(a, b File) int { return compareStrings(a.Path, b.Path) })
	rel, err := releaseText(suite, out, s, date)
	if err != nil {
		return nil, err
	}
	out.Release = rel
	return out, nil
}

func newFile(path string, body []byte) File {
	m := md5.Sum(body) //nolint:gosec // see the import
	h := sha256.Sum256(body)
	return File{Path: path, Body: body, Size: int64(len(body)), SHA256: hex.EncodeToString(h[:]), MD5: hex.EncodeToString(m[:])}
}

// suiteLists are the architectures (the settings' and those of the files, without all) and the components of a suite, sorted.
func suiteLists(entries []rows.IndexEntry, extraComponents []string, s Settings) (archs, comps []string) {
	for _, a := range s.Architectures {
		if a != All && !slices.Contains(archs, a) {
			archs = append(archs, a)
		}
	}
	comps = slices.Clone(extraComponents)
	for _, e := range entries {
		if e.Architecture != All && !slices.Contains(archs, e.Architecture) {
			archs = append(archs, e.Architecture)
		}
		if !slices.Contains(comps, e.Component) {
			comps = append(comps, e.Component)
		}
	}
	slices.Sort(archs)
	slices.Sort(comps)
	return archs, slices.Compact(comps)
}

func releaseText(suite string, st *Suite, s Settings, date time.Time) ([]byte, error) {
	md5s, sha256s := make([]string, 0, len(st.Listed)), make([]string, 0, len(st.Listed))
	for _, f := range st.Listed {
		md5s = append(md5s, fmt.Sprintf("%s %d %s", f.MD5, f.Size, f.Path))
		sha256s = append(sha256s, fmt.Sprintf("%s %d %s", f.SHA256, f.Size, f.Path))
	}
	p := control.Paragraph{Fields: []control.Field{
		{Name: "Origin", Value: s.Origin},
		{Name: "Label", Value: s.Label},
		{Name: "Suite", Value: suite},
		{Name: "Codename", Value: suite},
		{Name: "Date", Value: ReleaseDate(date)},
		{Name: "Architectures", Value: strings.Join(st.Architectures, " ")},
		{Name: "Components", Value: strings.Join(st.Components, " ")},
		{Name: "Acquire-By-Hash", Value: "yes"},
		{Name: "Description", Value: s.Description},
	}}
	if len(st.Listed) > 0 {
		p.Fields = append(p.Fields,
			control.Field{Name: "MD5Sum", Value: "\n" + strings.Join(md5s, "\n")},
			control.Field{Name: "SHA256", Value: "\n" + strings.Join(sha256s, "\n")})
	}
	b, err := p.Bytes()
	if err != nil {
		return nil, errors.Join(errors.New("index: the Release does not validate"), err)
	}
	return b, nil
}
