//go:build integration

package ci

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/ci"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/oidcstub"
)

// The mappings of work package W3 (R-CI-009, R-CI-017, R-CI-019, R-CI-020): the store, the principals, the
// first match, the save-time rules over HTTP, the policy binding, the events and the cache. The criteria
// that need the exchange are in exchange_mappings_test.go.

func TestR_CI_009_TheMappingLifecycle(t *testing.T) {
	e := newEnv(t, options{valkey: true})
	a, b := e.web("web-a"), e.web("web-b")
	e.issuer(t, a, "gitlab-main", false)
	e.stack.Repository("npm-internal", "local", "npm", "{}")
	path := mappingsOf("gitlab-main")

	versionBefore := e.number("SELECT mappings_version FROM ci_issuers WHERE name = 'gitlab-main'")
	epochBefore := e.epoch("ci_mappings")
	r := e.do(a, "POST", path, publishMapping("publish", 100, "npm-internal"))
	wantStatus(t, r, 201, "")
	if loc := r.Header.Get("Location"); !strings.HasSuffix(loc, path+"/publish") {
		t.Errorf("Location = %q", loc)
	}
	if etag(r) != `"1"` {
		t.Errorf("ETag = %q", etag(r))
	}
	got := r.JSON(t)
	if got["tier"] != "system" || got["enabled"] != true || got["issuer"] != "gitlab-main" || got["version"] != float64(1) {
		t.Errorf("created mapping = %v", got)
	}
	if opts, _ := got["options"].(map[string]any); opts["crossProjectSource"] != "deny" || opts["allowUnprotectedRefs"] != false {
		t.Errorf("options = %v, want the resolved defaults", got["options"])
	}
	if kinds := strs(got["subjectKinds"]); len(kinds) != 1 || kinds[0] != "id_token" {
		t.Errorf("subjectKinds = %v", kinds)
	}
	inl := got["grants"].(map[string]any)["inline"].([]any)[0].(map[string]any)
	if inl["ownership"] != "bind_project" {
		t.Errorf("R-CI-013 an inline grant with deploy and no ownership reads back %v, want bind_project", inl["ownership"])
	}
	if e.number("SELECT mappings_version FROM ci_issuers WHERE name = 'gitlab-main'") != versionBefore+1 || e.epoch("ci_mappings") != epochBefore+1 {
		t.Error("the create did not increment mappings_version and notify ci_mappings once")
	}
	if e.number("SELECT count(*) FROM principal WHERE kind = 'ci_mapping' AND name = 'gitlab-main/publish' AND status = 'active'") != 1 {
		t.Error("the principal gitlab-main/publish was not created with the mapping")
	}

	t.Run("R-CI-009 a name that exists is 409 and nothing is written", func(t *testing.T) {
		before := e.epoch("ci_mappings")
		wantStatus(t, e.do(b, "POST", path, publishMapping("publish", 200, "npm-internal")), 409, "already_exists")
		if e.epoch("ci_mappings") != before || e.number("SELECT count(*) FROM principal WHERE kind = 'ci_mapping'") != 1 {
			t.Error("a refused create wrote")
		}
	})

	t.Run("R-CI-019 the list is in evaluation order and a read answers its ETag", func(t *testing.T) {
		wantStatus(t, e.do(a, "POST", path, readOnlyMapping("readers", 900, all(eq("project_id", "4242")), "npm-internal")), 201, "")
		wantStatus(t, e.do(a, "POST", path, readOnlyMapping("urgent", 10, all(eq("project_id", "4242")), "npm-internal")), 201, "")
		l := e.do(b, "GET", path, nil)
		wantStatus(t, l, 200, "")
		if got := strings.Join(names(t, l), ","); got != "urgent,publish,readers" {
			t.Errorf("order = %s, want urgent,publish,readers (ascending priority)", got)
		}
		g := e.do(b, "GET", path+"/publish", nil)
		wantStatus(t, g, 200, "")
		if etag(g) != `"1"` {
			t.Errorf("ETag = %q", etag(g))
		}
		wantStatus(t, e.do(b, "GET", path+"/nobody", nil), 404, "not_found")
		wantStatus(t, e.do(b, "GET", mappingsOf("nobody"), nil), 404, "not_found")
	})

	t.Run("R-CI-019 a PUT needs If-Match, a stale one is 412 with currentEtag, name is immutable", func(t *testing.T) {
		body := e.do(a, "GET", path+"/publish", nil).JSON(t)
		body["description"] = "publishes"
		wantStatus(t, e.do(a, "PUT", path+"/publish", body), 428, "precondition_required")
		stale := e.do(a, "PUT", path+"/publish", body, "If-Match", `"7"`)
		wantStatus(t, stale, 412, "precondition_failed")
		if stale.JSON(t)["currentEtag"] != `"1"` {
			t.Errorf("currentEtag = %v", stale.JSON(t)["currentEtag"])
		}
		renamed := map[string]any{}
		for k, v := range body {
			renamed[k] = v
		}
		renamed["name"] = "renamed"
		rr := e.do(a, "PUT", path+"/publish", renamed, "If-Match", `"1"`)
		wantStatus(t, rr, 422, "")
		if f := problemFields(t, rr); len(f) != 1 || f[0] != "/name immutable" {
			t.Errorf("fields = %v", f)
		}
		ok := e.do(b, "PUT", path+"/publish", body, "If-Match", `"1"`)
		wantStatus(t, ok, 200, "")
		if etag(ok) != `"2"` || ok.JSON(t)["description"] != "publishes" {
			t.Errorf("after the PUT: ETag %q, %v", etag(ok), ok.JSON(t)["description"])
		}
		if _, has := ok.JSON(t)["warnings"]; !has {
			t.Error("the answer of a save carries warnings")
		}
		if _, has := e.do(a, "GET", path+"/publish", nil).JSON(t)["warnings"]; has {
			t.Error("a read carries no warnings")
		}
	})

	t.Run("R-CI-009 delete removes the mapping and its principal in one transaction", func(t *testing.T) {
		before := e.number("SELECT mappings_version FROM ci_issuers WHERE name = 'gitlab-main'")
		wantStatus(t, e.do(a, "DELETE", path+"/readers", nil, "If-Match", `"9"`), 412, "precondition_failed")
		wantStatus(t, e.do(b, "DELETE", path+"/readers", nil), 204, "")
		wantStatus(t, e.do(a, "GET", path+"/readers", nil), 404, "not_found")
		if e.number("SELECT count(*) FROM principal WHERE name = 'gitlab-main/readers'") != 0 {
			t.Error("the principal outlived the mapping")
		}
		if e.number("SELECT mappings_version FROM ci_issuers WHERE name = 'gitlab-main'") != before+1 {
			t.Error("the delete did not increment mappings_version")
		}
	})
}

