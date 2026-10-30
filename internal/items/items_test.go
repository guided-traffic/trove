package items

import (
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/repository"
)

func TestPathHelpers(t *testing.T) {
	t.Run("R-STOR-003 the ancestors of a path are its implicit folders, from the top", func(t *testing.T) {
		for in, want := range map[string][]string{
			"a":       nil,
			"a/b":     {"a"},
			"a/b/c/d": {"a", "a/b", "a/b/c"},
		} {
			if got := ancestors(in); !slices.Equal(got, want) {
				t.Errorf("ancestors(%q) = %v, want %v", in, got, want)
			}
		}
	})
	t.Run("R-STOR-003 the parent of a top-level entry is empty", func(t *testing.T) {
		for in, want := range map[string]string{"a": "", "a/b": "a", "a/b/c": "a/b"} {
			if got := ParentOf(in); got != want {
				t.Errorf("ParentOf(%q) = %q, want %q", in, got, want)
			}
		}
	})
	t.Run("R-STOR-018 the subtree of a folder is the range from its slash to the byte after it", func(t *testing.T) {
		lo, hi := subtreeBounds("a/b")
		if lo != "a/b/" || hi != "a/b0" {
			t.Errorf("bounds = %q, %q", lo, hi)
		}
		// "a/b-c" sorts after "a/b/" in the C collation? '-' (0x2D) < '/' (0x2F): outside the range.
		for _, in := range []string{"a/b/x", "a/b/x/y", "a/b/\xff"} {
			if in < lo || in >= hi {
				t.Errorf("%q is below a/b but outside [%q, %q)", in, lo, hi)
			}
		}
		for _, out := range []string{"a/b", "a/b-c", "a/b.x", "a/b0", "a/bb", "a/c"} {
			if out >= lo && out < hi {
				t.Errorf("%q is not below a/b but inside [%q, %q)", out, lo, hi)
			}
		}
		if lo, hi := subtreeBounds(""); lo != "" || hi != "" {
			t.Errorf("the root has no bounds: %q, %q", lo, hi)
		}
	})
	t.Run("R-STOR-007 an actor id is used only when it is a uuid", func(t *testing.T) {
		id := func(s string) string { return actorID(audit.Actor{ID: s}) }
		if id("0198a000-0000-7000-8000-000000000001") == "" || id("admin") != "" || id("") != "" || id("0198a000-0000-7000-8000-00000000000g") != "" {
			t.Error("actorID")
		}
	})
}

func TestRepoOf(t *testing.T) {
	t.Run("R-STOR-003 the id of a repository read model is the decimal text of its row", func(t *testing.T) {
		r := RepoOf(repository.Repository{ID: "42", Key: "generic-local", Class: repository.Local, Format: "generic"})
		if r != (RepoRef{ID: 42, Key: "generic-local", Class: "local", Format: "generic"}) {
			t.Errorf("RepoOf = %+v", r)
		}
		if got := RepoOf(repository.Repository{ID: "4x2"}).ID; got != 0 {
			t.Errorf("a id that is not a number matches no item: %d", got)
		}
	})
}

func TestOriginNames(t *testing.T) {
	t.Run("R-ARCH-009 the two origins of a commit have distinct names", func(t *testing.T) {
		if OriginUpload.String() != "upload" || OriginFill.String() != "fill" || Origin(0).String() != "unknown" {
			t.Errorf("%s %s %s", OriginUpload, OriginFill, Origin(0))
		}
	})
}

func TestUpstreamURLCarriesNoCredentials(t *testing.T) {
	t.Run("R-REPO-025 an upstream url is stored without user info, and a value that is no url is dropped", func(t *testing.T) {
		for in, want := range map[string]string{
			"https://registry.example.org/w/-/w-1.tgz":                "https://registry.example.org/w/-/w-1.tgz",
			"https://bot:s3cret@registry.example.org/w/-/w-1.tgz?a=b": "https://registry.example.org/w/-/w-1.tgz?a=b",
			"":            "",
			"http://[::1": "",
		} {
			if got := withoutUserInfo(in); got != want {
				t.Errorf("withoutUserInfo(%q) = %q, want %q", in, got, want)
			}
		}
	})
}
