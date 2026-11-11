package ansible

import (
	"context"
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
)

// withWikiCheck replaces the wiki check for one test.
func withWikiCheck(t *testing.T, c wikiReader) {
	t.Helper()
	old := wikiCheck
	wikiCheck = c
	t.Cleanup(func() { wikiCheck = old })
}

// TestR_ANS_037_EverySurfaceAsksTheWikiGate: every surface of the registry is a route of the table, and wikiReadable returns the answer of
// the check, asked once with the target given. The management operations of internal/api/v1 are walked by their own test in that
// package; the handlers of the routes join this walk when they land.
func TestR_ANS_037_EverySurfaceAsksTheWikiGate(t *testing.T) {
	var asked int
	var seen formats.Target
	withWikiCheck(t, func(_ context.Context, _ *formats.Request, tg formats.Target) bool { asked++; seen = tg; return false })

	h := formattest.NewHost(allClasses(), hostOptions())
	surfaces := registeredWikiSurfaces()
	if len(surfaces) == 0 {
		t.Fatal("no wiki surface is registered")
	}
	for _, name := range surfaces {
		var route *formats.Route
		for _, r := range allClasses().Routes() {
			if r.Name == name {
				route = &r
				break
			}
		}
		if route == nil {
			t.Errorf("the wiki surface %q is no route of the table", name)
			continue
		}
		rq, err := h.NewRequest(context.Background(), http.MethodGet, route.Examples[0], nil)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		before := asked
		if wikiReadable(context.Background(), rq, formats.Target{Package: "acme.tools"}) {
			t.Errorf("%s: wikiReadable said yes where the check said no", name)
		}
		if asked != before+1 || seen.Package != "acme.tools" {
			t.Errorf("%s: the check was asked %d times with %+v, want once for acme.tools", name, asked-before, seen)
		}
	}
}

// TestR_ANS_037_TheStandInFollowsTheRepositoryReadDecision: until Host.WikiReadable exists, whoever may read the target may read its
// documentation, and the decision is asked as a read.
func TestR_ANS_037_TheStandInFollowsTheRepositoryReadDecision(t *testing.T) {
	deny := errs.E(errs.Forbidden, errs.CodeForbidden, "no")
	var seen formats.Target
	opts := hostOptions()
	opts.Allow = func(tg formats.Target) error { seen = tg; return deny }
	h := formattest.NewHost(allClasses(), opts)
	rq, err := h.NewRequest(context.Background(), http.MethodGet, "v3/collections/acme/tools/versions/1.0.0/docs-blob/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if wikiReadable(context.Background(), rq, formats.Target{Package: "acme.tools"}) {
		t.Error("a repository the caller may not read has a readable wiki")
	}
	if len(seen.Actions) != 1 || seen.Actions[0] != formats.ActionRead || seen.Package != "acme.tools" {
		t.Errorf("the read decision was asked as %+v", seen)
	}
	opts.Allow = nil
	rq2, _ := formattest.NewHost(allClasses(), opts).NewRequest(context.Background(), http.MethodGet, "v3/collections/acme/tools/versions/1.0.0/docs-blob/", nil)
	if !wikiReadable(context.Background(), rq2, formats.Target{}) {
		t.Error("a readable repository has no readable wiki")
	}
}
