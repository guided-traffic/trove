package deb

import (
	"context"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/deb/control"
	"github.com/guided-traffic/trove/internal/formats/deb/debfile"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// MediaType is the type of a package file (R-DEB-005).
const MediaType = "application/vnd.debian.binary-package"

// incoming is a package file the format has read and checked against the grammars of R-DEB-002: what an upload declares.
type incoming struct {
	pkg *debfile.Package
	// path is the canonical pool path the file is stored at (R-DEB-008, Q-DEB-5).
	path string
}

func (in *incoming) name() string         { return in.pkg.Name }
func (in *incoming) version() string      { return in.pkg.Version }
func (in *incoming) architecture() string { return in.pkg.Architecture }

// invalidField is the 422 of a declared value that breaks the grammars of R-DEB-002, naming the control field.
func invalidField(field, reason string) error {
	return control.Refusal(&control.Error{Field: field, Reason: reason})
}

// readPackage reads a package file from the spool within the bounds of R-DEB-034 and applies the grammars of R-DEB-002 to what it
// declares: a package trove cannot index is refused, never stored unindexed (Q-DEB-4).
func readPackage(ra io.ReaderAt, size int64, lim untrusted.Limits) (*incoming, error) {
	p, err := debfile.Read(ra, size, lim)
	if err != nil {
		return nil, err
	}
	switch {
	case !ValidPackage(p.Name):
		return nil, invalidField("Package", "the package name is outside the grammar of Debian Policy 5.6.1 (lower case letters, digits, '+', '-', '.', two to 128 characters)")
	case !ValidVersion(p.Version):
		return nil, invalidField("Version", "the version is outside the grammar of deb-version(7)")
	case !ValidArchitecture(p.Architecture):
		return nil, invalidField("Architecture", "the architecture is outside the grammar, or is a wildcard")
	case !ValidPackage(p.Source):
		return nil, invalidField("Source", "the source package name is outside the grammar")
	}
	return &incoming{pkg: p, path: PoolPath(p.Source, p.Name, p.Version, p.Architecture)}, nil
}

// Inspect reads an upload that enters the repository (R-DEB-007): a package file is untrusted input and is read within the bounds of
// R-DEB-034 before anything uses it. It returns the declared package and version, which authorization checks (R-ARCH-014 step 5), and
// the read-only properties of the file (FMT-008). The upload route reads the package itself before the commit; Inspect is the same
// reader for an upload that reaches the pipeline another way and the one the conformance kit and the fuzz target drive.
func (f *Format) Inspect(_ context.Context, in formats.InspectInput) (*formats.Inspection, error) {
	ra, err := in.Body.ReaderAt()
	if err != nil {
		return nil, err
	}
	pk, err := readPackage(ra, in.Body.Size(), in.Limits.Tighten(f.Descriptor().Limits))
	if err != nil {
		return nil, err
	}
	return &formats.Inspection{Package: pk.name(), Version: pk.version(), Properties: properties(pk.pkg, nil), Payload: pk}, nil
}

// properties are the read-only properties of a package file (R-DEB-007): its identity and one value listing its coordinates.
func properties(p *debfile.Package, coords []string) map[string]string {
	props := map[string]string{
		"deb.package": p.Name, "deb.version": p.Version, "deb.architecture": p.Architecture, "deb.source": p.Source,
	}
	if len(coords) > 0 {
		props["deb.coordinate"] = joinSorted(coords)
	}
	return props
}

// joinSorted is the one value that lists the coordinates of a file: suite/component, sorted, comma-joined.
func joinSorted(coords []string) string {
	coords = slices.Clone(coords)
	sort.Strings(coords)
	return strings.Join(coords, ",")
}
