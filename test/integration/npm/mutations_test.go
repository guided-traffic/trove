//go:build integration

package npm

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// rev reads the _rev of a package the way npm does before it writes (R-NPM-009).
func (e *env) rev(r *testenv.Replica, repo, name string) (string, map[string]any) {
	e.t.Helper()
	doc := document(e.t, e.get(r, repo, escapeName(name)+"?write=true", e.tokA))
	rev, _ := doc["_rev"].(string)
	return rev, doc
}

// itemCount counts the live items at a path.
func (e *env) itemCount(repo, path string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.path = $2 AND i.kind = 'file'`, repo, path)
}

func (e *env) tombstones(repo, name string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM npm_tombstones t JOIN repositories r ON r.id = t.repo_id WHERE r.key = $1 AND t.name = $2`, repo, name)
}

// TestR_NPM_015_TarballDownload: the bytes that were published come back, from any
// replica, with the headers of an immutable file and the rules of R-STOR-012.
func TestR_NPM_015_TarballDownload(t *testing.T) {
	e := newEnv(t)
	a, b := e.replica("web-a"), e.replica("web-b")
	p := pkg{Name: "@acme/ui", Version: "1.2.0"}
	e.mustPublish(a, "npm-local", e.tokA, p)
	want := tarballOf(t, p)
	for _, path := range []string{"@acme%2fui/-/ui-1.2.0.tgz", "@acme/ui/-/ui-1.2.0.tgz"} {
		ans := e.get(b, "npm-local", path, e.tokC) // carol may only read
		if ans.Status != 200 || !bytes.Equal(ans.Body, want) {
			t.Fatalf("%s: %d, %d bytes (want %d)", path, ans.Status, len(ans.Body), len(want))
		}
		if cc := ans.Header.Get("Cache-Control"); cc != "private, max-age=31536000, immutable" {
			t.Fatalf("Cache-Control %q", cc)
		}
		if ans.Header.Get("ETag") == "" || ans.Header.Get("Accept-Ranges") != "bytes" {
			t.Fatalf("ETag %q Accept-Ranges %q", ans.Header.Get("ETag"), ans.Header.Get("Accept-Ranges"))
		}
	}
	etag := e.get(a, "npm-local", "@acme%2fui/-/ui-1.2.0.tgz", e.tokA).Header.Get("ETag")
	t.Run("the same strong validator on every replica and a 304 on a match", func(t *testing.T) {
		if got := e.get(b, "npm-local", "@acme/ui/-/ui-1.2.0.tgz", e.tokA).Header.Get("ETag"); got != etag || strings.HasPrefix(got, "W/") {
			t.Fatalf("ETag %q, want %q", got, etag)
		}
		if ans := e.get(b, "npm-local", "@acme/ui/-/ui-1.2.0.tgz", e.tokA, "If-None-Match", etag); ans.Status != 304 || ans.Header.Get("Cache-Control") != "private, max-age=31536000, immutable" {
			t.Fatalf("%d %q", ans.Status, ans.Header.Get("Cache-Control"))
		}
	})
	t.Run("a single range is a 206", func(t *testing.T) {
		ans := e.get(b, "npm-local", "@acme%2fui/-/ui-1.2.0.tgz", e.tokA, "Range", "bytes=0-9")
		if ans.Status != 206 || !bytes.Equal(ans.Body, want[:10]) {
			t.Fatalf("%d %d bytes", ans.Status, len(ans.Body))
		}
	})
	t.Run("a file name that does not match is 404 in the native shape", func(t *testing.T) {
		for _, path := range []string{"@acme%2fui/-/other-1.2.0.tgz", "@acme%2fui/-/ui-1.2.1.tgz", "@acme%2fui/-/ui-v1.2.0.tgz", "@acme%2fui/-/ui-1.2.tgz"} {
			if ans := e.get(a, "npm-local", path, e.tokA); ans.Status != 404 || code(t, ans) != "not_found" {
				t.Fatalf("%s: %d %s", path, ans.Status, ans.Body)
			}
		}
	})
	t.Run("R-AUTHZ-016 a caller without read gets the 404 of a missing package", func(t *testing.T) {
		outsider := e.st.Token(e.st.User("mallory"), scopeDefault())
		hidden := e.get(a, "npm-local", "@acme%2fui/-/ui-1.2.0.tgz", outsider)
		missing := e.get(a, "npm-local", "@acme%2fnope/-/nope-1.2.0.tgz", outsider)
		if hidden.Status != missing.Status || hidden.Status == 200 {
			t.Fatalf("%d / %d", hidden.Status, missing.Status)
		}
	})
}

