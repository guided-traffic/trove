package oci

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

func TestImageNamesFollowTheDistributionGrammar(t *testing.T) {
	for _, n := range []string{"app", "library/nginx", "team/app", "a__b/c-d", "a.b", "a--b", "0/1", "a/b/c/d/e", "x1.y2_z3"} {
		if !ValidName(n) {
			t.Errorf("%q is refused", n)
		}
	}
	for _, n := range []string{"", "App", "app/", "/app", "a//b", "a/./b", "a/../b", "-a", "a-", "a_", "a___b", "a._b", "a%2fb", "a b", "a:b", "a@sha256", "ä", "_hidden", "a/_tags/b"} {
		if ValidName(n) {
			t.Errorf("%q is accepted", n)
		}
	}
	t.Run("the whole name is bounded at 255 bytes", func(t *testing.T) {
		name := strings.Repeat("a", 255-len("web/docker/"))
		if !ValidFullName("web", "docker", name) || ValidFullName("web", "docker", name+"a") {
			t.Error("the bound is not 255 bytes")
		}
	})
}

func TestTagsAndDigests(t *testing.T) {
	for _, tag := range []string{"1", "latest", "v1.2.3-rc.1", "_x", "A", strings.Repeat("a", 128)} {
		if !ValidTag(tag) {
			t.Errorf("tag %q is refused", tag)
		}
	}
	for _, tag := range []string{"", "-x", ".x", "a:b", "a/b", strings.Repeat("a", 129), "ä"} {
		if ValidTag(tag) {
			t.Errorf("tag %q is accepted", tag)
		}
	}
	hex := strings.Repeat("ab", 32)
	for d, want := range map[string]bool{
		"sha256:" + hex:                      true,
		"sha256:" + strings.ToUpper(hex):     false,
		"sha256:" + hex[:63]:                 false,
		"sha256:" + hex + "0":                false,
		"sha512:" + strings.Repeat("ab", 64): false,
		hex:                                  false,
		"":                                   false,
		"sha256:../../x" + strings.Repeat("a", 51): false,
	} {
		if ValidDigest(d) != want {
			t.Errorf("ValidDigest(%q) = %v", d, !want)
		}
	}
}

func TestNamesNormalizeNeverFolds(t *testing.T) {
	n := names{}
	if got, ok := n.Normalize("team/app"); !ok || got != "team/app" {
		t.Errorf("Normalize = %q, %v", got, ok)
	}
	if _, ok := n.Normalize("Team/App"); ok {
		t.Error("an upper-case name was accepted: names are refused, never folded")
	}
	for _, pattern := range []string{"team/*", "te?m/app", "{a,b}/app", "team/[ab]*", "!team/*"} {
		if got, ok := n.Normalize(pattern); !ok || got != pattern {
			t.Errorf("pattern %q: %q, %v", pattern, got, ok)
		}
	}
}

func TestTheLayoutNeverCollidesWithAnImageName(t *testing.T) {
	digest := "sha256:" + strings.Repeat("0", 64)
	f := New()
	for _, tc := range []struct {
		path string
		want formats.PathInfo
	}{
		{ManifestPath("team/app", digest), formats.PathInfo{Kind: formats.Content, Package: "team/app"}},
		{BlobPath("app", digest), formats.PathInfo{Kind: formats.Content, Package: "app"}},
		{TagPath("a/b/c", "1.0"), formats.PathInfo{Kind: formats.PathMetadata, Package: "a/b/c", Version: "1.0"}},
		{TaglistPath("app"), formats.PathInfo{Kind: formats.PathMetadata, Package: "app"}},
		{"app", formats.PathInfo{Kind: formats.PathMetadata}},
		{"", formats.PathInfo{Kind: formats.PathMetadata}},
		{"app/_other/x", formats.PathInfo{Kind: formats.PathMetadata}},
		{"App/_tags/1", formats.PathInfo{Kind: formats.PathMetadata}},
		{"app/_tags/-bad", formats.PathInfo{Kind: formats.PathMetadata, Package: "app"}},
	} {
		if got := f.Classify(formats.Path{Path: tc.path}); got != tc.want {
			t.Errorf("Classify(%q) = %+v, want %+v", tc.path, got, tc.want)
		}
	}
	if p, ok := f.PackageDocumentPath("team/app"); !ok || p.Path != "team/app/_taglist" {
		t.Errorf("PackageDocumentPath = %v, %v", p, ok)
	}
	if _, ok := f.PackageDocumentPath("Team"); ok {
		t.Error("a document path for a name outside the grammar")
	}
	// Segments that start with '_' cannot be name components, so no image name is a layout path.
	for _, seg := range []string{"_manifests", "_tags", "_blobs", "_taglist"} {
		if ValidName("app/" + seg) {
			t.Errorf("%q is a valid image name", "app/"+seg)
		}
	}
}

func entry(t *testing.T) catalog.Entry {
	t.Helper()
	reg := catalog.NewRegistry()
	reg.Register(formats.CatalogEntry(New()))
	e, _ := reg.Lookup("oci")
	return e
}

