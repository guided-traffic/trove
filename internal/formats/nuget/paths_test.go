package nuget

import (
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
)

// TestR_NUGET_015_ClassifyIsTheRequestDescriptorOfACanonicalPath holds Classify to the table of
// R-NUGET-015: a package file is content with its package and version, a version list or a
// registration document is metadata of its package, the discovery document and the
// vulnerability pages name no package, a symbol file carries its routing name; a path it cannot
// classify is metadata.
func TestR_NUGET_015_ClassifyIsTheRequestDescriptorOfACanonicalPath(t *testing.T) {
	f := New()
	content := func(pkg, ver string) formats.PathInfo {
		return formats.PathInfo{Kind: formats.Content, Package: pkg, Version: ver}
	}
	meta := func(pkg, ver string) formats.PathInfo {
		return formats.PathInfo{Kind: formats.PathMetadata, Package: pkg, Version: ver}
	}
	cases := []struct {
		path string
		want formats.PathInfo
	}{
		{"", formats.PathInfo{Kind: formats.PathComputed}},
		{"acme.widgets", meta("acme.widgets", "")},
		{"acme.widgets/index.json", meta("acme.widgets", "")},
		{"acme.widgets/1.2.3", meta("acme.widgets", "1.2.3")},
		{"acme.widgets/1.2.3.0", meta("acme.widgets", "")},
		{"acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg", content("acme.widgets", "1.2.3")},
		{"acme.widgets/1.2.3/acme.widgets.nuspec", content("acme.widgets", "1.2.3")},
		{"acme.widgets/1.2.3/acme.widgets.1.2.3.snupkg", content("acme.widgets", "1.2.3")},
		{"acme.widgets/1.0.0-rc.1/acme.widgets.1.0.0-rc.1.nupkg", content("acme.widgets", "1.0.0-rc.1")},
		{"microsoft.extensions.logging/8.0.0/microsoft.extensions.logging.8.0.0.nupkg", content("microsoft.extensions.logging", "8.0.0")},
		{"acme.widgets/1.2.3/other.1.2.3.nupkg", content("acme.widgets", "")},
		{"acme.widgets/1.2.3/acme.widgets.1.2.4.nupkg", content("acme.widgets", "")},
		{"acme.widgets/1.2.3/notes.txt", meta("acme.widgets", "")},
		{"acme.widgets/1.2.3.0/acme.widgets.1.2.3.nupkg", meta("acme.widgets", "")},
		{"acme.widgets/1.2.3/.symbols/acme.widgets.pdb", content("acme.widgets", "1.2.3")},
		{"acme.widgets/1.2.3/.embedded/icon", content("acme.widgets", "1.2.3")},
		// The README is wiki content, served by a Metadata route (R-NUGET-024, R-AUTHZ-034).
		{"acme.widgets/1.2.3/.embedded/readme", formats.PathInfo{Kind: formats.PathMetadata, Package: "acme.widgets", Version: "1.2.3"}},
		{"acme.widgets/1.2.3/.embedded/other", meta("acme.widgets", "")},
		{"acme.widgets/.registration/index.json", meta("acme.widgets", "")},
		{"acme.widgets/.registration/page/0.json", meta("acme.widgets", "")},
		{"Acme.Widgets/index.json", formats.PathInfo{Kind: formats.PathMetadata}},
		{"a..b/index.json", formats.PathInfo{Kind: formats.PathMetadata}},
		{".nuget/index.json", formats.PathInfo{Kind: formats.PathMetadata}},
		{".vulnerabilities/index.json", formats.PathInfo{Kind: formats.PathMetadata}},
		{".vulnerabilities/base.json", formats.PathInfo{Kind: formats.PathMetadata}},
		{".symbols", formats.PathInfo{Kind: formats.PathMetadata}},
		{".symbols/acme.widgets.pdb/0123456789abcdef0123456789abcdefFFFFFFFF/acme.widgets.pdb", formats.PathInfo{Kind: formats.Content, RoutingName: "acme.widgets"}},
		{".symbols/Acme.Widgets.dll/0123456789abcdef0123456789abcdefFFFFFFFF/Acme.Widgets.dll", formats.PathInfo{Kind: formats.Content, RoutingName: "acme.widgets"}},
		{".symbols/mscorlib.pd_/abc/mscorlib.pd_", formats.PathInfo{Kind: formats.Content, RoutingName: "mscorlib"}},
	}
	for _, c := range cases {
		t.Run("R-NUGET-015 "+c.path, func(t *testing.T) {
			if got := f.Classify(formats.Path{Path: c.path}); got != c.want {
				t.Errorf("Classify(%q) = %+v, want %+v", c.path, got, c.want)
			}
		})
	}
}