// TestR_NPM_016_DistTagWrites: tag writes through any replica, their refusals, their
// audit and the effect on the document.
func TestR_NPM_016_DistTagWrites(t *testing.T) {
	e := newEnv(t)
	a, b := e.replica("web-a"), e.replica("web-b")
	for _, v := range []string{"1.0.0", "1.1.0", "2.0.0"} {
		e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "tagged", Version: v})
	}
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "tagged", Version: "3.0.0-beta.1", Tag: "next"})
	tags := func(r *testenv.Replica) map[string]string {
		ans := e.get(r, "npm-local", "-/package/tagged/dist-tags", e.tokC)
		if ans.Status != 200 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		out := map[string]string{}
		for k, v := range ans.JSON(t) {
			out[k], _ = v.(string)
		}
		return out
	}
	if got := tags(b); got["latest"] != "2.0.0" || got["next"] != "3.0.0-beta.1" || len(got) != 2 {
		t.Fatalf("tags %v", got)
	}
	t.Run("a tag is added through one replica and is in force on the other", func(t *testing.T) {
		before := e.revSeq("npm-local", "tagged")
		ans := e.send(a, "PUT", "npm-local", "-/package/tagged/dist-tags/stable", e.tokA, `"1.1.0"`)
		if ans.Status != 200 || ans.JSON(t)["ok"] != true {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		if tags(b)["stable"] != "1.1.0" || e.revSeq("npm-local", "tagged") != before+1 {
			t.Fatalf("tags %v rev_seq %d -> %d", tags(b), before, e.revSeq("npm-local", "tagged"))
		}
		if e.events("npm.dist_tag.set", "tagged", "") != 1 {
			t.Fatalf("npm.dist_tag.set events: %d", e.events("npm.dist_tag.set", "tagged", ""))
		}
	})
	t.Run("latest moves to an older version", func(t *testing.T) {
		if ans := e.send(b, "PUT", "npm-local", "-/package/tagged/dist-tags/latest", e.tokA, `"1.0.0"`); ans.Status != 200 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		if tags(a)["latest"] != "1.0.0" {
			t.Fatalf("%v", tags(a))
		}
		e.send(b, "PUT", "npm-local", "-/package/tagged/dist-tags/latest", e.tokA, `"2.0.0"`)
	})
	t.Run("a request that changes nothing raises nothing", func(t *testing.T) {
		before := e.revSeq("npm-local", "tagged")
		events := e.events("npm.dist_tag.set", "tagged", "")
		if ans := e.send(a, "PUT", "npm-local", "-/package/tagged/dist-tags/stable", e.tokA, `"1.1.0"`); ans.Status != 200 {
			t.Fatalf("%d", ans.Status)
		}
		if e.revSeq("npm-local", "tagged") != before || e.events("npm.dist_tag.set", "tagged", "") != events {
			t.Fatal("a no-op changed the package")
		}
	})
	t.Run("the refusals", func(t *testing.T) {
		for _, c := range []struct {
			name, method, path, token string
			body                      any
			status                    int
			code                      string
		}{
			{"a tag that is a range", "PUT", "-/package/tagged/dist-tags/1.x", e.tokA, `"1.0.0"`, 400, "npm_invalid_tag"},
			{"a version that is not published", "PUT", "-/package/tagged/dist-tags/beta", e.tokA, `"9.9.9"`, 400, "npm_invalid_tag"},
			{"a version that is no version", "PUT", "-/package/tagged/dist-tags/beta", e.tokA, `"latest"`, 400, "npm_invalid_version"},
			{"an unknown package", "PUT", "-/package/nope/dist-tags/beta", e.tokA, `"1.0.0"`, 404, "not_found"},
			{"removing latest", "DELETE", "-/package/tagged/dist-tags/latest", e.tokA, nil, 400, "npm_invalid_tag"},
			{"removing a tag that is not there", "DELETE", "-/package/tagged/dist-tags/nothing", e.tokA, nil, 404, "not_found"},
			{"a caller who may only read", "PUT", "-/package/tagged/dist-tags/beta", e.tokC, `"1.0.0"`, 403, "forbidden"},
			{"a deployer who does not own the name", "PUT", "-/package/tagged/dist-tags/beta", e.tokB, `"1.0.0"`, 403, "package_owned_by_other_owner"},
			{"a deployer who does not own the name removing a tag", "DELETE", "-/package/tagged/dist-tags/stable", e.tokB, nil, 403, "package_owned_by_other_owner"},
		} {
			ans := e.send(a, c.method, "npm-local", c.path, c.token, c.body)
			if ans.Status != c.status || code(t, ans) != c.code {
				t.Errorf("%s: %d %s", c.name, ans.Status, ans.Body)
			}
		}
		if tags(a)["beta"] != "" || tags(a)["stable"] != "1.1.0" {
			t.Fatalf("a refused write changed the tags: %v", tags(a))
		}
	})
	t.Run("a tag is removed", func(t *testing.T) {
		if ans := e.send(b, "DELETE", "npm-local", "-/package/tagged/dist-tags/stable", e.tokA, nil); ans.Status != 200 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		if _, ok := tags(a)["stable"]; ok || e.events("npm.dist_tag.delete", "tagged", "") != 1 {
			t.Fatalf("tags %v", tags(a))
		}
	})
}

