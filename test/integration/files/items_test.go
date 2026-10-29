//go:build integration

package files

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/config"
)

func TestR_AUTHZ_016_HiddenExistence(t *testing.T) {
	te := newTransferEnv(t)
	a, b := te.a, te.b
	secret := "/files/generic-local/secret/plan.txt"
	if res := a.put("ci", secret, "the plan"); res.status != 201 {
		t.Fatal(res.status)
	}
	missing := "/files/generic-local/secret/missing.txt"
	// A second repository nobody but admin may see: its files and a missing repository answer alike.
	a.createRepository("private", nil)
	if res := a.put("admin", "/files/private/x.txt", "x"); res.status != 201 {
		t.Fatal(res.status)
	}

	t.Run("R-AUTHZ-016 R-GENERIC-005 a caller without read gets a 404 byte-identical to the 404 of a missing file, for GET and HEAD, on both replicas", func(t *testing.T) {
		for _, r := range []*replica{a, b} {
			hidden, gone := r.get("nobody", "/files/private/x.txt"), r.get("nobody", "/files/private/nothing.txt")
			if hidden.status != 404 || gone.status != 404 || normalize(hidden.text()) != normalize(gone.text()) {
				t.Fatalf("%s hidden %d %s\nmissing %d %s", r.name, hidden.status, hidden.body, gone.status, gone.body)
			}
			unknownRepo := r.get("nobody", "/files/no-such-repository/nothing.txt")
			if unknownRepo.status != 404 || normalize(unknownRepo.text()) != normalize(gone.text()) {
				t.Fatalf("%s unknown repository %d %s", r.name, unknownRepo.status, unknownRepo.body)
			}
			if h, g := r.do("nobody", "HEAD", "/files/private/x.txt", nil), r.do("nobody", "HEAD", "/files/private/nothing.txt", nil); h.status != 404 || g.status != 404 {
				t.Errorf("%s HEAD %d %d", r.name, h.status, g.status)
			}
			// The headers match too (the replica header and the request ID differ by nature).
			// Content-Length differs only because the body echoes the request path.
			for _, k := range []string{"Content-Type", "Cache-Control", "Www-Authenticate"} {
				if hidden.hdr(k) != gone.hdr(k) {
					t.Errorf("%s header %s: %q vs %q", r.name, k, hidden.hdr(k), gone.hdr(k))
				}
			}
		}
	})
	t.Run("R-AUTHZ-016 the items operations hide in the same way", func(t *testing.T) {
		hidden := a.get("nobody", "/api/v1/repositories/private/items?path=x.txt")
		gone := a.get("nobody", "/api/v1/repositories/private/items?path=nothing.txt")
		missingRepo := a.get("nobody", "/api/v1/repositories/no-such-repository/items?path=x.txt")
		for _, r := range []response{hidden, gone, missingRepo} {
			if r.status != 404 {
				t.Fatalf("%d %s", r.status, r.body)
			}
		}
		if normalize(hidden.text()) != normalize(gone.text()) || normalize(gone.text()) != normalize(missingRepo.text()) {
			t.Fatalf("hidden %s\ngone %s\nmissing repository %s", hidden.body, gone.body, missingRepo.body)
		}
		for _, p := range []string{"children?path=", "stats?path=x.txt"} {
			if res := a.get("nobody", "/api/v1/repositories/private/"+p); res.status != 404 {
				t.Errorf("%s: %d", p, res.status)
			}
		}
	})
	t.Run("R-AUTHZ-016 an existing file the caller can read and a missing one in the same repository are told apart only by the caller who can read", func(t *testing.T) {
		if res := a.get("reader", secret); res.status != 200 {
			t.Fatalf("%d", res.status)
		}
		if res := a.get("reader", missing); res.status != 404 {
			t.Fatalf("%d", res.status)
		}
	})
	t.Run("R-AUTHZ-016 a refused write is 403 whether or not the target exists", func(t *testing.T) {
		for _, p := range []string{"/files/private/x.txt", "/files/private/new.txt", "/files/no-such-repository/new.txt"} {
			problemOf(t, a.put("nobody", p, "y"), 403, "forbidden")
			problemOf(t, a.do("nobody", "DELETE", p, nil), 403, "forbidden")
		}
	})
	t.Run("R-AUTHZ-016 an anonymous caller is answered 401 for reads and writes alike while anonymous access is off", func(t *testing.T) {
		problemOf(t, a.get("", "/files/private/x.txt"), 401, "unauthenticated")
		problemOf(t, a.get("", "/files/private/nothing.txt"), 401, "unauthenticated")
		problemOf(t, a.put("", "/files/private/x.txt", "y"), 401, "unauthenticated")
		problemOf(t, a.get("", "/api/v1/repositories/private/items?path=x.txt"), 401, "unauthenticated")
		if res := a.get("", "/files/private/x.txt"); res.hdr("WWW-Authenticate") == "" {
			t.Error("a 401 carries a challenge")
		}
	})
	t.Run("R-AUTHZ-016 with anonymous access on, an anonymous read is allowed only where anonymous holds read", func(t *testing.T) {
		te.grant(a, "private-anon", []string{"private"}, authz.PatternLists{}, map[string][]string{"anonymous": {"read"}})
		te.anonymous.Store(true)
		defer te.anonymous.Store(false)
		waitFor(t, "the anonymous grant", 10*time.Second, func() bool {
			return a.get("", "/files/private/x.txt").status == 200 && b.get("", "/files/private/x.txt").status == 200
		})
		if res := a.get("", "/files/private/x.txt"); res.status != 200 || res.hdr("Cache-Control") != "no-cache" {
			t.Fatalf("%d %v", res.status, res.header)
		}
		problemOf(t, a.put("", "/files/private/y.txt", "y"), 401, "unauthenticated")
		if res := a.get("", secret); res.status != 401 {
			t.Errorf("anonymous without a grant on generic-local: %d", res.status)
		}
	})
}

