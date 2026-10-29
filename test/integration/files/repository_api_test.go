//go:build integration

package files

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/authz"
)

// across returns a Do function for apitest.RunContract that spreads the requests
// over the replicas and signs them as the named user.
func across(user string, reps ...*replica) func(*http.Request) (*http.Response, error) {
	var n atomic.Int64
	return func(req *http.Request) (*http.Response, error) {
		rep := reps[int(n.Add(1))%len(reps)]
		req.URL.Scheme, req.URL.Host = "http", strings.TrimPrefix(rep.baseURL, "http://")
		req.Host = req.URL.Host
		if user != "" {
			req.Header.Set("Authorization", "Bearer "+user)
		}
		return client.Do(req) //nolint:bodyclose // the caller of Do closes the body
	}
}

func problemOf(t testing.TB, res response, status int, code string) map[string]any {
	t.Helper()
	m := res.json(t)
	if res.status != status || m["code"] != code {
		t.Fatalf("answer %d %v, want %d %s\n%s", res.status, m["code"], status, code, res.body)
	}
	return m
}

// TestRepositoryResourceContract points the contract tests of E1.6 at the repository
// resource: pagination, ETag with If-Match, merge patch and idempotency keys, with
// the requests spread over two replicas and every answer validated against the
// document.
func TestRepositoryResourceContract(t *testing.T) {
	e := newEnv(t)
	e.breakGlass()
	val := apitest.New(t, v1.EmbeddedYAML())
	a := e.newReplica("a", replicaOpts{validate: val})
	b := e.newReplica("b", replicaOpts{validate: val})
	run := randHex(3)

	apitest.RunContract(t, apitest.Resource{
		Name: "repositories", Do: across("admin", a, b), //nolint:bodyclose // the contract tests close the bodies
		Collection: a.baseURL + "/api/v1/repositories",
		Create: func(n int) map[string]any {
			return map[string]any{"key": fmt.Sprintf("contract-%s-%d", run, n), "class": "local", "format": "generic", "description": "contract"}
		},
		Key:         func(c map[string]any) string { return c["key"].(string) },
		Patch:       map[string]any{"description": "patched"},
		PatchMember: "description", PatchValue: "patched", AltPatch: map[string]any{"description": "patched again"},
		ClearMember: "description", ClearedValue: "",
		ImmutableMember: "class", ImmutableValue: "remote",
		OtherQuery: "class=local", Put: true,
		MalformedCode: "invalid_document", ImmutableCode: "immutable_field",
	})
	t.Run("R-API-002 E1.6 the answers of the repository operations were validated against the document", func(t *testing.T) {
		if missing := val.Uncovered("createRepository", "listRepositories", "getRepository", "updateRepository", "replaceRepository"); len(missing) > 0 {
			t.Fatalf("not reached: %v", missing)
		}
	})
}

func TestR_REPO_003_Formats(t *testing.T) {
	e := newEnv(t)
	e.user("alice")
	r := e.newReplica("a", replicaOpts{validate: apitest.New(t, v1.EmbeddedYAML())})

	t.Run("R-REPO-003 R-GENERIC-001 the catalog lists generic with the values of its descriptor", func(t *testing.T) {
		res := r.get("alice", "/api/v1/formats")
		if res.status != 200 {
			t.Fatalf("%d %s", res.status, res.body)
		}
		var got struct {
			Formats []map[string]any `json:"formats"`
		}
		_ = json.Unmarshal(res.body, &got)
		if len(got.Formats) == 0 {
			t.Fatal("no formats")
		}
		var g map[string]any
		for _, f := range got.Formats {
			if f["name"] == "generic" {
				g = f
			}
		}
		want := map[string]any{
			"layout": "generic", "protocolBasePath": "/files/{key}/", "defaultUpstreamUrl": nil, "defaultMetadataTtlSeconds": nil,
			"defaultNotFoundTtlSeconds": float64(1800), "patternSubject": "path", "priorityMembers": false, "pinsAndClaims": false, "caps": false,
		}
		for k, v := range want {
			if fmt.Sprint(g[k]) != fmt.Sprint(v) {
				t.Errorf("%s = %v, want %v", k, g[k], v)
			}
		}
		if cl := fmt.Sprint(g["classes"]); cl != "[local]" {
			t.Errorf("classes = %s: the descriptor lists only what is implemented (Q-GENERIC-1)", cl)
		}
	})
	t.Run("R-REPO-003 an anonymous caller is answered 401 while anonymous access is off, and 200 when it is on", func(t *testing.T) {
		problemOf(t, r.get("", "/api/v1/formats"), 401, "unauthenticated")
		e.anonymous.Store(true)
		defer e.anonymous.Store(false)
		if res := r.get("", "/api/v1/formats"); res.status != 200 {
			t.Fatalf("%d", res.status)
		}
	})
}

