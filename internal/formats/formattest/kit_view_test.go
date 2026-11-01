package formattest_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/formattest/testformat"
)

// memView is a view adapter over a list the fixture's Seed fills: versions are ordered by a number
// ("1.2" < "1.10"), the order a defect can break, and arrive in the order Seed stores them. Each flag
// breaks one rule of ViewAdapter.
type memView struct {
	arrived []string
	flags   map[string]bool
}

func num(v string) int {
	n, _ := strconv.Atoi(strings.ReplaceAll(v, ".", ""))
	return n
}

func (m *memView) Compare(a, b string) int {
	switch {
	case m.flags["all-equal"]:
		return 0
	case m.flags["cyclic"]:
		// 3 beats 1 beats 2 beats 3: antisymmetric and not transitive.
		if a == b {
			return 0
		}
		next := map[string]string{"1": "2", "2": "3", "3": "1"}
		if next[a] == b {
			return -1
		}
		return 1
	case m.flags["junk-last"] && num(a) == 0:
		return 1
	}
	_, an := strconv.Atoi(strings.ReplaceAll(a, ".", ""))
	_, bn := strconv.Atoi(strings.ReplaceAll(b, ".", ""))
	switch {
	case an != nil && bn != nil:
		return strings.Compare(a, b)
	case an != nil:
		return -1
	case bn != nil:
		return 1
	}
	return num(a) - num(b)
}

func (m *memView) Prerelease(string) bool { return false }

func (m *memView) Describe(context.Context, formats.DataPorts, []formats.ViewVersion, formats.ViewFields, func(string) bool) (map[formats.VersionID]formats.ViewData, error) {
	return nil, nil
}

func (m *memView) Find(context.Context, formats.DataPorts, formats.ViewFind, int) ([]formats.VersionID, error) {
	return nil, nil
}

func (m *memView) Withheld(_ context.Context, _ formats.DataPorts, repos []string) ([]formats.VersionID, error) {
	if m.flags["withheld-fails"] && len(repos) == 0 {
		return nil, errors.New("no repositories")
	}
	if m.flags["withholds-served"] && len(repos) > 0 && len(m.arrived) > 0 {
		return []formats.VersionID{1}, nil
	}
	return nil, nil
}

func (m *memView) Stored(_ context.Context, _ formats.DataPorts, q formats.StoredQuery) (formats.Stored, error) {
	// Until Seed ran the repository holds nothing: the package is unknown.
	if q.Package != "view-sample" || len(m.arrived) == 0 {
		if m.flags["unknown-fails"] {
			return formats.Stored{}, errors.New("no such package")
		}
		return formats.Stored{}, nil
	}
	if q.Repo.Class != formats.Local {
		return formats.Stored{}, nil
	}
	vs := slices.Clone(m.arrived)
	slices.Reverse(vs)
	if m.flags["by-version"] {
		slices.SortFunc(vs, func(a, b string) int { return m.Compare(b, a) })
	}
	if m.flags["ignores-limit"] {
		limit := len(vs)
		q.Limit = limit
	}
	vs = vs[:min(len(vs), max(q.Limit, 0))]
	st := formats.Stored{Versions: vs}
	if (q.Tags || m.flags["tags-unasked"]) && len(vs) > 0 {
		st.Tags = map[string]string{"latest": vs[0]}
	}
	if m.flags["tag-elsewhere"] && q.Tags {
		st.Tags = map[string]string{"latest": "9.9.9"}
	}
	return st, nil
}

type viewFormat struct {
	testformat.Format
	a *memView
}

func (v viewFormat) View() formats.ViewAdapter { return v.a }

// viewFixture is the fixture of the test format with a view fixture and without the skip.
func viewFixture(a *memView, versions ...string) formattest.Fixture {
	fx := testformat.Fixture()
	fx.Skip = nil
	fx.View = &formattest.ViewFixture{
		Versions: versions, Package: "view-sample",
		Seed: func(*formattest.Host) error {
			a.arrived = slices.Clone(versions)
			return nil
		},
	}
	return fx
}

