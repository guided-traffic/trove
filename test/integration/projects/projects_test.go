//go:build integration

package projects

import (
	"fmt"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/test/integration/stack"
)

// TestR_AUTHZ_026_ProjectLifecycleOverHTTP follows a project through create, read, list and replace, each request
// answered by another replica, and checks the ETag, the epoch header and the audit event of every write.
func TestR_AUTHZ_026_ProjectLifecycleOverHTTP(t *testing.T) {
	f := newFixture(t)
	before := f.epoch()

	created := f.do(f.admin, "POST", base, map[string]any{"key": "web", "displayName": "Web platform", "description": "The web team"})
	wantStatus(t, created, 201, "")
	if loc := created.Header.Get("Location"); loc != "http://trove.test"+base+"/web" {
		t.Errorf("Location = %q", loc)
	}
	body := created.JSON(t)
	if created.Header.Get("ETag") != `"1"` || body["version"] != float64(1) {
		t.Errorf("ETag %q, version %v", created.Header.Get("ETag"), body["version"])
	}
	if len(body["owners"].([]any)) != 0 || body["members"] != float64(0) || body["repositories"] != float64(0) || body["createdBy"] != "user:sec-root" {
		t.Errorf("%v", body)
	}
	if _, switches := body["managePolicies"]; switches {
		t.Errorf("a project has no switches (Q-AUTHZ-208): %v", body)
	}
	t.Run("R-AUTHZ-032 a create answers the epoch it produced and records it in the event", func(t *testing.T) {
		if created.Epoch() != f.epoch() || created.Epoch() <= before {
			t.Fatalf("header %d, database %d, before %d", created.Epoch(), f.epoch(), before)
		}
		if f.events("authz.project.created") != 1 || f.detail("authz.project.created", "epoch") != fmt.Sprint(created.Epoch()) {
			t.Errorf("events %d, epoch in the event %s", f.events("authz.project.created"), f.detail("authz.project.created", "epoch"))
		}
		if f.journal("authz.project.created") != 0 {
			t.Error("a create is not a revocation and has no journal entry")
		}
		if n := f.e.Number("SELECT count(*) FROM audit_event WHERE action = 'authz.project.created' AND project = 'web'"); n != 1 {
			t.Errorf("the event names the project: %d", n)
		}
	})
	t.Run("R-AUTHZ-026 a read answers the ETag, and a conditional read still answers the body (Q-AUTHZ-202)", func(t *testing.T) {
		for range 2 { // both replicas
			g := f.do(f.admin, "GET", base+"/web", nil)
			wantStatus(t, g, 200, "")
			if g.Header.Get("ETag") != `"1"` || g.JSON(t)["displayName"] != "Web platform" {
				t.Fatalf("%v", g.Header)
			}
		}
		wantStatus(t, f.do(f.admin, "GET", base+"/web", nil, "If-None-Match", `"1"`), 200, "")
		wantStatus(t, f.do(f.admin, "GET", base+"/nothing", nil), 404, "")
	})
	t.Run("R-AUTHZ-026 a key that exists answers 409 already_exists and changes nothing", func(t *testing.T) {
		epoch := f.epoch()
		wantStatus(t, f.do(f.admin, "POST", base, map[string]any{"key": "web", "displayName": "Again"}), 409, "already_exists")
		if f.epoch() != epoch || f.events("authz.project.created") != 1 {
			t.Error("a refused create raised the epoch or recorded an event")
		}
	})
	t.Run("R-AUTHZ-026 a replace with the current If-Match changes the display name and the description and moves the version", func(t *testing.T) {
		r := f.do(f.admin, "PUT", base+"/web", map[string]any{"displayName": "Web", "description": ""}, "If-Match", `"1"`)
		wantStatus(t, r, 200, "")
		m := r.JSON(t)
		if r.Header.Get("ETag") != `"2"` || m["displayName"] != "Web" || m["description"] != "" || m["key"] != "web" {
			t.Fatalf("%v %v", r.Header.Get("ETag"), m)
		}
		if r.Epoch() != f.epoch() {
			t.Errorf("epoch header %d, database %d", r.Epoch(), f.epoch())
		}
		// The display name and the description are not access-bearing (R-AUTHZ-031).
		if f.events("authz.project.updated") != 1 || f.journal("authz.project.updated") != 0 {
			t.Errorf("events %d, journal %d", f.events("authz.project.updated"), f.journal("authz.project.updated"))
		}
	})
	t.Run("R-AUTHZ-026 the list pages by key, filters by q and sorts both ways", func(t *testing.T) {
		for _, key := range []string{"docs", "ops"} {
			f.create(key)
		}
		all := f.do(f.admin, "GET", base, nil)
		wantStatus(t, all, 200, "")
		if got := keys(all, t); got != "docs,ops,web" {
			t.Fatalf("keys = %s", got)
		}
		first := f.do(f.admin, "GET", base+"?limit=2", nil)
		next, _ := first.JSON(t)["nextCursor"].(string)
		if keys(first, t) != "docs,ops" || next == "" {
			t.Fatalf("first page %s, cursor %q", keys(first, t), next)
		}
		second := f.do(f.admin, "GET", base+"?limit=2&cursor="+next, nil)
		if keys(second, t) != "web" || second.JSON(t)["nextCursor"] != nil {
			t.Errorf("second page %s", keys(second, t))
		}
		if got := keys(f.do(f.admin, "GET", base+"?sort=-key", nil), t); got != "web,ops,docs" {
			t.Errorf("descending: %s", got)
		}
		if got := keys(f.do(f.admin, "GET", base+"?q=PROJECT%20DO", nil), t); got != "docs" {
			t.Errorf("q on the display name: %s", got)
		}
		wantStatus(t, f.do(f.admin, "GET", base+"?limit=2&cursor=garbage", nil), 400, "invalid_cursor")
	})
}