func TestR_CI_009_FirstMatchPerTier(t *testing.T) {
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	e.issuer(t, a, "gitlab-main", false)
	e.stack.Repository("npm-internal", "local", "npm", "{}")
	path := mappingsOf("gitlab-main")
	// p10: merge request pipelines read only. p20: protected refs publish.
	wantStatus(t, e.do(a, "POST", path, mapping("p10", 10,
		all(eq("project_id", "4242"), eq("pipeline_source", "merge_request_event")),
		grants(inline("npm-internal", []string{"*"}, "read")))), 201, "")
	wantStatus(t, e.do(a, "POST", path, mapping("p20", 20,
		all(eq("project_id", "4242"), eq("ref_protected", true)),
		grants(inline("npm-internal", []string{"@acme/*"}, "read", "deploy")))), 201, "")

	evaluate := func(claims map[string]any, scope ...string) map[string]any {
		body := map[string]any{"claims": claims}
		if len(scope) > 0 {
			body["scope"] = scope[0]
		}
		r := e.do(a, "POST", issuersPath+"/gitlab-main/evaluate", body)
		wantStatus(t, r, 200, "")
		return r.JSON(t)
	}
	push := evaluate(map[string]any{"project_id": "4242", "ref_protected": "true", "pipeline_source": "push", "ref_type": "tag"})
	if sel := strs(push["selected"]); len(sel) != 1 || sel[0] != "p20" {
		t.Errorf("a push on a protected tag selected %v, want p20", sel)
	}
	if !strings.Contains(fmt.Sprint(push["scope"]), "deploy:npm-internal") {
		t.Errorf("the push pipeline's scope = %v, want deploy on npm-internal", push["scope"])
	}
	mr := evaluate(map[string]any{"project_id": "4242", "ref_protected": "true", "pipeline_source": "merge_request_event"})
	if sel := strs(mr["selected"]); len(sel) != 1 || sel[0] != "p10" {
		t.Errorf("a merge request pipeline of a protected branch selected %v, want p10 only", sel)
	}
	if strings.Contains(fmt.Sprint(mr["scope"]), "deploy") {
		t.Errorf("the merge request pipeline's scope = %v, which holds a write", mr["scope"])
	}
	trace := mr["trace"].([]any)
	if len(trace) != 1 || trace[0].(map[string]any)["mapping"] != "p10" {
		t.Errorf("the trace %v shows a mapping after the first match of the tier", trace)
	}
	// Asking for the write the first match does not hold is refused, not served from the second mapping.
	denied := evaluate(map[string]any{"project_id": "4242", "ref_protected": "true", "pipeline_source": "merge_request_event"}, "deploy:npm-internal")
	if denied["failure"] != "scope_not_granted" {
		t.Errorf("deploy on a merge request pipeline: failure %v, want scope_not_granted", denied["failure"])
	}
}

