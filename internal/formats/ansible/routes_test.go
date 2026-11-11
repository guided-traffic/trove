package ansible

import (
	"reflect"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/repository"
)

func repoOfClass(c formats.Class) *repository.Repository {
	return &repository.Repository{ID: "7", Class: c, Format: "ansible"}
}

// TestR_ANS_003_TheRouteTable is the table of the plan, section 2.4: name, method, kind, action and classes of the eleven routes, and the
// declaration of the Token scheme on each (R-ANS-006).
func TestR_ANS_003_TheRouteTable(t *testing.T) {
	local, remote, virtual := formats.Local, formats.Remote, formats.Virtual
	want := []struct {
		name, method string
		kind         formats.RouteKind
		action       formats.Action
		classes      []formats.Class
	}{
		{routeRoot, "GET", formats.Computed, formats.ActionRead, nil},
		{routeCollection, "GET", formats.Metadata, formats.ActionRead, nil},
		{routeVersions, "GET", formats.Metadata, formats.ActionRead, nil},
		{routeVersion, "GET", formats.Metadata, formats.ActionRead, nil},
		{routeDocs, "GET", formats.Metadata, formats.ActionRead, nil},
		{routeArtifact, "GET", formats.Download, formats.ActionRead, nil},
		{routePublish, "POST", formats.Upload, formats.ActionDeploy, []formats.Class{local, virtual}},
		{routeImport, "GET", formats.Metadata, formats.ActionRead, []formats.Class{local, virtual}},
		{routeRolesFind, "GET", formats.Volatile, formats.ActionRead, []formats.Class{remote, virtual}},
		{routeRoleVersions, "GET", formats.Metadata, formats.ActionRead, []formats.Class{remote, virtual}},
		{routeRoleArchive, "GET", formats.Download, formats.ActionRead, []formats.Class{remote, virtual}},
	}
	got := allClasses().Routes()
	if len(got) != len(want) {
		t.Fatalf("%d routes, want %d", len(got), len(want))
	}
	for i, w := range want {
		r := got[i]
		if r.Name != w.name || r.Method != w.method || r.Kind != w.kind || r.Action != w.action || !reflect.DeepEqual(r.Classes, w.classes) {
			t.Errorf("route %d = %s %s %v %v %v, want %+v", i, r.Name, r.Method, r.Kind, r.Action, r.Classes, w)
		}
		if !r.TokenScheme {
			t.Errorf("route %s does not declare the Token scheme", r.Name)
		}
		if r.APIKey {
			t.Errorf("route %s declares the API key header", r.Name)
		}
	}
	// With the local class alone, the routes of the other classes are not in the table and the others are narrowed to it.
	localOnly := &Format{classes: []formats.Class{formats.Local}}
	localOnly.routes = localOnly.buildRoutes()
	for _, r := range localOnly.Routes() {
		if r.Name == routeRolesFind || r.Name == routeRoleVersions || r.Name == routeRoleArchive {
			t.Errorf("the table of a format with the local class alone holds %s", r.Name)
		}
		if len(r.Classes) > 0 && !reflect.DeepEqual(r.Classes, []formats.Class{local}) {
			t.Errorf("route %s serves %v", r.Name, r.Classes)
		}
	}
}

