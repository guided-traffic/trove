//go:build integration

package npm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// A path pattern finer than a package's folder on npm (R-AUTHZ-005, Q-AUTHZ-255, Q-NPM-82): a version is decided on the
// path of its tarball, so a grant or a token scope that excludes one tarball hides that version wherever the package's
// metadata is shown — the package document in both forms, the version and dist-tag documents, the dist-tags, the
// document a write starts from and the package API — and a write is decided on every file it writes or removes.
//
//	npm-ver: local, packageOwnership off
//	  hidden 1.0.0, hidden 2.0.0 (latest, README CONFIDENTIAL-README-2.0.0, dist-tag beta)
//	  keep 1.0.0, keep 1.1.0; guard 1.0.0, guard 1.1.0
//
//	setup:     read, deploy, delete; no pattern
//	ver:       read, paths.exclude hidden/-/hidden-2.0.0.tgz
//	scoped:    read without patterns, a token whose scope excludes hidden/-/hidden-2.0.0.tgz
//	remover:   read, deploy and delete, paths.exclude keep/-/keep-1.0.0.tgz (the version is hidden from it)
//	deleter:   read without patterns; delete with paths.exclude guard/-/guard-1.1.0.tgz (it sees the version, may not remove it)
//	xdel:      read with paths.exclude hidden/-/hidden-2.0.0.tgz; delete without patterns (it may remove what it cannot see)
//	signer:    read and deploy, paths.exclude **/*.sigstore

const confidentialReadme = "CONFIDENTIAL-README-2.0.0"

type versionEnv struct {
	*env
	r                                                  *testenv.Replica
	setup, ver, scoped, remover, deleter, xdel, signer string
}

func newVersionEnv(t *testing.T) *versionEnv {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "npmver", Env: []string{"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off"}})
	e := &versionEnv{env: &env{t: t, st: st}}
	st.Repository("npm-ver", "local", "npm", `{"local":{"packageOwnership":"off"}}`)
	repos := []string{"npm-ver"}
	pattern := func(policy, mode, p string) {
		st.Exec(`INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) SELECT id, 'path', $2, $3 FROM authz_policy WHERE name = $1`, policy, mode, p)
	}
	setup, ver, scoped := st.User("setup"), st.User("ver"), st.User("scoped")
	remover, deleter, signer, xdel := st.User("remover"), st.User("deleter"), st.User("signer"), st.User("xdel")
	st.Policy("ver-setup", []string{"read", "deploy", "delete"}, repos, setup)
	st.Policy("ver-ver", []string{"read"}, repos, ver)
	pattern("ver-ver", "exclude", "hidden/-/hidden-2.0.0.tgz")
	st.Policy("ver-scoped", []string{"read"}, repos, scoped)
	st.Policy("ver-remover", []string{"read", "deploy", "delete"}, repos, remover)
	pattern("ver-remover", "exclude", "keep/-/keep-1.0.0.tgz")
	st.Policy("ver-deleter-read", []string{"read"}, repos, deleter)
	st.Policy("ver-deleter-delete", []string{"delete"}, repos, deleter)
	pattern("ver-deleter-delete", "exclude", "guard/-/guard-1.1.0.tgz")
	st.Policy("ver-xdel-read", []string{"read"}, repos, xdel)
	pattern("ver-xdel-read", "exclude", "hidden/-/hidden-2.0.0.tgz")
	st.Policy("ver-xdel-delete", []string{"delete"}, repos, xdel)
	st.Policy("ver-signer", []string{"read", "deploy"}, repos, signer)
	pattern("ver-signer", "exclude", "**/*.sigstore")
	e.setup, e.ver = st.Token(setup, scope.Default()), st.Token(ver, scope.Default())
	e.scoped = st.Token(scoped, scope.Scope{Repositories: []scope.Grant{{
		Repositories: []string{"it/npm-ver"}, Paths: scope.Patterns{Exclude: []string{"hidden/-/hidden-2.0.0.tgz"}}, Actions: []string{"read"},
	}}})
	e.remover, e.deleter, e.signer = st.Token(remover, scope.Default()), st.Token(deleter, scope.Default()), st.Token(signer, scope.Default())
	e.xdel = st.Token(xdel, scope.Default())
	e.r = e.replica("web-ver")
	e.mustPublish(e.r, "npm-ver", e.setup, pkg{Name: "hidden", Version: "1.0.0"})
	e.mustPublish(e.r, "npm-ver", e.setup, pkg{Name: "hidden", Version: "2.0.0", Tarball: readmeTarball(t, "hidden", "2.0.0", "# "+confidentialReadme+"\n")})
	if a := e.send(e.r, "PUT", "npm-ver", "-/package/hidden/dist-tags/beta", e.setup, `"2.0.0"`); a.Status/100 != 2 {
		t.Fatalf("dist-tag beta: %d %s", a.Status, a.Body)
	}
	for _, p := range []pkg{{Name: "keep", Version: "1.0.0"}, {Name: "keep", Version: "1.1.0"}, {Name: "guard", Version: "1.0.0"}, {Name: "guard", Version: "1.1.0"}} {
		e.mustPublish(e.r, "npm-ver", e.setup, p)
	}
	return e
}