func TestR_CI_009_ARenderFailureDoesNotFallThrough(t *testing.T) {
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	e.issuer(t, a, "gitlab-main", false)
	e.stack.Repository("npm-internal", "local", "npm", "{}")
	path := mappingsOf("gitlab-main")
	match := all(eq("project_id", "4242"))
	// p10's template names a claim the token does not carry; p20 would match and render.
	wantStatus(t, e.do(a, "POST", path, mapping("p10", 10, match,
		grants(inline("npm-internal", []string{"@{{ namespace_path | slug }}/*"}, "read")))), 201, "")
	wantStatus(t, e.do(a, "POST", path, mapping("p20", 20, match, grants(inline("npm-internal", []string{"*"}, "read")))), 201, "")
	r := e.do(a, "POST", issuersPath+"/gitlab-main/evaluate", map[string]any{"claims": map[string]any{"project_id": "4242"}})
	wantStatus(t, r, 200, "")
	got := r.JSON(t)
	if got["failure"] != "grant_render_failed" || len(strs(got["selected"])) != 0 {
		t.Errorf("failure %v, selected %v: a render failure does not fall through to p20", got["failure"], got["selected"])
	}
	if !strings.Contains(fmt.Sprint(got["detail"]), "namespace_path") || strings.Contains(fmt.Sprint(got["detail"]), "4242") {
		t.Errorf("detail = %v: it names the claim, never its value", got["detail"])
	}
}

func TestR_CI_009_EqualPriorityInATierIsRefused(t *testing.T) {
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	e.issuer(t, a, "gitlab-main", false)
	e.issuer(t, a, "gitlab-other", false)
	e.stack.Repository("npm-internal", "local", "npm", "{}")
	path := mappingsOf("gitlab-main")
	wantStatus(t, e.do(a, "POST", path, readOnlyMapping("first", 100, all(eq("project_id", "1")), "npm-internal")), 201, "")
	before := e.epoch("ci_mappings")
	wantStatus(t, e.do(a, "POST", path, readOnlyMapping("second", 100, all(eq("project_id", "2")), "npm-internal")), 409, "priority_taken")
	wantStatus(t, e.do(a, "POST", mappingsOf("gitlab-other"), readOnlyMapping("second", 100, all(eq("project_id", "2")), "npm-internal")), 201, "")
	wantStatus(t, e.do(a, "POST", path, readOnlyMapping("second", 200, all(eq("project_id", "2")), "npm-internal")), 201, "")
	if e.epoch("ci_mappings") != before+2 {
		t.Error("a refused create wrote a change")
	}
	// A PUT that moves a mapping onto a taken priority is refused as well.
	body := e.do(a, "GET", path+"/second", nil).JSON(t)
	body["priority"] = 100
	wantStatus(t, e.do(a, "PUT", path+"/second", body, "If-Match", `"1"`), 409, "priority_taken")
	if e.number("SELECT priority FROM ci_mappings m JOIN ci_issuers i ON i.id = m.issuer_id WHERE i.name = 'gitlab-main' AND m.name = 'second'") != 200 {
		t.Error("the refused PUT moved the priority")
	}
	if n := e.number("SELECT count(*) FROM principal WHERE kind = 'ci_mapping'"); n != 3 {
		t.Errorf("%d principals, want 3: a refused write left a principal behind", n)
	}
}

