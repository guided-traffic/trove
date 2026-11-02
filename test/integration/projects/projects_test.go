//go:build integration

package projects

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/test/integration/stack"
)

// TestR_AUTHZ_026_ProjectLifecycleOverHTTP follows a project through create, read,
// list and replace, each request answered by another replica, and checks the ETag,
// the epoch header and the audit event of every write.
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
	if body["managePolicies"] != true || body["manageRepositories"] != false {
		t.Errorf("the defaults are managePolicies on and manageRepositories off: %v", body)
	}
	if len(body["admins"].([]any)) != 0 || body["repositories"] != float64(0) || body["createdBy"] != "user:sec-root" {
		t.Errorf("%v", body)
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
	})
	t.Run("R-AUTHZ-026 a read answers the ETag and 304 for the current tag", func(t *testing.T) {
		for range 2 { // both replicas
			g := f.do(f.admin, "GET", base+"/web", nil)
			wantStatus(t, g, 200, "")
			if g.Header.Get("ETag") != `"1"` || g.JSON(t)["displayName"] != "Web platform" {
				t.Fatalf("%v", g.Header)
			}
		}
		wantStatus(t, f.do(f.admin, "GET", base+"/web", nil, "If-None-Match", `"1"`), 304, "")
		wantStatus(t, f.do(f.admin, "GET", base+"/nothing", nil), 404, "")
	})
	t.Run("R-AUTHZ-026 a key that exists answers 409 already_exists and changes nothing", func(t *testing.T) {
		epoch := f.epoch()
		wantStatus(t, f.do(f.admin, "POST", base, map[string]any{"key": "web", "displayName": "Again"}), 409, "already_exists")
		if f.epoch() != epoch || f.events("authz.project.created") != 1 {
			t.Error("a refused create raised the epoch or recorded an event")
		}
	})
	t.Run("R-AUTHZ-026 a replace with the current If-Match changes the four fields and moves the version", func(t *testing.T) {
		r := f.do(f.admin, "PUT", base+"/web", map[string]any{
			"displayName": "Web", "description": "", "managePolicies": true, "manageRepositories": true,
		}, "If-Match", `"1"`)
		wantStatus(t, r, 200, "")
		m := r.JSON(t)
		if r.Header.Get("ETag") != `"2"` || m["manageRepositories"] != true || m["displayName"] != "Web" || m["key"] != "web" {
			t.Fatalf("%v %v", r.Header.Get("ETag"), m)
		}
		if r.Epoch() != f.epoch() {
			t.Errorf("epoch header %d, database %d", r.Epoch(), f.epoch())
		}
		if f.events("authz.project.updated") != 1 || f.journal("authz.project.updated") != 1 {
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

// TestR_AUTHZ_026_SwitchesNeedTheCurrentIfMatch is the acceptance criterion: a PUT
// that switches manageRepositories on, without If-Match, answers 428 and the switch
// stays off; with the ETag read before another administrator's PUT it answers 412.
func TestR_AUTHZ_026_SwitchesNeedTheCurrentIfMatch(t *testing.T) {
	f := newFixture(t)
	f.create("web")
	put := map[string]any{"displayName": "Project web", "description": "", "managePolicies": true, "manageRepositories": true}
	read := f.do(f.admin, "GET", base+"/web", nil)
	staleTag := read.Header.Get("ETag")

	t.Run("R-AUTHZ-026 without If-Match the answer is 428 and the switch stays off", func(t *testing.T) {
		epoch := f.epoch()
		wantStatus(t, f.do(f.admin, "PUT", base+"/web", put), 428, "precondition_required")
		if f.epoch() != epoch {
			t.Error("a refused write raised the epoch")
		}
		if got := f.do(f.admin, "GET", base+"/web", nil).JSON(t)["manageRepositories"]; got != false {
			t.Fatalf("the switch is %v", got)
		}
	})
	t.Run("R-AUTHZ-026 another administrator turns the policies switch off meanwhile", func(t *testing.T) {
		other := map[string]any{"displayName": "Project web", "description": "", "managePolicies": false, "manageRepositories": false}
		wantStatus(t, f.do(f.second, "PUT", base+"/web", other, "If-Match", staleTag), 200, "")
		if f.journal("authz.project.updated") != 1 {
			t.Errorf("a switch turned off is a revocation: %d journal entries", f.journal("authz.project.updated"))
		}
	})
	t.Run("R-AUTHZ-026 the ETag read before that PUT answers 412 with the current tag", func(t *testing.T) {
		r := f.do(f.admin, "PUT", base+"/web", put, "If-Match", staleTag)
		wantStatus(t, r, 412, "precondition_failed")
		if r.JSON(t)["currentEtag"] != `"2"` {
			t.Errorf("currentEtag = %v", r.JSON(t)["currentEtag"])
		}
		if got := f.do(f.admin, "GET", base+"/web", nil).JSON(t); got["manageRepositories"] != false || got["managePolicies"] != false {
			t.Errorf("a refused write changed the project: %v", got)
		}
	})
	t.Run("R-AUTHZ-026 If-Match: * is the deliberate blind write", func(t *testing.T) {
		r := f.do(f.admin, "PUT", base+"/web", put, "If-Match", "*")
		wantStatus(t, r, 200, "")
		if r.Header.Get("ETag") != `"3"` || r.JSON(t)["manageRepositories"] != true {
			t.Errorf("%v", r.Header)
		}
	})
	t.Run("R-AUTHZ-026 the auditor reads every project and is refused the write", func(t *testing.T) {
		wantStatus(t, f.do(f.auditor, "GET", base, nil), 200, "")
		wantStatus(t, f.do(f.auditor, "PUT", base+"/web", put, "If-Match", `"3"`), 403, "")
		wantStatus(t, f.do(f.auditor, "POST", base, map[string]any{"key": "docs", "displayName": "Docs"}), 403, "")
		wantStatus(t, f.do(f.alice, "GET", base, nil), 403, "")
	})
}

func TestR_AUTHZ_026_KeyValidationAndImmutability(t *testing.T) {
	f := newFixture(t)
	f.create("web")
	for _, key := range []string{"Web", "w", "1ab", "web_app", "web.app", "-web", "a" + strings.Repeat("b", 32)} {
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
		put := map[string]any{"key": "other", "displayName": "X", "managePolicies": true, "manageRepositories": false}
		wantStatus(t, f.do(f.admin, "PUT", base+"/web", put, "If-Match", `"1"`), 422, "immutable")
		put["key"] = "web" // the body of a GET goes back unchanged
		wantStatus(t, f.do(f.admin, "PUT", base+"/web", put, "If-Match", `"1"`), 200, "")
	})
	t.Run("R-AUTHZ-026 a replace names displayName and both switches", func(t *testing.T) {
		wantStatus(t, f.do(f.admin, "PUT", base+"/web", map[string]any{"displayName": "X"}, "If-Match", `"2"`), 422, "required")
		wantStatus(t, f.do(f.admin, "PUT", base+"/nothing", map[string]any{"displayName": "X", "managePolicies": true, "manageRepositories": true}, "If-Match", "*"), 404, "")
	})
}

func TestR_AUTHZ_026_KeyOverlap(t *testing.T) {
	f := newFixture(t)
	f.create("web")
	t.Run("R-AUTHZ-026 a key that is another key followed by a hyphen is refused, either way round", func(t *testing.T) {
		wantStatus(t, f.do(f.admin, "POST", base, map[string]any{"key": "web-app", "displayName": "X"}), 409, "project_key_overlap")
		f.create("shop-app")
		wantStatus(t, f.do(f.admin, "POST", base, map[string]any{"key": "shop", "displayName": "X"}), 409, "project_key_overlap")
	})
	t.Run("R-AUTHZ-026 a key without the hyphen is no overlap", func(t *testing.T) {
		f.create("webapp")
		f.create("web2")
	})
	t.Run("R-AUTHZ-026 a key that starts the name of a global service account is refused", func(t *testing.T) {
		f.e.ServiceAccount("docs-bot")
		wantStatus(t, f.do(f.admin, "POST", base, map[string]any{"key": "docs", "displayName": "X"}), 409, "project_key_overlap")
		wantStatus(t, f.do(f.admin, "POST", base, map[string]any{"key": "doc", "displayName": "X"}), 201, "") // no hyphen after "doc"
	})
	t.Run("R-AUTHZ-026 a project service account does not block its own key", func(t *testing.T) {
		f.seedProjectServiceAccount("web", "web-partner")
		wantStatus(t, f.do(f.admin, "GET", base+"/web", nil), 200, "")
	})
	t.Run("R-AUTHZ-026 two creates of overlapping keys at once: exactly one wins", func(t *testing.T) {
		for trial := range 5 {
			outer, inner := fmt.Sprintf("race%d", trial), fmt.Sprintf("race%d-sub", trial)
			var wg sync.WaitGroup
			res := make([]int, 2)
			for i, key := range []string{outer, inner} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					res[i] = f.cl.Do(f.admin.Name, "POST", base, map[string]any{"key": key, "displayName": "X"}).Status
				}()
			}
			wg.Wait()
			if (res[0] == 201) == (res[1] == 201) {
				t.Fatalf("trial %d: statuses %v, want one 201 and one 409", trial, res)
			}
		}
	})
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
	if f.e.Number("SELECT count(*) FROM project") != 1 || f.events("authz.project.created") != 1 {
		t.Errorf("projects %d, events %d", f.e.Number("SELECT count(*) FROM project"), f.events("authz.project.created"))
	}
	// The same key with another body is a conflict, not a second project.
	wantStatus(t, f.do(f.admin, "POST", base, map[string]any{"key": "docs", "displayName": "Docs"}, "Idempotency-Key", "k-"+f.t.Name()), 422, "idempotency_key_reused")
}