func TestR_STOR_017_ListingsFilterWhatTheCallerMayRead(t *testing.T) {
	te := newTransferEnv(t)
	a, b := te.a, te.b
	for _, p := range []string{"releases/1.0/a.bin", "releases/1.0/b.bin", "releases/secret/k.bin", "tmp/x.bin", "top.txt"} {
		if res := a.put("maint", "/files/generic-local/"+p, p); res.status/100 != 2 {
			t.Fatalf("%s: %d", p, res.status)
		}
	}
	te.user("scoped")
	te.grant(a, "scoped-read", []string{"generic-local"},
		authz.PatternLists{Include: []string{"releases/**"}, Exclude: []string{"releases/secret/**"}},
		map[string][]string{"user:scoped": {"read"}})
	waitFor(t, "the scoped grant on both replicas", 10*time.Second, func() bool {
		return a.get("scoped", "/files/generic-local/releases/1.0/a.bin").status == 200 && b.get("scoped", "/files/generic-local/releases/1.0/a.bin").status == 200
	})

	paths := func(user, query string) []string {
		res := a.get(user, "/api/v1/repositories/generic-local/children?"+query)
		if res.status != 200 {
			t.Fatalf("%s: %d %s", query, res.status, res.body)
		}
		var out []string
		for _, it := range res.json(t)["items"].([]any) {
			out = append(out, it.(map[string]any)["path"].(string))
		}
		return out
	}
	t.Run("R-STOR-017 R-AUTHZ-016 entries the caller cannot read are omitted from a recursive listing", func(t *testing.T) {
		if got := paths("scoped", "recursive=true&folders=false"); fmt.Sprint(got) != "[releases/1.0/a.bin releases/1.0/b.bin]" {
			t.Errorf("scoped sees %v", got)
		}
		if got := paths("maint", "recursive=true&folders=false"); len(got) != 5 {
			t.Errorf("maint sees %v", got)
		}
	})
	t.Run("R-STOR-017 the root lists the folders the caller may enter, and a folder it may not read is a 404", func(t *testing.T) {
		if got := paths("scoped", ""); fmt.Sprint(got) != "[releases]" {
			t.Errorf("scoped root = %v", got)
		}
		if res := a.get("scoped", "/api/v1/repositories/generic-local/children?path=tmp"); res.status != 404 {
			t.Errorf("tmp: %d", res.status)
		}
		if res := a.get("scoped", "/api/v1/repositories/generic-local/children?path=releases/secret"); res.status != 404 {
			t.Errorf("releases/secret: %d", res.status)
		}
		if res := a.get("scoped", "/files/generic-local/tmp/x.bin"); res.status != 404 {
			t.Errorf("a file outside the grant: %d", res.status)
		}
	})
	t.Run("R-STOR-017 the directory index of /files/ filters in the same way", func(t *testing.T) {
		res := a.get("scoped", "/files/generic-local/releases/")
		if res.status != 200 || strings.Contains(res.text(), "secret") || !strings.Contains(res.text(), `"path":"1.0"`) {
			t.Fatalf("%d %s", res.status, res.body)
		}
	})
	t.Run("R-STOR-017 maxDepth bounds a recursive listing; a cursor walk with limit=1 returns every entry once", func(t *testing.T) {
		if got := paths("maint", "recursive=true&folders=false&maxDepth=1"); fmt.Sprint(got) != "[top.txt]" {
			t.Errorf("maxDepth=1: %v", got)
		}
		if got := paths("maint", "path=releases&recursive=true&folders=false&maxDepth=1"); len(got) != 0 {
			t.Errorf("releases maxDepth=1: %v", got)
		}
		if got := paths("maint", "path=releases&recursive=true&folders=false&maxDepth=2"); fmt.Sprint(got) != "[1.0/a.bin 1.0/b.bin secret/k.bin]" {
			t.Errorf("releases maxDepth=2: %v", got)
		}
		var walked []string
		url := "recursive=true&folders=false&limit=1"
		for range 20 {
			res := a.get("maint", "/api/v1/repositories/generic-local/children?"+url)
			m := res.json(t)
			for _, it := range m["items"].([]any) {
				walked = append(walked, it.(map[string]any)["path"].(string))
			}
			next, _ := m["nextCursor"].(string)
			if next == "" {
				break
			}
			url = "recursive=true&folders=false&limit=1&cursor=" + next
		}
		if len(walked) != 5 {
			t.Fatalf("walk = %v", walked)
		}
	})
	t.Run("R-STOR-017 a filtered listing pages through the entries the caller cannot read without ending early", func(t *testing.T) {
		var walked []string
		url := "recursive=true&folders=false&limit=1"
		for range 20 {
			m := b.get("scoped", "/api/v1/repositories/generic-local/children?"+url).json(t)
			for _, it := range m["items"].([]any) {
				walked = append(walked, it.(map[string]any)["path"].(string))
			}
			next, _ := m["nextCursor"].(string)
			if next == "" {
				break
			}
			url = "recursive=true&folders=false&limit=1&cursor=" + next
		}
		if fmt.Sprint(walked) != "[releases/1.0/a.bin releases/1.0/b.bin]" {
			t.Fatalf("walk = %v", walked)
		}
	})
	t.Run("R-API-002 the answers of listChildren matched the document", func(t *testing.T) {
		if missing := te.val.Uncovered("listChildren"); len(missing) > 0 {
			t.Fatalf("not reached: %v", missing)
		}
	})
	t.Run("R-API-010 a cursor of another listing is refused", func(t *testing.T) {
		m := a.get("maint", "/api/v1/repositories/generic-local/children?recursive=true&limit=1").json(t)
		cursor, _ := m["nextCursor"].(string)
		problemOf(t, a.get("maint", "/api/v1/repositories/generic-local/children?recursive=false&limit=1&cursor="+cursor), 400, "invalid_cursor")
		problemOf(t, a.get("maint", "/api/v1/repositories/generic-local/children?bogus=1"), 400, "malformed_request")
		problemOf(t, a.get("maint", "/api/v1/repositories/generic-local/children?path=../x"), 400, "invalid_path")
	})
}

