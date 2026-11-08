package formattest

import (
	"context"
	"slices"

	"github.com/guided-traffic/trove/internal/formats"
)

// junkVersion is a string that is no version of any format the kit knows; the contract of
// ViewAdapter.Compare sorts it before every version.
const junkVersion = "\x00?! not a version"

// view is check 11: the format's view adapter keeps its contract (R-ARCH-008, R-API-029, R-EP-016).
// Compare is a total order on the sample versions; Withheld and Stored answer an empty repository and
// an unknown package without an error, and, over the seeded package, Stored lists exactly the
// versions the repository serves, newest arrival first, within its limit, with tags only when asked.
func (r *run) view() {
	a := r.f.View()
	if a == nil {
		r.failf("the format has no view adapter and the check is not skipped")
		return
	}
	v := r.fx.View
	if v == nil || len(v.Versions) < 3 {
		r.failf("the fixture has no View sample of at least three versions")
		return
	}
	for i, x := range v.Versions {
		if slices.Contains(v.Versions[:i], x) {
			r.failf("the sample version %q is given twice", x)
		}
	}
	r.viewCompare(a, v.Versions)
	r.viewEmpty(a)
	if v.Seed == nil {
		r.failf("the View fixture has no Seed: Stored cannot be checked over a package")
		return
	}
	r.viewStored(a, v)
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// viewCompare checks that Compare is a total order over the sample: reflexive, antisymmetric,
// transitive, never equal for two different versions, and a string that is no version sorts first.
func (r *run) viewCompare(a formats.ViewAdapter, vs []string) {
	for _, x := range vs {
		if c := a.Compare(x, x); c != 0 {
			r.failf("Compare(%q, %q) = %d: equal strings compare equal", x, x, c)
		}
		if c := a.Compare(junkVersion, x); c >= 0 {
			r.failf("Compare(%q, %q) = %d: a string that is no version sorts before every version", junkVersion, x, c)
		}
		if c := a.Compare(x, junkVersion); c <= 0 {
			r.failf("Compare(%q, %q) = %d: a string that is no version sorts before every version", x, junkVersion, c)
		}
	}
	for _, x := range vs {
		for _, y := range vs {
			if x == y {
				continue
			}
			cxy, cyx := sign(a.Compare(x, y)), sign(a.Compare(y, x))
			switch {
			case cxy == 0:
				r.failf("Compare(%q, %q) = 0: two different versions are ordered, the order is total", x, y)
			case cxy != -cyx:
				r.failf("Compare(%q, %q) and Compare(%q, %q) disagree (%d, %d): the order is not antisymmetric", x, y, y, x, cxy, cyx)
			}
		}
	}
	for _, x := range vs {
		for _, y := range vs {
			for _, z := range vs {
				if a.Compare(x, y) < 0 && a.Compare(y, z) < 0 && a.Compare(x, z) >= 0 {
					r.failf("%q < %q < %q, but Compare(%q, %q) = %d: the order is not transitive", x, y, z, x, z, a.Compare(x, z))
				}
			}
		}
	}
}

// viewEmpty checks Withheld and Stored over an empty repository of every class the format lists for
// them: no error, nothing listed.
func (r *run) viewEmpty(a formats.ViewAdapter) {
	ctx := context.Background()
	for _, class := range []formats.Class{formats.Local, formats.Remote} {
		if !r.supports(class) {
			continue
		}
		h := r.host(class)
		ids, err := a.Withheld(ctx, h, []string{h.Ref().Name.String()})
		if err != nil || len(ids) != 0 {
			r.failf("Withheld over an empty %s repository = %v, %v, want nothing and no error", class, ids, err)
		}
		if ids, err = a.Withheld(ctx, h, nil); err != nil || len(ids) != 0 {
			r.failf("Withheld over no repository = %v, %v, want nothing and no error", ids, err)
		}
		for _, tags := range []bool{false, true} {
			st, err := a.Stored(ctx, h, formats.StoredQuery{Repo: h.Ref(), Package: r.unknownPackage(), Limit: 10, Tags: tags})
			if err != nil || len(st.Versions) != 0 || len(st.Tags) != 0 {
				r.failf("Stored of a package an empty %s repository does not hold (tags %v) = %+v, %v, want the empty answer and no error", class, tags, st, err)
			}
		}
	}
}

func (r *run) supports(class formats.Class) bool { return slices.Contains(r.d.Classes, class) }

// unknownPackage is a package no repository of the check holds: the name the View fixture seeds, asked
// on a host nothing was seeded on. A name of another shape could break a format's name rule.
func (r *run) unknownPackage() string { return r.fx.View.Package }

// viewStored seeds the package in a local repository and checks what Stored lists.
func (r *run) viewStored(a formats.ViewAdapter, v *ViewFixture) {
	ctx := context.Background()
	if !r.supports(formats.Local) {
		r.failf("the View fixture seeds a local repository and the format has no local class")
		return
	}
	h := r.host(formats.Local)
	if err := v.Seed(h); err != nil {
		r.failf("the View fixture could not seed %q: %v", v.Package, err)
		return
	}
	ask := func(limit int, tags bool) (formats.Stored, bool) {
		st, err := a.Stored(ctx, h, formats.StoredQuery{Repo: h.Ref(), Package: v.Package, Limit: limit, Tags: tags})
		if err != nil {
			r.failf("Stored(limit %d, tags %v) of the seeded package: %v", limit, tags, err)
			return st, false
		}
		return st, true
	}
	st, ok := ask(len(v.Versions)+10, false)
	if !ok {
		return
	}
	// Seed stores Versions in this order, so the newest arrival is the last one.
	newest := slices.Clone(v.Versions)
	slices.Reverse(newest)
	if !slices.Equal(st.Versions, newest) {
		r.failf("Stored lists %v, want the versions the repository serves, newest arrival first: %v", st.Versions, newest)
	}
	if len(st.Tags) != 0 {
		r.failf("Stored returned the tags %v although the query did not ask for them", st.Tags)
	}
	if two, ok := ask(2, false); ok && !slices.Equal(two.Versions, newest[:2]) {
		r.failf("Stored with a limit of 2 lists %v, want the two newest: %v", two.Versions, newest[:2])
	}
	if zero, ok := ask(0, false); ok && len(zero.Versions) != 0 {
		r.failf("Stored with a limit of 0 lists %v", zero.Versions)
	}
	if withTags, ok := ask(len(v.Versions)+10, true); ok {
		for tag, target := range withTags.Tags {
			if !slices.Contains(v.Versions, target) {
				r.failf("the tag %q points to %q, which the repository does not serve", tag, target)
			}
		}
		if !slices.Equal(withTags.Versions, newest) {
			r.failf("Stored with tags lists %v, want %v", withTags.Versions, newest)
		}
	}
	ids, err := a.Withheld(ctx, h, []string{h.Ref().Name.String()})
	if err != nil || len(ids) != 0 {
		r.failf("Withheld over a repository whose versions are all served = %v, %v, want nothing and no error", ids, err)
	}
	r.viewSizes(h, v)
}

// viewSizes checks the optional ViewSizer of a format that has one (ADR-177): over the seeded package it answers in one call, only
// for the ids it was asked for, with sizes that are not negative; it answers nothing, without an error, when asked for nothing.
func (r *run) viewSizes(h *Host, v *ViewFixture) {
	z := formats.CapabilitiesOf(r.f).ViewSizer
	if z == nil {
		return
	}
	ctx := context.Background()
	rows, _, err := h.Packages().Versions(ctx, h.Ref(), v.Package)
	if err != nil || len(rows) < 2 {
		r.failf("the versions of the seeded package could not be read for ViewSizes: %d rows, %v", len(rows), err)
		return
	}
	asked := make([]formats.ViewVersion, 0, len(rows))
	for _, row := range rows {
		asked = append(asked, formats.ViewVersion{ID: row.ID, Repo: h.Ref(), Package: v.Package, Version: row.Version})
	}
	// Ask for all but one: the answer holds no id that was not asked for.
	subset := asked[1:]
	got, err := z.ViewSizes(ctx, h, subset)
	if err != nil {
		r.failf("ViewSizes of the seeded package: %v", err)
		return
	}
	for id, n := range got {
		if !slices.ContainsFunc(subset, func(x formats.ViewVersion) bool { return x.ID == id }) {
			r.failf("ViewSizes answered for the version id %d, which it was not asked for", id)
		}
		if n < 0 {
			r.failf("ViewSizes answered %d for the version id %d: a size is not negative", n, id)
		}
	}
	if none, err := z.ViewSizes(ctx, h, nil); err != nil || len(none) != 0 {
		r.failf("ViewSizes of no version = %v, %v, want nothing and no error", none, err)
	}
}
