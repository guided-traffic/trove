//go:build integration

package packages

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

func versionsOf(items []map[string]any) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i], _ = it["version"].(string)
	}
	return out
}

// TestR_API_029_VersionList: the versions of a package for the caller, in each of the
// documented sorts, filtered by pre-release and by repository.
func TestR_API_029_VersionList(t *testing.T) {
	e := newEnv(t)
	e.seed()
	id := e.idOf(e.tokE, "@team-a/ui")
	base := "/api/v1/packages/" + id + "/versions"

	for sort, want := range map[string][]string{
		"-publishedAt": {"1.5.0", "2.0.0-beta.1", "1.4.0", "1.3.0"},
		"publishedAt":  {"1.3.0", "1.4.0", "2.0.0-beta.1", "1.5.0"},
		// 2.0.0-beta.1 is above 1.5.0 by SemVer precedence.
		"-version": {"2.0.0-beta.1", "1.5.0", "1.4.0", "1.3.0"},
		"version":  {"1.3.0", "1.4.0", "1.5.0", "2.0.0-beta.1"},
	} {
		t.Run("sort="+sort+" and a cursor of one item", func(t *testing.T) {
			if got := versionsOf(e.all(e.tokE, base+"?sort="+sort)); !slices.Equal(got, want) {
				t.Errorf("whole: %v, want %v", got, want)
			}
			if got := versionsOf(e.all(e.tokE, base+"?limit=1&sort="+sort)); !slices.Equal(got, want) {
				t.Errorf("one at a time: %v, want %v", got, want)
			}
		})
	}
	t.Run("the default sort is -publishedAt", func(t *testing.T) {
		if got := versionsOf(e.all(e.tokE, base)); !slices.Equal(got, []string{"1.5.0", "2.0.0-beta.1", "1.4.0", "1.3.0"}) {
			t.Fatal(got)
		}
	})
	t.Run("an entry holds the members of the contract", func(t *testing.T) {
		items := e.all(e.tokE, base+"?sort=version")
		v := items[1] // 1.4.0, stored in both repositories
		if v["version"] != "1.4.0" || v["prerelease"] != false || num(v["sizeBytes"]) <= 0 || !slices.Equal(toStrings(v["repositories"]), []string{"npm-local", "npm-team"}) {
			t.Fatalf("%v", v)
		}
		if items[3]["prerelease"] != true {
			t.Fatalf("%v", items[3])
		}
	})
	t.Run("prerelease and repository filters", func(t *testing.T) {
		if got := versionsOf(e.all(e.tokE, base+"?prerelease=true")); !slices.Equal(got, []string{"2.0.0-beta.1"}) {
			t.Errorf("prerelease=true: %v", got)
		}
		if got := versionsOf(e.all(e.tokE, base+"?prerelease=false&sort=version")); !slices.Equal(got, []string{"1.3.0", "1.4.0", "1.5.0"}) {
			t.Errorf("prerelease=false: %v", got)
		}
		if got := versionsOf(e.all(e.tokE, base+"?repository=npm-team&sort=version")); !slices.Equal(got, []string{"1.4.0", "1.5.0"}) {
			t.Errorf("repository=npm-team: %v", got)
		}
		if got := versionsOf(e.all(e.tokB, base+"?repository=npm-team")); len(got) != 0 {
			t.Errorf("a repository the caller may not read lists %v", got)
		}
	})
	t.Run("a reader of npm-local sees the versions of npm-local only", func(t *testing.T) {
		if got := versionsOf(e.all(e.tokB, base+"?sort=version")); !slices.Equal(got, []string{"1.3.0", "1.4.0", "2.0.0-beta.1"}) {
			t.Fatal(got)
		}
		for _, v := range e.all(e.tokB, base) {
			if !slices.Equal(toStrings(v["repositories"]), []string{"npm-local"}) {
				t.Errorf("%v", v)
			}
		}
	})
	t.Run("a sort or a filter the document does not list answers 400", func(t *testing.T) {
		for _, q := range []string{"sort=name", "prerelease=maybe", "stable=true", "cursor=@@"} {
			if a := e.get(e.tokE, base+"?"+q); a.Status != 400 {
				t.Errorf("%s: %d %s", q, a.Status, a.Body)
			}
		}
	})
}