func TestR_CI_009_TwoIssuersKeepTheirOwnPrincipals(t *testing.T) {
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	e.issuer(t, a, "gitlab-com", false)
	e.issuer(t, a, "gitlab-internal", false)
	e.stack.Repository("npm-internal", "local", "npm", "{}")
	e.stack.Policy("ci-read", nil, []string{"npm-internal"})
	body := func() map[string]any {
		return mapping("release-publish", 100, all(eq("project_id", "4242")), map[string]any{"policies": []string{"ci-read"}})
	}
	wantStatus(t, e.do(a, "POST", mappingsOf("gitlab-com"), body()), 201, "")
	wantStatus(t, e.do(a, "POST", mappingsOf("gitlab-internal"), mapping("release-publish", 100, all(eq("project_id", "4242")), grants(inline("npm-internal", []string{"*"}, "read")))), 201, "")
	for _, name := range []string{"gitlab-com/release-publish", "gitlab-internal/release-publish"} {
		if e.number("SELECT count(*) FROM principal WHERE kind = 'ci_mapping' AND name = $1", name) != 1 {
			t.Errorf("no principal %s", name)
		}
	}
	entries := func() []string {
		pol := e.do(a, "GET", "/api/v1/permission-policies/ci-read", nil)
		wantStatus(t, pol, 200, "")
		var out []string
		for _, en := range pol.JSON(t)["entries"].([]any) {
			out = append(out, en.(map[string]any)["principal"].(string))
		}
		return out
	}
	if got := entries(); len(got) != 1 || got[0] != "ci:gitlab-com/release-publish" {
		t.Errorf("the policy lists %v, want only ci:gitlab-com/release-publish: an entry names one mapping of one issuer", got)
	}
	oldID := e.text("SELECT id::text FROM principal WHERE name = 'gitlab-com/release-publish'")
	wantStatus(t, e.do(a, "DELETE", mappingsOf("gitlab-com")+"/release-publish", nil), 204, "")
	if got := entries(); len(got) != 0 {
		t.Errorf("after the delete the policy still lists %v", got)
	}
	wantStatus(t, e.do(a, "POST", mappingsOf("gitlab-com"), mapping("release-publish", 100, all(eq("project_id", "4242")), grants(inline("npm-internal", []string{"*"}, "read")))), 201, "")
	if got := entries(); len(got) != 0 {
		t.Errorf("a mapping created under the same name inherited the entries %v", got)
	}
	if e.text("SELECT id::text FROM principal WHERE name = 'gitlab-com/release-publish'") == oldID {
		t.Error("the re-created mapping reuses the old principal")
	}
}

// R-CI-017 (the principal mirrors enabled): the status of the principal follows the flag in the same write. The
// criteria of R-CI-017 itself are the revocation of tokens (revoke_test.go, exchange_mappings_test.go).
func TestThePrincipalOfAMappingMirrorsEnabled(t *testing.T) {
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	e.issuer(t, a, "gitlab-main", false)
	e.stack.Repository("npm-internal", "local", "npm", "{}")
	path := mappingsOf("gitlab-main")
	status := func() string { return e.text("SELECT status FROM principal WHERE name = 'gitlab-main/publish'") }
	wantStatus(t, e.do(a, "POST", path, mapping("publish", 10, all(eq("project_id", "1")), grants(inline("npm-internal", []string{"*"}, "read")), map[string]any{"enabled": false})), 201, "")
	if status() != "disabled" {
		t.Errorf("a mapping created disabled has a principal %q", status())
	}
	flip := func(enabled bool) {
		t.Helper()
		body := e.do(a, "GET", path+"/publish", nil).JSON(t)
		body["enabled"] = enabled
		wantStatus(t, e.do(a, "PUT", path+"/publish", body, "If-Match", "*"), 200, "")
	}
	flip(true)
	if status() != "active" {
		t.Errorf("enabling left the principal %q", status())
	}
	flip(false)
	if status() != "disabled" {
		t.Errorf("disabling left the principal %q", status())
	}
}

