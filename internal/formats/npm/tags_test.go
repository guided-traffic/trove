package npm

import (
	"maps"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/npm/name"
)

func TestR_NPM_016_TagAfterPublish(t *testing.T) {
	type tags = map[string]string
	cases := []struct {
		name    string
		cur     tags
		tag     string
		version string
		guard   bool
		want    tags
		kept    bool
	}{
		{"a first publish under latest", nil, "latest", "1.0.0", true, tags{"latest": "1.0.0"}, false},
		{"a first publish under another tag also sets latest", nil, "next", "1.0.0-rc.1", true, tags{"latest": "1.0.0-rc.1", "next": "1.0.0-rc.1"}, false},
		{"a higher version moves latest", tags{"latest": "1.0.0"}, "latest", "1.1.0", true, tags{"latest": "1.1.0"}, false},
		{"a lower version keeps latest", tags{"latest": "2.0.0"}, "latest", "1.1.5", true, tags{"latest": "2.0.0"}, true},
		{"a pre-release does not take latest from a release", tags{"latest": "1.0.0"}, "latest", "2.0.0-rc.1", true, tags{"latest": "1.0.0"}, true},
		{"a pre-release may follow a pre-release", tags{"latest": "2.0.0-rc.1"}, "latest", "2.0.0-rc.2", true, tags{"latest": "2.0.0-rc.2"}, false},
		{"a release may follow a pre-release", tags{"latest": "2.0.0-rc.1"}, "latest", "2.0.0", true, tags{"latest": "2.0.0"}, false},
		{"the guard off moves latest back", tags{"latest": "2.0.0"}, "latest", "1.1.5", false, tags{"latest": "1.1.5"}, false},
		{"another tag leaves latest", tags{"latest": "2.0.0"}, "beta", "3.0.0-beta.1", true, tags{"latest": "2.0.0", "beta": "3.0.0-beta.1"}, false},
		{"a tag moves", tags{"latest": "2.0.0", "beta": "3.0.0-beta.1"}, "beta", "3.0.0-beta.2", true, tags{"latest": "2.0.0", "beta": "3.0.0-beta.2"}, false},
	}
	for _, c := range cases {
		t.Run("R-NPM-016 "+c.name, func(t *testing.T) {
			before := maps.Clone(c.cur)
			got, kept := tagAfterPublish(c.cur, c.tag, c.version, c.guard)
			if !maps.Equal(got, c.want) || kept != c.kept {
				t.Fatalf("tags %v kept %v, want %v %v", got, kept, c.want, c.kept)
			}
			if !maps.Equal(before, c.cur) {
				t.Fatal("the input map was changed")
			}
		})
	}
}

func TestR_NPM_016_TagsAfterRemoval(t *testing.T) {
	type tags = map[string]string
	cases := []struct {
		name      string
		cur       tags
		removed   string
		remaining []string
		want      tags
	}{
		{"latest moves to the highest release", tags{"latest": "2.0.0"}, "2.0.0", []string{"1.0.0", "1.5.0", "1.5.1-rc.1"}, tags{"latest": "1.5.0"}},
		{"latest moves to the highest pre-release when all are", tags{"latest": "2.0.0"}, "2.0.0", []string{"1.0.0-rc.1", "1.0.0-rc.2"}, tags{"latest": "1.0.0-rc.2"}},
		{"other tags on the version go", tags{"latest": "2.0.0", "next": "2.0.0", "beta": "3.0.0-beta.1"}, "2.0.0", []string{"1.0.0"}, tags{"latest": "1.0.0", "beta": "3.0.0-beta.1"}},
		{"a tag elsewhere stays", tags{"latest": "2.0.0", "old": "1.0.0"}, "1.0.0", []string{"2.0.0"}, tags{"latest": "2.0.0"}},
		{"the last version leaves no latest", tags{"latest": "1.0.0", "x": "1.0.0"}, "1.0.0", nil, tags{}},
		{"an untagged version changes nothing", tags{"latest": "2.0.0"}, "1.0.0", []string{"2.0.0"}, tags{"latest": "2.0.0"}},
	}
	for _, c := range cases {
		t.Run("R-NPM-016 "+c.name, func(t *testing.T) {
			before := maps.Clone(c.cur)
			got := tagsAfterRemoval(c.cur, c.removed, c.remaining)
			if !maps.Equal(got, c.want) {
				t.Fatalf("tags %v, want %v", got, c.want)
			}
			if !maps.Equal(before, c.cur) {
				t.Fatal("the input map was changed")
			}
		})
	}
}

func TestR_NPM_015_FileNames(t *testing.T) {
	n, _ := name.Parse("@acme/ui")
	if got := TarballPath(n, "1.2.0"); got != "@acme/ui/-/ui-1.2.0.tgz" {
		t.Errorf("TarballPath = %q", got)
	}
	if got := SigstorePath(n, "1.2.0"); got != "@acme/ui/-/ui-1.2.0.sigstore" {
		t.Errorf("SigstorePath = %q", got)
	}
	for _, c := range []struct {
		file, suffix, want string
		ok                 bool
	}{
		{"ui-1.2.0.tgz", ".tgz", "1.2.0", true},
		{"ui-1.2.0-rc.1.tgz", ".tgz", "1.2.0-rc.1", true},
		{"ui-1.2.0.sigstore", ".sigstore", "1.2.0", true},
		{"other-1.2.0.tgz", ".tgz", "", false},
		{"ui-1.2.tgz", ".tgz", "", false},
		{"ui-v1.2.0.tgz", ".tgz", "", false},
		{"ui-1.2.0+b.tgz", ".tgz", "", false},
		{"ui-1.2.0.tgz", ".sigstore", "", false},
		{"ui-.tgz", ".tgz", "", false},
	} {
		got, ok := VersionOfFile(n, c.file, c.suffix)
		if got != c.want || ok != c.ok {
			t.Errorf("VersionOfFile(%q, %q) = %q %v, want %q %v", c.file, c.suffix, got, ok, c.want, c.ok)
		}
	}
}

func TestR_NPM_009_RevIsOpaqueAndBoundToThePackage(t *testing.T) {
	a, b := Rev(1, 10, 3), Rev(1, 10, 3)
	if a != b {
		t.Fatal("not deterministic")
	}
	if a[:2] != "3-" || len(a) != 2+32 {
		t.Fatalf("%q", a)
	}
	for _, other := range []string{Rev(2, 10, 3), Rev(1, 11, 3), Rev(1, 10, 4)} {
		if other == a {
			t.Fatalf("collision %q", other)
		}
	}
}