func keys(r stack.Response, t *testing.T) string {
	t.Helper()
	var ks []string
	items, _ := r.JSON(t)["items"].([]any)
	for _, it := range items {
		ks = append(ks, it.(map[string]any)["key"].(string))
	}
	return strings.Join(ks, ",")
}

// TestR_AUTHZ_026_AReplaceNeedsTheCurrentIfMatch: a PUT without If-Match answers 428 and changes nothing, one with the
// ETag read before another administrator's PUT 412 with the current tag, If-Match: * is the blind write (R-API-012,
// Q-API-2); the auditor reads every project and is refused every write.
func TestR_AUTHZ_026_AReplaceNeedsTheCurrentIfMatch(t *testing.T) {
	f := newFixture(t)
	f.create("web")
	put := map[string]any{"displayName": "Web platform", "description": ""}
	staleTag := f.do(f.admin, "GET", base+"/web", nil).Header.Get("ETag")

	t.Run("R-AUTHZ-026 without If-Match the answer is 428 and nothing changes", func(t *testing.T) {
		epoch := f.epoch()
		wantStatus(t, f.do(f.admin, "PUT", base+"/web", put), 428, "precondition_required")
		if f.epoch() != epoch || f.do(f.admin, "GET", base+"/web", nil).JSON(t)["displayName"] != "Project web" {
			t.Error("a refused write changed something")
		}
	})
	t.Run("R-AUTHZ-026 another administrator replaces it meanwhile", func(t *testing.T) {
		wantStatus(t, f.do(f.second, "PUT", base+"/web", map[string]any{"displayName": "Web", "description": "x"}, "If-Match", staleTag), 200, "")
	})
	t.Run("R-AUTHZ-026 the ETag read before that PUT answers 412 with the current tag", func(t *testing.T) {
		r := f.do(f.admin, "PUT", base+"/web", put, "If-Match", staleTag)
		wantStatus(t, r, 412, "precondition_failed")
		if r.JSON(t)["currentEtag"] != `"2"` {
			t.Errorf("currentEtag = %v", r.JSON(t)["currentEtag"])
		}
		if got := f.do(f.admin, "GET", base+"/web", nil).JSON(t); got["displayName"] != "Web" {
			t.Errorf("a refused write changed the project: %v", got)
		}
	})
	t.Run("R-AUTHZ-026 If-Match: * is the deliberate blind write", func(t *testing.T) {
		r := f.do(f.admin, "PUT", base+"/web", put, "If-Match", "*")
		wantStatus(t, r, 200, "")
		if r.Header.Get("ETag") != `"3"` || r.JSON(t)["displayName"] != "Web platform" {
			t.Errorf("%v", r.Header)
		}
	})
	t.Run("R-AUTHZ-026 the auditor reads every project and is refused the writes", func(t *testing.T) {
		list := f.do(f.auditor, "GET", base, nil)
		wantStatus(t, list, 200, "")
		if keys(list, t) != "web" {
			t.Errorf("the auditor lists %s", keys(list, t))
		}
		wantStatus(t, f.do(f.auditor, "GET", base+"/web", nil), 200, "")
		wantStatus(t, f.do(f.auditor, "PUT", base+"/web", put, "If-Match", `"3"`), 403, "")
		wantStatus(t, f.do(f.auditor, "POST", base, map[string]any{"key": "docs", "displayName": "Docs"}), 403, "")
		wantStatus(t, f.do(f.auditor, "PUT", base+"/web/members/user:bob", map[string]any{"role": "reader"}), 403, "")
	})
}

