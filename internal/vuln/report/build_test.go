package report

import (
	"errors"
	"testing"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// fakeScope is a ReadScope that reads npm-part in part and records how it is asked.
type fakeScope struct {
	asked []string // "repo pkg version" of every MayRead
	// allow decides a row; the version is the third word.
	allow func(pkg, version string) bool
}

func (f *fakeScope) Shape(key qname.Repo) authz.ReadShape {
	if key.Key == "npm-part" {
		return authz.ReadRows
	}
	return authz.ReadNone
}

func (f *fakeScope) MayRead(repo qname.Repo, pkg, version string) bool {
	f.asked = append(f.asked, repo.Key+" "+pkg+" "+version)
	return f.allow(pkg, version)
}

// independent adds the optional property; the value is the answer.
type independent struct {
	*fakeScope
	free bool
}

func (i independent) VersionIndependent() bool { return i.free }

func buildFixture() (*Service, *Snapshot) {
	repos := []repository.Repository{{ID: "11", Name: qr("npm-part"), Class: repository.Local, Format: "npm"}}
	return &Service{cfg: Config{MaxScopeRows: 1000}}, newSnapshot(repos)
}

// TestR_VULN_028_ScopeLoadsTheRowsTheScopeCanDecide: report.build reads the (repository, package) pairs only for a scope that
// says its decision does not look at the version (authz.VersionIndependent, Q-VULN-81), and the triples for every other: a scope
// that says false, one that does not implement the property, and a package view, which needs the versions to say who holds them.
// On the per-version path MayRead gets the version of each row, so a rule that reads it (an exposure range) is applied.
func TestR_VULN_028_ScopeLoadsTheRowsTheScopeCanDecide(t *testing.T) {
	triples := []store.Candidate{
		{RepoID: 11, Package: "@acme/sdk", Version: "1.9.0"},
		{RepoID: 11, Package: "@acme/sdk", Version: "2.1.0"},
		{RepoID: 11, Package: "other", Version: "1.0.0"},
	}
	pairs := []store.Pair{{RepoID: 11, Package: "@acme/sdk"}, {RepoID: 11, Package: "other"}}
	// A rule of an endpoint: @acme/sdk only from 2.0.0.
	rule := func(pkg, version string) bool { return pkg == "@acme/sdk" && version >= "2" }
	byPackage := func(pkg, version string) bool { return pkg == "@acme/sdk" }

	type loaders struct{ triples, pairs int }
	run := func(t *testing.T, rs authz.ReadScope, withWhole bool, pairLoader bool) (Scope, loaders) {
		t.Helper()
		s, sn := buildFixture()
		var n loaders
		load := func([]int64, int) ([]store.Candidate, error) { n.triples++; return triples, nil }
		var loadPairs func([]int64, int) ([]store.Pair, int, error)
		if pairLoader {
			loadPairs = func([]int64, int) ([]store.Pair, int, error) { n.pairs++; return pairs, len(triples), nil }
		}
		sc, err := s.build(sn, Caller{Reads: rs}, nil, withWhole, load, loadPairs)
		if err != nil {
			t.Fatal(err)
		}
		return sc, n
	}

	t.Run("R-VULN-028 Q-VULN-81 a version-independent scope reads the pairs and is asked without a version", func(t *testing.T) {
		fs := &fakeScope{allow: byPackage}
		sc, n := run(t, independent{fs, true}, false, true)
		if n != (loaders{pairs: 1}) {
			t.Fatalf("loaders %+v, want the pairs only", n)
		}
		if len(sc.Packages) != 1 || sc.Packages[0] != "11\x1f@acme/sdk" || len(sc.Rows) != 0 {
			t.Errorf("scope %+v", sc)
		}
		for _, a := range fs.asked {
			if a[len(a)-1] != ' ' {
				t.Errorf("asked with a version: %q", a)
			}
		}
	})
	t.Run("R-VULN-028 Q-VULN-81 a scope that says false reads the triples and MayRead gets each version", func(t *testing.T) {
		fs := &fakeScope{allow: rule}
		sc, n := run(t, independent{fs, false}, false, true)
		if n != (loaders{triples: 1}) {
			t.Fatalf("loaders %+v, want the triples only", n)
		}
		if len(sc.Rows) != 1 || sc.Rows[0] != "11\x1f@acme/sdk\x1f2.1.0" || len(sc.Packages) != 0 {
			t.Errorf("scope %+v: only 2.1.0 may be read", sc)
		}
		want := map[string]bool{"npm-part @acme/sdk 1.9.0": true, "npm-part @acme/sdk 2.1.0": true, "npm-part other 1.0.0": true}
		for _, a := range fs.asked {
			delete(want, a)
		}
		if len(want) != 0 {
			t.Errorf("rows MayRead was not asked with their version: %v (asked %v)", want, fs.asked)
		}
	})
	t.Run("R-VULN-028 Q-VULN-81 a scope without the property reads the triples", func(t *testing.T) {
		fs := &fakeScope{allow: rule}
		sc, n := run(t, fs, false, true)
		if n != (loaders{triples: 1}) || len(sc.Rows) != 1 || len(sc.Packages) != 0 {
			t.Errorf("loaders %+v, scope %+v", n, sc)
		}
	})
	t.Run("R-VULN-028 Q-VULN-81 a package view needs the versions even for a version-independent scope", func(t *testing.T) {
		fs := &fakeScope{allow: byPackage}
		sc, n := run(t, independent{fs, true}, true, true)
		if n != (loaders{triples: 1}) || len(sc.Holders) != 2 || len(sc.Packages) != 0 {
			t.Errorf("loaders %+v, scope %+v", n, sc)
		}
	})
	t.Run("R-VULN-028 Q-VULN-81 a caller without a pair loader reads the triples", func(t *testing.T) {
		fs := &fakeScope{allow: byPackage}
		if _, n := run(t, independent{fs, true}, false, false); n != (loaders{triples: 1}) {
			t.Errorf("loaders %+v", n)
		}
	})
	t.Run("R-VULN-028 more findings than a request decides are refused in both modes", func(t *testing.T) {
		s, sn := buildFixture()
		s.cfg.MaxScopeRows = 2
		fs := &fakeScope{allow: byPackage}
		_, err := s.build(sn, Caller{Reads: independent{fs, true}}, nil, false,
			func([]int64, int) ([]store.Candidate, error) { return triples, nil },
			func([]int64, int) ([]store.Pair, int, error) { return pairs, 3, nil })
		if err == nil {
			t.Error("the pair mode accepted 3 findings with a limit of 2")
		}
		_, err = s.build(sn, Caller{Reads: fs}, nil, false,
			func([]int64, int) ([]store.Candidate, error) { return triples, nil }, nil)
		if err == nil {
			t.Error("the triple mode accepted 3 findings with a limit of 2")
		}
		_, err = s.build(sn, Caller{Reads: fs}, nil, false,
			func([]int64, int) ([]store.Candidate, error) { return nil, errors.New("down") }, nil)
		if err == nil {
			t.Error("a loader error was swallowed")
		}
	})
}
