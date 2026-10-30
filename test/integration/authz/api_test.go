//go:build integration

package authz

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/test/integration/stack"
)

// The permission operations of the management API over HTTP, on three web
// replicas that share one database and answer in turn (R-ARCH-032): policies
// (R-AUTHZ-021), role bindings (R-AUTHZ-022), the checks of the caller
// (R-AUTHZ-023) and the epochs of R-AUTHZ-032. Every answer is validated against
// api/openapi.yaml.

const policies = "/api/v1/permission-policies"

// apiFixture is the people and repositories of the scenarios and the replicas.
type apiFixture struct {
	e                        *stack.Env
	val                      *apitest.Validator
	a, b, c                  *stack.Replica
	cl                       *stack.Cluster
	admin, alice, bob, carol stack.Person
	web                      stack.Person
	bot                      stack.Person
}

func newAPI(t *testing.T, o stack.Options) *apiFixture {
	t.Helper()
	e := stack.New(t)
	f := &apiFixture{e: e, val: apitest.New(t, v1.EmbeddedYAML())}
	f.admin = e.User("sec-alice")
	e.Admin(f.admin)
	f.alice, f.bob, f.carol = e.User("alice"), e.User("bob"), e.User("carol")
	f.web = e.Group("team-web")
	e.Join(f.web, f.alice)
	f.bot = e.ServiceAccount("ci-bot")
	e.Repo("npm-internal", "local", "npm")
	e.Repo("npm-team-a", "local", "npm")
	e.Repo("generic-local", "local", "generic")
	o.Validator = f.val
	f.a, f.b, f.c = e.NewReplica("web-a", o), e.NewReplica("web-b", o), e.NewReplica("web-c", o)
	f.cl = stack.NewCluster(f.a, f.b, f.c)
	return f
}

func policyBody(name string, entries ...map[string]any) map[string]any {
	es := make([]any, len(entries))
	for i, e := range entries {
		es[i] = e
	}
	return map[string]any{
		"name": name, "description": "policy " + name,
		"resources": map[string]any{"repositories": []string{"npm-internal"}, "packages": map[string]any{"include": []string{"@web/*"}, "exclude": []string{}}},
		"entries":   es,
	}
}

func entry(principal string, actions ...string) map[string]any {
	return map[string]any{"principal": principal, "actions": actions}
}

// wantStatus asserts the status and the code of an answer. A 422 names the
// resource-specific code of a violation in errors[], the top-level code being
// validation_failed (R-API-009); reserved_name and idempotency_key_reused are the
// convention's own top-level codes.
func wantStatus(t *testing.T, r stack.Response, status int, code string) {
	t.Helper()
	if status == 422 && code != "" && code != "validation_failed" && code != "reserved_name" && code != "idempotency_key_reused" {
		list, _ := r.JSON(t)["errors"].([]any)
		found := false
		for _, e := range list {
			found = found || e.(map[string]any)["code"] == code
		}
		if r.Status != 422 || r.Code() != "validation_failed" || !found {
			t.Fatalf("answered %d %q, want 422 validation_failed with %q in errors[]\n%s", r.Status, r.Code(), code, r.Body)
		}
		return
	}
	if r.Status != status || (code != "" && r.Code() != code) {
		t.Fatalf("answered %d %q, want %d %q\n%s", r.Status, r.Code(), status, code, r.Body)
	}
}

// current is the header that makes the next request see every write so far on
// whichever replica answers it (R-AUTHZ-032).
func (f *apiFixture) current() []string {
	return []string{"Trove-Authz-Min-Epoch", fmt.Sprint(f.e.AuthzEpoch())}
}

func (f *apiFixture) auditCount(action string) int64 {
	return f.e.Number("SELECT count(*) FROM audit_event WHERE action = $1 AND resource_type = 'permission_policy'", action)
}

