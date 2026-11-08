package deb

import (
	"slices"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
)

// The bounds of one replacement of coordinates (R-DEB-009).
const (
	maxRequestSuites     = 64
	maxRequestComponents = 32
)

// CoordinateList validates the list of a replacement of coordinates (R-DEB-009): not empty (a file without coordinates is deleted
// instead), every suite and component inside the grammar, duplicates dropped, at most 64 suites, 32 components and 32 coordinates.
// The pairs are suite and component.
func CoordinateList(pairs [][2]string) ([]rows.Coordinate, error) {
	if len(pairs) == 0 {
		return nil, errs.E(errs.Invalid, errs.CodeValidation, "A file is listed in at least one suite; delete the file instead.",
			errs.WithFields(errs.FieldError{Pointer: "/coordinates", Code: "invalid_value", Detail: "must name at least one coordinate"}))
	}
	var out []rows.Coordinate
	var suites, comps []string
	for _, p := range pairs {
		for _, v := range p {
			if !ValidSuite(v) {
				return nil, errs.E(errs.Invalid, errs.CodeDebInvalidCoordinate,
					"The distribution or component "+quote(v)+" is outside the grammar ^[a-z0-9][a-z0-9._+-]{0,63}$.")
			}
		}
		c := rows.Coordinate{Suite: p[0], Component: p[1]}
		if slices.Contains(out, c) {
			continue
		}
		out = append(out, c)
		if !slices.Contains(suites, c.Suite) {
			suites = append(suites, c.Suite)
		}
		if !slices.Contains(comps, c.Component) {
			comps = append(comps, c.Component)
		}
	}
	switch {
	case len(out) > maxCoordinates:
		return nil, OutOfRange("/coordinates", "A file has at most 32 coordinates.")
	case len(suites) > maxRequestSuites:
		return nil, OutOfRange("/coordinates", "A request names at most 64 suites.")
	case len(comps) > maxRequestComponents:
		return nil, OutOfRange("/coordinates", "A request names at most 32 components.")
	}
	return out, nil
}

// FileProperties are the read-only properties of a stored package file with its present coordinates, the value a change of
// coordinates writes again (R-DEB-007).
func FileProperties(b rows.Binary, coords []rows.Coordinate) map[string]string {
	props := map[string]string{
		"deb.package": b.Package, "deb.version": b.Version, "deb.architecture": b.Architecture, "deb.source": b.Source,
	}
	if cs := coordStrings(coords); len(cs) > 0 {
		props["deb.coordinate"] = joinSorted(cs)
	}
	return props
}