// TestR_NPM_017_UnpublishSequence: the three requests of npm unpublish, each through a
// different replica, and what is left afterwards: no item, no version row, a tombstone,
// the journal entry of the restore's replay, and tags that follow the rule.
func TestR_NPM_017_UnpublishSequence(t *testing.T) {
	e := newEnv(t)
	a, b, c := e.replica("web-a"), e.replica("web-b"), e.replica("web-c")
	for _, v := range []string{"1.0.0", "1.1.0", "1.2.0"} {
		e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "@acme/ui", Version: v, Sigstore: []byte(`{"bundle":"` + v + `"}`)})
	}
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "@acme/ui", Version: "2.0.0-rc.1", Tag: "next"})
	e.send(a, "PUT", "npm-local", "-/package/@acme%2fui/dist-tags/old", e.tokA, `"1.2.0"`)
	integrity := e.st.Text(`SELECT nv.integrity FROM npm_versions nv JOIN package_versions pv ON pv.id = nv.version_id WHERE pv.version = '1.2.0'`)

	// npm unpublish @acme/ui@1.2.0: read, put the document without the version, delete the tarball
	rev, doc := e.rev(c, "npm-local", "@acme/ui")
	delete(doc["versions"].(map[string]any), "1.2.0")
	put := e.send(a, "PUT", "npm-local", "@acme%2fui/-rev/"+rev, e.tokA, doc)
	if put.Status != 200 || put.JSON(t)["ok"] != true {
		t.Fatalf("PUT: %d %s", put.Status, put.Body)
	}
	newRev, _ := put.JSON(t)["rev"].(string)
	if newRev == "" || newRev == rev || newRev != func() string { r, _ := e.rev(b, "npm-local", "@acme/ui"); return r }() {
		t.Fatalf("the answer says rev %q, the package has another (was %q)", newRev, rev)
	}
	del := e.send(b, "DELETE", "npm-local", "@acme%2fui/-/ui-1.2.0.tgz/-rev/"+newRev, e.tokA, nil)
	if del.Status != 200 {
		t.Fatalf("DELETE tarball: %d %s", del.Status, del.Body)
	}

	t.Run("exactly that version is gone from every document", func(t *testing.T) {
		for _, r := range []*testenv.Replica{a, b, c} {
			d := document(t, e.get(r, "npm-local", "@acme%2fui", e.tokC))
			if got := versionNames(d); !slices.Equal(got, []string{"1.0.0", "1.1.0", "2.0.0-rc.1"}) {
				t.Fatalf("%s: versions %v", r.Name, got)
			}
			tags := tagsOfDoc(d)
			if tags["latest"] != "1.1.0" || tags["next"] != "2.0.0-rc.1" || tags["old"] != "" {
				t.Fatalf("%s: latest moved to the highest remaining release and the other tags on it went: %v", r.Name, tags)
			}
		}
	})
	t.Run("the items, the version row and the tombstone", func(t *testing.T) {
		if e.itemCount("npm-local", "@acme/ui/-/ui-1.2.0.tgz") != 0 || e.itemCount("npm-local", "@acme/ui/-/ui-1.2.0.sigstore") != 0 {
			t.Fatal("the files of the version are stored")
		}
		if e.itemCount("npm-local", "@acme/ui/-/ui-1.1.0.tgz") != 1 || e.itemCount("npm-local", "@acme/ui/-/ui-1.1.0.sigstore") != 1 {
			t.Fatal("the files of another version went")
		}
		if e.versions("npm-local", "@acme/ui") != 3 {
			t.Fatalf("%d version rows", e.versions("npm-local", "@acme/ui"))
		}
		if got := e.st.Text(`SELECT integrity || ' ' || source FROM npm_tombstones WHERE name = '@acme/ui' AND version = '1.2.0'`); got != integrity+" unpublish" {
			t.Fatalf("tombstone %q", got)
		}
		if e.tombstones("npm-local", "@acme/ui") != 1 {
			t.Fatalf("%d tombstones", e.tombstones("npm-local", "@acme/ui"))
		}
	})
	t.Run("npm.unpublish is audited with its journal entry for a restore's replay (Q-NPM-12)", func(t *testing.T) {
		if e.events("npm.unpublish", "@acme/ui", "") != 1 {
			t.Fatalf("%d events", e.events("npm.unpublish", "@acme/ui", ""))
		}
		entry := e.st.Text(`SELECT entry::text FROM revocation_outbox WHERE event = 'npm.unpublish' AND entry->>'package' = '@acme/ui'`)
		for _, want := range []string{`"version": "1.2.0"`, `"integrity": "` + integrity + `"`, `"actorId": "` + e.alice.ID + `"`, `"repositoryId"`} {
			if !strings.Contains(entry, want) {
				t.Fatalf("journal entry lacks %s: %s", want, entry)
			}
		}
		if got := e.st.Text(`SELECT actor_name FROM audit_event WHERE action = 'npm.unpublish' AND path = '@acme/ui'`); got != "alice" {
			t.Fatalf("actor %q", got)
		}
	})
	t.Run("the number is never accepted again, also by the one who owns it", func(t *testing.T) {
		ans := e.publish(c, "npm-local", e.tokA, pkg{Name: "@acme/ui", Version: "1.2.0"})
		if ans.Status != 409 || code(t, ans) != "npm_version_unpublished" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		e.mustPublish(c, "npm-local", e.tokA, pkg{Name: "@acme/ui", Version: "1.2.1"})
	})
	t.Run("the tarball and the raw item are 404, though the number is tombstoned and not unknown", func(t *testing.T) {
		for _, path := range []string{"/api/npm/npm-local/@acme%2fui/-/ui-1.2.0.tgz", "/files/npm-local/@acme/ui/-/ui-1.2.0.tgz"} {
			if ans := e.st.Do(b, "GET", path, e.tokA, nil); ans.Status != 404 {
				t.Fatalf("%s: %d", path, ans.Status)
			}
		}
	})
}