func TestMirrorSettingsSchema(t *testing.T) {
	e := entry(t)
	valid := []string{
		`{}`,
		`{"mirror":{}}`,
		`{"mirror":{"tags":["latest"],"excludeTags":[],"platforms":[]}}`,
		`{"mirror":{"tags":["1.*","stable","v?"],"excludeTags":["*-rc*"],"platforms":["linux/amd64","linux/arm64/v8"]}}`,
	}
	for _, doc := range valid {
		if v := e.ValidateSettings(formats.Remote, []byte(doc)); len(v) != 0 {
			t.Errorf("%s: %+v", doc, v)
		}
	}
	for doc, pointer := range map[string]string{
		`{"mirror":{"platforms":["linux"]}}`:                       "/mirror/platforms/0",
		`{"mirror":{"platforms":["Linux/amd64"]}}`:                 "/mirror/platforms/0",
		`{"mirror":{"tags":[]}}`:                                   "/mirror/tags",
		`{"mirror":{"tags":["a b"]}}`:                              "/mirror/tags/0",
		`{"mirror":{"tags":["a","a"]}}`:                            "/mirror/tags",
		`{"mirror":{"excludeTags":["a/b"]}}`:                       "/mirror/excludeTags/0",
		`{"mirror":{"tags":[1]}}`:                                  "/mirror/tags/0",
		`{"mirror":{"unknown":true}}`:                              "/mirror",
		`{"other":1}`:                                              "",
		`{"mirror":{"tags":["` + strings.Repeat("a", 129) + `"]}}`: "/mirror/tags/0",
	} {
		v := e.ValidateSettings(formats.Remote, []byte(doc))
		if len(v) == 0 {
			t.Errorf("%s: accepted", doc)
			continue
		}
		found := false
		for _, x := range v {
			found = found || x.Pointer == pointer
		}
		if !found {
			t.Errorf("%s: violations %+v, none at %q", doc, v, pointer)
		}
	}
	t.Run("too many patterns", func(t *testing.T) {
		tags := make([]string, 65)
		for i := range tags {
			tags[i] = "t" + strings.Repeat("a", i%5) + string(rune('a'+i%26)) + string(rune('a'+i/26))
		}
		doc := `{"mirror":{"tags":["` + strings.Join(tags, `","`) + `"]}}`
		if v := e.ValidateSettings(formats.Remote, []byte(doc)); len(v) == 0 {
			t.Error("65 patterns accepted")
		}
	})
	t.Run("the mirror settings apply to remote repositories only", func(t *testing.T) {
		for _, class := range []formats.Class{formats.Local, formats.Virtual} {
			v := e.ValidateSettings(class, []byte(`{"mirror":{"tags":["latest"]}}`))
			if len(v) != 1 || v[0].Code != catalog.SettingNotApplicable || v[0].Pointer != "/mirror" {
				t.Errorf("%s: %+v", class, v)
			}
		}
	})
}

func TestMirrorSettingsDefaults(t *testing.T) {
	want := MirrorSettings{Tags: []string{"latest"}, ExcludeTags: []string{}, Platforms: []string{}}
	for _, cfg := range []string{``, `{}`, `{"oci":{}}`, `{"oci":{"mirror":{}}}`, `not json`} {
		if got := mirrorSettingsOf([]byte(cfg)); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %+v", cfg, got)
		}
	}
	got := mirrorSettingsOf([]byte(`{"oci":{"mirror":{"tags":["1.*"],"excludeTags":["*-rc"],"platforms":["linux/amd64"]}}}`))
	if !reflect.DeepEqual(got, MirrorSettings{Tags: []string{"1.*"}, ExcludeTags: []string{"*-rc"}, Platforms: []string{"linux/amd64"}}) {
		t.Errorf("set: %+v", got)
	}
}

// The expressions of the distribution specification v1.1.1, copied from its text: the name grammar and the tag and digest forms.
func TestGrammarsEqualTheDistributionSpecificationExpressions(t *testing.T) {
	spec := regexp.MustCompile(`^[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*(\/[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*)*$`)
	tagSpec := regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$`)
	alphabet := []byte{'a', '0', '.', '_', '-', '/', 'A', ':'}
	var walk func(prefix []byte, depth int)
	count := 0
	walk = func(prefix []byte, depth int) {
		s := string(prefix)
		if ValidName(s) != spec.MatchString(s) {
			t.Fatalf("ValidName(%q) = %v, the specification says %v", s, ValidName(s), spec.MatchString(s))
		}
		if ValidTag(s) != tagSpec.MatchString(s) {
			t.Fatalf("ValidTag(%q) = %v, the specification says %v", s, ValidTag(s), tagSpec.MatchString(s))
		}
		count++
		if depth > 0 {
			for _, c := range alphabet {
				walk(append(prefix, c), depth-1)
			}
		}
	}
	walk(nil, 6)
	if count < 200_000 {
		t.Fatalf("checked %d strings", count)
	}
}