func TestR_GENERIC_006_DeleteAndItems(t *testing.T) {
	te := newTransferEnv(t)
	a, b := te.a, te.b
	base := "/files/generic-local/"

	t.Run("R-GENERIC-006 R-STOR-018 DELETE answers 204 and the next GET answers 404; the item information says package null", func(t *testing.T) {
		if res := a.put("ci", base+"tools/widget.bin", "w"); res.status != 201 {
			t.Fatal(res.status)
		}
		info := b.get("reader", "/api/v1/repositories/generic-local/items?path=tools/widget.bin")
		m := info.json(t)
		if info.status != 200 || m["package"] != nil || m["upstreamUrl"] != nil || m["kind"] != "file" || m["contentStatus"] != "ok" {
			t.Fatalf("%d %s", info.status, info.body)
		}
		problemOf(t, a.do("ci", "DELETE", base+"tools/widget.bin", nil), 403, "forbidden")
		del := b.do("maint", "DELETE", base+"tools/widget.bin", nil)
		if del.status != 204 || len(del.body) != 0 {
			t.Fatalf("%d %s", del.status, del.body)
		}
		if res := a.get("reader", base+"tools/widget.bin"); res.status != 404 {
			t.Fatalf("%d", res.status)
		}
		if n := te.number("SELECT count(*) FROM items WHERE path IN ('tools', 'tools/widget.bin')"); n != 0 {
			t.Errorf("%d items left (the implicit folder goes with its last child)", n)
		}
		problemOf(t, a.do("maint", "DELETE", base+"tools/widget.bin", nil), 404, "not_found")
		if ev := b.rec.ofType("artifact.deleted"); len(ev) != 1 || ev[0].Path != "tools/widget.bin" {
			t.Errorf("artifact.deleted = %+v", ev)
		}
	})

	t.Run("R-GENERIC-006 R-STOR-018 deleting a folder with one file the caller may not delete answers 403 and removes nothing", func(t *testing.T) {
		for _, p := range []string{"mix/keep/a.bin", "mix/keep/b.bin", "mix/other/c.bin"} {
			if res := a.put("maint", base+p, p); res.status != 201 {
				t.Fatal(res.status)
			}
		}
		te.user("partial")
		te.grant(a, "partial-delete", []string{"generic-local"},
			authz.PatternLists{Include: []string{"mix/**"}, Exclude: []string{"mix/keep/b.bin"}},
			map[string][]string{"user:partial": {"read", "delete"}})
		waitFor(t, "the grant", 10*time.Second, func() bool { return a.get("partial", base+"mix/other/c.bin").status == 200 })
		res := a.do("partial", "DELETE", base+"mix/", nil)
		problemOf(t, res, 403, "forbidden")
		if strings.Contains(res.text(), "b.bin") {
			t.Error("the refusal names an item below the path the caller sent")
		}
		if n := te.number("SELECT count(*) FROM items WHERE path LIKE 'mix/%' AND kind = 'file'"); n != 3 {
			t.Fatalf("%d files left, want 3", n)
		}
		if res := a.do("partial", "DELETE", base+"mix/other/c.bin", nil); res.status != 204 {
			t.Fatalf("a file it may delete: %d", res.status)
		}
		if res := a.do("maint", "DELETE", base+"mix", nil); res.status != 204 {
			t.Fatalf("the folder by a caller who may delete everything: %d", res.status)
		}
		if n := te.number("SELECT count(*) FROM items WHERE path LIKE 'mix%'"); n != 0 {
			t.Errorf("%d items left", n)
		}
	})

	t.Run("R-STOR-018 the management API deletes too, and the root cannot be deleted", func(t *testing.T) {
		if res := a.put("maint", base+"api/x.bin", "x"); res.status != 201 {
			t.Fatal(res.status)
		}
		if res := b.do("maint", "DELETE", "/api/v1/repositories/generic-local/items?path=api/x.bin", nil); res.status != 204 {
			t.Fatalf("%d %s", res.status, res.body)
		}
		problemOf(t, a.do("admin", "DELETE", "/api/v1/repositories/generic-local/items", nil), 400, "invalid_path")
		problemOf(t, a.do("admin", "DELETE", base, nil), 400, "invalid_path")
		if missing := te.val.Uncovered("getItem", "deleteItem", "deleteFile"); len(missing) > 0 {
			t.Fatalf("not reached: %v", missing)
		}
	})

	t.Run("R-STOR-018 a subtree above storage.delete.max_sync_items is removed by a job in batches, after the permission check", func(t *testing.T) {
		e := newEnv(t)
		e.breakGlass()
		web := e.newReplica("web", replicaOpts{storage: func(c *config.Storage) { c.Delete.MaxSyncItems, c.Delete.BatchSize = 5, 4 }})
		worker := e.newReplica("worker", replicaOpts{worker: true, storage: func(c *config.Storage) { c.Delete.MaxSyncItems, c.Delete.BatchSize = 5, 4 }})
		_ = worker
		web.createRepository("bulk", nil)
		for i := range 14 {
			if res := web.put("admin", fmt.Sprintf("/files/bulk/tree/d%d/f%d.bin", i%3, i), fmt.Sprint(i)); res.status != 201 {
				t.Fatalf("%d %s", res.status, res.body)
			}
		}
		del := web.do("admin", "DELETE", "/files/bulk/tree", nil)
		if del.status != 202 || !strings.Contains(del.hdr("Location"), "/api/v1/system/jobs/") {
			t.Fatalf("%d %v\n%s", del.status, del.header, del.body)
		}
		waitFor(t, "the job to remove the subtree", 30*time.Second, func() bool {
			return e.number("SELECT count(*) FROM items WHERE path LIKE 'tree%'") == 0
		})
		if n := e.number("SELECT count(*) FROM job WHERE kind = 'items-delete' AND state = 'succeeded'"); n != 1 {
			t.Errorf("%d succeeded jobs", n)
		}
		if n := len(web.rec.ofType("artifact.deleted")) + len(worker.rec.ofType("artifact.deleted")); n != 14 {
			t.Errorf("%d artifact.deleted events, want one per file (14)", n)
		}
	})
}