// TestR_NPM_009_StaleRevisions: a stale revision is a 409 and changes nothing, unless
// the request would change nothing; the retry of an unpublish whose answer was lost
// succeeds through any replica with the revision of the first attempt.
func TestR_NPM_009_StaleRevisions(t *testing.T) {
	e := newEnv(t)
	a, b := e.replica("web-a"), e.replica("web-b")
	for _, v := range []string{"1.0.0", "1.1.0", "1.2.0"} {
		e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "retry", Version: v})
	}
	rev, doc := e.rev(a, "npm-local", "retry")
	versions := doc["versions"].(map[string]any)
	delete(versions, "1.0.0")
	t.Run("a revision that a later commit made stale is 409 npm_stale_rev", func(t *testing.T) {
		e.mustPublish(b, "npm-local", e.tokA, pkg{Name: "retry", Version: "1.3.0"})
		ans := e.send(a, "PUT", "npm-local", "retry/-rev/"+rev, e.tokA, doc)
		if ans.Status != 409 || code(t, ans) != "npm_stale_rev" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		if e.versions("npm-local", "retry") != 4 || e.tombstones("npm-local", "retry") != 0 {
			t.Fatal("a stale request changed the package")
		}
	})
	rev, doc = e.rev(b, "npm-local", "retry")
	delete(doc["versions"].(map[string]any), "1.0.0")
	first := e.send(a, "PUT", "npm-local", "retry/-rev/"+rev, e.tokA, doc)
	if first.Status != 200 {
		t.Fatalf("%d %s", first.Status, first.Body)
	}
	current, _ := e.rev(a, "npm-local", "retry")
	t.Run("the retry of the request whose answer was lost is a 200 with the current _rev, through another replica", func(t *testing.T) {
		seq := e.revSeq("npm-local", "retry")
		again := e.send(b, "PUT", "npm-local", "retry/-rev/"+rev, e.tokA, doc)
		if again.Status != 200 || again.JSON(t)["rev"] != current || e.revSeq("npm-local", "retry") != seq {
			t.Fatalf("%d %s (rev_seq %d -> %d)", again.Status, again.Body, seq, e.revSeq("npm-local", "retry"))
		}
		if e.tombstones("npm-local", "retry") != 1 || e.events("npm.unpublish", "retry", "") != 1 {
			t.Fatal("the retry wrote again")
		}
	})
	t.Run("a document that names a version the package does not hold, or none, is 400", func(t *testing.T) {
		bad := map[string]any{"name": "retry", "versions": map[string]any{"9.9.9": map[string]any{}}}
		if ans := e.send(a, "PUT", "npm-local", "retry/-rev/"+current, e.tokA, bad); ans.Status != 400 || code(t, ans) != "npm_bad_publish" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		partial := map[string]any{"name": "retry", "maintainers": []any{map[string]any{"name": "bob"}}}
		if ans := e.send(a, "PUT", "npm-local", "retry/-rev/"+current, e.tokA, partial); ans.Status != 400 || code(t, ans) != "npm_bad_publish" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("the DELETE of the tarball of a version that is still published is 400", func(t *testing.T) {
		ans := e.send(a, "DELETE", "npm-local", "retry/-/retry-1.1.0.tgz/-rev/"+current, e.tokA, nil)
		if ans.Status != 400 || code(t, ans) != "npm_bad_publish" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
}

// TestR_NPM_017_UnpublishPackageAndPermissions: the whole package, the permission that
// the unpublish needs (delete, not deploy) and the ownership of the name.
func TestR_NPM_017_UnpublishPackageAndPermissions(t *testing.T) {
	e := newEnv(t)
	a, b := e.replica("web-a"), e.replica("web-b")
	for _, v := range []string{"1.0.0", "1.1.0"} {
		e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "gone-soon", Version: v})
	}
	rev, _ := e.rev(a, "npm-local", "gone-soon")
	t.Run("a caller with deploy but not delete gets 403", func(t *testing.T) {
		deployer := e.st.User("dora")
		e.st.Policy("npm-deployers", []string{"read", "deploy"}, []string{"npm-local"}, deployer)
		// the grant index of the replicas reads the policy at their next epoch; a token minted now
		// is known to the chain at once, the grant arrives through the change log
		tok := e.st.Token(deployer, scopeDefault())
		testenv.WaitFor(t, "the policy of dora", 10*time.Second, func() bool {
			return e.send(a, "DELETE", "npm-local", "gone-soon/-rev/"+rev, tok, nil).Status != 404
		})
		ans := e.send(a, "DELETE", "npm-local", "gone-soon/-rev/"+rev, tok, nil)
		if ans.Status != 403 || e.versions("npm-local", "gone-soon") != 2 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("a deleter who does not own the name gets 403 package_owned_by_other_owner", func(t *testing.T) {
		ans := e.send(b, "DELETE", "npm-local", "gone-soon/-rev/"+rev, e.tokB, nil)
		if ans.Status != 403 || code(t, ans) != "package_owned_by_other_owner" || e.versions("npm-local", "gone-soon") != 2 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("the owner removes every version and every tag", func(t *testing.T) {
		ans := e.send(b, "DELETE", "npm-local", "gone-soon/-rev/"+rev, e.tokA, nil)
		if ans.Status != 200 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		for _, r := range []*testenv.Replica{a, b} {
			if got := e.get(r, "npm-local", "gone-soon", e.tokA); got.Status != 404 {
				t.Fatalf("%s: the document is %d", r.Name, got.Status)
			}
			if got := e.get(r, "npm-local", "-/package/gone-soon/dist-tags", e.tokA); got.Status != 404 {
				t.Fatalf("%s: the tags are %d", r.Name, got.Status)
			}
		}
		if e.versions("npm-local", "gone-soon") != 0 || e.tombstones("npm-local", "gone-soon") != 2 || len(e.tags("npm-local", "gone-soon")) != 0 {
			t.Fatalf("versions %d tombstones %d tags %v", e.versions("npm-local", "gone-soon"), e.tombstones("npm-local", "gone-soon"), e.tags("npm-local", "gone-soon"))
		}
		if e.itemCount("npm-local", "gone-soon/-/gone-soon-1.0.0.tgz")+e.itemCount("npm-local", "gone-soon/-/gone-soon-1.1.0.tgz") != 0 {
			t.Fatal("items are stored")
		}
		if e.st.Number(`SELECT count(*) FROM packages WHERE name = 'gone-soon'`) != 1 {
			t.Fatal("the package row went: its rev_seq would start again")
		}
	})
	t.Run("the retry of the DELETE, with the revision of the first attempt, is a 200 that changes nothing", func(t *testing.T) {
		seq := e.revSeq("npm-local", "gone-soon")
		if ans := e.send(a, "DELETE", "npm-local", "gone-soon/-rev/"+rev, e.tokA, nil); ans.Status != 200 || e.revSeq("npm-local", "gone-soon") != seq {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("a package that never existed is 404", func(t *testing.T) {
		if ans := e.send(a, "DELETE", "npm-local", "never/-rev/1-abc", e.tokA, nil); ans.Status != 404 {
			t.Fatalf("%d", ans.Status)
		}
		if e.st.Number(`SELECT count(*) FROM packages WHERE name = 'never'`) != 0 {
			t.Fatal("a package row was created by a delete")
		}
	})
}

// TestR_NPM_017_RawDeleteUnpublishes: a delete of the tarball through /files/ takes the
// road of the unpublish: tombstone, tags, revision, the audit event (OnItemsRemoved).
func TestR_NPM_017_RawDeleteUnpublishes(t *testing.T) {
	e := newEnv(t)
	a, b := e.replica("web-a"), e.replica("web-b")
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "rawdel", Version: "1.0.0"})
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "rawdel", Version: "1.1.0"})
	seq := e.revSeq("npm-local", "rawdel")
	if ans := e.st.Do(b, "DELETE", "/files/npm-local/rawdel/-/rawdel-1.1.0.tgz", e.tokA, nil); ans.Status/100 != 2 {
		t.Fatalf("%d %s", ans.Status, ans.Body)
	}
	if e.revSeq("npm-local", "rawdel") <= seq {
		t.Fatal("rev_seq did not rise")
	}
	d := document(t, e.get(a, "npm-local", "rawdel", e.tokA))
	if !slices.Equal(versionNames(d), []string{"1.0.0"}) || tagsOfDoc(d)["latest"] != "1.0.0" {
		t.Fatalf("%v %v", versionNames(d), tagsOfDoc(d))
	}
	if e.tombstones("npm-local", "rawdel") != 1 || e.events("npm.unpublish", "rawdel", "") != 1 || e.versions("npm-local", "rawdel") != 1 {
		t.Fatalf("tombstones %d events %d", e.tombstones("npm-local", "rawdel"), e.events("npm.unpublish", "rawdel", ""))
	}
	if ans := e.publish(a, "npm-local", e.tokA, pkg{Name: "rawdel", Version: "1.1.0"}); ans.Status != 409 || code(t, ans) != "npm_version_unpublished" {
		t.Fatalf("%d %s", ans.Status, ans.Body)
	}
	t.Run("R-AUTHZ-018 a deleter who does not own the name gets the route's 403 through /files/ and the items API", func(t *testing.T) {
		// The generic layer asks the format seam the ownership question for the packages of
		// the removed files, under the package row lock, before the first row goes (Q-AUTHZ-161).
		e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "rawdel2", Version: "1.0.0"})
		e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "rawdel2", Version: "1.1.0"})
		for _, path := range []string{"/files/npm-local/rawdel2/-/rawdel2-1.1.0.tgz", "/api/v1/repositories/npm-local/items?path=rawdel2/-/rawdel2-1.1.0.tgz"} {
			ans := e.st.Do(b, "DELETE", path, e.tokB, nil)
			if ans.Status != 403 || problemCode(t, ans) != "package_owned_by_other_owner" {
				t.Fatalf("%s: %d %s", path, ans.Status, ans.Body)
			}
		}
		if e.tombstones("npm-local", "rawdel2") != 0 || e.versions("npm-local", "rawdel2") != 2 || e.itemCount("npm-local", "rawdel2/-/rawdel2-1.1.0.tgz") != 1 {
			t.Fatal("a refused delete removed something")
		}
		if ans := e.st.Do(b, "DELETE", "/api/v1/repositories/npm-local/items?path=rawdel2/-/rawdel2-1.1.0.tgz", e.tokA, nil); ans.Status != 204 {
			t.Fatalf("the owner through the items API: %d %s", ans.Status, ans.Body)
		}
		if e.tombstones("npm-local", "rawdel2") != 1 || e.versions("npm-local", "rawdel2") != 1 {
			t.Fatal("the items API did not unpublish the version")
		}
	})
}

