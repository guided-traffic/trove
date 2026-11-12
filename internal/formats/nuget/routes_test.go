package nuget

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
)

// TestR_NUGET_001_MatchResolvesEveryRoute: every resource of the endpoint layout reaches its
// route, with the ID and the version resolved to their normal forms whatever the casing and
// the spelling of the request (R-NUGET-003: "any casing and any non-normalized spelling").
func TestR_NUGET_001_MatchResolvesEveryRoute(t *testing.T) {
	f := New()
	cases := []struct {
		name, method, path string
		route              string
		params             formats.Params
	}{
		{"service index without a slash", http.MethodGet, "v3", routeIndex, formats.Params{}},
		{"service index with a slash", http.MethodGet, "v3/", routeIndex, formats.Params{}},
		{"service index with index.json", http.MethodGet, "v3/index.json", routeIndex, formats.Params{}},
		{"service index by HEAD", http.MethodHead, "v3/index.json", routeIndex, formats.Params{}},
		{"version list, any casing", http.MethodGet, "v3/flatcontainer/Acme.Widgets/index.json", routeFlatIndex, formats.Params{"id": "acme.widgets"}},
		{
			"version list of an ID with several dots", http.MethodGet, "v3/flatcontainer/Microsoft.Extensions.Logging.Abstractions/index.json", routeFlatIndex,
			formats.Params{"id": "microsoft.extensions.logging.abstractions"},
		},
		{
			"package", http.MethodGet, "v3/flatcontainer/acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg", routeNupkg,
			formats.Params{"id": "acme.widgets", "version": "1.2.3", "file": "nupkg"},
		},
		{
			"package of a non-normalized version and another casing", http.MethodGet, "v3/flatcontainer/ACME.Widgets/01.2.3.0/acme.widgets.1.2.3.nupkg", routeNupkg,
			formats.Params{"id": "acme.widgets", "version": "1.2.3", "file": "nupkg"},
		},
		{
			"package with a file name in capitals", http.MethodGet, "v3/flatcontainer/acme.widgets/1.2.3/ACME.WIDGETS.1.2.3.NUPKG", routeNupkg,
			formats.Params{"id": "acme.widgets", "version": "1.2.3", "file": "nupkg"},
		},
		{
			"package of a pre-release with build metadata dropped", http.MethodGet, "v3/flatcontainer/acme.widgets/1.0.0-RC.1+sha.9f/acme.widgets.1.0.0-rc.1.nupkg", routeNupkg,
			formats.Params{"id": "acme.widgets", "version": "1.0.0-rc.1", "file": "nupkg"},
		},
		{
			"nuspec", http.MethodGet, "v3/flatcontainer/Acme.Widgets/1.2.3/Acme.Widgets.nuspec", routeNuspec,
			formats.Params{"id": "acme.widgets", "version": "1.2.3", "file": "nuspec"},
		},
		{
			"embedded readme", http.MethodGet, "v3/flatcontainer/acme.widgets/1.2.3/README", routeReadme,
			formats.Params{"id": "acme.widgets", "version": "1.2.3", "file": "readme"},
		},
		{
			"registration index, plain hive", http.MethodGet, "v3/registration/Acme.Widgets/index.json", routeRegistrationIndex,
			formats.Params{"hive": "registration", "id": "acme.widgets"},
		},
		{
			"registration index, gzip hive", http.MethodGet, "v3/registration-gz/acme.widgets/index.json", routeRegistrationIndex,
			formats.Params{"hive": "registration-gz", "id": "acme.widgets"},
		},
		{
			"registration index, SemVer 2 hive", http.MethodGet, "v3/registration-semver2/acme.widgets/index.json", routeRegistrationIndex,
			formats.Params{"hive": "registration-semver2", "id": "acme.widgets"},
		},
		{
			"registration page", http.MethodGet, "v3/registration-semver2/acme.widgets/page/1.0.0/2.0.0-RC.1.json", routeRegistrationPage,
			formats.Params{"hive": "registration-semver2", "id": "acme.widgets", "lower": "1.0.0", "upper": "2.0.0-rc.1"},
		},
		{
			"registration leaf", http.MethodGet, "v3/registration-gz/acme.widgets/1.2.3.0.json", routeRegistrationLeaf,
			formats.Params{"hive": "registration-gz", "id": "acme.widgets", "version": "1.2.3"},
		},
		{"search", http.MethodGet, "v3/search", routeSearch, formats.Params{}},
		{"autocomplete", http.MethodGet, "v3/autocomplete", routeAutocomplete, formats.Params{}},
		{"push", http.MethodPut, "v3/package", routePush, formats.Params{}},
		{"push with the trailing slash the clients add", http.MethodPut, "v3/package/", routePush, formats.Params{}},
		{"unlist and delete", http.MethodDelete, "v3/package/Acme.Widgets/1.2.3.0", routeDelete, formats.Params{"id": "acme.widgets", "version": "1.2.3"}},
		{"relist", http.MethodPost, "v3/package/acme.widgets/1.2.3", routeRelist, formats.Params{"id": "acme.widgets", "version": "1.2.3"}},
		{"symbol push", http.MethodPut, "v3/symbolpackage", routeSymbolPush, formats.Params{}},
		{"symbol push with the trailing slash", http.MethodPut, "v3/symbolpackage/", routeSymbolPush, formats.Params{}},
		{
			"symbol file", http.MethodGet, "v3/symbols/acme.widgets.pdb/0123456789abcdef0123456789abcdefFFFFFFFF/acme.widgets.pdb", routeSymbols,
			formats.Params{"key": "acme.widgets.pdb/0123456789abcdef0123456789abcdefFFFFFFFF/acme.widgets.pdb"},
		},
	}
	for _, c := range cases {
		t.Run("R-NUGET-001 "+c.name, func(t *testing.T) {
			r, p, err := f.Match(c.method, c.path)
			if err != nil {
				t.Fatalf("Match(%s, %q): %v", c.method, c.path, err)
			}
			if r.Name != c.route {
				t.Errorf("route = %s, want %s", r.Name, c.route)
			}
			if !reflect.DeepEqual(p, c.params) {
				t.Errorf("params = %v, want %v", p, c.params)
			}
		})
	}
}

