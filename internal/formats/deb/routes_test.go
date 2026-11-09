package deb

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

// allClasses is the format as it will be with every class registered: the adapters are placeholders that only make the format
// whole for formats.Validate, which holds the complete route table to the ambiguity check of R-ARCH-010 (check 1).
func allClasses() *Format {
	f := &Format{classes: []formats.Class{formats.Local, formats.Remote, formats.Virtual}}
	f.remote = struct{ formats.RemoteAdapter }{}
	f.virtual = struct{ formats.VirtualAdapter }{}
	f.routes = f.buildRoutes()
	return f
}

func TestRouteTableOfEveryClassIsUnambiguous(t *testing.T) {
	if err := formats.Validate(allClasses()); err != nil {
		t.Fatal(err)
	}
}

func TestRoutesFollowTheRegisteredClasses(t *testing.T) {
	f := New()
	for _, r := range f.Routes() {
		for _, c := range r.Classes {
			if c != formats.Local && c != formats.Remote {
				t.Errorf("route %s serves class %s, and only the local and the remote class are registered", r.Name, c)
			}
		}
		// A route that serves every class names none; the others name the classes that are registered.
		switch r.Name {
		case routeDists, routeByHash, routePoolGet, routeSigningKey:
		default:
			if len(r.Classes) == 0 {
				t.Errorf("route %s serves every class and names none", r.Name)
			}
		}
	}
	if f.Remote() == nil || f.Virtual() != nil {
		t.Error("the format has the remote adapter and no virtual adapter until the virtual class registers")
	}
}

func match(t *testing.T, f *Format, method, path string) (string, formats.Params, error) {
	t.Helper()
	r, p, err := f.Match(method, path)
	if err != nil {
		return "", nil, err
	}
	return r.Name, p, nil
}

// The route table of R-DEB-003, class by class.
func TestMatch(t *testing.T) {
	f := allClasses()
	type want struct {
		method, path, route string
		classes             []formats.Class
		params              formats.Params
	}
	local, remote, virtual := formats.Local, formats.Remote, formats.Virtual
	cases := []want{
		{"GET", "dists/stable/InRelease", routeDists, nil, formats.Params{"rest": "stable/InRelease"}},
		{"GET", "dists/stable/Release.gpg", routeDists, nil, nil},
		{"GET", "dists/stable/main/binary-amd64/Packages.gz", routeDists, nil, formats.Params{"rest": "stable/main/binary-amd64/Packages.gz"}},
		{"GET", "dists/stable/contrib/binary-all/Packages.xz", routeDists, nil, nil},
		{"GET", "dists/bookworm/main/binary-amd64/Packages.diff/Index", routeDists, nil, nil},
		{"GET", "dists/stable/updates/InRelease", routeDists, nil, formats.Params{"rest": "stable/updates/InRelease"}},
		{
			"GET", "dists/stable/main/binary-amd64/by-hash/SHA256/" + zeros, routeByHash, nil,
			formats.Params{"rest": "stable/main/binary-amd64", "hash": zeros},
		},
		{"GET", "dists/stable/by-hash/SHA256/" + zeros, routeByHash, nil, formats.Params{"rest": "stable", "hash": zeros}},
		{"GET", "dists/stable/updates/main/binary-amd64/by-hash/SHA256/" + zeros, routeByHash, nil, nil},
		{"GET", "pool/h/hello/hello_2.10-3_amd64.deb", routePoolGet, nil, formats.Params{"path": "h/hello/hello_2.10-3_amd64.deb"}},
		{
			"GET", "pool/libx/libxml2/libxml2-utils_2.9.14%2Bdfsg-1.3_amd64.deb", routePoolGet, nil,
			formats.Params{"path": "libx/libxml2/libxml2-utils_2.9.14+dfsg-1.3_amd64.deb"},
		},
		{"DELETE", "pool/h/hello/hello_2.10-3_amd64.deb", routePoolDelete, []formats.Class{local}, nil},
		{
			"GET", "members/deb-local/pool/h/hello/hello_2.10-3_amd64.deb", routeMemberGet,
			[]formats.Class{virtual},
			formats.Params{"member": "deb-local", "path": "pool/h/hello/hello_2.10-3_amd64.deb"},
		},
		{"GET", "binary/InRelease", routeFlatRelease, []formats.Class{remote}, formats.Params{"dir": "binary", "file": "InRelease"}},
		{"GET", "InRelease", routeFlatRelease, []formats.Class{remote}, formats.Params{"dir": "", "file": "InRelease"}},
		{"GET", "binary/Packages.gz", routeFlatIndex, []formats.Class{remote}, formats.Params{"dir": "binary", "file": "Packages.gz"}},
		{"GET", "binary/Packages.diff/Index", routeFlatIndex, []formats.Class{remote}, nil},
		{"GET", "binary/trove-hello_1.0_all.deb", routeFlatFile, []formats.Class{remote}, formats.Params{"dir": "binary", "file": "trove-hello_1.0_all.deb"}},
		{"GET", "signing-key.asc", routeSigningKey, nil, nil},
		{"PUT", "upload/hello_2.10-3_amd64.deb", routeUpload, []formats.Class{local}, formats.Params{"file": "hello_2.10-3_amd64.deb"}},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			r, p, err := f.Match(c.method, c.path)
			if err != nil {
				t.Fatal(err)
			}
			if r.Name != c.route || !sameClasses(r.Classes, c.classes) {
				t.Fatalf("route = %s %v, want %s %v", r.Name, r.Classes, c.route, c.classes)
			}
			for k, v := range c.params {
				if p[k] != v {
					t.Errorf("param %s = %q, want %q", k, p[k], v)
				}
			}
		})
	}
}