// TestR_NPM_047_WithheldVersionsAreNotServed: the state a restore's replay leaves
// (R-NPM-047): a version that is stored and tombstoned with source 'journal' is in no
// document, its tarball and its raw item are 404 on every replica, and the
// administrator's confirmation (deleting the items) keeps the tombstone.
func TestR_NPM_047_WithheldVersionsAreNotServed(t *testing.T) {
	e := newEnv(t)
	a, b := e.replica("web-a"), e.replica("web-b")
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "withheld", Version: "1.0.0"})
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "withheld", Version: "1.1.0"})
	// both replicas have the document and the tarball cached
	for _, r := range []*testenv.Replica{a, b} {
		e.get(r, "npm-local", "withheld", e.tokA)
		if e.get(r, "npm-local", "withheld/-/withheld-1.1.0.tgz", e.tokA).Status != 200 {
			t.Fatal("the tarball is not served before the replay")
		}
	}
	// what the replay writes in one transaction: the tombstone, the tag rule, rev_seq + 1
	e.st.Exec(`
		WITH t AS (
		  INSERT INTO npm_tombstones (repo_id, name, version, integrity, source, unpublished_at, unpublished_by)
		  SELECT p.repo_id, p.name, pv.version, nv.integrity, 'journal', now(), $1::uuid
		    FROM packages p JOIN package_versions pv ON pv.package_id = p.id JOIN npm_versions nv ON nv.version_id = pv.id
		   WHERE p.name = 'withheld' AND pv.version = '1.1.0' RETURNING 1),
		     n AS (UPDATE npm_packages SET dist_tags = '{"latest":"1.0.0"}'::jsonb, modified_at = greatest(modified_at + interval '1 millisecond', clock_timestamp())
		            WHERE package_id = (SELECT id FROM packages WHERE name = 'withheld'))
		UPDATE packages SET rev_seq = rev_seq + 1 WHERE name = 'withheld'`, e.alice.ID)
	for _, r := range []*testenv.Replica{a, b} {
		d := document(t, e.get(r, "npm-local", "withheld", e.tokA))
		if !slices.Equal(versionNames(d), []string{"1.0.0"}) || tagsOfDoc(d)["latest"] != "1.0.0" {
			t.Fatalf("%s: %v %v", r.Name, versionNames(d), tagsOfDoc(d))
		}
		for _, path := range []string{"/api/npm/npm-local/withheld/-/withheld-1.1.0.tgz", "/files/npm-local/withheld/-/withheld-1.1.0.tgz", "/files/npm-local/withheld/-/withheld-1.1.0.tgz"} {
			if ans := e.st.Do(r, "GET", path, e.tokA, nil); ans.Status != 404 {
				t.Fatalf("%s %s: %d", r.Name, path, ans.Status)
			}
		}
		if ans := e.st.Do(r, "GET", "/files/npm-local/withheld/-/withheld-1.0.0.tgz", e.tokA, nil); ans.Status != 200 {
			t.Fatalf("%s: a live version: %d", r.Name, ans.Status)
		}
	}
	if e.itemCount("npm-local", "withheld/-/withheld-1.1.0.tgz") != 1 {
		t.Fatal("the withheld item was removed: the replay deletes nothing")
	}
	if ans := e.publish(b, "npm-local", e.tokA, pkg{Name: "withheld", Version: "1.1.0"}); ans.Status != 409 || code(t, ans) != "npm_version_unpublished" {
		t.Fatalf("%d %s", ans.Status, ans.Body)
	}
	t.Run("the administrator confirms by deleting the items: the version row goes and the tombstone stays", func(t *testing.T) {
		before := e.st.Text(`SELECT source FROM npm_tombstones WHERE name = 'withheld'`)
		if ans := e.st.Do(a, "DELETE", "/files/npm-local/withheld/-/withheld-1.1.0.tgz", e.tokA, nil); ans.Status/100 != 2 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		if e.versions("npm-local", "withheld") != 1 || e.itemCount("npm-local", "withheld/-/withheld-1.1.0.tgz") != 0 {
			t.Fatal("the withheld version is still stored")
		}
		if got := e.st.Text(`SELECT source FROM npm_tombstones WHERE name = 'withheld'`); got != before || before != "journal" {
			t.Fatalf("tombstone source %q (was %q)", got, before)
		}
		if tags := e.tags("npm-local", "withheld"); tags["latest"] != "1.0.0" || len(tags) != 1 {
			t.Fatalf("tags %v", tags)
		}
	})
}