func TestR_AUTHZ_026_KeyValidationAndImmutability(t *testing.T) {
	f := newFixture(t)
	f.create("web")
	for _, key := range []string{"Web", "w", "1ab", "web_app", "web.app", "-web", "web/app", "a" + strings.Repeat("b", 32)} {
		wantStatus(t, f.do(f.admin, "POST", base, map[string]any{"key": key, "displayName": "X"}), 422, "invalid_key")
	}
	t.Run("R-AUTHZ-026 the longest key of 32 characters and the shortest of 2 are accepted", func(t *testing.T) {
		f.create("a" + strings.Repeat("b", 31))
		f.create("zz")
	})
	t.Run("R-AUTHZ-026 the fields are checked", func(t *testing.T) {
		wantStatus(t, f.do(f.admin, "POST", base, map[string]any{"displayName": "No key"}), 422, "required")
		wantStatus(t, f.do(f.admin, "POST", base, map[string]any{"key": "ok", "displayName": ""}), 422, "invalid_display_name")
		wantStatus(t, f.do(f.admin, "POST", base, map[string]any{"key": "ok"}), 422, "invalid_display_name")
		wantStatus(t, f.do(f.admin, "POST", base, map[string]any{"key": "ok", "displayName": strings.Repeat("x", 129)}), 422, "invalid_display_name")
		wantStatus(t, f.do(f.admin, "POST", base, map[string]any{"key": "ok", "displayName": "X", "description": strings.Repeat("x", 1025)}), 422, "invalid_description")
		wantStatus(t, f.do(f.admin, "POST", base, map[string]any{"key": "ok", "displayName": "X", "surprise": 1}), 400, "")
		if f.e.Number("SELECT count(*) FROM project WHERE key = 'ok'") != 0 {
			t.Error("a refused create stored a project")
		}
	})
	t.Run("R-AUTHZ-026 the key cannot change: a different key in a replace is 422", func(t *testing.T) {
		put := map[string]any{"key": "other", "displayName": "X"}
		wantStatus(t, f.do(f.admin, "PUT", base+"/web", put, "If-Match", `"1"`), 422, "immutable")
		put["key"] = "web" // the body of a GET goes back unchanged
		wantStatus(t, f.do(f.admin, "PUT", base+"/web", put, "If-Match", `"1"`), 200, "")
		wantStatus(t, f.do(f.admin, "PUT", base+"/nothing", map[string]any{"displayName": "X"}, "If-Match", "*"), 404, "")
	})
}

// TestQ_AUTHZ_209_KeysAndNamesNeedNoOverlapRule: the names of a project's objects carry its key and a `/`, which no
// key holds, so a key that starts another key or the name of a service account of no project is just a key
// (Q-AUTHZ-209: the prefix and its overlap rule went).
func TestQ_AUTHZ_209_KeysAndNamesNeedNoOverlapRule(t *testing.T) {
	f := newFixture(t)
	f.create("web")
	f.create("web-app")
	f.e.ServiceAccount("docs-bot")
	f.create("docs")
	f.seedProjectServiceAccount("docs", "bot")
	if n := f.e.Number("SELECT count(*) FROM principal WHERE name IN ('docs-bot', 'docs/bot')"); n != 2 {
		t.Fatalf("%d accounts", n)
	}
}