func TestR_GENERIC_007_Patterns(t *testing.T) {
	te := newTransferEnv(t)
	a, b := te.a, te.b
	base := "/files/generic-local/"
	patch := func(body string) {
		t.Helper()
		g := a.get("admin", "/api/v1/repositories/generic-local")
		res := a.do("admin", "PATCH", "/api/v1/repositories/generic-local", strings.NewReader(body),
			"Content-Type", "application/merge-patch+json", "If-Match", g.hdr("ETag"))
		if res.status != 200 {
			t.Fatalf("patch: %d %s", res.status, res.body)
		}
	}
	patch(`{"includePatterns":["releases/**"],"excludePatterns":["**/*.pdb"]}`)
	waitFor(t, "the patterns on B", 10*time.Second, func() bool {
		return b.put("maint", base+"tmp/probe.bin", "x").status == 403
	})
	te.exec("DELETE FROM items WHERE path = 'tmp/probe.bin' OR path = 'tmp'") // stored by a probe before the patterns arrived

	t.Run("R-GENERIC-007 a deploy outside the include patterns or inside an exclude pattern answers 403 not_admitted for every caller", func(t *testing.T) {
		for _, user := range []string{"maint", "admin"} {
			problemOf(t, a.put(user, base+"tmp/x.bin", "x"), 403, "not_admitted")
			problemOf(t, b.put(user, base+"releases/1.0/x.pdb", "x"), 403, "not_admitted")
		}
		if n := te.number("SELECT count(*) FROM items WHERE path IN ('tmp/x.bin', 'releases/1.0/x.pdb')"); n != 0 {
			t.Fatalf("%d items stored", n)
		}
		if res := a.put("ci", base+"releases/1.0/x.bin", "x"); res.status != 201 {
			t.Fatalf("%d %s", res.status, res.body)
		}
	})
	t.Run("R-GENERIC-007 after a change of the patterns, GET answers 404 while the management API still returns the item", func(t *testing.T) {
		patch(`{"includePatterns":["other/**"]}`)
		waitFor(t, "the new patterns on both replicas", 10*time.Second, func() bool {
			return a.get("reader", base+"releases/1.0/x.bin").status == 404 && b.get("reader", base+"releases/1.0/x.bin").status == 404
		})
		if res := a.do("reader", "HEAD", base+"releases/1.0/x.bin", nil); res.status != 404 {
			t.Errorf("HEAD %d", res.status)
		}
		if res := a.get("maint", "/api/v1/repositories/generic-local/items?path=releases/1.0/x.bin"); res.status != 200 {
			t.Fatalf("items API: %d %s", res.status, res.body)
		}
		if res := a.do("maint", "DELETE", base+"releases/1.0/x.bin", nil); res.status != 404 {
			t.Errorf("DELETE through /files/: %d", res.status)
		}
		if res := a.do("maint", "DELETE", "/api/v1/repositories/generic-local/items?path=releases/1.0/x.bin", nil); res.status != 204 {
			t.Errorf("DELETE through the items API: %d", res.status)
		}
		if n := te.number("SELECT count(*) FROM items WHERE kind = 'file'"); n != 0 {
			t.Errorf("%d files", n)
		}
	})
	t.Run("R-REPO-004 a disabled repository takes no part in download or deploy and the management API still works", func(t *testing.T) {
		patch(`{"includePatterns":[],"excludePatterns":[]}`)
		if res := a.put("maint", base+"d/x.bin", "x"); res.status != 201 {
			t.Fatalf("%d %s", res.status, res.body)
		}
		patch(`{"disabled":true}`)
		waitFor(t, "the repository to be disabled on B", 10*time.Second, func() bool { return b.get("reader", base+"d/x.bin").status == 404 })
		problemOf(t, a.get("reader", base+"d/x.bin"), 404, "repository_disabled")
		problemOf(t, a.put("maint", base+"d/y.bin", "y"), 404, "repository_disabled")
		if res := a.get("maint", "/api/v1/repositories/generic-local/items?path=d/x.bin"); res.status != 200 {
			t.Errorf("items API on a disabled repository: %d", res.status)
		}
		m := a.get("admin", "/api/v1/repositories?state=inactive").json(t)
		if items, _ := m["items"].([]any); len(items) != 1 {
			t.Errorf("inactive = %v", m["items"])
		}
	})
}