func sameClasses(a, b []formats.Class) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// One route serves every path below dists/ in all classes, because Match is blind to the class (Q-DEB-45): the paths a local or
// virtual repository does not build (another index variant, a source or translation index) reach the route and the handler answers
// them 404 (R-DEB-003); a malformed hash is no by-hash file and is such a path too.
func TestPathsBelowDistsAreOneRoute(t *testing.T) {
	f := New()
	for _, p := range []string{
		"dists/stable/main/binary-amd64/Packages.bz2", "dists/stable/main/binary-amd64/Packages", "dists/stable/main/source/Sources.gz",
		"dists/stable/main/i18n/Translation-en.xz", "dists/stable/Contents-amd64.gz", "dists/stable/main/binary-amd64/by-hash/SHA256/abc",
	} {
		if name, _, err := match(t, f, http.MethodGet, p); err != nil || name != routeDists {
			t.Errorf("GET %s = %s, %v, want %s", p, name, err, routeDists)
		}
	}
	for _, p := range []string{"dists", "dists/stable", "dists/stable/"} {
		if _, _, err := f.Match(http.MethodGet, p); err == nil {
			t.Errorf("GET %s matches a route", p)
		}
	}
}

func TestMethodsOfARoute(t *testing.T) {
	f := New()
	for _, c := range []struct{ method, path string }{
		{http.MethodPut, "dists/stable/InRelease"},
		{http.MethodPost, "pool/h/hello/hello_2.10-3_amd64.deb"},
		{http.MethodGet, "upload/hello_2.10-3_amd64.deb"},
		{http.MethodDelete, "signing-key.asc"},
	} {
		_, _, err := f.Match(c.method, c.path)
		var e *errs.Error
		if !errors.As(err, &e) || e.Kind != errs.MethodNotAllowed {
			t.Errorf("%s %s = %v, want 405", c.method, c.path, err)
		}
	}
}

// The flat root of R-DEB-003 and Q-DEB-39: a leading "./" names the root directory of a flat repository and only the routes of flat
// repositories take it; any other dot segment is 400 invalid_path.
func TestFlatRoot(t *testing.T) {
	f := allClasses()
	name, p, err := match(t, f, http.MethodGet, "./InRelease")
	if err != nil || name != routeFlatRelease || p["dir"] != "" || p["file"] != "InRelease" || p[flatRootParam] != "true" {
		t.Fatalf("./InRelease = %s %v %v", name, p, err)
	}
	for _, path := range []string{"./Packages.gz", "./trove-hello_1.0_all.deb"} {
		if name, _, err := match(t, f, http.MethodGet, path); err != nil || !strings.HasPrefix(name, "deb.flat.") {
			t.Errorf("%s = %s, %v", path, name, err)
		}
	}
	for _, path := range []string{"a/./b", "./pool/h/hello/hello_2.10-3_amd64.deb", "./dists/stable/InRelease", "././InRelease", "a/../InRelease", "./signing-key.asc"} {
		_, _, err := f.Match(http.MethodGet, path)
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != errs.CodeInvalidPath {
			t.Errorf("GET %s = %v, want 400 invalid_path", path, err)
		}
	}
}

// A flat directory cannot shadow a route of the format: the reserved first segments are the routes' own.
func TestFlatPathsDoNotShadowTheRoutes(t *testing.T) {
	f := allClasses()
	for path, want := range map[string]string{
		"pool/a/b/c_1.0_all.deb": routePoolGet, "dists/x/InRelease": routeDists, "signing-key.asc": routeSigningKey,
	} {
		r, _, err := f.Match(http.MethodGet, path)
		if err != nil || r.Name != want {
			t.Errorf("GET %s = %v, %v, want %s", path, r, err, want)
		}
	}
}