func TestR_ANS_003_MatchResolvesTheProtocolPaths(t *testing.T) {
	f := allClasses()
	for _, tc := range []struct {
		method, path string
		route        string
		params       formats.Params
	}{
		{"GET", "", routeRoot, formats.Params{}},
		{"GET", "/", routeRoot, formats.Params{}},
		{"HEAD", "", routeRoot, formats.Params{}},
		{"GET", "v3/collections/acme/tools/", routeCollection, formats.Params{"ns": "acme", "name": "tools"}},
		{"GET", "v3/collections/acme/tools", routeCollection, formats.Params{"ns": "acme", "name": "tools"}},
		{"GET", "v3/collections/Community/General/", routeCollection, formats.Params{"ns": "Community", "name": "General"}},
		{"GET", "v3/collections/acme/tools/versions/", routeVersions, formats.Params{"ns": "acme", "name": "tools"}},
		{"GET", "v3/collections/acme/tools/versions", routeVersions, formats.Params{"ns": "acme", "name": "tools"}},
		{"GET", "v3/collections/acme/tools/versions/1.0.0-rc.1+b.5/", routeVersion, formats.Params{"ns": "acme", "name": "tools", "version": "1.0.0-rc.1+b.5"}},
		{"GET", "v3/collections/acme/tools/versions/1.0.0/docs-blob/", routeDocs, formats.Params{"ns": "acme", "name": "tools", "version": "1.0.0"}},
		{"GET", "v3/collections/acme/tools/versions/1.0.0-rc.1+b.5/docs-blob", routeDocs, formats.Params{"ns": "acme", "name": "tools", "version": "1.0.0-rc.1+b.5"}},
		{"GET", "v3/artifacts/collections/acme-tools-1.0.0.tar.gz", routeArtifact, formats.Params{"file": "acme-tools-1.0.0.tar.gz"}},
		{"POST", "v3/artifacts/collections/", routePublish, formats.Params{}},
		{"POST", "v3/artifacts/collections", routePublish, formats.Params{}},
		{"GET", "v3/imports/collections/abc/", routeImport, formats.Params{"id": "abc"}},
		{"GET", "v1/roles/", routeRolesFind, formats.Params{}},
		{"GET", "v1/roles/16/versions/", routeRoleVersions, formats.Params{"id": "16"}},
		{"GET", "v1/roles/archives/16/v1.0.0.tar.gz", routeRoleArchive, formats.Params{"id": "16", "file": "v1.0.0.tar.gz"}},
	} {
		r, p, err := f.Match(tc.method, tc.path)
		if err != nil {
			t.Errorf("%s %q: %v", tc.method, tc.path, err)
			continue
		}
		if r.Name != tc.route || !reflect.DeepEqual(p, tc.params) {
			t.Errorf("%s %q = %s %v, want %s %v", tc.method, tc.path, r.Name, p, tc.route, tc.params)
		}
	}
}

func TestR_ANS_003_MatchRefusesWhatIsNoRoute(t *testing.T) {
	f := allClasses()
	for _, p := range []string{
		"v3", "v3/", "v3/collections", "v3/collections/acme", "v3/collections/acme/tools/other", "v3/collections/acme/tools/versions/1.0.0/x",
		"v3/artifacts", "v3/artifacts/collections/a/b", "v3/imports/collections", "v2/collections/a/b/", "v1", "v1/roles/16",
		"v1/roles/16/other", "v1/roles/archives/16", "v3/collections/acme%2Ftools/x/", "v3/collections/acme/tools/versions/1.0.0%2F2/",
	} {
		if _, _, err := f.Match("GET", p); err == nil {
			t.Errorf("GET %q matched a route", p)
		} else if e, _ := errs.As(err); e == nil || e.Kind != errs.NotFound {
			t.Errorf("GET %q: %v, want 404", p, err)
		}
	}
	// A wrong method answers 405 with Allow, and a path with a dot segment never reaches a matcher.
	_, _, err := f.Match("DELETE", "v3/collections/acme/tools/")
	if e, _ := errs.As(err); e == nil || e.Kind != errs.MethodNotAllowed || !reflect.DeepEqual(e.Allow, []string{"GET", "HEAD"}) {
		t.Errorf("DELETE of a collection: %v", err)
	}
	_, _, err = f.Match("GET", "v3/collections/../x/")
	if e, _ := errs.As(err); e == nil || e.Kind != errs.Invalid {
		t.Errorf("a dot segment: %v", err)
	}
}

// TestR_ARCH_010_NoTwoRoutesAcceptOnePath: the version list and the version document, which share a prefix, never match one path
// (kit check 1 covers the examples; this holds the shapes against each other over the segments they differ in).
func TestR_ARCH_010_NoTwoRoutesAcceptOnePath(t *testing.T) {
	f := allClasses()
	paths := []string{
		"v3/collections/a/versions/versions", "v3/collections/versions/versions/versions", "v3/collections/a/b/versions/versions/",
		"v1/roles/archives/versions", "v1/roles/archives/1/versions", "v1/roles/versions/versions",
	}
	for _, p := range paths {
		n := 0
		for _, r := range f.Routes() {
			if _, ok := r.Match(p); ok {
				n++
			}
		}
		if n > 1 {
			t.Errorf("%d routes accept %q", n, p)
		}
	}
}
