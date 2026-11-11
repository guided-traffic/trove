package formattest_test

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/formattest/testformat"
)

// routeOverride replaces the routes of a format, to declare a route Wiki or to give one another kind.
type routeOverride struct {
	formats.Format
	routes []formats.Route
}

func (o routeOverride) Routes() []formats.Route { return o.routes }

// wikiFixture is the conforming fixture with the README text of the format of WithWiki as its wiki sample.
func wikiFixture() formattest.Fixture {
	fx := testformat.Fixture()
	fx.WikiSamples = []string{testformat.ReadmeSample}
	return fx
}

// withRoute returns the format with every route of base, the route named name changed by mod.
func withRoute(base formats.Format, name string, mod func(*formats.Route)) formats.Format {
	rs := base.Routes()
	for i := range rs {
		if rs[i].Name == name {
			mod(&rs[i])
		}
	}
	return routeOverride{Format: base, routes: rs}
}

// TestR_ARCH_010_Check15HoldsAFormatWithWikiContentToTheWikiRules: a caller who reads the artifacts and not the wiki finds no
// wiki byte in any answer (R-AUTHZ-034).
func TestR_ARCH_010_Check15HoldsAFormatWithWikiContentToTheWikiRules(t *testing.T) {
	check := func(f formats.Format, fx formattest.Fixture) string {
		return messages(formattest.Evaluate(f, fx), formattest.CheckWiki)
	}
	t.Run("R-ARCH-010 a format with wiki content that asks Host.WikiReadable before it puts the README in passes", func(t *testing.T) {
		if got := check(testformat.With(testformat.WithWiki), wikiFixture()); got != "" {
			t.Fatalf("%s", got)
		}
	})
	t.Run("R-ARCH-010 a package document that holds the README for every caller fails", func(t *testing.T) {
		got := check(testformat.New(string(testformat.WithWiki), string(testformat.LeaksWiki)), wikiFixture())
		if !strings.Contains(got, "answered a caller who may not read the wiki with the wiki text") {
			t.Fatalf("check 15 does not name the leak:\n%s", got)
		}
	})
	t.Run("R-ARCH-010 a format with wiki content and no wiki sample fails", func(t *testing.T) {
		fx := testformat.Fixture()
		f := withWikiFacts()
		if got := check(f, fx); !strings.Contains(got, "no WikiSamples") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("R-ARCH-010 a wiki format whose content cases never serve a case for the caller judges nothing and fails", func(t *testing.T) {
		fx := wikiFixture()
		fx.Content = nil
		if got := check(testformat.With(testformat.WithWiki), fx); !strings.Contains(got, "no content case") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("R-ARCH-010 a format that has no wiki content declares no Wiki route", func(t *testing.T) {
		f := withRoute(testformat.New(), "tf.doc.get", func(r *formats.Route) { r.Wiki = true })
		if got := check(f, testformat.Fixture()); !strings.Contains(got, "has no wiki content") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("R-ARCH-010 a Wiki route is a Metadata or Computed read", func(t *testing.T) {
		base := testformat.With(testformat.WithWiki)
		for name, mod := range map[string]func(*formats.Route){
			"an upload route":   func(r *formats.Route) { r.Wiki, r.Kind, r.Action = true, formats.Upload, formats.ActionDeploy },
			"a download route":  func(r *formats.Route) { r.Wiki, r.Kind = true, formats.Download },
			"a write on a read": func(r *formats.Route) { r.Wiki, r.Action = true, formats.ActionDeploy },
		} {
			f := withRoute(base, "tf.doc.get", mod)
			if got := check(f, wikiFixture()); !strings.Contains(got, "a wiki route is a Metadata or Computed read") {
				t.Errorf("%s: got %q", name, got)
			}
		}
		ok := withRoute(base, "tf.doc.get", func(r *formats.Route) { r.Wiki = true })
		if got := check(ok, wikiFixture()); strings.Contains(got, "a wiki route is a Metadata") {
			t.Errorf("a Metadata read declared Wiki is refused: %q", got)
		}
	})
	t.Run("R-ARCH-010 a format with wiki content may not skip the check", func(t *testing.T) {
		fx := wikiFixture()
		fx.Skip = map[formattest.Check]string{formattest.CheckWiki: "no README"}
		if got := check(testformat.With(testformat.WithWiki), fx); !strings.Contains(got, "has wiki content") {
			t.Fatalf("got %q", got)
		}
	})
}

// withWikiFacts is the test format with wiki content that no fixture seeds a sample for.
func withWikiFacts() formats.Format { return testformat.With(testformat.WithWiki) }