// TestR_NPM_016_ConcurrentTagWritesPublishesAndUnpublishes: writes of all kinds through
// three replicas at once leave a package with a latest tag that points to a live
// version and no tag that points to a removed one (R-NPM-016).
func TestR_NPM_016_ConcurrentTagWritesPublishesAndUnpublishes(t *testing.T) {
	e := newEnv(t)
	reps := []*testenv.Replica{e.replica("web-a"), e.replica("web-b"), e.replica("web-c")}
	for i := range 10 {
		e.mustPublish(reps[0], "npm-local", e.tokA, pkg{Name: "mix", Version: fmt.Sprintf("1.0.%d", i)})
	}
	var wg sync.WaitGroup
	fail := make(chan string, 64)
	report := func(format string, args ...any) {
		select {
		case fail <- fmt.Sprintf(format, args...):
		default:
		}
	}
	run := func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}
	run(func() { // publishes
		for i := range 5 {
			if a := e.publish(reps[0], "npm-local", e.tokA, pkg{Name: "mix", Version: fmt.Sprintf("2.0.%d", i)}); a.Status != 201 {
				report("publish 2.0.%d: %d %s", i, a.Status, a.Body)
			}
		}
	})
	run(func() { // unpublishes of the oldest five, each with the read-modify-write of npm
		for i := range 5 {
			version := fmt.Sprintf("1.0.%d", i)
			for attempt := 0; ; attempt++ {
				ans := e.st.Do(reps[1], "GET", "/api/npm/npm-local/mix?write=true", e.tokA, nil)
				var doc map[string]any
				if err := jsonUnmarshal(ans.Body, &doc); err != nil || doc["versions"] == nil {
					report("read for write: %d %s", ans.Status, ans.Body)
					return
				}
				delete(doc["versions"].(map[string]any), version)
				put := e.st.Do(reps[1], "PUT", "/api/npm/npm-local/mix/-rev/"+doc["_rev"].(string), e.tokA, doc)
				if put.Status == 200 {
					break
				}
				if put.Status != 409 || attempt > 50 {
					report("unpublish %s: %d %s", version, put.Status, put.Body)
					return
				}
			}
		}
	})
	run(func() { // tags on versions that stay
		for i := range 10 {
			tag := fmt.Sprintf("t%d", i)
			if a := e.st.Do(reps[2], "PUT", "/api/npm/npm-local/-/package/mix/dist-tags/"+tag, e.tokA, fmt.Sprintf(`"1.0.%d"`, 5+i%5)); a.Status != 200 {
				report("tag %s: %d %s", tag, a.Status, a.Body)
			}
			if i%3 == 0 {
				e.st.Do(reps[2], "DELETE", "/api/npm/npm-local/-/package/mix/dist-tags/"+tag, e.tokA, nil)
			}
		}
	})
	run(func() { // latest moved around
		for i := range 8 {
			a := e.st.Do(reps[0], "PUT", "/api/npm/npm-local/-/package/mix/dist-tags/latest", e.tokA, fmt.Sprintf(`"1.0.%d"`, 5+i%5))
			if a.Status != 200 {
				report("latest: %d %s", a.Status, a.Body)
			}
		}
	})
	wg.Wait()
	close(fail)
	for f := range fail {
		t.Error(f)
	}
	if t.Failed() {
		return
	}
	doc := document(t, e.get(reps[1], "npm-local", "mix", e.tokA))
	live := versionNames(doc)
	if len(live) != 10 {
		t.Fatalf("versions %v", live)
	}
	tags := tagsOfDoc(doc)
	if tags["latest"] == "" {
		t.Fatalf("no latest: %v", tags)
	}
	for tag, v := range tags {
		if !slices.Contains(live, v) {
			t.Errorf("tag %s points to %s, which is not live", tag, v)
		}
	}
	if stored := e.tags("npm-local", "mix"); fmt.Sprint(stored) != fmt.Sprint(tags) {
		t.Fatalf("stored %v, document %v", stored, tags)
	}
	if e.tombstones("npm-local", "mix") != 5 {
		t.Fatalf("%d tombstones", e.tombstones("npm-local", "mix"))
	}
}

