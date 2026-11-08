package deb

import (
	"context"
	"sort"
	"strings"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/formats/deb/version"
)

// viewAdapter is the format's part of the package screens (R-DEB-030): the versions in dpkg order and, from the rows of the stored
// files, the first line of the Description, the architectures, the coordinates and some control fields. There is no README. It runs no
// SQL: the data port answers.
type viewAdapter struct{}

var _ formats.ViewAdapter = viewAdapter{}

// Compare orders two versions by the dpkg rules. A string that is no version sorts before every version, so a stored value that cannot
// be read never hides a real one; two such strings are ordered as text.
func (viewAdapter) Compare(a, b string) int {
	okA, okB := ValidVersion(a), ValidVersion(b)
	switch {
	case !okA && !okB:
		return strings.Compare(a, b)
	case !okA:
		return -1
	case !okB:
		return 1
	}
	return version.Compare(a, b)
}

func viewStoreOf(ports formats.DataPorts) (Store, error) { return storeOf(ports.FormatData(ID)) }

// Describe describes the versions from their rows. Fields of the control paragraph that a version lacks are left out.
func (viewAdapter) Describe(ctx context.Context, ports formats.DataPorts, vs []formats.ViewVersion, want formats.ViewFields, _ func(string) bool) (map[formats.VersionID]formats.ViewData, error) {
	st, err := viewStoreOf(ports)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(vs))
	for _, v := range vs {
		ids = append(ids, int64(v.ID))
	}
	got, err := st.Describe(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[formats.VersionID]formats.ViewData, len(got))
	for id, v := range got {
		d := formats.ViewData{Description: v.Description}
		if want.Has(formats.ViewDetails) {
			d.Metadata = metadataOf(v)
		}
		out[formats.VersionID(id)] = d
	}
	return out, nil
}

// metadataOf is the `metadata` object of a version's details: the architectures, the coordinates and the control fields of R-DEB-030.
func metadataOf(v rows.VersionView) map[string]any {
	m := map[string]any{}
	arch := append([]string(nil), v.Architectures...)
	sort.Strings(arch)
	m["architectures"] = arch
	coords := make([]string, 0, len(v.Coordinates))
	for _, c := range v.Coordinates {
		coords = append(coords, c.Suite+"/"+c.Component)
	}
	m["coordinates"] = coords
	for field, key := range map[string]string{
		"Maintainer": "maintainer", "Section": "section", "Priority": "priority", "Installed-Size": "installedSize",
		"Homepage": "homepage", "Depends": "depends", "Source": "source",
	} {
		if val, ok := v.Fields[field]; ok && val != "" {
			m[key] = val
		}
	}
	return m
}

// Find searches the control data of the versions of the repositories.
func (viewAdapter) Find(ctx context.Context, ports formats.DataPorts, f formats.ViewFind, limit int) ([]formats.VersionID, error) {
	if f.Text == "" {
		return nil, nil // Debian versions have no keywords
	}
	st, err := viewStoreOf(ports)
	if err != nil {
		return nil, err
	}
	ids, err := st.Find(ctx, f.Repos, f.Text, limit+1)
	if err != nil {
		return nil, err
	}
	out := make([]formats.VersionID, 0, len(ids))
	for _, id := range ids {
		out = append(out, formats.VersionID(id))
	}
	return out, nil
}

// Withheld lists the versions whose files the replay withheld.
func (viewAdapter) Withheld(ctx context.Context, ports formats.DataPorts, repos []string) ([]formats.VersionID, error) {
	st, err := viewStoreOf(ports)
	if err != nil {
		return nil, err
	}
	ids, err := st.Withheld(ctx, repos)
	if err != nil {
		return nil, err
	}
	out := make([]formats.VersionID, 0, len(ids))
	for _, id := range ids {
		out = append(out, formats.VersionID(id))
	}
	return out, nil
}

// Stored lists the versions the repository serves of a package, newest arrival first. A remote repository's cached files are the
// remote class's.
func (viewAdapter) Stored(ctx context.Context, ports formats.DataPorts, q formats.StoredQuery) (formats.Stored, error) {
	if q.Repo.Class != formats.Local {
		return formats.Stored{}, nil
	}
	st, err := viewStoreOf(ports)
	if err != nil {
		return formats.Stored{}, err
	}
	vs, err := st.Stored(ctx, q.Repo.ID, q.Package, q.Limit)
	if err != nil {
		return formats.Stored{}, err
	}
	return formats.Stored{Versions: vs}, nil
}