// TestR_API_029_VersionDetails: the details of a version carry the members of the
// contract, the files with their digests and a working download address.
func TestR_API_029_VersionDetails(t *testing.T) {
	e := newEnv(t)
	e.seed()
	e.publish("npm-local", spec{
		Name: "rich", Version: "3.2.1", Description: "A package with every member", License: "Apache-2.0",
		Extra: map[string]any{
			"dependencies": map[string]string{"lit": "^3.1.0"}, "devDependencies": map[string]string{"vitest": "^2"},
			"peerDependencies": map[string]string{"react": ">=18"}, "optionalDependencies": map[string]string{"fsevents": "^2"},
			"engines": map[string]string{"node": ">=20"}, "homepage": "https://example.com/rich",
			"author":     map[string]string{"name": "Ada", "email": "ada@example.com", "url": "https://example.com/ada"},
			"repository": map[string]string{"type": "git", "url": "git+https://example.com/rich.git"},
			"bugs":       map[string]string{"url": "https://example.com/rich/issues"},
		},
	})
	e.publish("npm-local", spec{Name: "rich", Version: "3.3.0-rc.1", Tag: "next"})
	id := e.idOf(e.tokE, "rich")
	d := e.get(e.tokE, "/api/v1/packages/"+id+"/versions/3.2.1")
	if d.Status != 200 {
		t.Fatalf("%d %s", d.Status, d.Body)
	}
	v := d.JSON(t)
	for k, want := range map[string]any{"packageId": id, "format": "npm", "name": "rich", "version": "3.2.1", "prerelease": false, "hasReadme": true} {
		if v[k] != want {
			t.Errorf("%s = %v, want %v", k, v[k], want)
		}
	}
	if !slices.Equal(toStrings(v["licenses"]), []string{"Apache-2.0"}) || num(v["sizeBytes"]) <= 0 || num(v["downloadCount"]) != 0 {
		t.Errorf("licenses %v size %v downloads %v", v["licenses"], v["sizeBytes"], v["downloadCount"])
	}
	md := v["metadata"].(map[string]any)
	if got := md["dependencies"].(map[string]any)["lit"]; got != "^3.1.0" {
		t.Errorf("dependencies %v", md["dependencies"])
	}
	for k, want := range map[string]any{"homepage": "https://example.com/rich", "repositoryUrl": "git+https://example.com/rich.git", "bugsUrl": "https://example.com/rich/issues"} {
		if md[k] != want {
			t.Errorf("%s = %v, want %v", k, md[k], want)
		}
	}
	if !slices.Equal(toStrings(md["authors"]), []string{"Ada <ada@example.com> (https://example.com/ada)"}) {
		t.Errorf("authors %v", md["authors"])
	}
	for _, k := range []string{"devDependencies", "peerDependencies", "optionalDependencies", "engines", "distTags"} {
		if _, ok := md[k]; !ok {
			t.Errorf("metadata.%s is missing: %v", k, md)
		}
	}
	if _, ok := md["deprecated"]; ok {
		t.Errorf("a member that does not apply is present: %v", md)
	}
	tags := md["distTags"].(map[string]any)
	if tags["latest"] != "3.2.1" || tags["next"] != "3.3.0-rc.1" {
		t.Errorf("distTags %v", tags)
	}
	repos := v["repositories"].([]any)
	if len(repos) != 1 || repos[0].(map[string]any)["key"] != "npm-local" || repos[0].(map[string]any)["class"] != "local" {
		t.Errorf("repositories %v", repos)
	}
	files := v["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("files %v", files)
	}
	f := files[0].(map[string]any)
	if f["path"] != "rich/-/rich-3.2.1.tgz" || f["downloadUrl"] != "http://trove.test/api/npm/npm-local/rich/-/rich-3.2.1.tgz" {
		t.Fatalf("file %v", f)
	}
	// The address works, and what it serves is what the digest says.
	dl := e.get(e.tokE, strings.TrimPrefix(f["downloadUrl"].(string), "http://trove.test"))
	sum := sha256.Sum256(dl.Body)
	if dl.Status != 200 || hex.EncodeToString(sum[:]) != f["sha256"] || int64(len(dl.Body)) != num(f["sizeBytes"]) {
		t.Fatalf("download %d, sha256 %s, size %d; the file says %v", dl.Status, hex.EncodeToString(sum[:]), len(dl.Body), f)
	}

	t.Run("a scoped name, escaped once in the address", func(t *testing.T) {
		ui := e.get(e.tokE, "/api/v1/packages/"+e.idOf(e.tokE, "@team-a/ui")+"/versions/1.5.0").JSON(t)
		f := ui["files"].([]any)[0].(map[string]any)
		if f["downloadUrl"] != "http://trove.test/api/npm/npm-team/@team-a/ui/-/ui-1.5.0.tgz" {
			t.Fatalf("%v", f["downloadUrl"])
		}
	})
	t.Run("a version string is percent-encoded in the path", func(t *testing.T) {
		if a := e.get(e.tokE, "/api/v1/packages/"+e.idOf(e.tokE, "@team-a/ui")+"/versions/2.0.0-beta.1"); a.Status != 200 {
			t.Fatalf("%d", a.Status)
		}
		if a := e.get(e.tokE, "/api/v1/packages/"+e.idOf(e.tokE, "@team-a/ui")+"/versions/2.0.0%2Dbeta.1"); a.Status != 200 {
			t.Fatalf("encoded: %d", a.Status)
		}
	})
	t.Run("the package summary of the id has the same id", func(t *testing.T) {
		s := e.get(e.tokE, "/api/v1/packages/"+id).JSON(t)
		if s["id"] != id || s["name"] != "rich" || s["latestVersion"] != "3.2.1" || num(s["versionCount"]) != 2 {
			t.Fatalf("%v", s)
		}
	})
}

