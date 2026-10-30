//go:build integration

package npm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

// ent is one entry of a hand-made tarball.
type ent struct {
	name string
	typ  byte
	body string
	link string
	size int64 // when set, the size the header declares
}

// archive writes a gzip-compressed tar of the entries; trailing is appended to the gzip
// stream, and cutTar leaves the tar archive unterminated.
func archive(t *testing.T, ents []ent, trailing []byte, cutTar bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range ents {
		h := &tar.Header{Name: e.name, Mode: 0o644, Typeflag: e.typ, Linkname: e.link, Size: int64(len(e.body))}
		if e.typ == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Typeflag != tar.TypeReg {
			h.Size = 0
		}
		if e.size > 0 {
			h.Size = e.size
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.size == 0 && h.Size > 0 {
			_, _ = tw.Write([]byte(e.body))
		}
	}
	if !cutTar {
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return append(buf.Bytes(), trailing...)
}

func pj(name, version, extra string) string {
	return `{"name":"` + name + `","version":"` + version + `"` + extra + `}`
}

// TestR_NPM_007_ArchiveRules: the tarballs R-NPM-007 names answer 400 npm_bad_publish,
// a package.json that declares 2 GiB answers 413 without being read, and nothing of
// a refused publish is stored.
func TestR_NPM_007_ArchiveRules(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	items := e.st.Number(`SELECT count(*) FROM items`)
	good := pj("evil", "1.0.0", "")
	cases := []struct {
		name   string
		tgz    []byte
		status int
		code   string
	}{
		{"two package.json entries, the second adds a postinstall", archive(t, []ent{
			{name: "package/package.json", body: good}, {name: "package/package.json", body: pj("evil", "1.0.0", `,"scripts":{"postinstall":"x"}`)},
		}, nil, false), 400, "npm_bad_publish"},
		{"package.json is a symbolic link", archive(t, []ent{
			{name: "package/real.json", body: good}, {name: "package/package.json", typ: tar.TypeSymlink, link: "real.json"},
		}, nil, false), 400, "npm_bad_publish"},
		{"names that start with ./", archive(t, []ent{{name: "./package/package.json", body: good}}, nil, false), 400, "npm_bad_publish"},
		{"two first components", archive(t, []ent{
			{name: "package/package.json", body: good}, {name: "other/package.json", body: good},
		}, nil, false), 400, "npm_bad_publish"},
		{"a symbolic link out of the package", archive(t, []ent{
			{name: "package/package.json", body: good}, {name: "package/pw", typ: tar.TypeSymlink, link: "../../etc/passwd"},
		}, nil, false), 400, "npm_bad_publish"},
		{"a hard link to another first component", archive(t, []ent{
			{name: "package/package.json", body: good}, {name: "package/x", typ: tar.TypeLink, link: "other/package.json"},
		}, nil, false), 400, "npm_bad_publish"},
		{"a package.json in another case", archive(t, []ent{
			{name: "package/package.json", body: good}, {name: "package/Package.json", body: good},
		}, nil, false), 400, "npm_bad_publish"},
		{"bytes after the gzip stream", archive(t, []ent{{name: "package/package.json", body: good}}, []byte("trailing"), false), 400, "npm_bad_publish"},
		{"no package.json", archive(t, []ent{{name: "package/index.js", body: "x"}}, nil, false), 400, "npm_bad_publish"},
		{"not an archive", []byte("this is not gzip"), 400, "npm_bad_publish"},
		{"a package.json that declares 2 GiB", archive(t, []ent{{name: "package/package.json", size: 2 << 30}}, nil, true), 413, "npm_too_large"},
		{"another name in the package.json", archive(t, []ent{{name: "package/package.json", body: pj("other", "1.0.0", "")}}, nil, false), 400, "npm_manifest_mismatch"},
		{"another version in the package.json", archive(t, []ent{{name: "package/package.json", body: pj("evil", "9.9.9", "")}}, nil, false), 400, "npm_manifest_mismatch"},
	}
	for _, c := range cases {
		t.Run("R-NPM-007 "+c.name, func(t *testing.T) {
			ans := e.publish(a, "npm-local", e.tokA, pkg{Name: "evil", Version: "1.0.0", Tarball: c.tgz})
			if ans.Status != c.status || code(t, ans) != c.code {
				t.Fatalf("%d %s", ans.Status, ans.Body)
			}
		})
	}
	if e.versions("npm-local", "evil") != 0 || e.st.Number(`SELECT count(*) FROM items`) != items || e.revSeq("npm-local", "evil") != 0 {
		t.Fatal("a refused publish stored something")
	}
}

// TestR_NPM_007_ManifestConsistency: a request manifest that omits a dependency the
// tarball declares is refused with 400 npm_manifest_mismatch and audited; in warn mode
// the publish is accepted and audited (Q-NPM-6).
func TestR_NPM_007_ManifestConsistency(t *testing.T) {
	e := newEnv(t)
	reject := e.replica("web-a")
	warn := e.replica("web-w", "TROVE_NPM_PUBLISH_MANIFEST_CONSISTENCY=warn")
	withDeps := tarballOf(t, pkg{Name: "needy", Version: "1.0.0", Deps: map[string]string{"left-pad": "^1.0.0"}})
	p := pkg{Name: "needy", Version: "1.0.0", Tarball: withDeps} // the request manifest declares none
	t.Run("R-NPM-007 reject", func(t *testing.T) {
		ans := e.publish(reject, "npm-local", e.tokA, p)
		if ans.Status != 400 || code(t, ans) != "npm_manifest_mismatch" || !contains(string(ans.Body), "dependencies") {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		if e.versions("npm-local", "needy") != 0 {
			t.Fatal("stored")
		}
		waitEvent(t, e, "npm.publish.manifest_mismatch", "needy", 1)
	})
	t.Run("R-NPM-007 warn accepts the publish and stores the request's manifest", func(t *testing.T) {
		e.mustPublish(warn, "npm-local", e.tokA, p)
		if e.versions("npm-local", "needy") != 1 {
			t.Fatal("not stored")
		}
		stored := e.st.Text(`SELECT nv.manifest::text FROM npm_versions nv`)
		if contains(stored, "left-pad") {
			t.Fatalf("the stored manifest is the tarball's: %s", stored)
		}
		waitEvent(t, e, "npm.publish.manifest_mismatch", "needy", 2)
	})
	t.Run("R-NPM-007 a manifest that agrees with the tarball is not a mismatch", func(t *testing.T) {
		e.mustPublish(reject, "npm-local", e.tokA, pkg{Name: "agrees", Version: "1.0.0", Deps: map[string]string{"left-pad": "^1.0.0"}})
		time.Sleep(300 * time.Millisecond)
		if e.events("npm.publish.manifest_mismatch", "agrees", "") != 0 {
			t.Fatal("a mismatch was recorded")
		}
	})
}

// waitEvent waits for an asynchronous audit event.
func waitEvent(t *testing.T, e *env, typ, pkgName string, n int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for e.events(typ, pkgName, "") < n {
		if time.Now().After(deadline) {
			t.Fatalf("%s for %s: %d events, want %d", typ, pkgName, e.events(typ, pkgName, ""), n)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestR_NPM_007_RequestRules: the checks of step 3 that need no tarball rule.
func TestR_NPM_007_RequestRules(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	t.Run("R-NPM-002 a name with capitals is 400", func(t *testing.T) {
		ans := e.publish(a, "npm-local", e.tokA, pkg{Name: "Acme-UI"})
		if ans.Status != 400 || code(t, ans) != "npm_invalid_name" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-NPM-002 a look-alike of an existing name is 400", func(t *testing.T) {
		e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "acme-ui"})
		ans := e.publish(a, "npm-local", e.tokA, pkg{Name: "acme_ui"})
		if ans.Status != 400 || code(t, ans) != "npm_invalid_name" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		if e.versions("npm-local", "acme_ui") != 0 || e.revSeq("npm-local", "acme_ui") != 0 {
			t.Fatal("the look-alike left rows")
		}
	})
	t.Run("R-NPM-002 a look-alike in another repository is fine", func(t *testing.T) {
		e.mustPublish(a, "npm-claims", e.tokA, pkg{Name: "acme_ui"})
	})
	t.Run("R-NPM-002 a second layer of escapes is 400 before anything is read", func(t *testing.T) {
		ans := e.st.Do(a, "PUT", "/api/npm/npm-local/a%2525b", e.tokA, []byte(`{}`))
		if ans.Status != 400 || code(t, ans) != "npm_invalid_name" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-NPM-007 the name in the body is the name in the path", func(t *testing.T) {
		b := body(t, pkg{Name: "other-name"})
		ans := e.st.Do(a, "PUT", pubPath("npm-local", "some-name"), e.tokA, b)
		if ans.Status != 400 || code(t, ans) != "npm_bad_publish" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-NPM-007 a dist-tag that is a range is 400 npm_invalid_tag", func(t *testing.T) {
		ans := e.publish(a, "npm-local", e.tokA, pkg{Name: "tagged", Tag: "1.x"})
		if ans.Status != 400 || code(t, ans) != "npm_invalid_tag" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-NPM-007 a version with build metadata is 400 npm_invalid_version", func(t *testing.T) {
		ans := e.publish(a, "npm-local", e.tokA, pkg{Name: "built", Version: "1.0.0+build.5"})
		if ans.Status != 400 || code(t, ans) != "npm_invalid_version" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-NPM-007 a version that npm cleans is stored under its canonical form", func(t *testing.T) {
		e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "cleaned", Version: "v1.0.0"})
		if got := e.st.Text(`SELECT pv.version FROM package_versions pv JOIN packages p ON p.id = pv.package_id WHERE p.name = 'cleaned'`); got != "1.0.0" {
			t.Fatalf("version %q", got)
		}
		if tags := e.tags("npm-local", "cleaned"); tags["latest"] != "1.0.0" {
			t.Fatalf("tags %v", tags)
		}
	})
	t.Run("R-NPM-007 a request with two versions is 400", func(t *testing.T) {
		var doc map[string]any
		_ = json.Unmarshal(body(t, pkg{Name: "twice"}), &doc)
		doc["versions"].(map[string]any)["1.0.1"] = map[string]any{"name": "twice", "version": "1.0.1"}
		b, _ := json.Marshal(doc)
		ans := e.st.Do(a, "PUT", pubPath("npm-local", "twice"), e.tokA, b)
		if ans.Status != 400 || code(t, ans) != "npm_bad_publish" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-NPM-007 an integrity that differs from the attachment stores nothing and is 409", func(t *testing.T) {
		items := e.st.Number(`SELECT count(*) FROM items`)
		wrong := "sha512-" + strings.Repeat("A", 86) + "=="
		ans := e.publish(a, "npm-local", e.tokA, pkg{Name: "wrongsum", Integrity: wrong})
		if ans.Status != 409 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		if e.versions("npm-local", "wrongsum") != 0 || e.st.Number(`SELECT count(*) FROM items`) != items {
			t.Fatal("stored")
		}
		ans = e.publish(a, "npm-local", e.tokA, pkg{Name: "wrongsum", Shasum: strings.Repeat("0", 40)})
		if ans.Status != 409 {
			t.Fatalf("shasum: %d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-NPM-007 a body that is no JSON is 400", func(t *testing.T) {
		ans := e.st.Do(a, "PUT", pubPath("npm-local", "garbage"), e.tokA, []byte(`{"name":`))
		if ans.Status != 400 || code(t, ans) != "npm_bad_publish" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-NPM-017 a PUT without a version is the partial update of npm owner and is 400", func(t *testing.T) {
		ans := e.st.Do(a, "PUT", pubPath("npm-local", "acme-ui"), e.tokA, []byte(`{"_id":"acme-ui","name":"acme-ui","maintainers":[{"name":"x"}]}`))
		if ans.Status != 400 || code(t, ans) != "npm_bad_publish" || !contains(string(ans.Body), "maintainer lists are not supported") {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
}

// TestR_NPM_007_Limits: a publish beyond a limit is 413 npm_too_large and stores nothing.
func TestR_NPM_007_Limits(t *testing.T) {
	e := newEnv(t)
	small := e.replica("web-s", "TROVE_NPM_PUBLISH_MAX_TARBALL_SIZE=64KiB", "TROVE_NPM_PUBLISH_MAX_TAR_ENTRIES=4", "TROVE_NPM_PUBLISH_MAX_UNPACKED_SIZE=32KiB")
	noise := make([]byte, 200<<10)
	r := rand.NewChaCha8([32]byte{1})
	_, _ = r.Read(noise)
	items := e.st.Number(`SELECT count(*) FROM items`)
	t.Run("R-NPM-007 a tarball above max_tarball_size", func(t *testing.T) {
		ans := e.publish(small, "npm-local", e.tokA, pkg{Name: "big", Files: map[string]string{"blob.bin": string(noise)}})
		if ans.Status != 413 || code(t, ans) != "npm_too_large" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-NPM-007 a body above 4/3 of the limit plus 1 MiB is refused on its announced length", func(t *testing.T) {
		huge := append([]byte(`{"name":"huge"}`), bytes.Repeat([]byte(" "), 3<<20)...)
		ans := rawPut(t, small, pubPath("npm-local", "huge"), e.tokA, bytes.NewReader(huge), int64(len(huge)), true)
		if ans.Status != 413 || code(t, ans) != "npm_too_large" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-NPM-007 a streamed body above the cap is refused when it reaches it", func(t *testing.T) {
		huge := append([]byte(`{"name":"streamed"}`), bytes.Repeat([]byte(" "), 1100<<10)...)
		ans := rawPut(t, small, pubPath("npm-local", "streamed"), e.tokA, bytes.NewReader(huge), -1, false)
		if ans.Status != 413 || code(t, ans) != "npm_too_large" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-NPM-007 more entries than max_tar_entries", func(t *testing.T) {
		ans := e.publish(small, "npm-local", e.tokA, pkg{Name: "many", Files: map[string]string{"a": "1", "b": "2", "c": "3", "d": "4"}})
		if ans.Status != 413 || code(t, ans) != "npm_too_large" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-NPM-007 more unpacked bytes than max_unpacked_size", func(t *testing.T) {
		ans := e.publish(small, "npm-local", e.tokA, pkg{Name: "inflated", Files: map[string]string{"zeros.bin": strings.Repeat("0", 40<<10)}})
		if ans.Status != 413 || code(t, ans) != "npm_too_large" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-NPM-007 a tarball within the limits is fine", func(t *testing.T) {
		e.mustPublish(small, "npm-local", e.tokA, pkg{Name: "tiny", Files: map[string]string{"index.js": "module.exports = 1\n"}})
	})
	for _, name := range []string{"big", "many", "inflated", "huge"} {
		if e.versions("npm-local", name) != 0 {
			t.Errorf("%s was stored", name)
		}
	}
	if got := e.st.Number(`SELECT count(*) FROM items`) - items; got != 3 { // the tiny tarball and its folders
		t.Errorf("a refused publish left items: %d new", got)
	}
}