// TestR_ARCH_010_Check11ViewAdapter: a conforming adapter passes, and each defect fails the check that
// is meant for it and no other (R-ARCH-008, R-API-029, R-EP-016).
func TestR_ARCH_010_Check11ViewAdapter(t *testing.T) {
	versions := []string{"2", "1", "3"} // not in version order, so a Stored that sorts by version differs
	run := func(flags ...string) formattest.Report {
		a := &memView{flags: map[string]bool{}}
		for _, f := range flags {
			a.flags[f] = true
		}
		return formattest.Evaluate(viewFormat{Format: testformat.New(), a: a}, viewFixture(a, versions...))
	}
	t.Run("R-ARCH-010 a conforming view adapter passes check 11", func(t *testing.T) {
		rep := run()
		if got := messages(rep, formattest.CheckView); got != "" {
			t.Fatalf("check 11: %s", got)
		}
		if f := rep.Failed(); len(f) != 0 {
			t.Fatalf("failed %v", f)
		}
	})
	for _, c := range []struct {
		name, flag, want string
	}{
		{"a Compare that orders nothing", "all-equal", "the order is total"},
		{"a Compare that is not transitive", "cyclic", "not transitive"},
		{"a Compare that sorts a string that is no version last", "junk-last", "sorts before every version"},
		{"a Stored that sorts by version and not by arrival", "by-version", "newest arrival first"},
		{"a Stored that ignores its limit", "ignores-limit", "limit of 2"},
		{"a Stored that returns tags nobody asked for", "tags-unasked", "did not ask for them"},
		{"a Stored whose tag points to a version it does not serve", "tag-elsewhere", "does not serve"},
		{"a Stored that fails for a package the repository does not hold", "unknown-fails", "does not hold"},
		{"a Withheld that fails for no repository", "withheld-fails", "Withheld over no repository"},
		{"a Withheld that lists a version the repository serves", "withholds-served", "all served"},
	} {
		t.Run("R-ARCH-010 "+c.name+" fails check 11", func(t *testing.T) {
			rep := run(c.flag)
			if f := rep.Failed(); len(f) != 1 || f[0] != formattest.CheckView {
				t.Fatalf("failed %v, want only check 11", f)
			}
			if got := messages(rep, formattest.CheckView); !strings.Contains(got, c.want) {
				t.Fatalf("%q does not say %q", got, c.want)
			}
		})
	}
	t.Run("R-ARCH-010 check 11 is not skipped for a format with a view adapter", func(t *testing.T) {
		a := &memView{}
		fx := viewFixture(a, versions...)
		fx.Skip = map[formattest.Check]string{formattest.CheckView: "later"}
		rep := formattest.Evaluate(viewFormat{Format: testformat.New(), a: a}, fx)
		if got := messages(rep, formattest.CheckView); !strings.Contains(got, "has a view adapter") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("R-ARCH-010 check 11 needs a sample and a seed", func(t *testing.T) {
		a := &memView{}
		f := viewFormat{Format: testformat.New(), a: a}
		fx := testformat.Fixture()
		fx.Skip = nil
		if got := messages(formattest.Evaluate(f, fx), formattest.CheckView); !strings.Contains(got, "no View sample") {
			t.Fatalf("no fixture: %q", got)
		}
		fx.View = &formattest.ViewFixture{Versions: []string{"1", "1", "2"}, Package: "view-sample"}
		got := messages(formattest.Evaluate(f, fx), formattest.CheckView)
		if !strings.Contains(got, "given twice") || !strings.Contains(got, "no Seed") {
			t.Fatalf("duplicate and no seed: %q", got)
		}
	})
	t.Run("R-ARCH-010 a format without a view adapter and without a skip fails check 11", func(t *testing.T) {
		fx := testformat.Fixture()
		fx.Skip = nil
		if got := messages(formattest.Evaluate(testformat.New(), fx), formattest.CheckView); !strings.Contains(got, "no view adapter") {
			t.Fatalf("got %q", got)
		}
	})
}