// TestR_NUGET_001_APathThatNamesNothingIsA404 splits the two 404s of Match: a path no route
// accepts is the nuget_v2_unsupported of R-NUGET-001, a path of a route that cannot hold
// what it names (an ID or a version that is none, a file of another package) is the plain
// not_found of a package that does not exist.
func TestR_NUGET_001_APathThatNamesNothingIsA404(t *testing.T) {
	f := New()
	v2 := []string{
		"", "FindPackagesById()", "$metadata", "Packages()", "Search()", "api/v2/package", "Packages(Id='Acme.Widgets',Version='1.2.3')",
		"v3/unknown", "v3/flatcontainer", "v3/flatcontainer/acme.widgets", "v3/flatcontainer/acme.widgets/1.2.3", "v3/flatcontainer/acme.widgets/1.2.3/notes.txt",
		"v3/index.json/extra", "v3/search/more", "v3/registration/acme.widgets", "v4/index.json", "v3//index.json",
	}
	for _, p := range v2 {
		t.Run("R-NUGET-001 v2 "+p, func(t *testing.T) {
			_, _, err := f.Match(http.MethodGet, p)
			e, ok := errs.As(err)
			if !ok || e.Kind != errs.NotFound || e.Code != errs.CodeNuGetV2Unsupported {
				// a path with an empty segment is refused as no path at all, which is as good a refusal
				if ok && e.Code == errs.CodeInvalidPath {
					return
				}
				t.Fatalf("Match(%q) = %v, want 404 nuget_v2_unsupported", p, err)
			}
		})
	}
	plain := []string{
		"v3/flatcontainer/a..b/index.json",
		"v3/flatcontainer/acme%2Fwidgets/index.json",
		"v3/flatcontainer/acme.widgets/1.0.0-/acme.widgets.1.0.0-.nupkg",
		"v3/flatcontainer/acme.widgets/1.2.3/other.1.2.3.nupkg",
		"v3/flatcontainer/acme.widgets/1.2.3/acme.widgets.1.2.4.nupkg",
		"v3/flatcontainer/acme.widgets/1.2.3/acme.widgets.nupkg",
		"v3/flatcontainer/acme.widgets/1.2.3/other.nuspec",
		"v3/registration/acme.widgets/not-a-version.json",
		"v3/registration/acme.widgets/page/1.0.0/oops.json",
	}
	for _, p := range plain {
		t.Run("R-NUGET-001 no such resource "+p, func(t *testing.T) {
			_, _, err := f.Match(http.MethodGet, p)
			e, ok := errs.As(err)
			if !ok || e.Kind != errs.NotFound || e.Code != errs.CodeNotFound {
				t.Fatalf("Match(%q) = %v, want 404 not_found", p, err)
			}
		})
	}
	for _, p := range []string{"v3/package/a..b/1.0.0", "v3/package/acme.widgets/one"} {
		t.Run("R-NUGET-007 no such version "+p, func(t *testing.T) {
			_, _, err := f.Match(http.MethodDelete, p)
			if e, ok := errs.As(err); !ok || e.Kind != errs.NotFound || e.Code != errs.CodeNotFound {
				t.Fatalf("Match(%q) = %v, want 404 not_found", p, err)
			}
		})
	}
}