func TestR_CI_020_APutWritesExactlyOneEvent(t *testing.T) {
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	e.issuer(t, a, "gitlab-main", false)
	e.stack.Repository("npm-internal", "local", "npm", "{}")
	path := mappingsOf("gitlab-main")
	wantStatus(t, e.do(a, "POST", path, readOnlyMapping("publish", 10, all(eq("project_id", "1")), "npm-internal")), 201, "")
	events := func() map[string]int64 {
		out := map[string]int64{}
		for _, n := range []string{"created", "updated", "disabled", "enabled", "deleted"} {
			out[n] = e.events("ci.mapping." + n)
		}
		return out
	}
	put := func(mutate func(map[string]any)) {
		t.Helper()
		body := e.do(a, "GET", path+"/publish", nil).JSON(t)
		mutate(body)
		wantStatus(t, e.do(a, "PUT", path+"/publish", body, "If-Match", "*"), 200, "")
	}
	put(func(b map[string]any) { b["description"] = "changed" })
	put(func(b map[string]any) {
		b["enabled"] = false
		b["description"] = "disabled together with a change"
		b["priority"] = 11
	})
	put(func(b map[string]any) { b["description"] = "still disabled, only edited" })
	put(func(b map[string]any) { b["enabled"] = true })
	want := map[string]int64{"created": 1, "updated": 2, "disabled": 1, "enabled": 1, "deleted": 0}
	for k, v := range want {
		if got := events()[k]; got != v {
			t.Errorf("ci.mapping.%s written %d times, want %d: a PUT writes the one event its enabled change picks", k, got, v)
		}
	}
	// The .disabled event shows the other changes of the same PUT in its before and after record.
	d := e.eventDetails("ci.mapping.disabled")
	before, after := d["before"].(map[string]any), d["after"].(map[string]any)
	if before["enabled"] != true || after["enabled"] != false || after["priority"] != float64(11) || before["priority"] != float64(10) || d["issuer"] != "gitlab-main" {
		t.Errorf("the .disabled event: before %v, after %v", before, after)
	}
	wantStatus(t, e.do(a, "DELETE", path+"/publish", nil), 204, "")
	if events()["deleted"] != 1 {
		t.Error("the delete wrote no ci.mapping.deleted")
	}
	// The journal holds the removals of access and the changes, not what grants (R-CI-020).
	if e.journal("ci.mapping.created") != 0 || e.journal("ci.mapping.enabled") != 0 {
		t.Error("a .created or .enabled event entered the revocation journal")
	}
	if e.journal("ci.mapping.updated") != 2 || e.journal("ci.mapping.disabled") != 1 || e.journal("ci.mapping.deleted") != 1 {
		t.Errorf("journal: %d updated, %d disabled, %d deleted", e.journal("ci.mapping.updated"), e.journal("ci.mapping.disabled"), e.journal("ci.mapping.deleted"))
	}
	entry := e.text("SELECT entry::text FROM revocation_outbox WHERE event = 'ci.mapping.deleted'")
	for _, member := range []string{`"mappingId"`, `"issuer"`, `"name"`, `"tier"`, `"priority"`, `"before"`} {
		if !strings.Contains(entry, member) {
			t.Errorf("the journal entry of the delete lacks %s: %s", member, entry)
		}
	}
}