func TestR_AUTHZ_026_IdempotentCreate(t *testing.T) {
	f := newFixture(t)
	body := map[string]any{"key": "web", "displayName": "Web"}
	first := f.do(f.admin, "POST", base, body, "Idempotency-Key", "k-"+f.t.Name())
	wantStatus(t, first, 201, "")
	for range 3 {
		again := f.do(f.admin, "POST", base, body, "Idempotency-Key", "k-"+f.t.Name())
		wantStatus(t, again, 201, "")
		if again.Header.Get("Idempotency-Replayed") != "true" || again.Epoch() != first.Epoch() || string(again.Body) != string(first.Body) ||
			again.Header.Get("ETag") != first.Header.Get("ETag") {
			t.Fatalf("replay: epoch %d, want %d; %q", again.Epoch(), first.Epoch(), again.Header.Get("Idempotency-Replayed"))
		}
	}
	if f.e.Number("SELECT count(*) FROM project WHERE key = 'web'") != 1 || f.events("authz.project.created") != 1 {
		t.Errorf("projects %d, events %d", f.e.Number("SELECT count(*) FROM project WHERE key = 'web'"), f.events("authz.project.created"))
	}
	// The same key with another body is a conflict, not a second project.
	wantStatus(t, f.do(f.admin, "POST", base, map[string]any{"key": "docs", "displayName": "Docs"}, "Idempotency-Key", "k-"+f.t.Name()), 422, "idempotency_key_reused")
}

// TestR_AUTHZ_032_EveryProjectWriteRaisesTheEpochAndAnswersIt walks every write of the project operations; each
// answers Trove-Authz-Epoch equal to the epoch the database holds after it, and each epoch is higher than the one
// before. The events are recorded once each, and those that narrow are journaled (R-AUTHZ-029, R-AUTHZ-031).
func TestR_AUTHZ_032_EveryProjectWriteRaisesTheEpochAndAnswersIt(t *testing.T) {
	f := newFixture(t)
	f.e.User("carol")
	f.create("docs")
	f.seedRepo("docs", "npm", "active")
	last := f.epoch()
	step := func(name string, r stack.Response) {
		t.Helper()
		if got := r.Epoch(); got <= last || got != f.epoch() {
			t.Fatalf("%s: header %d, database %d, previous %d", name, got, f.epoch(), last)
		}
		last = r.Epoch()
	}
	r := f.do(f.admin, "POST", base, map[string]any{"key": "web", "displayName": "Web"})
	wantStatus(t, r, 201, "")
	step("create", r)
	r = f.do(f.admin, "PUT", base+"/web", map[string]any{"displayName": "Web platform"}, "If-Match", `"1"`)
	wantStatus(t, r, 200, "")
	step("replace", r)
	r = f.member("web", "user:carol", "owner")
	wantStatus(t, r, 201, "")
	step("add a member", r)
	r = f.member("web", "user:carol", "reader")
	wantStatus(t, r, 200, "")
	step("change the role", r)
	r = f.do(f.admin, "POST", base+"/docs/repositories/npm/move", map[string]any{"project": "web"})
	wantStatus(t, r, 200, "")
	step("move in", r)
	r = f.do(f.admin, "POST", base+"/web/repositories/npm/move", map[string]any{"project": "docs"})
	wantStatus(t, r, 200, "")
	step("move out", r)
	r = f.do(f.admin, "DELETE", base+"/web/members/user:carol", nil)
	wantStatus(t, r, 204, "")
	step("remove the member", r)
	r = f.do(f.admin, "DELETE", base+"/web", nil)
	wantStatus(t, r, 204, "")
	step("delete", r)

	t.Run("R-AUTHZ-029 each write is one audit event in its own transaction", func(t *testing.T) {
		for action, want := range map[string]int64{
			"authz.project.created": 2, "authz.project.updated": 1, "authz.project.member_added": 1, "authz.project.member_changed": 1,
			"repository.moved": 2, "authz.project.member_removed": 1, "authz.project.deleted": 1,
		} {
			if got := f.events(action); got != want {
				t.Errorf("%s: %d events, want %d", action, got, want)
			}
		}
	})
	t.Run("R-AUTHZ-031 the narrowings are journaled, the grants and the name are not", func(t *testing.T) {
		for event, want := range map[string]int64{
			"authz.project.created": 0, "authz.project.member_added": 0, "authz.project.updated": 0,
			"authz.project.member_changed": 1, "authz.project.member_removed": 1, "repository.moved": 2, "authz.project.deleted": 1,
		} {
			if got := f.journal(event); got != want {
				t.Errorf("%s: %d journal entries, want %d", event, got, want)
			}
		}
		entry := f.e.Text("SELECT entry::text FROM revocation_outbox WHERE event = 'authz.project.member_removed'")
		if !strings.Contains(entry, `"principal": "user:carol"`) || !strings.Contains(entry, `"project": "web"`) || !strings.Contains(entry, "principalId") {
			t.Errorf("the journal entry of the removal: %s", entry)
		}
		entry = f.e.Text("SELECT entry::text FROM revocation_outbox WHERE event = 'authz.project.member_changed'")
		if !strings.Contains(entry, `"before": "owner"`) || !strings.Contains(entry, `"after": "reader"`) {
			t.Errorf("the journal entry of the change: %s", entry)
		}
	})
}