func TestR_STOR_015_StatisticsActivityAndAudit(t *testing.T) {
	te := newTransferEnv(t)
	a, b := te.a, te.b
	p := "/files/generic-local/stats/widget.bin"
	data := content(2048, 7)
	if res := a.put("ci", p, string(data)); res.status != 201 {
		t.Fatal(res.status)
	}

	t.Run("R-STOR-015 downloads are counted in memory and flushed; HEAD, 304, a range past byte 0 and ?stats=false do not count", func(t *testing.T) {
		for range 3 {
			a.get("reader", p)
		}
		b.get("reader", p, "Range", "bytes=0-9")
		b.get("reader", p, "Range", "bytes=10-19")
		a.get("reader", p+"?stats=false")
		a.do("reader", "HEAD", p, nil)
		a.get("reader", p, "If-None-Match", `"`+sum(data)+`"`)
		a.flush()
		b.flush()
		m := a.get("reader", "/api/v1/repositories/generic-local/stats?path=stats/widget.bin").json(t)
		if m["downloads"] != float64(4) || m["lastDownloadAt"] == nil || m["lastDownloader"] != "user:reader" {
			t.Fatalf("stats = %v", m)
		}
		problemOf(t, a.get("reader", "/api/v1/repositories/generic-local/stats?path=stats"), 404, "not_found")
		if res := a.get("reader", "/api/v1/repositories/generic-local/stats?path=nothing"); res.status != 404 {
			t.Errorf("a missing file: %d", res.status)
		}
		if res := a.get("reader", "/api/v1/repositories/generic-local/stats"); res.status != 400 {
			t.Errorf("without a path: %d", res.status)
		}
	})
	t.Run("R-REPO-012 last_activity_at is written by the flush, at most once per interval, and the list shows it", func(t *testing.T) {
		waitFor(t, "last_activity_at", 5*time.Second, func() bool {
			return te.number("SELECT count(*) FROM repositories WHERE key = 'generic-local' AND last_activity_at IS NOT NULL") == 1
		})
		items := a.get("admin", "/api/v1/repositories").json(t)["items"].([]any)
		if items[0].(map[string]any)["lastActivityAt"] == nil {
			t.Errorf("list = %v", items[0])
		}
		if g := a.get("admin", "/api/v1/repositories/generic-local").json(t); g["lastActivityAt"] == nil {
			t.Errorf("read = %v", g)
		}
	})
	t.Run("R-OPS-015 artifact.deployed is transactional, artifact.downloaded is recorded with audit.record_downloads", func(t *testing.T) {
		if ev := a.rec.ofType("artifact.deployed"); len(ev) == 0 || ev[len(ev)-1].Repository != "generic-local" || ev[len(ev)-1].Actor.Name != "ci" {
			t.Errorf("artifact.deployed = %+v", ev)
		}
		if len(a.rec.ofType("artifact.downloaded")) == 0 {
			t.Error("no artifact.downloaded event")
		}
	})
	t.Run("R-API-002 the answers of the statistics operation matched the document", func(t *testing.T) {
		if missing := te.val.Uncovered("getItemStats", "downloadFile", "headFile"); len(missing) > 0 {
			t.Fatalf("not reached: %v", missing)
		}
	})
	t.Run("R-AUTHZ-029 a refused write is audited as authz.denied", func(t *testing.T) {
		a.put("reader", "/files/generic-local/nope", "x")
		waitFor(t, "the denial event", 5*time.Second, func() bool { return len(a.rec.ofType("authz.denied")) > 0 })
	})
}
