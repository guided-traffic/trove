package testformat_test

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/formattest/testformat"
)

// TestR_ARCH_010_Check13HoldsARefusingFixtureToItsDecisions: a format whose fixture lists every route in NoContent and
// NoRevision and whose cases only refuse cannot pass check 13 with a leaking handler. A refusing answer that asked the
// decision point anything is judged like a served one (R-AUTHZ-005, Q-AUTHZ-250), so a handler that authorizes the
// repository, or a package without a path, before it answers 404 fails; a skeleton whose routes decide nothing has
// nothing to judge. The case is the one the second penetration test of F-82 built.
func TestR_ARCH_010_Check13HoldsARefusingFixtureToItsDecisions(t *testing.T) {
	refusing := func(f testformat.Format) formattest.Fixture {
		fx := testformat.Fixture()
		fx.Content = []formattest.ContentCase{
			{Name: "an unknown package document", Path: "left-pad", Status: 404},
			{Name: "an unknown tarball", Path: "left-pad/-/left-pad-1.0.0.tgz", Status: 404},
			{Name: "all, refused", Path: "-/all"},
			{Name: "search, refused", Path: "-/search"},
		}
		fx.Changes = nil
		fx.Remote, fx.Merge = nil, nil
		fx.NoContent, fx.NoRevision = map[string]string{}, map[string]string{}
		for _, rt := range f.Routes() {
			if rt.Kind.WrapsContent() {
				fx.NoContent[rt.Name] = "not built yet"
			} else {
				fx.NoRevision[rt.Name] = "not built yet"
			}
		}
		for _, h := range []string{"OnCommit", "OnItemsRemoved", "OnItemsRestored"} {
			fx.NoRevision[h] = "not built yet"
		}
		delete(fx.NoContent, "tf.search.get")
		delete(fx.NoContent, "tf.all.get")
		return fx
	}
	for _, c := range []struct {
		defect testformat.Defect
		match  string
	}{
		{testformat.AuthorizesRepositoryLevel, "authorized no target that names the package at a path"},
		{testformat.AuthorizesWithoutPath, "and no path"},
	} {
		t.Run("R-ARCH-010 "+string(c.defect), func(t *testing.T) {
			f := testformat.With(c.defect)
			res := formattest.Evaluate(f, refusing(f)).Result(formattest.CheckAuthzPaths)
			if got := strings.Join(res.Failures, "\n"); !strings.Contains(got, c.match) {
				t.Fatalf("check 13 failures do not say %q:\n%s", c.match, got)
			}
		})
	}
	t.Run("R-ARCH-010 the same fixture over the sound format passes check 13", func(t *testing.T) {
		f := testformat.New()
		if res := formattest.Evaluate(f, refusing(f)).Result(formattest.CheckAuthzPaths); !res.Passed() {
			t.Fatalf("%+v", res)
		}
	})
}