// readmeTarball is a tarball whose README is the given text.
func readmeTarball(t *testing.T, name, version, readme string) []byte {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"name": name, "version": version})
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, f := range [][2]string{{"package.json", string(raw)}, {"README.md", readme}} {
		if err := tw.WriteHeader(&tar.Header{Name: "package/" + f[0], Mode: 0o644, Size: int64(len(f[1])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(f[1]))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// expectOnlyFirst asserts that a caller whose grant hides hidden@2.0.0 meets hidden as a package with the one version
// 1.0.0 on every protocol read.
func (e *versionEnv) expectOnlyFirst(t *testing.T, who, token string) {
	t.Helper()
	if a := e.get(e.r, "npm-ver", "hidden/-/hidden-2.0.0.tgz", token); a.Status != http.StatusNotFound {
		t.Errorf("%s, the excluded tarball: %d, want 404", who, a.Status)
	}
	for _, form := range []struct {
		what, path string
		accept     []string
	}{{"full", "hidden", nil}, {"abbreviated", "hidden", []string{"Accept", abbreviatedAccept}}, {"write", "hidden?write=true", nil}} {
		a := e.get(e.r, "npm-ver", form.path, token, form.accept...)
		if a.Status != http.StatusOK {
			t.Errorf("%s, %s package document: %d, want 200\n%s", who, form.what, a.Status, a.Body)
			continue
		}
		doc := document(t, a)
		if got := versionNames(doc); !slices.Equal(got, []string{"1.0.0"}) {
			t.Errorf("%s, %s package document lists %v, want [1.0.0]", who, form.what, got)
		}
		if got := tagsOfDoc(doc); got["latest"] != "1.0.0" || got["beta"] != "" {
			t.Errorf("%s, %s package document has dist-tags %v, want latest 1.0.0 and no beta", who, form.what, got)
		}
		if strings.Contains(string(a.Body), confidentialReadme) || strings.Contains(string(a.Body), `"2.0.0"`) {
			t.Errorf("%s, %s package document carries the hidden version or its README:\n%s", who, form.what, a.Body)
		}
	}
	for _, ref := range []string{"2.0.0", "beta"} {
		if a := e.get(e.r, "npm-ver", "hidden/"+ref, token); a.Status != http.StatusNotFound {
			t.Errorf("%s, document hidden/%s: %d, want 404\n%s", who, ref, a.Status, a.Body)
		}
	}
	if a := e.get(e.r, "npm-ver", "hidden/latest", token); a.Status != http.StatusOK || !strings.Contains(string(a.Body), `"1.0.0"`) {
		t.Errorf("%s, document hidden/latest: %d, want 200 with 1.0.0\n%s", who, a.Status, a.Body)
	}
	a := e.get(e.r, "npm-ver", "-/package/hidden/dist-tags", token)
	var tags map[string]string
	if a.Status != http.StatusOK || json.Unmarshal(a.Body, &tags) != nil || len(tags) != 1 || tags["latest"] != "1.0.0" {
		t.Errorf("%s, dist-tags: %d %s, want {\"latest\":\"1.0.0\"}", who, a.Status, a.Body)
	}
}

// expectBoth asserts that a caller without a pattern meets both versions.
func (e *versionEnv) expectBoth(t *testing.T, who, token string) {
	t.Helper()
	for _, accept := range [][]string{nil, {"Accept", abbreviatedAccept}} {
		doc := document(t, e.get(e.r, "npm-ver", "hidden", token, accept...))
		if got := versionNames(doc); !slices.Equal(got, []string{"1.0.0", "2.0.0"}) {
			t.Errorf("%s, package document lists %v, want [1.0.0 2.0.0]", who, got)
		}
		if got := tagsOfDoc(doc); got["latest"] != "2.0.0" || got["beta"] != "2.0.0" {
			t.Errorf("%s, dist-tags %v, want latest and beta at 2.0.0", who, got)
		}
	}
}

// packageAPI reads the package API's view of hidden for a caller: the latest version, the versions and the status of the
// README of 2.0.0.
func (e *versionEnv) packageAPI(t *testing.T, token string) (latest string, versions []string, readme int) {
	t.Helper()
	a := e.st.Do(e.r, "GET", "/api/v1/packages/lookup?format=npm&name=hidden", token, nil)
	var p struct {
		ID            string `json:"id"`
		LatestVersion string `json:"latestVersion"`
	}
	if a.Status != http.StatusOK || json.Unmarshal(a.Body, &p) != nil || p.ID == "" {
		t.Fatalf("lookup: %d %s", a.Status, a.Body)
	}
	l := e.st.Do(e.r, "GET", "/api/v1/packages/"+p.ID+"/versions", token, nil)
	var vs struct {
		Items []struct {
			Version string `json:"version"`
		} `json:"items"`
	}
	if l.Status != http.StatusOK || json.Unmarshal(l.Body, &vs) != nil {
		t.Fatalf("versions: %d %s", l.Status, l.Body)
	}
	for _, v := range vs.Items {
		versions = append(versions, v.Version)
	}
	slices.Sort(versions)
	return p.LatestVersion, versions, e.st.Do(e.r, "GET", "/api/v1/packages/"+p.ID+"/versions/2.0.0/readme", token, nil).Status
}

func TestR_AUTHZ_005_AVersionPathPatternHidesTheVersionFromNPMDocuments(t *testing.T) {
	e := newVersionEnv(t)

	t.Run("R-AUTHZ-005 a grant that excludes a tarball hides its version from every document and leaves the others", func(t *testing.T) {
		// The unrestricted caller first, so a rendition of the whole document is in the replica's cache when the narrowed
		// caller asks, and again afterwards, so a filtered rendition is never served to it either (R-ARCH-012).
		e.expectBoth(t, "setup", e.setup)
		e.expectOnlyFirst(t, "ver", e.ver)
		e.expectBoth(t, "setup", e.setup)
	})

	t.Run("R-AUTHZ-005 a token scope that excludes a tarball hides its version the same way", func(t *testing.T) {
		e.expectOnlyFirst(t, "scoped token", e.scoped)
	})

	t.Run("R-API-029 the package API decides a version on its tarball's path", func(t *testing.T) {
		if latest, versions, readme := e.packageAPI(t, e.ver); latest != "1.0.0" || !slices.Equal(versions, []string{"1.0.0"}) || readme != http.StatusNotFound {
			t.Errorf("ver: latest %q, versions %v, README of 2.0.0 %d; want 1.0.0, [1.0.0], 404", latest, versions, readme)
		}
		if latest, versions, readme := e.packageAPI(t, e.setup); latest != "2.0.0" || !slices.Equal(versions, []string{"1.0.0", "2.0.0"}) || readme != http.StatusOK {
			t.Errorf("setup: latest %q, versions %v, README of 2.0.0 %d; want 2.0.0, [1.0.0 2.0.0], 200", latest, versions, readme)
		}
	})

	t.Run("R-AUTHZ-005 a dist-tag is not pointed at a version the caller may not read, and the answer is that of a missing one", func(t *testing.T) {
		hidden := e.send(e.r, "PUT", "npm-ver", "-/package/keep/dist-tags/old", e.remover, `"1.0.0"`)
		missing := e.send(e.r, "PUT", "npm-ver", "-/package/keep/dist-tags/old", e.remover, `"9.9.9"`)
		if hidden.Status != http.StatusBadRequest || hidden.Status != missing.Status || code(t, hidden) != code(t, missing) {
			t.Errorf("remover, dist-tag at the hidden keep@1.0.0: %d %s; at the missing 9.9.9: %d %s; want the same 400",
				hidden.Status, hidden.Body, missing.Status, missing.Body)
		}
		if got := e.tags("npm-ver", "keep"); got["old"] != "" {
			t.Errorf("keep's tags after the refused write: %v", got)
		}
	})

	t.Run("R-AUTHZ-005 a dist-tag that points to a version the caller may not read is not moved and not removed", func(t *testing.T) {
		// setup points old and latest of keep at 1.0.0, which the remover may not read; the remover sees neither old nor
		// latest there (its latest is 1.1.0).
		for _, tag := range []string{"old", "latest"} {
			if a := e.send(e.r, "PUT", "npm-ver", "-/package/keep/dist-tags/"+tag, e.setup, `"1.0.0"`); a.Status/100 != 2 {
				t.Fatalf("setup, %s at 1.0.0: %d %s", tag, a.Status, a.Body)
			}
		}
		for _, tag := range []string{"old", "latest"} {
			if a := e.send(e.r, "PUT", "npm-ver", "-/package/keep/dist-tags/"+tag, e.remover, `"1.1.0"`); a.Status != http.StatusForbidden {
				t.Errorf("remover, %s over the hidden 1.0.0 to 1.1.0: %d, want 403\n%s", tag, a.Status, a.Body)
			}
		}
		hidden := e.send(e.r, "DELETE", "npm-ver", "-/package/keep/dist-tags/old", e.remover, nil)
		unknown := e.send(e.r, "DELETE", "npm-ver", "-/package/keep/dist-tags/nosuch", e.remover, nil)
		if hidden.Status != http.StatusNotFound || hidden.Status != unknown.Status || code(t, hidden) != code(t, unknown) {
			t.Errorf("remover, DELETE of old over the hidden 1.0.0: %d %s; of an unknown tag: %d %s; want the same 404",
				hidden.Status, hidden.Body, unknown.Status, unknown.Body)
		}
		if got := e.tags("npm-ver", "keep"); got["old"] != "1.0.0" || got["latest"] != "1.0.0" {
			t.Errorf("keep's tags after the refused writes: %v, want old and latest at 1.0.0", got)
		}
	})
}

// TestR_NPM_016_APublishLeavesATagAtAVersionThePublisherMayNotReadWhereItIs: a publish stores the version and does not move
// a tag that points to a version the publisher may not read, latest included and whatever the new version is; its
// npm-notice says so and names no version (Q-NPM-85). Found by the review of F-83: the remover's publish of keep@1.2.0
// under beta moved beta off the hidden 1.0.0, which a dist-tag PUT is refused (Q-NPM-84), and the latest guard answered a
// publish of 0.9.0 with "latest stays at 1.0.0", the number of a version the remover may not read.
func TestR_NPM_016_APublishLeavesATagAtAVersionThePublisherMayNotReadWhereItIs(t *testing.T) {
	e := newVersionEnv(t)
	for _, tag := range []string{"beta", "latest"} {
		if a := e.send(e.r, "PUT", "npm-ver", "-/package/keep/dist-tags/"+tag, e.setup, `"1.0.0"`); a.Status/100 != 2 {
			t.Fatalf("setup, %s at 1.0.0: %d %s", tag, a.Status, a.Body)
		}
	}
	held := func(t *testing.T, a testenv.Answer, tag string) {
		t.Helper()
		want := tag + " was not moved: it points to a version you may not read"
		if got := a.Header.Get("npm-notice"); got != want {
			t.Errorf("npm-notice %q, want %q", got, want)
		}
		if got := e.tags("npm-ver", "keep"); got["beta"] != "1.0.0" || got["latest"] != "1.0.0" {
			t.Errorf("keep's tags: %v, want beta and latest at 1.0.0", got)
		}
	}

	t.Run("R-NPM-016 a publish under a tag at a hidden version stores the version and leaves the tag", func(t *testing.T) {
		a := e.mustPublish(e.r, "npm-ver", e.remover, pkg{Name: "keep", Version: "1.2.0", Tag: "beta"})
		held(t, a, "beta")
		if n := e.versions("npm-ver", "keep"); n != 3 {
			t.Errorf("keep: %d versions, want 3", n)
		}
		d := e.st.Text(`SELECT COALESCE(details->>'tag', '') || ' ' || COALESCE(details->>'tag_held', '') FROM audit_event
			WHERE action = 'npm.publish' AND path = 'keep' AND details->>'version' = '1.2.0'`)
		if d != "beta true" {
			t.Errorf("the npm.publish event of keep@1.2.0: tag and tag_held %q, want \"beta true\"", d)
		}
	})

	t.Run("R-NPM-016 a higher version does not move latest off a hidden version", func(t *testing.T) {
		held(t, e.mustPublish(e.r, "npm-ver", e.remover, pkg{Name: "keep", Version: "2.0.0"}), "latest")
	})

	t.Run("R-NPM-016 the latest guard does not name a hidden latest", func(t *testing.T) {
		a := e.mustPublish(e.r, "npm-ver", e.remover, pkg{Name: "keep", Version: "0.9.0"})
		held(t, a, "latest")
		if strings.Contains(a.Header.Get("npm-notice"), "1.0.0") {
			t.Errorf("the notice names the hidden version: %q", a.Header.Get("npm-notice"))
		}
	})

	t.Run("R-NPM-016 the latest guard names a latest the publisher may read", func(t *testing.T) {
		// The remover reads every version of guard: the guard's notice is unchanged.
		a := e.mustPublish(e.r, "npm-ver", e.remover, pkg{Name: "guard", Version: "1.0.5"})
		if got, want := a.Header.Get("npm-notice"), "latest stays at 1.1.0; move it with npm dist-tag add"; got != want {
			t.Errorf("npm-notice %q, want %q", got, want)
		}
	})
}

// TestR_AUTHZ_005_TheTarballDeleteOfAnUnpublishAnswersAHiddenVersionAsAMissingOne: the third request of npm's unpublish
// sequence, DELETE {name}/-/{file}/-rev/{rev}, by a caller who may delete the tarball of a version it may not read,
// answers as for a version the package does not hold, and removes nothing (Q-NPM-84; found by the penetration test of
// F-83 as an existence oracle: 400 "still published" for the hidden version, 200 for a missing one).
func TestR_AUTHZ_005_TheTarballDeleteOfAnUnpublishAnswersAHiddenVersionAsAMissingOne(t *testing.T) {
	e := newVersionEnv(t)
	if got := versionNames(document(t, e.get(e.r, "npm-ver", "hidden", e.xdel))); !slices.Equal(got, []string{"1.0.0"}) {
		t.Fatalf("xdel sees %v in hidden, want [1.0.0]", got)
	}
	hidden := e.send(e.r, "DELETE", "npm-ver", "hidden/-/hidden-2.0.0.tgz/-rev/1-x", e.xdel, nil)
	missing := e.send(e.r, "DELETE", "npm-ver", "hidden/-/hidden-9.9.9.tgz/-rev/1-x", e.xdel, nil)
	if hidden.Status != http.StatusOK || hidden.Status != missing.Status || string(hidden.Body) != string(missing.Body) {
		t.Errorf("xdel, tarball DELETE of the hidden 2.0.0: %d %s; of the missing 9.9.9: %d %s; want the same 200",
			hidden.Status, hidden.Body, missing.Status, missing.Body)
	}
	if n := e.itemCount("npm-ver", "hidden/-/hidden-2.0.0.tgz"); n != 1 {
		t.Errorf("hidden-2.0.0.tgz: %d items after the request, want 1", n)
	}
	// The version the caller may read is still refused while it is published.
	if a := e.send(e.r, "DELETE", "npm-ver", "hidden/-/hidden-1.0.0.tgz/-rev/1-x", e.xdel, nil); a.Status != http.StatusBadRequest {
		t.Errorf("xdel, tarball DELETE of the published 1.0.0: %d, want 400\n%s", a.Status, a.Body)
	}
}

func TestR_AUTHZ_005_AnNPMWriteIsDecidedOnEveryFileItWritesOrRemoves(t *testing.T) {
	e := newVersionEnv(t)

	t.Run("R-AUTHZ-005 a revisioned PUT that names a version the caller may not read is answered as one that names a missing version", func(t *testing.T) {
		wdoc := document(t, e.get(e.r, "npm-ver", "keep?write=true", e.remover))
		rev, _ := wdoc["_rev"].(string)
		versions, _ := wdoc["versions"].(map[string]any)
		send := func(extra string) testenv.Answer {
			body := map[string]any{"_id": "keep", "name": "keep", "versions": map[string]any{"1.1.0": versions["1.1.0"], extra: map[string]any{}}}
			return e.send(e.r, "PUT", "npm-ver", "keep/-rev/"+rev, e.remover, body)
		}
		hidden, missing := send("1.0.0"), send("9.9.9")
		if hidden.Status != http.StatusBadRequest || hidden.Status != missing.Status ||
			strings.ReplaceAll(string(hidden.Body), "1.0.0", "V") != strings.ReplaceAll(string(missing.Body), "9.9.9", "V") {
			t.Errorf("remover, PUT naming the hidden 1.0.0: %d %s; naming the missing 9.9.9: %d %s; want the same 400",
				hidden.Status, hidden.Body, missing.Status, missing.Body)
		}
		for _, v := range []string{"1.0.0", "1.1.0"} {
			if n := e.itemCount("npm-ver", "keep/-/keep-"+v+".tgz"); n != 1 {
				t.Errorf("keep-%s.tgz: %d items after the refused PUTs, want 1", v, n)
			}
		}
	})

	t.Run("R-AUTHZ-005 a revisioned PUT leaves a version the caller may not read where it is", func(t *testing.T) {
		// The remover's own document of keep lists 1.1.0 only (1.0.0 is hidden from it); sent back without 1.1.0 it
		// removes 1.1.0 and leaves 1.0.0, which the remover can neither see nor remove.
		wdoc := document(t, e.get(e.r, "npm-ver", "keep?write=true", e.remover))
		if got := versionNames(wdoc); !slices.Equal(got, []string{"1.1.0"}) {
			t.Errorf("remover's write document lists %v, want [1.1.0]", got)
		}
		rev, _ := wdoc["_rev"].(string)
		wdoc["versions"] = map[string]any{}
		if a := e.send(e.r, "PUT", "npm-ver", "keep/-rev/"+rev, e.remover, wdoc); a.Status != http.StatusOK {
			t.Errorf("remover, PUT without 1.1.0: %d, want 200\n%s", a.Status, a.Body)
		}
		if n := e.itemCount("npm-ver", "keep/-/keep-1.0.0.tgz"); n != 1 {
			t.Errorf("keep-1.0.0.tgz: %d items after the remover's PUT, want 1", n)
		}
		if n := e.itemCount("npm-ver", "keep/-/keep-1.1.0.tgz"); n != 0 {
			t.Errorf("keep-1.1.0.tgz: %d items after the remover's PUT, want 0", n)
		}
	})

	t.Run("R-AUTHZ-005 a removal of a version whose tarball the caller may not delete is refused whole", func(t *testing.T) {
		wdoc := document(t, e.get(e.r, "npm-ver", "guard?write=true", e.deleter))
		rev, _ := wdoc["_rev"].(string)
		versions, _ := wdoc["versions"].(map[string]any)
		wdoc["versions"] = map[string]any{"1.0.0": versions["1.0.0"]}
		if a := e.send(e.r, "PUT", "npm-ver", "guard/-rev/"+rev, e.deleter, wdoc); a.Status != http.StatusForbidden {
			t.Errorf("deleter, PUT without 1.1.0: %d, want 403\n%s", a.Status, a.Body)
		}
		if a := e.send(e.r, "DELETE", "npm-ver", "guard/-rev/"+rev, e.deleter, nil); a.Status != http.StatusForbidden {
			t.Errorf("deleter, DELETE of the package: %d, want 403\n%s", a.Status, a.Body)
		}
		for _, v := range []string{"1.0.0", "1.1.0"} {
			if n := e.itemCount("npm-ver", "guard/-/guard-"+v+".tgz"); n != 1 {
				t.Errorf("guard-%s.tgz: %d items after the refused removals, want 1", v, n)
			}
		}
	})

	t.Run("R-AUTHZ-005 a publish whose provenance bundle the caller may not write is refused whole", func(t *testing.T) {
		a := e.publish(e.r, "npm-ver", e.signer, pkg{Name: "signed", Version: "1.0.0", Sigstore: []byte(`{"bundle":"x"}`)})
		if a.Status != http.StatusForbidden {
			t.Errorf("signer, publish with a bundle: %d, want 403\n%s", a.Status, a.Body)
		}
		if n := e.itemCount("npm-ver", "signed/-/signed-1.0.0.sigstore"); n != 0 {
			t.Errorf("the refused bundle: %d items, want 0", n)
		}
		if n := e.versions("npm-ver", "signed"); n != 0 {
			t.Errorf("signed: %d versions after the refused publish, want 0", n)
		}
		e.mustPublish(e.r, "npm-ver", e.signer, pkg{Name: "signed", Version: "1.0.1"})
	})
}