// TestR_AUTHZ_032_EveryProjectWriteRaisesTheEpochAndAnswersIt walks every write of the
// project operations; each answers Trove-Authz-Epoch equal to the epoch the database
// holds after it, and each epoch is higher than the one before.
func TestR_AUTHZ_032_EveryProjectWriteRaisesTheEpochAndAnswersIt(t *testing.T) {
	f := newFixture(t)
	f.seedRepo("web-npm", "active")
	f.e.User("carol")
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
	r = f.do(f.admin, "PUT", base+"/web", map[string]any{"displayName": "Web", "managePolicies": true, "manageRepositories": true}, "If-Match", `"1"`)
	wantStatus(t, r, 200, "")
	step("replace", r)
	r = f.do(f.admin, "PUT", base+"/web/admins/user:carol", nil)
	wantStatus(t, r, 201, "")
	step("add admin", r)
	r = f.do(f.admin, "PUT", base+"/web/repositories/web-npm", nil)
	wantStatus(t, r, 204, "")
	step("assign", r)
	r = f.do(f.admin, "DELETE", base+"/web/repositories/web-npm", nil)
	wantStatus(t, r, 204, "")
	step("unassign", r)
	r = f.do(f.admin, "DELETE", base+"/web/admins/user:carol", nil)
	wantStatus(t, r, 204, "")
	step("remove admin", r)
	r = f.do(f.admin, "DELETE", base+"/web", nil)
	wantStatus(t, r, 204, "")
	step("delete", r)

	t.Run("R-AUTHZ-029 each write is one audit event in its own transaction", func(t *testing.T) {
		for action, want := range map[string]int64{
			"authz.project.created": 1, "authz.project.updated": 1, "authz.project.admin_added": 1,
			"authz.project.repository_assigned": 1, "authz.project.repository_unassigned": 1,
			"authz.project.admin_removed": 1, "authz.project.deleted": 1,
		} {
			if got := f.events(action); got != want {
				t.Errorf("%s: %d events, want %d", action, got, want)
			}
		}
	})
	t.Run("R-AUTHZ-031 the revocations are journaled, the grants are not", func(t *testing.T) {
		for event, want := range map[string]int64{
			"authz.project.created": 0, "authz.project.admin_added": 0, "authz.project.repository_assigned": 0,
			"authz.project.updated": 1, "authz.project.admin_removed": 1, "authz.project.repository_unassigned": 1, "authz.project.deleted": 1,
		} {
			if got := f.journal(event); got != want {
				t.Errorf("%s: %d journal entries, want %d", event, got, want)
			}
		}
		entry := f.e.Text("SELECT entry::text FROM revocation_outbox WHERE event = 'authz.project.admin_removed'")
		if !strings.Contains(entry, `"principal": "user:carol"`) || !strings.Contains(entry, `"project": "web"`) || !strings.Contains(entry, "principalId") {
			t.Errorf("the journal entry of the removal: %s", entry)
		}
		entry = f.e.Text("SELECT entry::text FROM revocation_outbox WHERE event = 'authz.project.updated'")
		if !strings.Contains(entry, `"manageRepositories"`) || !strings.Contains(entry, "projectId") {
			t.Errorf("the journal entry of the update: %s", entry)
		}
	})
}