// TestR_AUTHZ_021_PolicyLifecycleOverHTTP follows a policy through create, read,
// replace without and with a stale and the current If-Match, merge patch and
// delete, each request answered by another replica, and checks the epoch, the ETag
// and the audit event of every write.
func TestR_AUTHZ_021_PolicyLifecycleOverHTTP(t *testing.T) {
	f := newAPI(t, stack.Options{})
	me := f.admin.Name

	created := f.cl.Do(me, "POST", policies, policyBody("team-web-publish", entry("group:team-web", "annotate", "deploy", "read")))
	wantStatus(t, created, 201, "")
	if loc := created.Header.Get("Location"); loc != "http://trove.test"+policies+"/team-web-publish" {
		t.Errorf("Location = %q", loc)
	}
	if created.Header.Get("ETag") != `"1"` || created.Epoch() == 0 {
		t.Fatalf("ETag %q, epoch %d", created.Header.Get("ETag"), created.Epoch())
	}
	body := created.JSON(t)
	if body["version"] != float64(1) || body["inert"] != false || body["createdBy"] != "user:sec-alice" {
		t.Errorf("%v", body)
	}
	firstEpoch := created.Epoch()

	// The next request names the epoch and sees the policy on whichever replica answers.
	for range 3 {
		g := f.cl.Do(me, "GET", policies+"/team-web-publish", nil, "Trove-Authz-Min-Epoch", fmt.Sprint(firstEpoch))
		wantStatus(t, g, 200, "")
		if g.Header.Get("ETag") != `"1"` {
			t.Fatalf("ETag = %q", g.Header.Get("ETag"))
		}
	}

	t.Run("R-AUTHZ-032 R-API-014 a replay of an idempotent create carries the epoch of the first answer", func(t *testing.T) {
		key := "idem-" + stack.RandHex(4)
		first := f.cl.Do(me, "POST", policies, policyBody("idem-policy", entry("user:bob", "read")), "Idempotency-Key", key)
		wantStatus(t, first, 201, "")
		if first.Epoch() == 0 {
			t.Fatal("the first answer carries no epoch")
		}
		for range 3 {
			again := f.cl.Do(me, "POST", policies, policyBody("idem-policy", entry("user:bob", "read")), "Idempotency-Key", key)
			wantStatus(t, again, 201, "")
			if again.Header.Get("Idempotency-Replayed") != "true" || again.Epoch() != first.Epoch() ||
				again.Header.Get("ETag") != first.Header.Get("ETag") || string(again.Body) != string(first.Body) {
				t.Fatalf("replay: epoch %d, want %d; replayed %q", again.Epoch(), first.Epoch(), again.Header.Get("Idempotency-Replayed"))
			}
		}
		wantStatus(t, f.cl.Do(me, "DELETE", policies+"/idem-policy", nil), 204, "")
	})
	t.Run("R-AUTHZ-021 a replace without If-Match answers 428 and stores nothing", func(t *testing.T) {
		put := policyBody("team-web-publish", entry("group:team-web", "read"))
		wantStatus(t, f.cl.Do(me, "PUT", policies+"/team-web-publish", put), 428, "precondition_required")
		if g := f.cl.Do(me, "GET", policies+"/team-web-publish", nil); g.Header.Get("ETag") != `"1"` {
			t.Fatalf("a refused PUT changed the policy: %q", g.Header.Get("ETag"))
		}
	})
	t.Run("R-AUTHZ-021 a stale If-Match answers 412 with the current tag", func(t *testing.T) {
		put := policyBody("team-web-publish", entry("group:team-web", "read"))
		r := f.cl.Do(me, "PUT", policies+"/team-web-publish", put, "If-Match", `"0"`)
		wantStatus(t, r, 412, "precondition_failed")
		if r.JSON(t)["currentEtag"] != `"1"` {
			t.Fatalf("currentEtag = %v", r.JSON(t)["currentEtag"])
		}
	})
	t.Run("R-AUTHZ-021 the current If-Match replaces the policy and returns the new epoch", func(t *testing.T) {
		put := policyBody("team-web-publish", entry("group:team-web", "read"), entry("user:bob", "read"))
		r := f.cl.Do(me, "PUT", policies+"/team-web-publish", put, "If-Match", `"1"`)
		wantStatus(t, r, 200, "")
		if r.Header.Get("ETag") != `"2"` || r.Epoch() <= firstEpoch {
			t.Fatalf("ETag %q, epoch %d, first epoch %d", r.Header.Get("ETag"), r.Epoch(), firstEpoch)
		}
		if got := r.JSON(t)["entries"].([]any); len(got) != 2 {
			t.Fatalf("entries = %v", got)
		}
	})
	t.Run("R-AUTHZ-021 R-API-013 a merge patch changes one member and keeps the rest", func(t *testing.T) {
		r := f.cl.Do(me, "PATCH", policies+"/team-web-publish", map[string]any{"description": "patched"},
			"If-Match", `"2"`, "Content-Type", "application/merge-patch+json")
		wantStatus(t, r, 200, "")
		m := r.JSON(t)
		if m["description"] != "patched" || len(m["entries"].([]any)) != 2 || r.Header.Get("ETag") != `"3"` {
			t.Fatalf("%v %q", m, r.Header.Get("ETag"))
		}
		// An array is replaced as a whole.
		r = f.cl.Do(me, "PATCH", policies+"/team-web-publish",
			map[string]any{"entries": []any{entry("user:bob", "read")}}, "If-Match", `"3"`, "Content-Type", "application/merge-patch+json")
		wantStatus(t, r, 200, "")
		if got := r.JSON(t)["entries"].([]any); len(got) != 1 {
			t.Fatalf("entries = %v", got)
		}
	})
	t.Run("R-AUTHZ-021 a delete with a stale tag answers 412, the current tag deletes", func(t *testing.T) {
		wantStatus(t, f.cl.Do(me, "DELETE", policies+"/team-web-publish", nil, "If-Match", `"1"`), 412, "precondition_failed")
		r := f.cl.Do(me, "DELETE", policies+"/team-web-publish", nil, "If-Match", `"4"`)
		wantStatus(t, r, 204, "")
		if r.Epoch() == 0 {
			t.Fatal("no epoch")
		}
		wantStatus(t, f.cl.Do(me, "GET", policies+"/team-web-publish", nil), 404, "not_found")
		wantStatus(t, f.cl.Do(me, "DELETE", policies+"/team-web-publish", nil), 404, "not_found")
	})
	t.Run("R-AUTHZ-021 each change is one audit event", func(t *testing.T) {
		for action, want := range map[string]int64{"authz.policy.created": 2, "authz.policy.updated": 3, "authz.policy.deleted": 2} {
			if got := f.auditCount(action); got != want {
				t.Errorf("%s: %d events, want %d", action, got, want)
			}
		}
		// The journaled types wrote the revocation journal in the same transaction.
		if got := f.e.Number("SELECT count(*) FROM revocation_outbox"); got != 5 {
			t.Errorf("revocation_outbox holds %d rows, want 5 (three updates and two deletes are journaled)", got)
		}
	})
}