// TestR_NPM_019_WhoamiAndPing: the identity endpoints on every replica.
func TestR_NPM_019_WhoamiAndPing(t *testing.T) {
	e := newEnv(t)
	sa := e.st.ServiceAccount("builder")
	e.st.Policy("npm-bots", []string{"read"}, []string{"npm-local"}, sa)
	satok := e.st.Token(sa, scopeDefault())
	reps := []*testenv.Replica{e.replica("web-a"), e.replica("web-b")}
	for _, r := range reps {
		for _, c := range []struct{ token, want string }{{e.tokA, "alice"}, {e.tokC, "carol"}, {satok, "sa:builder"}} {
			ans := e.get(r, "npm-local", "-/whoami", c.token)
			if ans.Status != 200 || ans.JSON(t)["username"] != c.want || ans.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("%s whoami: %d %s", r.Name, ans.Status, ans.Body)
			}
			ping := e.get(r, "npm-local", "-/ping?write=true", c.token)
			if ping.Status != 200 || strings.TrimSpace(string(ping.Body)) != "{}" {
				t.Fatalf("%s ping: %d %s", r.Name, ping.Status, ping.Body)
			}
		}
		// Basic with the token as the password and any user name, as npm's _auth sends it
		basic := e.st.Do(r, "GET", "/api/npm/npm-local/-/whoami", "", nil, "Authorization", "Basic "+basicOf("anything", e.tokA))
		if basic.Status != 200 || basic.JSON(t)["username"] != "alice" {
			t.Fatalf("%s basic: %d %s", r.Name, basic.Status, basic.Body)
		}
		for _, p := range []string{"-/whoami", "-/ping"} {
			ans := e.get(r, "npm-local", p, "")
			if ans.Status != 401 || strings.Contains(strings.ToLower(ans.Header.Get("WWW-Authenticate")), "otp") || ans.Header.Get("WWW-Authenticate") == "" {
				t.Fatalf("%s %s anonymous: %d %q", r.Name, p, ans.Status, ans.Header.Get("WWW-Authenticate"))
			}
			bad := e.get(r, "npm-local", p, "trv_pat_notatoken")
			if bad.Status != 401 {
				t.Fatalf("%s %s with an unknown token: %d", r.Name, p, bad.Status)
			}
		}
	}
}

func scopeDefault() scope.Scope { return scope.Default() }

// basicOf is the credential of npm's keyed _auth: any user name, the token as the password.
func basicOf(user, secret string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + secret))
}
