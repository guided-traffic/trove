package npm

import (
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/name"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestR_NPM_003_Classification is the table of R-NPM-003: which npm paths are
// metadata, which are artifacts, and which package and version they belong to.
func TestR_NPM_003_Classification(t *testing.T) {
	f := New()
	cases := []struct {
		path string
		kind formats.PathKind
		pkg  string
		ver  string
	}{
		{"", formats.PathComputed, "", ""},
		{"lodash", formats.PathMetadata, "lodash", ""},
		{"@acme/ui", formats.PathMetadata, "@acme/ui", ""},
		{"JSONStream", formats.PathMetadata, "JSONStream", ""},
		{"lodash/4.17.21", formats.PathMetadata, "lodash", "4.17.21"},
		{"@acme/ui/1.2.0-rc.1", formats.PathMetadata, "@acme/ui", "1.2.0-rc.1"},
		{"@acme/ui/latest", formats.PathMetadata, "@acme/ui", ""}, // a dist-tag is no version
		{"@acme/ui/1.2", formats.PathMetadata, "@acme/ui", ""},
		{"@acme/ui/v1.2.0", formats.PathMetadata, "@acme/ui", ""},
		{"@acme/ui/1.2.0+build", formats.PathMetadata, "@acme/ui", ""},
		{"lodash/-/lodash-4.17.21.tgz", formats.Content, "lodash", "4.17.21"},
		{"@acme/ui/-/ui-1.2.0.tgz", formats.Content, "@acme/ui", "1.2.0"},
		{"@acme/ui/-/ui-1.2.0-rc.1.tgz", formats.Content, "@acme/ui", "1.2.0-rc.1"},
		{"@acme/ui/-/ui-1.2.0.sigstore", formats.Content, "@acme/ui", "1.2.0"},
		{"@acme/ui/-/other-1.2.0.tgz", formats.Content, "@acme/ui", ""},
		{"@acme/ui/-/ui-1.2.tgz", formats.Content, "@acme/ui", ""},
		{"@acme/ui/-/ui-1.2.0.tar", formats.Content, "@acme/ui", ""},
		{"-/ping", formats.PathComputed, "", ""},
		{"-/whoami", formats.PathComputed, "", ""},
		{"-/v1/search", formats.PathComputed, "", ""},
		{"-/package/lodash/dist-tags", formats.PathComputed, "", ""},
		// a path the format cannot classify is metadata (R-REPO-003)
		{"@acme", formats.PathMetadata, "", ""},
		{"a/b/c", formats.PathMetadata, "", ""},
	}
	for _, c := range cases {
		t.Run("R-NPM-003 "+c.path, func(t *testing.T) {
			p, err := repository.CleanPath(c.path)
			if err != nil {
				t.Fatal(err)
			}
			got := f.Classify(p)
			if got.Kind != c.kind || got.Package != c.pkg || got.Version != c.ver {
				t.Fatalf("Classify(%q) = %+v, want kind %v package %q version %q", c.path, got, c.kind, c.pkg, c.ver)
			}
		})
	}
	t.Run("R-NPM-003 the request kinds follow the table", func(t *testing.T) {
		want := map[formats.PathKind]formats.RequestKind{
			formats.PathMetadata: formats.KindMetadata, formats.Content: formats.KindArtifact,
		}
		for k, rk := range want {
			if k.RequestKind() != rk {
				t.Errorf("%v: %v", k, k.RequestKind())
			}
		}
		for _, r := range f.Routes() {
			switch r.Name {
			case "npm.tarball.get":
				if r.Kind.RequestKind() != formats.KindArtifact {
					t.Errorf("%s is %v", r.Name, r.Kind.RequestKind())
				}
			case "npm.document.get", "npm.version.get", "npm.disttags.get":
				if r.Kind.RequestKind() != formats.KindMetadata {
					t.Errorf("%s is %v", r.Name, r.Kind.RequestKind())
				}
			case "npm.publish", "npm.unpublish.put", "npm.unpublish.delete", "npm.unpublish.tarball", "npm.disttag.put", "npm.disttag.delete":
				if r.Kind.RequestKind() != formats.KindWrite {
					t.Errorf("%s is %v", r.Name, r.Kind.RequestKind())
				}
			}
		}
	})
	t.Run("R-NPM-003 the package document path is the normalized name", func(t *testing.T) {
		for _, pkg := range []string{"lodash", "@acme/ui", "JSONStream"} {
			p, ok := f.PackageDocumentPath(pkg)
			if !ok || p.Path != pkg {
				t.Errorf("%q: %+v %v", pkg, p, ok)
			}
		}
		if _, ok := f.PackageDocumentPath("a/b/c"); ok {
			t.Error("a/b/c is a package")
		}
	})
	t.Run("R-NPM-003 the item path of a version is the path Classify attributes to it", func(t *testing.T) {
		n, _ := name.Parse("@acme/ui")
		p := f.Classify(formats.Path{Path: TarballPath(n, "1.2.0")})
		if p.Kind != formats.Content || p.Package != "@acme/ui" || p.Version != "1.2.0" {
			t.Fatalf("%+v", p)
		}
		p = f.Classify(formats.Path{Path: SigstorePath(n, "1.2.0")})
		if p.Kind != formats.Content || p.Package != "@acme/ui" || p.Version != "1.2.0" {
			t.Fatalf("%+v", p)
		}
	})
}

// TestR_AUTHZ_005_NamesAreExactAndKeepPatterns: the name rule is the one authorization
// folds names and patterns with (R-AUTHZ-005): exact and case sensitive, a pattern with
// wildcards stays as written.
func TestR_AUTHZ_005_NamesAreExactAndKeepPatterns(t *testing.T) {
	n := names{}
	for in, want := range map[string]string{
		"lodash": "lodash", "@acme/ui": "@acme/ui", "JSONStream": "JSONStream", "@acme/*": "@acme/*", "acme-*": "acme-*",
		"*": "*", "**": "**", "@*/*": "@*/*", "a?c": "a?c", "{a,b}": "{a,b}", "[ab]": "[ab]",
	} {
		if got, ok := n.Normalize(in); !ok || got != want {
			t.Errorf("Normalize(%q) = %q %v, want %q", in, got, ok, want)
		}
		again, _ := n.Normalize(want)
		if again != want {
			t.Errorf("Normalize is not idempotent on %q", want)
		}
	}
	for _, in := range []string{"", "a/b", "a/b/c", "@acme", "%2e", "a\\b", "-"} {
		if _, ok := n.Normalize(in); ok {
			t.Errorf("Normalize(%q) took it for a name", in)
		}
	}
}

// FuzzMatch: whatever the method and the path, Match answers a route with a validated
// name, a classified refusal, or 404/405; it never panics, never returns a name that
// still holds an escape, and the route it finds is the one that matches the path.
func FuzzMatch(f *testing.F) {
	for _, r := range New().Routes() {
		for _, ex := range r.Examples {
			f.Add(r.Method, ex)
		}
	}
	for _, s := range []string{"a%2525b", "%2e%2e", "@acme%2f%2e%2e", "@a%2fb/-/c.tgz", "//", "-/", "-/package/", "-/package/@a", "%", "%zz", "@%2f"} {
		f.Add("GET", s)
		f.Add("PUT", s)
	}
	format := New()
	f.Fuzz(func(t *testing.T, method, path string) {
		route, params, err := format.Match(method, path)
		if err != nil {
			e, ok := errs.As(err)
			if !ok {
				t.Fatalf("unclassified error %v", err)
			}
			switch e.Status() {
			case http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed:
			default:
				t.Fatalf("status %d for %s %q: %v", e.Status(), method, path, err)
			}
			return
		}
		if n, ok := params["name"]; ok {
			if strings.Contains(n, "%") {
				t.Fatalf("name %q still holds an escape", n)
			}
			if parsed, err := name.Parse(n); err != nil || parsed.String() != n {
				t.Fatalf("name %q is not canonical: %v", n, err)
			}
		}
		if _, ok := route.Match(path); !ok {
			t.Fatalf("route %s does not match %q again", route.Name, path)
		}
	})
}
