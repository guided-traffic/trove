//go:build integration

package projects

import (
	"fmt"
	"testing"
	"time"
)

// TestR_REPO_063_TheDefaultsOfANewRepository: over HTTP, with real roles and the installation's settings, an Owner of an internal
// project of an installation whose default is internal, who holds no settings:read, reads the defaults of a new repository: the
// visibility it may not read elsewhere (D13 of the T-21b plan), the default preset of a remote with its hosts and the format's
// periods, and a key that is free; a reader of the project is refused, an unknown project is 404, and nothing is written.
func TestR_REPO_063_TheDefaultsOfANewRepository(t *testing.T) {
	f := newFixture(t)
	wantStatus(t, f.do(f.admin, "PATCH", settingsPath, map[string]any{"defaultLevel": "internal"},
		"Content-Type", "application/merge-patch+json", "If-Match", "*"), 200, "")
	f.create("web", map[string]any{"visibility": "internal"})
	wantStatus(t, f.member("web", "user:alice", "owner"), 201, "")
	wantStatus(t, f.member("web", "user:bob", "reader"), 201, "")
	defaults := func(format, class string) string {
		return fmt.Sprintf("%s/web/repository-defaults?format=%s&class=%s", base, format, class)
	}
	// The roles reach both replicas through the change log: wait until each one answers the Owner.
	deadline := time.Now().Add(15 * time.Second)
	for streak := 0; streak < 6; {
		if got := f.do(f.alice, "GET", defaults("oci", "remote"), nil); got.Status == 200 {
			streak++
		} else if streak = 0; time.Now().After(deadline) {
			t.Fatalf("the Owner is not answered by both replicas: %d %s", got.Status, got.Body)
		}
	}
	events := f.e.Number("SELECT count(*) FROM audit_event")
	repos := f.e.Number("SELECT count(*) FROM repositories")

	t.Run("R-REPO-063 an Owner without settings:read reads the level, the preset and the periods of an OCI remote", func(t *testing.T) {
		r := f.do(f.alice, "GET", defaults("oci", "remote"), nil)
		wantStatus(t, r, 200, "")
		body := r.JSON(t)
		repo := body["repository"].(map[string]any)
		if body["suggestedKey"] != "oci-remote" || repo["key"] != nil || repo["class"] != "remote" || repo["format"] != "oci" {
			t.Fatalf("answer = %s", r.Body)
		}
		if got := fmt.Sprint(repo["visibility"]); got != "map[artifacts:internal wiki:internal]" {
			t.Errorf("visibility = %s", got)
		}
		remote := repo["remote"].(map[string]any)
		if remote["url"] != "https://registry-1.docker.io/" || fmt.Sprint(remote["extraHosts"]) != "[map[pattern:auth.docker.io sendCredentials:true]]" {
			t.Errorf("url %v, extraHosts %v", remote["url"], remote["extraHosts"])
		}
		cache := remote["cache"].(map[string]any)
		mirror := remote["mirror"].(map[string]any)
		if cache["metadataTtlSeconds"] != float64(300) || mirror["intervalSeconds"] != float64(3600) || remote["mode"] != "pullThrough" {
			t.Errorf("cache %v, mirror %v, mode %v", cache, mirror, remote["mode"])
		}
	})
	t.Run("R-REPO-063 the suggestion skips a key the project holds", func(t *testing.T) {
		wantStatus(t, f.do(f.admin, "POST", base+"/web/repositories", map[string]any{"key": "npm-local", "class": "local", "format": "npm"}), 201, "")
		repos++
		r := f.do(f.alice, "GET", defaults("npm", "local"), nil)
		wantStatus(t, r, 200, "")
		body := r.JSON(t)
		repo := body["repository"].(map[string]any)
		if body["suggestedKey"] != "npm-local-2" || repo["remote"] != nil {
			t.Fatalf("answer = %s", r.Body)
		}
		if v := f.do(f.alice, "GET", defaults("npm", "virtual"), nil).JSON(t)["repository"].(map[string]any)["virtual"].(map[string]any); fmt.Sprint(v["members"]) != "[]" || v["deployTarget"] != nil {
			t.Errorf("virtual = %v", v)
		}
	})
	t.Run("R-REPO-063 a reader of the project gets 403, an unknown project 404, a class the format lacks 422", func(t *testing.T) {
		wantStatus(t, f.do(f.bob, "GET", defaults("oci", "remote"), nil), 403, "")
		wantStatus(t, f.do(f.admin, "GET", base+"/nope/repository-defaults?format=oci&class=remote", nil), 404, "")
		wantStatus(t, f.do(f.admin, "GET", defaults("generic", "remote"), nil), 422, "unsupported_class")
		wantStatus(t, f.do(f.admin, "GET", defaults("cobol", "local"), nil), 422, "unknown_format")
	})
	t.Run("R-REPO-063 the defaults write nothing", func(t *testing.T) {
		if got := f.e.Number("SELECT count(*) FROM repositories"); got != repos {
			t.Errorf("%d repositories, want %d", got, repos)
		}
		// The one create of the subtest above is the only event since the reads began.
		if got := f.e.Number("SELECT count(*) FROM audit_event"); got != events+1 {
			t.Errorf("%d audit events, want %d", got, events+1)
		}
	})
}