// TestR_AUTHZ_021_ValidationAndPermissions covers the 422 codes through the API,
// validate-policy, and the capabilities of the operations.
func TestR_AUTHZ_021_ValidationAndPermissions(t *testing.T) {
	f := newAPI(t, stack.Options{})
	me := f.admin.Name

	t.Run("R-AUTHZ-021 a rule that fails answers 422 validation_failed with every problem in errors[]", func(t *testing.T) {
		body := policyBody("bad", entry("user:ghost", "read"), entry("user:bob", "fly"))
		r := f.cl.Do(me, "POST", policies, body)
		wantStatus(t, r, 422, "validation_failed")
		wantStatus(t, r, 422, "unknown_principal")
		errs, _ := r.JSON(t)["errors"].([]any)
		if len(errs) != 2 || errs[0].(map[string]any)["pointer"] != "/entries/0/principal" || errs[1].(map[string]any)["code"] != "invalid_action" {
			t.Fatalf("errors = %v", errs)
		}
		wantStatus(t, f.cl.Do(me, "POST", policies, policyBody("dup", entry("user:bob", "read"), entry("user:bob", "deploy"))), 422, "duplicate_principal")
		wantStatus(t, f.cl.Do(me, "POST", policies, policyBody("anon", entry("anonymous", "deploy"))), 422, "anonymous_write_action")
		wantStatus(t, f.cl.Do(me, "POST", policies, map[string]any{"name": "x", "resources": map[string]any{"repositories": []string{"ghost"}}}), 422, "unknown_repository")
		wantStatus(t, f.cl.Do(me, "POST", policies, map[string]any{"name": "x", "resources": map[string]any{"repositories": []string{"npm-internal"}, "packages": map[string]any{"include": []string{"a**b/c\\"}}}}), 422, "invalid_pattern")
		if n := f.e.Number("SELECT count(*) FROM authz_policy"); n != 0 {
			t.Fatalf("%d policies stored by refused requests", n)
		}
	})
	t.Run("R-AUTHZ-021 a name that exists answers 409", func(t *testing.T) {
		wantStatus(t, f.cl.Do(me, "POST", policies, policyBody("once", entry("user:bob", "read"))), 201, "")
		wantStatus(t, f.cl.Do(me, "POST", policies, policyBody("once", entry("user:bob", "read"))), 409, "already_exists")
	})
	t.Run("R-AUTHZ-021 validate-policy returns the problems, the warnings and the covered repositories", func(t *testing.T) {
		r := f.cl.Do(me, "POST", "/api/v1/authz/validate-policy", policyBody("v", entry("user:ghost", "read")))
		wantStatus(t, r, 200, "")
		m := r.JSON(t)
		if m["valid"] != false || len(m["errors"].([]any)) != 1 {
			t.Fatalf("%v", m)
		}
		ok := f.cl.Do(me, "POST", "/api/v1/authz/validate-policy", map[string]any{
			"name": "v", "resources": map[string]any{"repositories": []string{"npm-*"}},
			"entries": []any{entry("group:team-web", "read")},
		})
		wantStatus(t, ok, 200, "")
		m = ok.JSON(t)
		if m["valid"] != true || fmt.Sprint(m["covers"]) != "[npm-internal npm-team-a]" || fmt.Sprint(m["warnings"]) != "[]" {
			t.Fatalf("%v", m)
		}
		only := f.cl.Do(me, "POST", "/api/v1/authz/validate-policy", map[string]any{"name": "v", "resources": map[string]any{"repositories": []string{"nothing-*"}}})
		if got := fmt.Sprint(only.JSON(t)["warnings"]); got != "[selector_matches_nothing]" {
			t.Fatalf("warnings = %s", got)
		}
	})
	t.Run("R-AUTHZ-010 the operations need their capability", func(t *testing.T) {
		wantStatus(t, f.cl.Do("", "GET", policies, nil), 401, "")
		wantStatus(t, f.cl.Do(f.bob.Name, "GET", policies, nil), 403, "")
		wantStatus(t, f.cl.Do(f.bob.Name, "POST", policies, policyBody("p", entry("user:bob", "read"))), 403, "")
		wantStatus(t, f.cl.Do(f.bob.Name, "GET", "/api/v1/role-bindings", nil), 403, "")
		// A role is read through a binding: the auditor reads policies and bindings but writes nothing.
		r := f.cl.Do(me, "PUT", "/api/v1/role-bindings/auditor/user:carol", nil)
		wantStatus(t, r, 201, "")
		min := "Trove-Authz-Min-Epoch"
		ep := fmt.Sprint(r.Epoch())
		for range 3 {
			wantStatus(t, f.cl.Do(f.carol.Name, "GET", policies, nil, min, ep), 200, "")
			wantStatus(t, f.cl.Do(f.carol.Name, "GET", "/api/v1/role-bindings", nil, min, ep), 200, "")
			wantStatus(t, f.cl.Do(f.carol.Name, "POST", policies, policyBody("p", entry("user:bob", "read")), min, ep), 403, "")
			wantStatus(t, f.cl.Do(f.carol.Name, "POST", "/api/v1/authz/validate-policy", policyBody("p"), min, ep), 200, "")
		}
	})
}