// TestR_NUGET_001_AMethodTheResourceDoesNotTakeAnswers405 holds the Allow list to the methods
// of the routes that accept the path; HEAD comes with GET.
func TestR_NUGET_001_AMethodTheResourceDoesNotTakeAnswers405(t *testing.T) {
	f := New()
	cases := []struct {
		method, path string
		allow        []string
	}{
		{http.MethodPut, "v3/index.json", []string{"GET", "HEAD"}},
		{http.MethodDelete, "v3/search", []string{"GET", "HEAD"}},
		{http.MethodGet, "v3/package", []string{"PUT"}},
		{http.MethodPut, "v3/package/acme.widgets/1.2.3", []string{"DELETE", "POST"}},
		{http.MethodPost, "v3/flatcontainer/acme.widgets/index.json", []string{"GET", "HEAD"}},
		{http.MethodGet, "v3/symbolpackage", []string{"PUT"}},
	}
	for _, c := range cases {
		t.Run("R-NUGET-001 "+c.method+" "+c.path, func(t *testing.T) {
			_, _, err := f.Match(c.method, c.path)
			e, ok := errs.As(err)
			if !ok || e.Kind != errs.MethodNotAllowed {
				t.Fatalf("Match = %v, want 405", err)
			}
			if !reflect.DeepEqual(e.Allow, c.allow) {
				t.Errorf("Allow = %v, want %v", e.Allow, c.allow)
			}
		})
	}
}

// TestR_NUGET_001_TraversalNeverMatches: a path that is no clean path is refused before any
// matcher sees it, whichever resource it imitates (R-ARCH-005).
func TestR_NUGET_001_TraversalNeverMatches(t *testing.T) {
	f := New()
	for _, p := range []string{
		"v3/flatcontainer/../index.json", "v3/flatcontainer/%2e%2e/acme/index.json", "v3/flatcontainer/./acme/index.json",
		"v3/flatcontainer/acme%00/index.json", "v3/flatcontainer/acme%5c/index.json", "v3/symbols/../../x", "v3/symbols/a%2f..%2fb/c/d",
	} {
		t.Run(p, func(t *testing.T) {
			if r, _, err := f.Match(http.MethodGet, p); err == nil {
				t.Fatalf("Match(%q) resolved route %s", p, r.Name)
			}
		})
	}
}

// TestR_NUGET_001_EveryWriteIsAnAPIKeyRouteOfTheLocalClass: the authentication stage examines
// X-NuGet-ApiKey on the routes that declare it and on no other (R-NUGET-012), and the writes
// are served by the local class alone, so a remote answers 405 and a virtual repository's
// write goes to its deployment target (R-NUGET-019).
func TestR_NUGET_001_EveryWriteIsAnAPIKeyRouteOfTheLocalClass(t *testing.T) {
	wantAction := map[string]formats.Action{
		routePush: formats.ActionDeploy, routeDelete: formats.ActionDelete, routeRelist: formats.ActionDeploy, routeSymbolPush: formats.ActionDeploy,
	}
	writes := 0
	for _, r := range New().Routes() {
		write := r.Action != formats.ActionRead
		if r.APIKey != write {
			t.Errorf("route %s: APIKey = %v for action %s", r.Name, r.APIKey, r.Action)
		}
		if write {
			writes++
			if !reflect.DeepEqual(r.Classes, []formats.Class{formats.Local}) {
				t.Errorf("route %s: classes = %v, want [local]", r.Name, r.Classes)
			}
			if want, ok := wantAction[r.Name]; !ok || r.Action != want {
				t.Errorf("route %s: action %s, want %s", r.Name, r.Action, want)
			}
		} else if len(r.Classes) != 0 {
			t.Errorf("route %s is a read and is restricted to %v", r.Name, r.Classes)
		}
	}
	if writes != len(wantAction) {
		t.Errorf("%d write routes, want %d", writes, len(wantAction))
	}
}