// TestR_NUGET_005_ThePathHelpersAreTheLayoutClassifyUnderstands: the canonical paths the
// writers build are classified as the storage layout says, for IDs with dots, pre-releases
// and a fourth version part.
func TestR_NUGET_005_ThePathHelpersAreTheLayoutClassifyUnderstands(t *testing.T) {
	f := New()
	for _, c := range []struct{ id, ver string }{
		{"acme.widgets", "1.2.3"},
		{"newtonsoft.json", "13.0.3"},
		{"microsoft.extensions.logging.abstractions", "9.0.0-preview.1.24080.9"},
		{"a", "1.0.0.1"},
		{"acme-core_x", "2.0.0-rc.1"},
		{"müller.logging", "1.0.0"},
	} {
		for name, p := range map[string]string{
			"nupkg": NupkgPath(c.id, c.ver), "nuspec": NuspecPath(c.id, c.ver), "snupkg": SnupkgPath(c.id, c.ver),
		} {
			want := formats.PathInfo{Kind: formats.Content, Package: c.id, Version: c.ver}
			if got := f.Classify(formats.Path{Path: p}); got != want {
				t.Errorf("%s %s %s: Classify(%q) = %+v, want %+v", c.id, c.ver, name, p, got, want)
			}
		}
		doc := PackageDocumentPath(c.id)
		if got := f.Classify(formats.Path{Path: doc}); got.Kind != formats.PathMetadata || got.Package != c.id {
			t.Errorf("Classify(%q) = %+v", doc, got)
		}
		if got, ok := f.PackageDocumentPath(c.id); !ok || got.Path != doc {
			t.Errorf("PackageDocumentPath(%q) = %q, %v, want %q", c.id, got.Path, ok, doc)
		}
	}
}

// TestR_NUGET_003_PackageNames: the name rule is the widest one the format accepts (the upstream
// rule), folded to the ASCII-lowercased ID; a string with a character of the pattern syntax is a
// name pattern and is folded as it is (R-AUTHZ-005).
func TestR_NUGET_003_PackageNames(t *testing.T) {
	n := names{}
	good := map[string]string{
		"Acme.Widgets": "acme.widgets", "ACME.WIDGETS": "acme.widgets", "acme.widgets": "acme.widgets", "Newtonsoft.Json": "newtonsoft.json",
		"Müller.Logging": "müller.logging", "Acme-Core_X": "acme-core_x", "A": "a",
		"Acme.*": "acme.*", "ACME.{A,B}": "acme.{a,b}", "!Acme.Widgets": "!acme.widgets", "*": "*", "Acme.?idgets": "acme.?idgets",
	}
	for in, want := range good {
		got, ok := n.Normalize(in)
		if !ok || got != want {
			t.Errorf("Normalize(%q) = %q, %v, want %q", in, got, ok, want)
		}
		if again, ok := n.Normalize(got); !ok || again != got {
			t.Errorf("Normalize is not idempotent on %q: %q, %v", got, again, ok)
		}
	}
	for _, in := range []string{"", "Acme..Widgets", ".Acme", "Acme.", "Acme Widgets", "Acme/Widgets", "Acme\nWidgets", "Acme.Widgets\n", "\xff", "a\x00b"} {
		if got, ok := n.Normalize(in); ok {
			t.Errorf("Normalize(%q) = %q, want no name", in, got)
		}
	}
	if _, ok := New().PackageDocumentPath("Acme..Widgets"); ok {
		t.Error("PackageDocumentPath accepts a string that is no ID")
	}
	if p, ok := New().PackageDocumentPath("Acme.Widgets"); !ok || p.Path != "acme.widgets/index.json" {
		t.Errorf("PackageDocumentPath = %q, %v", p.Path, ok)
	}
}

// TestR_NUGET_015_TheRoutingNameOfASymbolKey: the first segment of the key without its last
// extension, lowercased, for every key shape (R-NUGET-015).
func TestR_NUGET_015_TheRoutingNameOfASymbolKey(t *testing.T) {
	for key, want := range map[string]string{
		"acme.widgets.pdb/ABC/acme.widgets.pdb": "acme.widgets",
		"Acme.Widgets.dll/ABC/Acme.Widgets.dll": "acme.widgets",
		"acme.widgets.pd_/ABC/acme.widgets.pd_": "acme.widgets",
		"noextension/ABC/noextension":           "noextension",
		".hidden/ABC/.hidden":                   ".hidden",
		"a.b.c":                                 "a.b",
	} {
		if got := routingName(key); got != want {
			t.Errorf("routingName(%q) = %q, want %q", key, got, want)
		}
	}
}