// TestR_API_029_ETags: the lookup, the package, the details and the README carry strong
// tags and answer 304 for the tag they sent; the tag follows what the caller sees.
func TestR_API_029_ETags(t *testing.T) {
	e := newEnv(t)
	e.seed()
	id := e.idOf(e.tokB, "@team-a/ui")
	paths := map[string]string{
		"lookup":  "/api/v1/packages/lookup?format=npm&name=%40team-a%2Fui",
		"package": "/api/v1/packages/" + id,
		"details": "/api/v1/packages/" + id + "/versions/1.4.0",
		"readme":  "/api/v1/packages/" + id + "/versions/1.4.0/readme",
	}
	tags := map[string]string{}
	for name, p := range paths {
		t.Run(name+" is tagged and answers 304 for its tag", func(t *testing.T) {
			a := e.get(e.tokB, p)
			tag := a.Header.Get("ETag")
			if a.Status != 200 || !strings.HasPrefix(tag, `"`) || strings.HasPrefix(tag, "W/") {
				t.Fatalf("%d, ETag %q", a.Status, tag)
			}
			tags[name] = tag
			again := e.get(e.tokB, p, "If-None-Match", tag)
			if again.Status != 304 || len(again.Body) != 0 || again.Header.Get("ETag") != tag {
				t.Fatalf("with the tag: %d %q ETag %q", again.Status, again.Body, again.Header.Get("ETag"))
			}
			if other := e.get(e.tokB, p, "If-None-Match", `"other"`); other.Status != 200 {
				t.Fatalf("with another tag: %d", other.Status)
			}
			if star := e.get(e.tokB, p, "If-None-Match", "*"); star.Status != 304 {
				t.Fatalf("with *: %d", star.Status)
			}
		})
	}
	t.Run("the tag of the package follows the versions the caller sees, not the ones it does not", func(t *testing.T) {
		// A version in npm-team is invisible to bob: his tag stays. erin sees it.
		erin := e.get(e.tokE, paths["package"]).Header.Get("ETag")
		e.publish("npm-team", spec{Name: "@team-a/ui", Version: "1.6.0"})
		if got := e.get(e.tokB, paths["package"]).Header.Get("ETag"); got != tags["package"] {
			t.Errorf("a hidden publish changed the tag of a reader of npm-local: %s -> %s", tags["package"], got)
		}
		if got := e.get(e.tokE, paths["package"]).Header.Get("ETag"); got == erin {
			t.Errorf("a visible publish kept the tag")
		}
		e.publish("npm-local", spec{Name: "@team-a/ui", Version: "1.7.0"})
		if got := e.get(e.tokB, paths["package"]).Header.Get("ETag"); got == tags["package"] {
			t.Errorf("a visible publish kept the tag")
		}
	})
	t.Run("a download changes the tag of the package", func(t *testing.T) {
		before := e.get(e.tokE, paths["package"]).Header.Get("ETag")
		e.st.Do(e.r, "GET", "/api/npm/npm-local/left-pad/-/left-pad-1.0.0.tgz", e.tokA, nil)
		e.st.Do(e.r, "GET", "/api/npm/npm-local/@team-a/ui/-/ui-1.3.0.tgz", e.tokA, nil)
		testenv.WaitFor(t, "the download statistics", 30*time.Second, func() bool {
			return e.get(e.tokE, paths["package"]).Header.Get("ETag") != before
		})
	})
}