func TestR_REPO_009_to_013_RepositoryLifecycle(t *testing.T) {
	e := newEnv(t)
	e.breakGlass()
	e.user("bob")
	rita := e.user("rita")
	val := apitest.New(t, v1.EmbeddedYAML())
	a := e.newReplica("a", replicaOpts{validate: val})
	b := e.newReplica("b", replicaOpts{validate: val})
	if _, _, err := a.policy.BindRole(context.Background(), e.actor(), authz.RoleRepositoryAdmin, "user:"+rita.Name); err != nil {
		t.Fatal(err)
	}

	t.Run("R-REPO-009 create answers 201 with Location and ETag, defaults applied, and never an upsert", func(t *testing.T) {
		res := a.postJSON("admin", "/api/v1/repositories", map[string]any{"key": "gen-one", "class": "local", "format": "generic", "description": "one"})
		if res.status != 201 || res.header.Get("ETag") != `"1"` || !strings.HasSuffix(res.header.Get("Location"), "/api/v1/repositories/gen-one") {
			t.Fatalf("%d %v\n%s", res.status, res.header, res.body)
		}
		m := res.json(t)
		if m["layout"] != "generic" || m["hideExistence"] != "inherit" || m["lifecycle"] != "active" || m["warnings"] == nil {
			t.Errorf("document = %v", m)
		}
		for _, k := range []string{"local", "remote", "virtual"} {
			if _, ok := m[k]; ok {
				t.Errorf("a generic local repository has no %s member (R-GENERIC-008)", k)
			}
		}
		problemOf(t, a.postJSON("admin", "/api/v1/repositories", map[string]any{"key": "gen-one", "class": "local", "format": "generic"}), 409, "repository_exists")
	})

	t.Run("R-REPO-009 a repository created on A is usable on B at once (R-API-052)", func(t *testing.T) {
		if res := a.postJSON("admin", "/api/v1/repositories", map[string]any{"key": "gen-two", "class": "local", "format": "generic"}); res.status != 201 {
			t.Fatalf("%d %s", res.status, res.body)
		}
		if res := b.get("admin", "/api/v1/repositories/gen-two"); res.status != 200 {
			t.Fatalf("read on B: %d %s", res.status, res.body)
		}
		if res := b.put("admin", "/files/gen-two/x.txt", "x"); res.status != 201 {
			t.Fatalf("deploy on B: %d %s", res.status, res.body)
		}
	})

	t.Run("R-REPO-008 the validation reports unknown members, classes, formats, keys and several violations at once", func(t *testing.T) {
		post := func(body map[string]any) response { return a.postJSON("admin", "/api/v1/repositories", body) }
		base := func(extra map[string]any) map[string]any {
			m := map[string]any{"key": "val-" + randHex(2), "class": "local", "format": "generic"}
			for k, v := range extra {
				m[k] = v
			}
			return m
		}
		m := problemOf(t, post(base(map[string]any{"excludePattern": []string{"x"}})), 400, "invalid_document")
		if errs, _ := m["errors"].([]any); len(errs) == 0 || errs[0].(map[string]any)["pointer"] != "/excludePattern" {
			t.Errorf("errors = %v", m["errors"])
		}
		problemOf(t, post(base(map[string]any{"generic": map[string]any{}})), 400, "invalid_document")
		problemOf(t, post(base(map[string]any{"class": "remote"})), 422, "unsupported_class")
		problemOf(t, post(base(map[string]any{"class": "virtual"})), 422, "unsupported_class")
		problemOf(t, post(base(map[string]any{"format": "cobol"})), 422, "unknown_format")
		problemOf(t, post(base(map[string]any{"layout": "maven-2"})), 422, "unsupported_layout")
		problemOf(t, post(base(map[string]any{"key": "batch-create"})), 422, "reserved_name")
		problemOf(t, post(base(map[string]any{"key": "Bad_Key"})), 422, "invalid_key")
		problemOf(t, post(base(map[string]any{"local": map[string]any{"packageOwnership": "enforce"}})), 422, "not_applicable")
		multi := problemOf(t, post(base(map[string]any{"key": "NOPE", "format": "cobol", "description": strings.Repeat("x", 2001)})), 422, "validation_failed")
		if errs, _ := multi["errors"].([]any); len(errs) < 3 {
			t.Errorf("one response must report every violation: %v", multi["errors"])
		}
	})

	t.Run("R-REPO-009 a caller without repositories:write-config gets 403 and nothing is created; anonymous gets 401", func(t *testing.T) {
		problemOf(t, a.postJSON("bob", "/api/v1/repositories", map[string]any{"key": "bobs", "class": "local", "format": "generic"}), 403, "forbidden")
		problemOf(t, a.postJSON("", "/api/v1/repositories", map[string]any{"key": "bobs", "class": "local", "format": "generic"}), 401, "unauthenticated")
		if n := e.number("SELECT count(*) FROM repositories WHERE key = 'bobs'"); n != 0 {
			t.Fatalf("%d repositories", n)
		}
	})

	t.Run("R-AUTHZ-012 the repository administrator creates a repository and still cannot read its content", func(t *testing.T) {
		waitFor(t, "the role binding on replica A", 5*time.Second, func() bool {
			return a.engine.Require(context.Background(), authz.Subject{Kind: "user", ID: rita.ID, Name: rita.Name}, "repositories:write-config") == nil
		})
		if res := a.postJSON("rita", "/api/v1/repositories", map[string]any{"key": "ritas", "class": "local", "format": "generic"}); res.status != 201 {
			t.Fatalf("%d %s", res.status, res.body)
		}
		if res := a.get("rita", "/files/ritas/x"); res.status != 404 {
			t.Fatalf("content read: %d %s", res.status, res.body)
		}
		if res := a.put("rita", "/files/ritas/x", "x"); res.status != 403 {
			t.Fatalf("content write: %d %s", res.status, res.body)
		}
		if res := a.get("rita", "/api/v1/repositories?view=full"); res.status != 200 {
			t.Fatalf("view=full: %d %s", res.status, res.body)
		}
	})

	t.Run("R-REPO-010 a holder of read-config reads the document; a reader gets the consumer view; the rest get the same 404 as for a missing repository", func(t *testing.T) {
		e.user("carol")
		e.user("dave")
		e.grant(a, "gen-one-read", []string{"gen-one"}, authz.PatternLists{}, map[string][]string{"user:carol": {"read"}})
		adminRead := a.get("admin", "/api/v1/repositories/gen-one")
		if adminRead.status != 200 || adminRead.header.Get("ETag") != `"1"` {
			t.Fatalf("%d %v", adminRead.status, adminRead.header)
		}
		waitFor(t, "carol's grant on both replicas", 5*time.Second, func() bool {
			return a.get("carol", "/api/v1/repositories/gen-one").status == 200 && b.get("carol", "/api/v1/repositories/gen-one").status == 200
		})
		consumer := a.get("carol", "/api/v1/repositories/gen-one")
		cm := consumer.json(t)
		if consumer.header.Get("ETag") != `"1-consumer"` || cm["key"] != "gen-one" || cm["layout"] != "generic" {
			t.Fatalf("%v\n%s", consumer.header, consumer.body)
		}
		for _, k := range []string{"includePatterns", "adminNotes", "disabled", "remote", "storage", "lifecycle"} {
			if _, ok := cm[k]; ok {
				t.Errorf("the consumer view has %s", k)
			}
		}
		if addr, _ := cm["addresses"].([]any); len(addr) == 0 || !strings.HasSuffix(addr[0].(string), "/files/gen-one/") {
			t.Errorf("addresses = %v", cm["addresses"])
		}
		hidden, missing := a.get("dave", "/api/v1/repositories/gen-one"), a.get("dave", "/api/v1/repositories/no-such-repository")
		if hidden.status != 404 || missing.status != 404 || normalize(hidden.text()) != normalize(missing.text()) {
			t.Fatalf("hidden %d %s\nmissing %d %s", hidden.status, hidden.body, missing.status, missing.body)
		}
		if res := a.get("carol", "/api/v1/repositories/gen-one", "If-None-Match", `"1-consumer"`); res.status != 304 {
			t.Fatalf("304 expected: %d", res.status)
		}
	})

	t.Run("R-REPO-011 two PATCH calls with one If-Match: the first wins, the second is 412 with currentEtag; without If-Match 428; with * 200", func(t *testing.T) {
		patch := func(rep *replica, body string, hdr ...string) response {
			return rep.do("admin", "PATCH", "/api/v1/repositories/gen-one", strings.NewReader(body),
				append([]string{"Content-Type", "application/merge-patch+json"}, hdr...)...)
		}
		first := patch(a, `{"description":"first"}`, "If-Match", `"1"`)
		if first.status != 200 || first.header.Get("ETag") != `"2"` {
			t.Fatalf("%d %v\n%s", first.status, first.header, first.body)
		}
		stale := problemOf(t, patch(b, `{"description":"second"}`, "If-Match", `"1"`), 412, "precondition_failed")
		if stale["currentEtag"] != `"2"` {
			t.Errorf("currentEtag = %v", stale["currentEtag"])
		}
		problemOf(t, patch(a, `{"description":"third"}`), 428, "precondition_required")
		if g := a.get("admin", "/api/v1/repositories/gen-one"); g.header.Get("ETag") != `"2"` {
			t.Errorf("a refused PATCH changed the repository: %v", g.header.Get("ETag"))
		}
		if star := patch(a, `{"description":"third"}`, "If-Match", "*"); star.status != 200 || star.header.Get("ETag") != `"3"` {
			t.Fatalf("%d %v", star.status, star.header)
		}
		same := patch(a, `{"description":"third"}`, "If-Match", `"3"`)
		if same.status != 200 || same.header.Get("ETag") != `"3"` {
			t.Errorf("a patch that changes nothing writes nothing: %d %v", same.status, same.header)
		}
		problemOf(t, patch(a, `{"key":"other"}`, "If-Match", `"3"`), 422, "immutable_field")
		problemOf(t, patch(a, `{"description":1}`, "If-Match", `"3"`), 400, "invalid_document")
		problemOf(t, patch(a, `{"description":"`+strings.Repeat("x", 2001)+`"}`, "If-Match", `"3"`), 422, "out_of_range")
	})

	t.Run("R-REPO-011 PUT never creates a repository", func(t *testing.T) {
		problemOf(t, a.do("admin", "PUT", "/api/v1/repositories/no-such-repository", strings.NewReader(`{"key":"no-such-repository","class":"local","format":"generic"}`),
			"Content-Type", "application/json", "If-Match", "*"), 404, "repository_not_found")
	})

	t.Run("R-REPO-012 the list filters by class, format and q and a walk with limit=1 returns every repository once", func(t *testing.T) {
		list := func(user, query string) map[string]any {
			res := a.get(user, "/api/v1/repositories"+query)
			if res.status != 200 {
				t.Fatalf("%s: %d %s", query, res.status, res.body)
			}
			return res.json(t)
		}
		items := func(m map[string]any) []string {
			var keys []string
			for _, it := range m["items"].([]any) {
				keys = append(keys, it.(map[string]any)["key"].(string))
			}
			return keys
		}
		if got := items(list("admin", "?class=remote")); len(got) != 0 {
			t.Errorf("remote: %v", got)
		}
		if got := items(list("admin", "?format=generic&q=GEN-O")); fmt.Sprint(got) != "[gen-one]" {
			t.Errorf("q: %v", got)
		}
		if got := items(list("admin", "?class=local,remote&format=generic&format=npm&state=inactive")); len(got) != 0 {
			t.Errorf("state=inactive: %v", got)
		}
		if got := items(list("admin", "?project=acme")); len(got) != 0 {
			t.Errorf("project: %v", got)
		}
		all := items(list("admin", ""))
		var walked []string
		url := "?limit=1"
		for range 50 {
			page := list("admin", url)
			walked = append(walked, items(page)...)
			next, _ := page["nextCursor"].(string)
			if next == "" {
				break
			}
			url = "?limit=1&cursor=" + next
		}
		if fmt.Sprint(walked) != fmt.Sprint(all) || len(all) < 3 {
			t.Fatalf("walk %v, list %v", walked, all)
		}
		if res := a.get("admin", "/api/v1/repositories?state=bogus"); res.status != 400 {
			t.Errorf("state=bogus: %d", res.status)
		}
	})

	t.Run("R-REPO-012 R-AUTHZ-016 the list holds the repositories the caller may read, anonymous included", func(t *testing.T) {
		if m := a.get("carol", "/api/v1/repositories").json(t); len(m["items"].([]any)) != 1 || m["items"].([]any)[0].(map[string]any)["key"] != "gen-one" {
			t.Errorf("carol sees %v", m["items"])
		}
		if m := a.get("dave", "/api/v1/repositories").json(t); len(m["items"].([]any)) != 0 {
			t.Errorf("dave sees %v", m["items"])
		}
		problemOf(t, a.get("", "/api/v1/repositories"), 401, "unauthenticated")
		e.grant(a, "gen-two-anon", []string{"gen-two"}, authz.PatternLists{}, map[string][]string{"anonymous": {"read"}})
		e.anonymous.Store(true)
		defer e.anonymous.Store(false)
		waitFor(t, "the anonymous grant", 5*time.Second, func() bool {
			m := b.get("", "/api/v1/repositories").json(t)
			items, _ := m["items"].([]any)
			return len(items) == 1 && items[0].(map[string]any)["key"] == "gen-two"
		})
		problemOf(t, a.get("carol", "/api/v1/repositories?view=full"), 403, "forbidden")
	})

	t.Run("R-REPO-013 delete answers 202 with the job, a repeat the same job; the repository is gone for readers at once and its key is free when the job is done", func(t *testing.T) {
		w := e.newReplica("w", replicaOpts{worker: true})
		a.createRepository("to-delete", nil)
		for i := range 5 {
			if res := a.put("admin", fmt.Sprintf("/files/to-delete/d/f%d.txt", i), "content "+fmt.Sprint(i)); res.status != 201 {
				t.Fatalf("%d %s", res.status, res.body)
			}
		}
		stale := a.do("admin", "DELETE", "/api/v1/repositories/to-delete", nil, "If-Match", `"99"`)
		problemOf(t, stale, 412, "precondition_failed")
		del := a.do("admin", "DELETE", "/api/v1/repositories/to-delete", nil)
		if del.status != 202 || !strings.Contains(del.header.Get("Location"), "/api/v1/system/jobs/") {
			t.Fatalf("%d %v\n%s", del.status, del.header, del.body)
		}
		job := del.json(t)["job"].(map[string]any)
		again := b.do("admin", "DELETE", "/api/v1/repositories/to-delete", nil)
		if again.status != 202 || again.json(t)["job"].(map[string]any)["id"] != job["id"] {
			t.Fatalf("repeat: %d %s", again.status, again.body)
		}
		if res := b.get("admin", "/files/to-delete/d/f0.txt"); res.status != 404 {
			t.Errorf("a repository being deleted is not addressable: %d", res.status)
		}
		waitFor(t, "the key to be free", 30*time.Second, func() bool {
			return e.number("SELECT count(*) FROM repositories WHERE key = 'to-delete'") == 0
		})
		if n := e.number("SELECT count(*) FROM items i WHERE NOT EXISTS (SELECT 1 FROM repositories r WHERE r.id = i.repo_id)"); n != 0 {
			t.Errorf("%d orphan items", n)
		}
		a.createRepository("to-delete", nil)
		_ = w
	})

	t.Run("R-REPO-009 R-API-014 an Idempotency-Key on create is replayed through another replica, byte for byte", func(t *testing.T) {
		body := map[string]any{"key": "idem-" + randHex(2), "class": "local", "format": "generic", "description": "once"}
		key := "key-" + randHex(4)
		first := a.postJSON("admin", "/api/v1/repositories", body, "Idempotency-Key", key)
		replay := b.postJSON("admin", "/api/v1/repositories", body, "Idempotency-Key", key)
		if first.status != 201 || replay.status != 201 || replay.header.Get("Idempotency-Replayed") != "true" || string(first.body) != string(replay.body) {
			t.Fatalf("first %d\n%s\nreplay %d %v\n%s", first.status, first.body, replay.status, replay.header, replay.body)
		}
		if n := e.number("SELECT count(*) FROM repositories WHERE key = $1", body["key"]); n != 1 {
			t.Fatalf("%d repositories", n)
		}
		problemOf(t, b.postJSON("admin", "/api/v1/repositories", map[string]any{"key": "other-" + randHex(2), "class": "local", "format": "generic"}, "Idempotency-Key", key),
			422, "idempotency_key_reused")
	})

	t.Run("R-REPO-009 R-REPO-011 R-REPO-013 the audit events of the writes are recorded in their transactions", func(t *testing.T) {
		for _, typ := range []string{"repository.created", "repository.updated", "repository.deleted"} {
			if len(a.rec.ofType(typ)) == 0 {
				t.Errorf("no %s event on A", typ)
			}
		}
	})

	t.Run("R-API-002 the answers of every repository operation matched the document", func(t *testing.T) {
		if missing := val.Uncovered("createRepository", "listRepositories", "getRepository", "updateRepository", "deleteRepository"); len(missing) > 0 {
			t.Fatalf("not reached: %v", missing)
		}
	})
}

var volatile = regexp.MustCompile(`"(instance|requestId)":"[^"]*"`)

// normalize removes the members of a problem that differ between two requests
// (R-AUTHZ-016: a hidden 404 equals a missing one in body and headers, not in the
// request ID and the path it echoes).
func normalize(body string) string { return volatile.ReplaceAllString(body, `"$1":""`) }
