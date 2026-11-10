package ansible

import (
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
)

func TestR_ANS_002_NamesNormalize(t *testing.T) {
	n := names{}
	for in, want := range map[string]string{
		"community.general": "community.general", "Community.General": "community.general", "ACME.tools_2": "acme.tools_2",
		"a1.b2": "a1.b2", "9a.b": "9a.b",
	} {
		if got, ok := n.Normalize(in); !ok || got != want {
			t.Errorf("Normalize(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"a.b.c", "_a.b", "1.2", "acme", "", ".b", "a.", "a-b.c", "acme.tools-x", "é.b", "a b.c"} {
		if got, ok := n.Normalize(in); ok {
			t.Errorf("Normalize(%q) = %q, want a refusal", in, got)
		}
	}
	// A pattern is left as it is, and folds with the same function that folds a name.
	for _, p := range []string{"community.*", "acme.{tools,other}", "!acme.x", "*", "acme.[a-c]*"} {
		if got, ok := n.Normalize(p); !ok || got != p {
			t.Errorf("Normalize(%q) = %q %v, want it unchanged", p, got, ok)
		}
	}
	// Idempotent.
	once, _ := n.Normalize("Community.General")
	if twice, _ := n.Normalize(once); twice != once {
		t.Errorf("Normalize is not idempotent: %q then %q", once, twice)
	}
}

func TestR_ANS_002_ClassifyAndCanonicalPaths(t *testing.T) {
	f := New()
	for _, tc := range []struct {
		path string
		want formats.PathInfo
	}{
		{"", formats.PathInfo{Kind: formats.PathComputed}},
		{"collections/acme/tools/acme-tools-1.0.0-rc.1.tar.gz", formats.PathInfo{Kind: formats.Content, Package: "acme.tools", Version: "1.0.0-rc.1"}},
		{"collections/acme/tools/acme-tools-2.0.0+build.5.tar.gz", formats.PathInfo{Kind: formats.Content, Package: "acme.tools", Version: "2.0.0+build.5"}},
		{"collections/acme/tools/acme-other-1.0.0.tar.gz", formats.PathInfo{Kind: formats.PathMetadata, Package: "acme.tools"}},
		{"collections/acme/tools/acme-tools-1.0.tar.gz", formats.PathInfo{Kind: formats.PathMetadata, Package: "acme.tools"}},
		{"collections/acme/tools/collection.json", formats.PathInfo{Kind: formats.PathMetadata, Package: "acme.tools"}},
		{"collections/acme/tools/versions.json", formats.PathInfo{Kind: formats.PathMetadata, Package: "acme.tools"}},
		{"collections/acme/tools/1.2.3.json", formats.PathInfo{Kind: formats.PathMetadata, Package: "acme.tools", Version: "1.2.3"}},
		{"collections/acme/tools/x.json", formats.PathInfo{Kind: formats.PathMetadata, Package: "acme.tools"}},
		{"collections/acme/tools", formats.PathInfo{Kind: formats.PathMetadata, Package: "acme.tools"}},
		{"collections/acme", formats.PathInfo{Kind: formats.PathMetadata}},
		{"collections/Acme/tools/Acme-tools-1.0.0.tar.gz", formats.PathInfo{Kind: formats.PathMetadata}},
		{"collections/acme/tools/a/b", formats.PathInfo{Kind: formats.PathMetadata, Package: "acme.tools"}},
		{"api-root.json", formats.PathInfo{Kind: formats.PathMetadata}},
		{"roles/lookup.json", formats.PathInfo{Kind: formats.PathMetadata}},
		{"roles/16/versions.json", formats.PathInfo{Kind: formats.PathMetadata, Package: "role/16"}},
		{"roles/16/v1.0.0.tar.gz", formats.PathInfo{Kind: formats.Content, Package: "role/16"}},
		{"roles/16/v1.0.0.zip", formats.PathInfo{Kind: formats.PathMetadata, Package: "role/16"}},
		{"roles/16/bad ref.tar.gz", formats.PathInfo{Kind: formats.PathMetadata, Package: "role/16"}},
		{"roles/x/v1.0.0.tar.gz", formats.PathInfo{Kind: formats.PathMetadata}},
		{"roles/1234567890123/v1.tar.gz", formats.PathInfo{Kind: formats.PathMetadata}},
		{"other/x", formats.PathInfo{Kind: formats.PathMetadata}},
	} {
		if got := f.Classify(formats.Path{Path: tc.path}); got != tc.want {
			t.Errorf("Classify(%q) = %+v, want %+v", tc.path, got, tc.want)
		}
	}
	if got := ArtifactPath("acme", "tools", "1.0.0"); got != "collections/acme/tools/acme-tools-1.0.0.tar.gz" {
		t.Errorf("ArtifactPath = %q", got)
	}
}

// TestR_ANS_002_VersionPathClassifiesBack: Classify of the path VersionPath returns gives the name and the version back (Q-AUTHZ-255).
func TestR_ANS_002_VersionPathClassifiesBack(t *testing.T) {
	f := New()
	for _, tc := range []struct{ name, version string }{{"acme.tools", "1.0.0"}, {"acme.tools", "1.0.0-rc.1"}, {"a1.b2", "10.20.30+build"}} {
		p, ok := f.VersionPath(tc.name, tc.version)
		if !ok {
			t.Errorf("VersionPath(%q, %q) is not ok", tc.name, tc.version)
			continue
		}
		if info := f.Classify(p); info.Kind != formats.Content || info.Package != tc.name || info.Version != tc.version {
			t.Errorf("Classify(%q) = %+v", p.Path, info)
		}
	}
	for _, tc := range []struct{ name, version string }{{"acme", "1.0.0"}, {"acme.tools", "1.0"}, {"Acme.tools", "1.0.0"}, {"a.b.c", "1.0.0"}} {
		if _, ok := f.VersionPath(tc.name, tc.version); ok {
			t.Errorf("VersionPath(%q, %q) is ok", tc.name, tc.version)
		}
	}
	if _, ok := f.PackageDocumentPath("not a name"); ok {
		t.Error("PackageDocumentPath of a string that is no name is ok")
	}
}

func TestR_ANS_002_CanonicalPathOfARoute(t *testing.T) {
	for _, tc := range []struct {
		route  string
		params formats.Params
		want   string
		ok     bool
	}{
		{routeCollection, formats.Params{"ns": "acme", "name": "tools"}, "collections/acme/tools/collection.json", true},
		{routeVersions, formats.Params{"ns": "acme", "name": "tools"}, "collections/acme/tools/versions.json", true},
		{routeVersion, formats.Params{"ns": "acme", "name": "tools", "version": "1.0.0"}, "collections/acme/tools/1.0.0.json", true},
		{routeArtifact, formats.Params{"file": "acme-tools-1.0.0-rc.1.tar.gz"}, "collections/acme/tools/acme-tools-1.0.0-rc.1.tar.gz", true},
		{routeArtifact, formats.Params{"file": "nope.tar.gz"}, "", false},
		{routeRoot, formats.Params{}, "", false},
		{routePublish, formats.Params{}, "", false},
		{routeImport, formats.Params{"id": "x"}, "", false},
	} {
		got, ok := CanonicalPath(tc.route, tc.params)
		if got != tc.want || ok != tc.ok {
			t.Errorf("CanonicalPath(%s, %v) = %q %v, want %q %v", tc.route, tc.params, got, ok, tc.want, tc.ok)
		}
	}
}