func TestR_CI_002_CascadeDeletesTheMappingsAndTheirPrincipals(t *testing.T) {
	d := startDouble(t, oidcstub.Config{})
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	wantStatus(t, e.do(a, "POST", issuersPath, d.body("gitlab-main")), 201, "")
	e.stack.Repository("npm-internal", "local", "npm", "{}")
	for i, name := range []string{"publish", "readers"} {
		wantStatus(t, e.do(a, "POST", mappingsOf("gitlab-main"), readOnlyMapping(name, 10*(i+1), all(eq("project_id", "4242")), "npm-internal")), 201, "")
	}
	wantStatus(t, e.do(a, "DELETE", issuersPath+"/gitlab-main", nil), 409, "conflict")
	wantStatus(t, e.do(a, "DELETE", issuersPath+"/gitlab-main?cascade=true", nil), 204, "")
	if e.number("SELECT count(*) FROM ci_mappings") != 0 || e.number("SELECT count(*) FROM principal WHERE kind = 'ci_mapping'") != 0 {
		t.Error("the mappings or their principals remain")
	}
	if e.events("ci.mapping.deleted") != 2 || e.events("ci.issuer.deleted") != 1 {
		t.Errorf("events: %d mapping deletions, %d issuer deletions, want 2 and 1", e.events("ci.mapping.deleted"), e.events("ci.issuer.deleted"))
	}
	if e.journal("ci.mapping.deleted") != 2 {
		t.Errorf("%d journal entries for the mapping deletions", e.journal("ci.mapping.deleted"))
	}
	if d := e.eventDetails("ci.mapping.deleted"); d["cascade"] != true {
		t.Errorf("the event of a cascaded mapping says cascade %v", d["cascade"])
	}
}

// R-CI-017: a mapping through which tokens were issued is disabled and deleted through the revoker in the write's
// transaction, and its principal mirrors enabled; an edit that leaves enabled alone revokes nothing. The revocation of
// live tokens on every replica is TestR_CI_017_* of revoke_test.go.
func TestAMappingWithIssuanceRecordsIsDisabledAndDeletedThroughTheRevoker(t *testing.T) {
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	e.issuer(t, a, "gitlab-main", false)
	e.stack.Repository("npm-internal", "local", "npm", "{}")
	path := mappingsOf("gitlab-main")
	wantStatus(t, e.do(a, "POST", path, readOnlyMapping("publish", 10, all(eq("project_id", "1")), "npm-internal")), 201, "")
	e.seedMappingIssuance("gitlab-main", "publish")
	body := e.do(a, "GET", path+"/publish", nil).JSON(t)
	body["enabled"] = false
	wantStatus(t, e.do(a, "PUT", path+"/publish", body, "If-Match", "*"), 200, "")
	if got := e.text("SELECT status FROM principal WHERE name = 'gitlab-main/publish'"); got != "disabled" {
		t.Errorf("the principal of a disabled mapping is %q", got)
	}
	body["enabled"], body["description"] = true, "an edit does not revoke"
	wantStatus(t, e.do(a, "PUT", path+"/publish", body, "If-Match", "*"), 200, "")
	if got := e.text("SELECT status FROM principal WHERE name = 'gitlab-main/publish'"); got != "active" {
		t.Errorf("the principal of an enabled mapping is %q", got)
	}
	wantStatus(t, e.do(a, "DELETE", path+"/publish", nil), 204, "")
	if e.number("SELECT count(*) FROM ci_mappings") != 0 || e.number("SELECT count(*) FROM principal WHERE name = 'gitlab-main/publish'") != 0 {
		t.Error("the deleted mapping or its principal remains")
	}
}