// TestR_AUTHZ_021_ListFilters: the filters principal, expandGroups and repository.
func TestR_AUTHZ_021_ListFilters(t *testing.T) {
	f := newAPI(t, stack.Options{})
	me := f.admin.Name
	for _, p := range []map[string]any{
		policyBody("team-web-publish", entry("group:team-web", "annotate", "deploy", "read")),
		policyBody("bob-reads", entry("user:bob", "read")),
		{"name": "teama", "resources": map[string]any{"repositories": []string{"npm-team-*"}}, "entries": []any{entry("authenticated", "read")}},
	} {
		wantStatus(t, f.cl.Do(me, "POST", policies, p), 201, "")
	}
	names := func(q string) string {
		r := f.cl.Do(me, "GET", policies+q, nil)
		wantStatus(t, r, 200, "")
		var out []string
		for _, it := range r.Items(t) {
			out = append(out, it["name"].(string))
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct{ name, query, want string }{
		{"no filter lists every policy by name", "", "bob-reads,team-web-publish,teama"},
		{"R-AUTHZ-021 the principal alice matches through group:team-web", "?principal=user:alice", "team-web-publish"},
		{"R-AUTHZ-021 expandGroups=false drops the policy of the group", "?principal=user:alice&expandGroups=false", ""},
		{"a principal with an entry of its own", "?principal=user:bob&expandGroups=false", "bob-reads"},
		{"the group itself", "?principal=group:team-web", "team-web-publish"},
		{"R-AUTHZ-021 repository=npm-team-a is covered by the pattern selector", "?repository=npm-team-a", "teama"},
		{"repository=npm-internal is covered by two policies", "?repository=npm-internal", "bob-reads,team-web-publish"},
		{"descending", "?sort=-name", "teama,team-web-publish,bob-reads"},
		{"both filters", "?principal=user:alice&repository=npm-team-a", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := names(tc.query); got != tc.want {
				t.Fatalf("%s: %q, want %q", tc.query, got, tc.want)
			}
		})
	}
	wantStatus(t, f.cl.Do(me, "GET", policies+"?principal=user:ghost", nil), 422, "unknown_principal")
	wantStatus(t, f.cl.Do(me, "GET", policies+"?repository=ghost", nil), 422, "unknown_repository")
	wantStatus(t, f.cl.Do(me, "GET", policies+"?expandGroups=maybe", nil), 400, "")
	// The list carries the warnings that GET carries.
	wantStatus(t, f.cl.Do(me, "POST", policies, map[string]any{"name": "empty", "resources": map[string]any{"repositories": []string{"nothing-*"}}}), 201, "")
	for _, it := range f.cl.Do(me, "GET", policies+"?sort=name&limit=2", nil).Items(t) {
		if it["name"] == "empty" && fmt.Sprint(it["warnings"]) != "[selector_matches_nothing]" {
			t.Errorf("warnings = %v", it["warnings"])
		}
	}
}

// TestR_API_010_PermissionPoliciesContract runs the convention suite of E1.6 on
// the policy resource: cursor paging, ETag with If-Match, merge patch and
// idempotency keys, with every request answered by another of three replicas.
func TestR_API_010_PermissionPoliciesContract(t *testing.T) {
	f := newAPI(t, stack.Options{})
	apitest.RunContract(t, apitest.Resource{
		Name: "permission-policies", Collection: "http://cluster" + policies, Do: f.cl.Transport(f.admin.Name), //nolint:bodyclose // the contract tests close the bodies
		Create: func(n int) map[string]any {
			return policyBody(fmt.Sprintf("contract-%s-%d", stack.RandHex(3), n), entry("group:team-web", "read"))
		},
		Key:   func(m map[string]any) string { return m["name"].(string) },
		Patch: map[string]any{"description": "patched"}, PatchMember: "description", PatchValue: "patched",
		ClearMember: "description", ClearedValue: "",
		ImmutableMember: "name", ImmutableValue: "other-name",
		OtherQuery: "sort=-name", Put: true, RevisionMember: "version",
	})
}

// TestR_AUTHZ_022_RolesAndBindingsOverHTTP: the roles, bind (idempotent), unbind
// and the guards, and a traversal of the bindings with limit=1.
func TestR_AUTHZ_022_RolesAndBindingsOverHTTP(t *testing.T) {
	f := newAPI(t, stack.Options{})
	me := f.admin.Name
	bindings := "/api/v1/role-bindings"

	r := f.cl.Do(f.bob.Name, "GET", "/api/v1/roles", nil)
	wantStatus(t, r, 200, "")
	roles := map[string]bool{}
	for _, it := range r.Items(t) {
		roles[it["name"].(string)] = true
	}
	if len(roles) != 5 || !roles["auditor"] || r.JSON(t)["nextCursor"] != nil {
		t.Fatalf("roles = %v", roles)
	}
	wantStatus(t, f.cl.Do("", "GET", "/api/v1/roles", nil), 401, "")

	t.Run("R-AUTHZ-022 bind answers 201 and then 200", func(t *testing.T) {
		first := f.cl.Do(me, "PUT", bindings+"/admin/group:team-web", nil)
		wantStatus(t, first, 201, "")
		if first.Epoch() == 0 || first.JSON(t)["principal"] != "group:team-web" || first.JSON(t)["grantedBy"] != "user:sec-alice" {
			t.Fatalf("%v epoch %d", first.JSON(t), first.Epoch())
		}
		again := f.cl.Do(me, "PUT", bindings+"/admin/group:team-web", nil)
		wantStatus(t, again, 200, "")
		if again.Epoch() < first.Epoch() {
			t.Fatalf("epoch %d went below %d", again.Epoch(), first.Epoch())
		}
		if n := f.e.Number("SELECT count(*) FROM audit_event WHERE action = 'authz.role.bound'"); n != 1 {
			t.Fatalf("%d role.bound events, want 1 (the repeat changed nothing)", n)
		}
	})
	t.Run("R-AUTHZ-022 the refusals", func(t *testing.T) {
		wantStatus(t, f.cl.Do(me, "PUT", bindings+"/admin/authenticated", nil), 422, "role_not_allowed")
		wantStatus(t, f.cl.Do(me, "PUT", bindings+"/admin/anonymous", nil), 422, "role_not_allowed")
		wantStatus(t, f.cl.Do(me, "PUT", bindings+"/god/user:bob", nil), 422, "unknown_role")
		wantStatus(t, f.cl.Do(me, "PUT", bindings+"/auditor/user:ghost", nil), 422, "unknown_principal")
		auto := f.e.Group("everyone")
		f.e.Exec("UPDATE user_group SET auto_join = true WHERE principal_id = $1::uuid", auto.ID)
		wantStatus(t, f.cl.Do(me, "PUT", bindings+"/auditor/group:everyone", nil), 409, "auto_join_admin_conflict")
		wantStatus(t, f.cl.Do(me, "DELETE", bindings+"/auditor/user:bob", nil), 404, "not_found")
	})
	t.Run("R-AUTHZ-022 the last admin binding stays while the break-glass administrator is disabled", func(t *testing.T) {
		// Two admin bindings exist: sec-alice and group:team-web.
		wantStatus(t, f.cl.Do(me, "DELETE", bindings+"/admin/group:team-web", nil), 204, "")
		wantStatus(t, f.cl.Do(me, "DELETE", bindings+"/admin/user:sec-alice", nil), 409, "last_admin")
		if n := f.e.Number("SELECT count(*) FROM authz_role_binding WHERE role = 'admin'"); n != 1 {
			t.Fatalf("%d admin bindings", n)
		}
	})
	t.Run("R-API-010 R-AUTHZ-022 the bindings page with limit=1 on any replica", func(t *testing.T) {
		for _, p := range []string{"user:alice", "user:bob", "user:carol"} {
			wantStatus(t, f.cl.Do(me, "PUT", bindings+"/auditor/"+p, nil), 201, "")
		}
		want := f.cl.Do(me, "GET", bindings+"?limit=100", nil).Items(t)
		var got []string
		next := bindings + "?limit=1"
		for range 50 {
			r := f.cl.Do(me, "GET", next, nil)
			wantStatus(t, r, 200, "")
			for _, it := range r.Items(t) {
				got = append(got, it["role"].(string)+"/"+it["principal"].(string))
			}
			c, _ := r.JSON(t)["nextCursor"].(string)
			if c == "" {
				break
			}
			next = bindings + "?limit=1&cursor=" + url.QueryEscape(c)
		}
		if len(got) != len(want) || len(want) != 4 {
			t.Fatalf("walked %v, the full page has %d", got, len(want))
		}
		if one := f.cl.Do(me, "GET", bindings+"?role=auditor&principal=user:bob", nil).Items(t); len(one) != 1 {
			t.Fatalf("%v", one)
		}
		wantStatus(t, f.cl.Do(me, "GET", bindings+"?role=nope", nil), 422, "unknown_role")
	})
}

// TestR_AUTHZ_023_ChecksOverHTTP: /authz/me and /authz/check for the caller, and the
// built-in principal authenticated (R-AUTHZ-030) through the API (E1.3).
func TestR_AUTHZ_023_ChecksOverHTTP(t *testing.T) {
	f := newAPI(t, stack.Options{})
	me := f.admin.Name
	check := func(person string, items ...map[string]any) []bool {
		t.Helper()
		r := f.cl.Do(person, "POST", "/api/v1/authz/check", map[string]any{"checks": items}, f.current()...)
		wantStatus(t, r, 200, "")
		var out []bool
		for _, res := range r.JSON(t)["results"].([]any) {
			out = append(out, res.(map[string]any)["allowed"].(bool))
		}
		return out
	}
	item := func(repo, action string) map[string]any { return map[string]any{"repository": repo, "action": action} }

	wantStatus(t, f.cl.Do(me, "POST", policies, policyBody("team-web-publish", entry("group:team-web", "annotate", "deploy", "read"))), 201, "")
	t.Run("R-AUTHZ-023 the example of the requirement: alice true, false, true", func(t *testing.T) {
		wantStatus(t, f.cl.Do(me, "POST", policies, map[string]any{"name": "all-read", "resources": map[string]any{"repositories": []string{"npm-team-a"}}, "entries": []any{entry("group:team-web", "read")}}), 201, "")
		got := check(f.alice.Name,
			map[string]any{"repository": "npm-internal", "package": "@web/button", "action": "deploy"},
			map[string]any{"repository": "npm-internal", "package": "@web/button", "action": "delete"},
			item("npm-team-a", "read"))
		if fmt.Sprint(got) != "[true false true]" {
			t.Fatalf("alice: %v", got)
		}
	})
	t.Run("R-AUTHZ-023 carol gets false for all, also for a repository that does not exist", func(t *testing.T) {
		for _, repo := range []string{"npm-internal", "ghost"} {
			got := check(f.carol.Name, item(repo, "read"), item(repo, "deploy"), map[string]any{"repository": repo, "package": "@web/button", "action": "deploy"})
			if fmt.Sprint(got) != "[false false false]" {
				t.Fatalf("carol on %s: %v", repo, got)
			}
		}
	})
	t.Run("R-AUTHZ-023 more than 100 checks, an unknown action and no credential", func(t *testing.T) {
		many := make([]map[string]any, 101)
		for i := range many {
			many[i] = item("npm-internal", "read")
		}
		wantStatus(t, f.cl.Do(f.alice.Name, "POST", "/api/v1/authz/check", map[string]any{"checks": many}), 422, "validation_failed")
		hundred := many[:100]
		if got := check(f.admin.Name, hundred...); len(got) != 100 {
			t.Fatalf("%d results", len(got))
		}
		wantStatus(t, f.cl.Do(f.alice.Name, "POST", "/api/v1/authz/check", map[string]any{"checks": []any{item("npm-internal", "fly")}}), 422, "invalid_action")
		wantStatus(t, f.cl.Do("", "POST", "/api/v1/authz/check", map[string]any{"checks": []any{}}), 401, "")
		wantStatus(t, f.cl.Do(f.alice.Name, "POST", "/api/v1/authz/check", map[string]any{"chekcs": []any{}}), 400, "malformed_request")
	})
	t.Run("R-AUTHZ-030 E1.3 the built-in principal authenticated through the API", func(t *testing.T) {
		wantStatus(t, f.cl.Do(me, "POST", policies, map[string]any{
			"name": "everyone-reads", "resources": map[string]any{"repositories": []string{"generic-local"}},
			"entries": []any{entry("authenticated", "read")},
		}), 201, "")
		// An entry giving authenticated deploy is refused.
		wantStatus(t, f.cl.Do(me, "POST", policies, map[string]any{
			"name": "everyone-writes", "resources": map[string]any{"repositories": []string{"generic-local"}},
			"entries": []any{entry("authenticated", "deploy")},
		}), 422, "authenticated_write_action")
		// A user in no group reads; a service account without a grant of its own does not (404 as for a missing repository).
		if got := check(f.carol.Name, item("generic-local", "read"), item("generic-local", "deploy")); fmt.Sprint(got) != "[true false]" {
			t.Fatalf("carol: %v", got)
		}
		if got := check(f.bot.Name, item("generic-local", "read")); fmt.Sprint(got) != "[false]" {
			t.Fatalf("service account: %v", got)
		}
		wantStatus(t, f.cl.Do("", "POST", "/api/v1/authz/check", map[string]any{"checks": []any{item("generic-local", "read")}}), 401, "")
	})
	t.Run("R-AUTHZ-022 me lists the capabilities, the roles and the principal set of the caller only", func(t *testing.T) {
		r := f.cl.Do(f.alice.Name, "GET", "/api/v1/authz/me", nil, f.current()...)
		wantStatus(t, r, 200, "")
		m := r.JSON(t)
		if fmt.Sprint(m["principals"]) != "[user:alice group:team-web authenticated]" || fmt.Sprint(m["capabilities"]) != "[]" || m["scope"] != nil || m["epoch"].(float64) < 1 {
			t.Fatalf("%v", m)
		}
		adm := f.cl.Do(me, "GET", "/api/v1/authz/me", nil, f.current()...).JSON(t)
		if fmt.Sprint(adm["roles"]) != "[admin]" || len(adm["capabilities"].([]any)) != 23 {
			t.Fatalf("admin: %v", adm)
		}
		wantStatus(t, f.cl.Do("", "GET", "/api/v1/authz/me", nil), 401, "")
	})
}

// TestR_AUTHZ_032_MinEpochOverHTTP is E1.12 through HTTP: three web replicas, Valkey
// stopped (none is part of the stack), cluster.epoch_poll_interval at 30 s and
// authz.cache.max_staleness at 120 s, so only the header can explain a prompt
// result. A grant made through replica A is seen by twenty parallel requests that
// carry its epoch on B and C, and its removal by twenty more.
func TestR_AUTHZ_032_MinEpochOverHTTP(t *testing.T) {
	f := newAPI(t, stack.Options{Poll: 30 * time.Second, Staleness: 120 * time.Second})
	min := "Trove-Authz-Min-Epoch"
	mobile := f.e.Group("team-mobile")
	f.e.Join(mobile, f.bob)
	// The grant is made through A; the index of B and C was built at start and the change
	// log would tell them in 30 s.
	deploy := map[string]any{"checks": []any{map[string]any{"repository": "generic-local", "path": "app.zip", "action": "deploy"}}}
	parallel := func(epoch int64, want bool) {
		t.Helper()
		var wg sync.WaitGroup
		var mu sync.Mutex
		seen := map[string]int{}
		for i := range 20 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := []*stack.Replica{f.b, f.c}[i%2].Do(f.bob.Name, "POST", "/api/v1/authz/check", deploy, min, fmt.Sprint(epoch))
				if r.Status != 200 {
					t.Errorf("request %d: %d %s", i, r.Status, r.Body)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				seen[r.Header.Get("X-Trove-Replica")]++
				if got := r.JSON(t)["results"].([]any)[0].(map[string]any)["allowed"]; got != want {
					t.Errorf("request %d with epoch %d: allowed %v, want %v", i, epoch, got, want)
				}
			}()
		}
		wg.Wait()
		if len(seen) != 2 {
			t.Errorf("answered by %v, want replicas B and C", seen)
		}
	}
	r := f.a.Do(f.admin.Name, "POST", policies, map[string]any{
		"name": "files-deploy", "resources": map[string]any{"repositories": []string{"generic-local"}},
		"entries": []any{entry("group:team-mobile", "deploy")},
	})
	wantStatus(t, r, 201, "")
	parallel(r.Epoch(), true)
	// The administrator removes the grant (epoch m); twenty requests with m are all refused.
	d := f.a.Do(f.admin.Name, "DELETE", policies+"/files-deploy", nil)
	wantStatus(t, d, 204, "")
	parallel(d.Epoch(), false)

	t.Run("R-AUTHZ-032 an epoch beyond the database answers 412 at once", func(t *testing.T) {
		for _, rep := range []*stack.Replica{f.b, f.c} {
			start := time.Now()
			r := rep.Do(f.bob.Name, "POST", "/api/v1/authz/check", deploy, min, fmt.Sprint(f.e.AuthzEpoch()+1000))
			wantStatus(t, r, 412, "authz_epoch_unknown")
			if took := time.Since(start); took > time.Second {
				t.Errorf("took %s", took)
			}
		}
	})
	t.Run("R-AUTHZ-032 a malformed value answers 400, an anonymous caller ignores the header", func(t *testing.T) {
		wantStatus(t, f.b.Do(f.bob.Name, "POST", "/api/v1/authz/check", deploy, min, "abc"), 400, "invalid_header")
		wantStatus(t, f.b.Do(f.bob.Name, "POST", "/api/v1/authz/check", deploy, min, "0"), 400, "invalid_header")
		// Anonymous: the same 401 with and without the header.
		with := f.b.Do("", "POST", "/api/v1/authz/check", deploy, min, "abc")
		without := f.b.Do("", "POST", "/api/v1/authz/check", deploy)
		wantStatus(t, with, 401, "")
		if with.Code() != without.Code() {
			t.Fatalf("with %q, without %q", with.Code(), without.Code())
		}
	})
	t.Run("R-AUTHZ-032 every write returns its epoch and the epochs grow", func(t *testing.T) {
		var last int64
		for i := range 3 {
			r := f.cl.Do(f.admin.Name, "POST", policies, policyBody(fmt.Sprintf("e-%d", i), entry("user:bob", "read")))
			wantStatus(t, r, 201, "")
			if r.Epoch() <= last {
				t.Fatalf("epoch %d after %d", r.Epoch(), last)
			}
			last = r.Epoch()
		}
		if got := f.e.AuthzEpoch(); got != last {
			t.Fatalf("database epoch %d, last answer %d", got, last)
		}
	})
}