// TestR_AUTHZ_026_AMemberSeesItsOwnProjectsOnly: a member holds no projects:read and sees exactly the projects it is
// a member of, each with its role; the read of another project answers as an unknown one does (R-AUTHZ-026,
// R-AUTHZ-016).
func TestR_AUTHZ_026_AMemberSeesItsOwnProjectsOnly(t *testing.T) {
	f := newFixture(t)
	for _, k := range []string{"web", "shop", "lab"} {
		f.create(k)
	}
	wantStatus(t, f.member("web", "group:web-leads", "owner"), 201, "")
	last := f.member("lab", "user:alice", "reader")
	wantStatus(t, last, 201, "")
	// The decision point of each replica takes the epoch of the write (R-AUTHZ-032).
	epoch := fmt.Sprint(last.Epoch())
	t.Run("R-AUTHZ-026 the list holds the projects of the memberships with the role, and no other", func(t *testing.T) {
		for range 2 { // both replicas
			r := f.do(f.alice, "GET", base, nil, authz.HeaderMinEpoch, epoch)
			wantStatus(t, r, 200, "")
			if got := keys(r, t); got != "lab,web" {
				t.Fatalf("alice sees %s", got)
			}
			for _, it := range r.Items(t) {
				if want := map[string]string{"lab": "reader", "web": "owner"}[it["key"].(string)]; it["role"] != want {
					t.Errorf("%v: role %v, want %s", it["key"], it["role"], want)
				}
			}
		}
	})
	t.Run("R-AUTHZ-026 the read of a project of a membership is 200, of another 404 as of an unknown one", func(t *testing.T) {
		wantStatus(t, f.do(f.alice, "GET", base+"/web", nil, authz.HeaderMinEpoch, epoch), 200, "")
		wantStatus(t, f.do(f.alice, "GET", base+"/lab", nil, authz.HeaderMinEpoch, epoch), 200, "")
		shop := f.do(f.alice, "GET", base+"/shop", nil, authz.HeaderMinEpoch, epoch)
		wantStatus(t, shop, 404, "")
		unknown := f.do(f.alice, "GET", base+"/nothing", nil, authz.HeaderMinEpoch, epoch)
		wantStatus(t, unknown, 404, "")
		if shop.Code() != unknown.Code() {
			t.Errorf("another project %q, an unknown one %q", shop.Code(), unknown.Code())
		}
	})
	t.Run("R-AUTHZ-026 a caller that holds neither projects:read nor a membership is refused the list and reads nothing", func(t *testing.T) {
		wantStatus(t, f.do(f.bob, "GET", base, nil, authz.HeaderMinEpoch, epoch), 403, "forbidden")
		wantStatus(t, f.do(f.bob, "GET", base+"/web", nil, authz.HeaderMinEpoch, epoch), 404, "")
	})
	t.Run("R-AUTHZ-026 a Reader reads the project and is refused its administration", func(t *testing.T) {
		wantStatus(t, f.do(f.alice, "GET", base+"/lab/members", nil, authz.HeaderMinEpoch, epoch), 200, "")
		wantStatus(t, f.do(f.alice, "PUT", base+"/lab", map[string]any{"displayName": "Lab"}, authz.HeaderMinEpoch, epoch, "If-Match", "*"), 403, "")
		wantStatus(t, f.do(f.alice, "PUT", base+"/lab/members/user:bob", map[string]any{"role": "reader"}, authz.HeaderMinEpoch, epoch), 403, "")
	})
	t.Run("R-AUTHZ-026 an Owner changes the display name and the description", func(t *testing.T) {
		wantStatus(t, f.do(f.alice, "PUT", base+"/web", map[string]any{"displayName": "Web", "description": "d"}, authz.HeaderMinEpoch, epoch, "If-Match", "*"), 200, "")
		wantStatus(t, f.do(f.alice, "DELETE", base+"/web", nil, authz.HeaderMinEpoch, epoch), 403, "")
	})
}
