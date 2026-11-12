package ansible

import (
	"context"
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
)

// TestR_ANS_037_EverySurfaceAsksTheWikiGate: every surface of the registry is a route of the table, that route is declared Wiki, and
// wikiReadable returns the answer of the Host: false for a caller who reads the artifacts and not the wiki, true for one who reads both.
// The management operations of internal/api/v1 are walked by their own test in that package.
func TestR_ANS_037_EverySurfaceAsksTheWikiGate(t *testing.T) {
	surfaces := registeredWikiSurfaces()
	if len(surfaces) == 0 {
		t.Fatal("no wiki surface is registered")
	}
	for _, denied := range []bool{true, false} {
		opts := hostOptions()
		opts.WikiDenied = denied
		h := formattest.NewHost(allClasses(), opts)
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
			if !route.Wiki {
				t.Errorf("%s: a wiki surface is declared Wiki, so the dispatcher refuses a caller who may not read the wiki before the handler runs", name)
			}
			rq, err := h.NewRequest(context.Background(), http.MethodGet, route.Examples[0], nil)
			if err != nil {
				t.Errorf("%s: %v", name, err)
				continue
			}
			if got := wikiReadable(context.Background(), rq, formats.Target{Package: "acme.tools"}); got == denied {
				t.Errorf("%s: wikiReadable = %v for a host whose wiki is denied = %v", name, got, denied)
			}
		}
	}
}

// TestR_ANS_037_TheGateAsksTheTargetAsAWikiRead: the Host is asked about the target with the package the surface names, and a
// repository the caller may not read has no readable wiki.
func TestR_ANS_037_TheGateAsksTheTargetAsAWikiRead(t *testing.T) {
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
