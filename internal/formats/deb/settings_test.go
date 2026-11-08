package deb

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// entry is the catalog entry of the format with the classes of the full format, so that the settings of every class are validated.
func entry(t *testing.T) catalog.Entry {
	t.Helper()
	reg := catalog.NewRegistry()
	e := formats.CatalogEntry(allClasses())
	reg.Register(e)
	got, ok := reg.Lookup("deb")
	if !ok {
		t.Fatal("the format is not in the catalog")
	}
	return got
}

type violation struct{ pointer, code string }

func violations(t *testing.T, class catalog.Class, doc string) []violation {
	t.Helper()
	var out []violation
	for _, v := range entry(t).ValidateSettings(class, json.RawMessage(doc)) {
		out = append(out, violation{v.Pointer, v.Code})
	}
	return out
}

// The settings of R-DEB-008 and R-DEB-024, validated by the schema the repository service holds a document to.
func TestSettingsSchema(t *testing.T) {
	big := strings.Repeat("A", 262145)
	cases := []struct {
		name  string
		class catalog.Class
		doc   string
		want  []violation
	}{
		{"defaults of a local repository", formats.Local, `{"architectures":["amd64","arm64"],"compression":[],"origin":"trove","label":"deb-local"}`, nil},
		{"xz", formats.Local, `{"compression":["xz"]}`, nil},
		{"bzip2 is not offered", formats.Local, `{"compression":["bz2"]}`, []violation{{"/compression/0", "invalid_value"}}},
		{"all is not an architecture setting", formats.Local, `{"architectures":["all"]}`, []violation{{"/architectures/0", "invalid_value"}}},
		{"no architecture", formats.Local, `{"architectures":[]}`, []violation{{"/architectures", "invalid_value"}}},
		{"more than 32 architectures", formats.Local, `{"architectures":[` + archNames(33) + `]}`, []violation{{"/architectures", "invalid_value"}}},
		{"an origin with a colon", formats.Local, `{"origin":"a:b"}`, []violation{{"/origin", "invalid_value"}}},
		{"an origin longer than 64", formats.Local, `{"origin":"` + strings.Repeat("a", 65) + `"}`, []violation{{"/origin", "invalid_value"}}},
		{"suites of a virtual repository", formats.Virtual, `{"suites":["stable","testing"],"components":["main","contrib"]}`, nil},
		{"suites of a local repository", formats.Local, `{"suites":["stable"]}`, []violation{{"/suites", "not_applicable"}}},
		{"an upper-case suite", formats.Virtual, `{"suites":["Stable"]}`, []violation{{"/suites/0", "invalid_value"}}},
		{"keys of a local repository", formats.Local, `{"upstreamKeys":"x"}`, []violation{{"/upstreamKeys", "not_applicable"}}},
		{"keys of a remote", formats.Remote, `{"upstreamKeys":"-----BEGIN PGP PUBLIC KEY BLOCK-----"}`, nil},
		{"null keys of a remote", formats.Remote, `{"upstreamKeys":null}`, nil},
		{"keys above 256 KiB", formats.Remote, `{"upstreamKeys":"` + big + `"}`, []violation{{"/upstreamKeys", "invalid_value"}}},
		{
			"mirror settings of a remote", formats.Remote,
			`{"mirror":{"suites":["bookworm","bookworm-updates"],"components":["main"],"architectures":["amd64"],"translations":["en","pt_BR"],"sources":true}}`, nil,
		},
		{"all in the mirror architectures", formats.Remote, `{"mirror":{"architectures":["all"]}}`, []violation{{"/mirror/architectures/0", "invalid_value"}}},
		{"a mirror of a local repository", formats.Local, `{"mirror":{}}`, []violation{{"/mirror", "not_applicable"}}},
		{"an unknown member", formats.Local, `{"nope":1}`, []violation{{"", "invalid_value"}}},
		{"an unknown mirror member", formats.Remote, `{"mirror":{"nope":1}}`, []violation{{"/mirror", "invalid_value"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := violations(t, c.class, c.doc)
			if len(c.want) == 0 && len(got) == 0 {
				return
			}
			for _, w := range c.want {
				if !contains(got, w) {
					t.Fatalf("violations = %v, want %v among them", got, w)
				}
			}
			if len(c.want) == 0 {
				t.Fatalf("violations = %v, want none", got)
			}
		})
	}
}

func archNames(n int) string {
	var b []string
	for i := range n {
		b = append(b, `"a`+string(rune('a'+i%26))+string(rune('a'+i/26))+`"`)
	}
	return strings.Join(b, ",")
}

func contains(list []violation, v violation) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// The defaults of R-DEB-008 and R-DEB-024 for what a document leaves out.
func TestSettingsOfDefaults(t *testing.T) {
	want := Settings{
		Architectures: []string{"amd64", "arm64"}, Compression: []string{}, Origin: "trove", Suites: []string{}, Components: []string{"main"},
		Mirror: MirrorSettings{Suites: []string{}, Components: []string{"main"}, Architectures: []string{"amd64"}, Translations: []string{"en"}},
	}
	for _, member := range []json.RawMessage{nil, json.RawMessage(`{}`), json.RawMessage(`not json`)} {
		if got := SettingsOf(member); !reflect.DeepEqual(got, want) {
			t.Errorf("SettingsOf(%q) = %+v, want %+v", member, got, want)
		}
	}
	got := SettingsOf(json.RawMessage(`{"architectures":["riscv64"],"label":"mine","mirror":{"suites":["noble"],"sources":true}}`))
	if got.Architectures[0] != "riscv64" || got.Label != "mine" || got.Origin != "trove" || got.Mirror.Suites[0] != "noble" || !got.Mirror.Sources || got.Mirror.Components[0] != "main" {
		t.Errorf("SettingsOf = %+v", got)
	}
}