// TestR_NUGET_028_RouteNamesAreTheHandlerNames: a route is named by a handler name of
// R-NUGET-028, with the operation after a dot where a resource has several.
func TestR_NUGET_028_RouteNamesAreTheHandlerNames(t *testing.T) {
	handlers := []string{
		"nuget.index", "nuget.flatcontainer", "nuget.registration", "nuget.search", "nuget.autocomplete",
		"nuget.push", "nuget.delete", "nuget.relist", "nuget.symbolpush", "nuget.symbols", "nuget.vulnerabilities",
	}
	seen := map[string]bool{}
	for _, r := range New().Routes() {
		ok := false
		for _, h := range handlers {
			if r.Name == h || strings.HasPrefix(r.Name, h+".") {
				ok, seen[h] = true, true
			}
		}
		if !ok {
			t.Errorf("route %s is no handler name of R-NUGET-028", r.Name)
		}
	}
	for _, h := range handlers {
		// the vulnerability information is served by remote and virtual repositories, which join later
		if !seen[h] && h != "nuget.vulnerabilities" {
			t.Errorf("no route carries the handler name %s", h)
		}
	}
}

// TestR_NUGET_001_CanonicalPathOfARoute: the canonical path a read route addresses is the one
// Classify understands, so a handler and the storage layout agree.
func TestR_NUGET_001_CanonicalPathOfARoute(t *testing.T) {
	f := New()
	cases := []struct{ path, want string }{
		{"v3/flatcontainer/Acme.Widgets/index.json", "acme.widgets/index.json"},
		{"v3/flatcontainer/Acme.Widgets/1.2.3.0/acme.widgets.1.2.3.nupkg", "acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg"},
		{"v3/flatcontainer/acme.widgets/1.2.3/acme.widgets.nuspec", "acme.widgets/1.2.3/acme.widgets.nuspec"},
		{"v3/flatcontainer/acme.widgets/1.2.3/icon", "acme.widgets/1.2.3/.embedded/icon"},
		{"v3/registration-gz/acme.widgets/1.2.3.json", "acme.widgets/.registration/index.json"},
		{"v3/symbols/a.pdb/ABC/a.pdb", ".symbols/a.pdb/ABC/a.pdb"},
	}
	for _, c := range cases {
		r, p, err := f.Match(http.MethodGet, c.path)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := CanonicalPath(r.Name, p)
		if !ok || got != c.want {
			t.Errorf("CanonicalPath(%s) = %q, %v, want %q", c.path, got, ok, c.want)
		}
		if info := f.Classify(formats.Path{Path: got}); info.Kind == 0 {
			t.Errorf("Classify(%q) has no kind", got)
		}
	}
	for _, p := range []string{"v3/index.json", "v3/search"} {
		r, params, err := f.Match(http.MethodGet, p)
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := CanonicalPath(r.Name, params); ok {
			t.Errorf("CanonicalPath(%s) = %q, want none", p, got)
		}
	}
}

// TestR_NUGET_005_InspectReadsTheIdentityOfAPackage: the roads around the push route meet the
// package through Inspect, which runs steps 1 to 4 of R-NUGET-005 and names the package by
// the identity it declares (the lower-cased ID and the URL form of the version), refuses what
// the rules refuse with 422, and refuses a path that names another coordinate.
func TestR_NUGET_005_InspectReadsTheIdentityOfAPackage(t *testing.T) {
	f := New()
	ok := formattest.Spool(buildNupkg(t, "Acme.Widgets", "1.2.3.0"))
	got, err := f.Inspect(t.Context(), formats.InspectInput{Path: formats.Path{Path: acmePath}, Body: ok})
	if err != nil {
		t.Fatal(err)
	}
	if got.Package != "acme.widgets" || got.Version != "1.2.3" || got.Properties["nuget.id"] != "acme.widgets" {
		t.Errorf("Inspect = %+v", got)
	}
	for name, c := range map[string]struct {
		path string
		body formats.Spooled
	}{
		"another version in the path": {"acme.widgets/9.9.9/acme.widgets.9.9.9.nupkg", ok},
		"another ID in the path":      {"other.id/1.2.3/other.id.1.2.3.nupkg", ok},
		"a symbol package":            {acmePath, formattest.Spool(buildNupkg(t, "Acme.Widgets", "1.2.3", withType("SymbolsPackage")))},
		"not an archive":              {acmePath, formattest.Spool([]byte("no archive"))},
	} {
		_, err := f.Inspect(t.Context(), formats.InspectInput{Path: formats.Path{Path: c.path}, Body: c.body})
		var e *errs.Error
		if !errors.As(err, &e) || e.Kind != errs.Unprocessable || e.Code != errs.CodeContentRefused {
			t.Errorf("%s: Inspect = %v, want 422 content_refused", name, err)
		}
	}
}