// TestR_API_002_EveryPermissionOperationAnswersAsDocumented walks every operation of
// the authorization and audit areas once with a validator that compares each answer
// with the document (E1.6), and asserts none was missed.
func TestR_API_002_EveryPermissionOperationAnswersAsDocumented(t *testing.T) {
	f := newAPI(t, stack.Options{})
	me := f.admin.Name
	for _, step := range []struct {
		method, path string
		body         any
		want         int
		hdr          []string
	}{
		{"POST", policies, policyBody("p", entry("group:team-web", "read")), 201, nil},
		{"GET", policies, nil, 200, nil},
		{"GET", policies + "/p", nil, 200, nil},
		{"PUT", policies + "/p", policyBody("p", entry("group:team-web", "read", "deploy")), 200, []string{"If-Match", `"1"`}},
		{"PATCH", policies + "/p", map[string]any{"description": "x"}, 200, []string{"If-Match", `"2"`, "Content-Type", "application/merge-patch+json"}},
		{"POST", "/api/v1/authz/validate-policy", policyBody("p"), 200, nil},
		{"GET", "/api/v1/roles", nil, 200, nil},
		{"PUT", "/api/v1/role-bindings/auditor/user:carol", nil, 201, nil},
		{"GET", "/api/v1/role-bindings", nil, 200, nil},
		{"DELETE", "/api/v1/role-bindings/auditor/user:carol", nil, 204, nil},
		{"GET", "/api/v1/authz/me", nil, 200, nil},
		{"POST", "/api/v1/authz/check", map[string]any{"checks": []any{map[string]any{"repository": "npm-internal", "action": "read"}}}, 200, nil},
		{"DELETE", policies + "/p", nil, 204, nil},
	} {
		r := f.cl.Do(me, step.method, step.path, step.body, step.hdr...)
		if r.Status != step.want {
			t.Fatalf("%s %s: %d, want %d\n%s", step.method, step.path, r.Status, step.want, r.Body)
		}
	}
	// The audit operations: a page, one event and the export in both formats.
	list := f.cl.Do(me, "GET", "/api/v1/audit/events?limit=2", nil)
	wantStatus(t, list, 200, "")
	id := list.Items(t)[0]["id"].(string)
	wantStatus(t, f.cl.Do(me, "GET", "/api/v1/audit/events/"+id, nil), 200, "")
	from, to := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	wantStatus(t, f.cl.Do(me, "GET", "/api/v1/audit/events/export?from="+from+"&to="+to, nil), 200, "")
	wantStatus(t, f.cl.Do(me, "GET", "/api/v1/audit/events/export?format=csv&from="+from+"&to="+to, nil), 200, "")
	// A refusal of each kind is a documented problem answer.
	wantStatus(t, f.cl.Do(f.bob.Name, "GET", "/api/v1/audit/events", nil), 403, "forbidden")
	wantStatus(t, f.cl.Do("", "GET", "/api/v1/audit/events/"+id, nil), 401, "")

	ops := []string{
		"listPermissionPolicies", "createPermissionPolicy", "getPermissionPolicy", "replacePermissionPolicy", "patchPermissionPolicy",
		"deletePermissionPolicy", "validatePermissionPolicy", "listRoles", "listRoleBindings", "bindRole", "unbindRole",
		"getAuthzMe", "checkPermissions", "listAuditEvents", "getAuditEvent", "exportAuditEvents",
	}
	if missing := f.val.Uncovered(ops...); len(missing) > 0 {
		t.Fatalf("operations no answer reached: %v", missing)
	}
}