func TestR_CI_009_ACachedMappingSetFollowsTheChangeAreaOnEveryReplica(t *testing.T) {
	for _, valkey := range []bool{true, false} {
		t.Run(fmt.Sprintf("valkey=%v", valkey), func(t *testing.T) {
			e := newEnv(t, options{valkey: valkey})
			a, b := e.web("web-a"), e.web("web-b")
			e.issuer(t, a, "gitlab-main", false)
			e.stack.Repository("npm-internal", "local", "npm", "{}")
			id := e.text("SELECT id::text FROM ci_issuers WHERE name = 'gitlab-main'")
			if set, err := b.App.CIMappings().Set(t.Context(), id); err != nil || len(set.Mappings) != 0 {
				t.Fatalf("the first fill of B: %v %v", set, err)
			}
			trials, bound := 100, time.Second
			if !valkey {
				// The change-log poll carries it: cluster.epoch_poll_interval (500 ms here) plus 1 s.
				trials, bound = 12, 500*time.Millisecond+time.Second
			}
			var took []time.Duration
			for i := 0; i < trials; i++ {
				name := fmt.Sprintf("m%03d", i)
				wantStatus(t, e.do(a, "POST", mappingsOf("gitlab-main"), readOnlyMapping(name, i+1, all(eq("project_id", "1")), "npm-internal")), 201, "")
				start := time.Now()
				within(t, "B's cache holding "+name, 5*time.Second, func() bool {
					set, err := b.App.CIMappings().Set(t.Context(), id)
					if err != nil {
						return false
					}
					for _, m := range set.Mappings {
						if m.Name == name {
							return true
						}
					}
					return false
				})
				took = append(took, time.Since(start))
			}
			// The criterion is the p99 over the trials: with 100 trials the second slowest decides.
			sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
			if p99 := took[(len(took)*99+99)/100-1]; p99 > bound {
				t.Errorf("the p99 of %d trials is %s, want within %s (R-CI-019); the slowest took %s", trials, p99, bound, took[len(took)-1])
			}
			set, err := b.App.CIMappings().Set(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			if want := e.number("SELECT mappings_version FROM ci_issuers WHERE id = $1::uuid", id); set.MappingsVersion != want {
				t.Errorf("B's set is labelled mappings_version %d, the row has %d", set.MappingsVersion, want)
			}
		})
	}
}

func TestR_CI_009_TheSetTxReadsTheCommittedStateAndLabelsIt(t *testing.T) {
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	e.issuer(t, a, "gitlab-main", false)
	e.stack.Repository("npm-internal", "local", "npm", "{}")
	id := e.text("SELECT id::text FROM ci_issuers WHERE name = 'gitlab-main'")
	wantStatus(t, e.do(a, "POST", mappingsOf("gitlab-main"), readOnlyMapping("one", 1, all(eq("project_id", "1")), "npm-internal")), 201, "")
	// The change message of the create evicts the issuer's set when it is applied, and the fill rule (R-ARCH-018)
	// does not cache a set read while one is being applied: wait until A applied everything, then fill the copy.
	within(t, "A applying the change of the create", 5*time.Second, func() bool {
		return a.App.Changes().Applied(cluster.AreaCIMappings) == e.epoch(cluster.AreaCIMappings)
	})
	within(t, "A's copy holding the set", 5*time.Second, func() bool {
		set, err := a.App.CIMappings().Set(t.Context(), id)
		if err != nil || len(set.Mappings) != 1 {
			t.Fatalf("Set: %v %v", set, err)
		}
		for _, st := range a.App.Caches().Stats() {
			if st.Name == ci.CacheMappings && st.Entries > 0 {
				return true
			}
		}
		return false
	})
	// A write the change area has not told this replica about yet: the row moves, the copy stays.
	e.exec(`UPDATE ci_mappings SET priority = 7 WHERE name = 'one'`)
	e.exec(`UPDATE ci_issuers SET mappings_version = mappings_version + 1 WHERE id = $1::uuid`, id)
	stale, _ := a.App.CIMappings().Set(t.Context(), id)
	if stale.Mappings[0].Priority != 1 {
		t.Fatal("the copy followed a write nobody announced: the test cannot compare it with PostgreSQL")
	}
	if stale.MappingsVersion >= e.number("SELECT mappings_version FROM ci_issuers WHERE id = $1::uuid", id) {
		t.Error("the copy is labelled with a mappings_version newer than what it holds")
	}
	// SetTx reads PostgreSQL itself and refreshes the copy (R-CI-007, Q-CI-15).
	fresh, err := readSetTx(t, e, a, id)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Mappings[0].Priority != 7 || fresh.MappingsVersion != e.number("SELECT mappings_version FROM ci_issuers WHERE id = $1::uuid", id) {
		t.Errorf("SetTx = priority %d, mappings_version %d", fresh.Mappings[0].Priority, fresh.MappingsVersion)
	}
}

func readSetTx(t *testing.T, e *env, r *testenv.Replica, issuerID string) (ci.MappingSet, error) {
	t.Helper()
	db, err := postgres.Open(t.Context(), e.stack.DatabaseConfig(), "set-tx", "web", testenv.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	var set ci.MappingSet
	err = db.InTx(t.Context(), func(tx meta.Tx) error {
		var err error
		set, err = r.App.CIMappings().SetTx(t.Context(), tx, issuerID)
		return err
	})
	return set, err
}
