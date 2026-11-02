//go:build integration

package ci

import (
	"fmt"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The dry run (R-CI-019, R-UI-036): a claim set in, the decision trace out; nothing issued, no jti recorded.

const evaluatePath = issuersPath + "/gitlab-com/evaluate"

// acme creates the CI issuer gitlab-com (public, so the pin rule applies) and the two mappings of the
// specification's example (R-CI-019): a merge request mapping that grants read through a policy and the
// release mapping of namespace 4711.
func (e *env) acme(t *testing.T, r *testenv.Replica) {
	t.Helper()
	e.issuer(t, r, "gitlab-com", true)
	e.stack.Repository("npm-internal", "local", "npm", "{}")
	e.stack.Repository("nuget-internal", "local", "nuget", "{}")
	e.stack.Policy("ci-read-internal", nil, []string{"npm-internal", "nuget-internal"})
	path := mappingsOf("gitlab-com")
	wantStatus(t, e.do(r, "POST", path, mapping("acme-mr-readonly", 50,
		all(eq("namespace_id", "4711"), eq("pipeline_source", "merge_request_event")),
		map[string]any{"policies": []string{"ci-read-internal"}})), 201, "")
	release := mapping("acme-release-publish", 100,
		all(eq("namespace_id", "4711"), eq("ref_type", "tag"), eq("ref_protected", true), map[string]any{"claim": "pipeline_source", "in": []string{"push", "web"}}),
		map[string]any{
			"policies": []string{"ci-read-internal"},
			"inline": []any{
				map[string]any{
					"repositories": []string{"npm-internal"}, "packages": map[string]any{"include": []string{"@{{ namespace_path | slug }}/*"}},
					"actions": []string{"read", "deploy"}, "ownership": "bind_project",
				},
				map[string]any{
					"repositories": []string{"nuget-internal"}, "packages": map[string]any{"include": []string{
						`{{ project_path | replace "/" "." | replace "-" "" }}`, `{{ project_path | replace "/" "." | replace "-" "" }}.*`,
					}},
					"actions": []string{"read", "deploy"}, "ownership": "bind_project",
				},
			},
		}, map[string]any{"maxTtlSeconds": 1800})
	wantStatus(t, e.do(r, "POST", path, release), 201, "")
}

// exampleClaims is the claim set of the specification's example: a protected tag pipeline of
// acme/billing/invoice-api.
func exampleClaims(extra ...map[string]any) map[string]any {
	c := map[string]any{
		"iss": "https://gitlab.com", "project_id": "4242", "namespace_id": "4711", "project_path": "acme/billing/invoice-api",
		"namespace_path": "acme/billing", "ref": "v1.4.0", "ref_type": "tag", "ref_protected": "true", "pipeline_source": "push",
		"jti": "x", "exp": 1792940000, "iat": 1792936400,
	}
	for _, m := range extra {
		for k, v := range m {
			c[k] = v
		}
	}
	return c
}

func TestR_CI_019_TheDryRunOfTheExampleClaimSet(t *testing.T) {
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	e.acme(t, a)
	evaluate := func(body map[string]any) map[string]any {
		t.Helper()
		r := e.do(a, "POST", evaluatePath, body)
		wantStatus(t, r, 200, "")
		return r.JSON(t)
	}

	got := evaluate(map[string]any{"claims": exampleClaims(), "scope": "deploy:npm-internal"})
	if sel := strs(got["selected"]); len(sel) != 1 || sel[0] != "acme-release-publish" {
		t.Fatalf("selected = %v, want acme-release-publish", sel)
	}
	if got["failure"] != nil {
		t.Errorf("failure = %v", got["failure"])
	}
	grantsOut := got["grants"].(map[string]any)
	inl := grantsOut["inline"].([]any)
	if len(inl) != 1 {
		t.Fatalf("the scope deploy:npm-internal narrows to one inline grant, got %v", inl)
	}
	first := inl[0].(map[string]any)
	if inc := strs(first["packages"].(map[string]any)["include"]); len(inc) != 1 || inc[0] != "@acme-billing/*" {
		t.Errorf("the template renders %v, want @acme-billing/*", inc)
	}
	if first["ownership"] != "bind_project" || strs(first["actions"])[0] != "deploy" || len(strs(first["actions"])) != 1 {
		t.Errorf("the narrowed grant = %v, want only deploy, bind_project", first)
	}
	if pol := strs(grantsOut["policies"]); len(pol) != 1 || pol[0] != "ci-read-internal" {
		t.Errorf("policies = %v", pol)
	}
	if got["scope"] != "deploy:npm-internal" || got["ttlSeconds"] != float64(900) {
		t.Errorf("scope %v, ttlSeconds %v, want deploy:npm-internal and 900", got["scope"], got["ttlSeconds"])
	}
	trace := got["trace"].([]any)
	if len(trace) != 2 {
		t.Fatalf("trace = %v", trace)
	}
	mr := trace[0].(map[string]any)
	if mr["mapping"] != "acme-mr-readonly" || mr["matched"] != false || mr["failedLeaf"] != "/all/1" || mr["tier"] != "system" {
		t.Errorf("the first trace entry = %v", mr)
	}
	if !strings.Contains(fmt.Sprint(mr["detail"]), `pipeline_source equals "merge_request_event": value "push"`) {
		t.Errorf("detail = %v", mr["detail"])
	}
	if leaves := mr["leaves"].([]any); len(leaves) != 2 || leaves[1].(map[string]any)["passed"] != false {
		t.Errorf("leaves = %v: every leaf is in the trace", leaves)
	}
	if last := trace[1].(map[string]any); last["mapping"] != "acme-release-publish" || last["matched"] != true || last["contributes"] != true {
		t.Errorf("the second trace entry = %v", last)
	}

	t.Run("the same claim set with ref_protected false shows the failing leaf", func(t *testing.T) {
		got := evaluate(map[string]any{"claims": exampleClaims(map[string]any{"ref_protected": "false"})})
		if got["failure"] != "no_matching_mapping" || len(strs(got["selected"])) != 0 {
			t.Errorf("failure %v, selected %v", got["failure"], got["selected"])
		}
		release := got["trace"].([]any)[1].(map[string]any)
		if release["mapping"] != "acme-release-publish" || release["matched"] != false || release["failedLeaf"] != "/all/2" {
			t.Errorf("the release mapping's entry = %v, want failed at /all/2", release)
		}
		if !strings.Contains(fmt.Sprint(release["detail"]), "ref_protected") {
			t.Errorf("detail = %v", release["detail"])
		}
	})

	t.Run("a boolean claim and its string form are one value", func(t *testing.T) {
		got := evaluate(map[string]any{"claims": exampleClaims(map[string]any{"ref_protected": true})})
		if sel := strs(got["selected"]); len(sel) != 1 {
			t.Errorf("selected = %v", sel)
		}
	})

	t.Run("a policy's scope items are taken as held with a warning, and manage is no scope", func(t *testing.T) {
		got := evaluate(map[string]any{"claims": exampleClaims(), "scope": "delete:npm-internal"})
		// The mapping lists a policy, which the dry run does not read.
		if got["failure"] != nil {
			t.Fatalf("failure = %v", got["failure"])
		}
		var warned bool
		for _, w := range got["warnings"].([]any) {
			warned = warned || w.(map[string]any)["code"] == "scope_policy_grants_not_checked"
		}
		if !warned {
			t.Errorf("warnings = %v", got["warnings"])
		}
		bad := evaluate(map[string]any{"claims": exampleClaims(), "scope": "manage:npm-internal"})
		if bad["failure"] != "scope_malformed" {
			t.Errorf("manage in the scope: failure %v", bad["failure"])
		}
	})

	t.Run("no time check without now, and with now the time claims are held to the issuer's skew", func(t *testing.T) {
		expired := exampleClaims(map[string]any{"exp": 1000, "iat": 900})
		if got := evaluate(map[string]any{"claims": expired}); got["failure"] != nil {
			t.Errorf("an expired token without now: failure %v", got["failure"])
		}
		got := evaluate(map[string]any{"claims": expired, "now": 1792936500})
		if got["failure"] != "token_expired" || len(got["trace"].([]any)) != 0 {
			t.Errorf("with now: failure %v, trace %v", got["failure"], got["trace"])
		}
		ok := evaluate(map[string]any{"claims": exampleClaims(), "now": 1792936500})
		if ok["failure"] != nil {
			t.Errorf("a valid token with now: failure %v", ok["failure"])
		}
		// The remaining life of the subject token bounds the lifetime (R-CI-016): 100 s left.
		short := evaluate(map[string]any{"claims": exampleClaims(map[string]any{"exp": 1792936600}), "now": 1792936500})
		if short["ttlSeconds"] != float64(100) {
			t.Errorf("ttlSeconds = %v, want 100", short["ttlSeconds"])
		}
		tooShort := evaluate(map[string]any{"claims": exampleClaims(map[string]any{"exp": 1792936510}), "now": 1792936500})
		if tooShort["failure"] != "subject_token_expires_too_soon" {
			t.Errorf("10 s left: failure %v", tooShort["failure"])
		}
	})

	t.Run("it issues nothing and records no jti and no event", func(t *testing.T) {
		before := e.number("SELECT count(*) FROM audit_event WHERE action LIKE 'ci.%'")
		evaluate(map[string]any{"claims": exampleClaims()})
		evaluate(map[string]any{"claims": exampleClaims()})
		if e.number("SELECT count(*) FROM api_token WHERE type = 'cit'") != 0 || e.number("SELECT count(*) FROM ci_subject_token_uses") != 0 ||
			e.number("SELECT count(*) FROM ci_token_issuances") != 0 {
			t.Error("the dry run wrote a token, a jti or an issuance record")
		}
		if e.number("SELECT count(*) FROM audit_event WHERE action LIKE 'ci.%'") != before {
			t.Error("the dry run wrote an audit event")
		}
	})

	t.Run("the request takes claims, never a token, and a claim set is a JSON object", func(t *testing.T) {
		wantStatus(t, e.do(a, "POST", evaluatePath, map[string]any{}), 422, "")
		wantStatus(t, e.do(a, "POST", evaluatePath, map[string]any{"claims": "eyJhbGciOiJSUzI1NiJ9.e30.sig"}), 422, "")
		wantStatus(t, e.do(a, "POST", evaluatePath, map[string]any{"claims": exampleClaims(), "subjectKind": "job_token"}), 422, "")
		wantStatus(t, e.do(a, "POST", issuersPath+"/nobody/evaluate", map[string]any{"claims": exampleClaims()}), 404, "not_found")
		if r := e.as(a, e.alicePAT, "POST", evaluatePath, map[string]any{"claims": exampleClaims()}); r.Status != 403 {
			t.Errorf("a user without a role got %d", r.Status)
		}
	})
}

func TestR_CI_012_TheDryRunClassifiesAForkPipelineAndHonoursTheOptIn(t *testing.T) {
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	e.issuer(t, a, "gitlab-main", false)
	e.stack.Repository("npm-internal", "local", "npm", "{}")
	path := mappingsOf("gitlab-main")
	fork := map[string]any{"project_id": "900", "job_project_id": "42", "ref_protected": "false", "pipeline_source": "merge_request_event"}
	evaluate := func() map[string]any {
		r := e.do(a, "POST", issuersPath+"/gitlab-main/evaluate", map[string]any{"claims": fork})
		wantStatus(t, r, 200, "")
		return r.JSON(t)
	}
	wantStatus(t, e.do(a, "POST", path, readOnlyMapping("first", 10, all(eq("pipeline_source", "merge_request_event")), "npm-internal")), 201, "")
	got := evaluate()
	if got["crossProject"] != true || got["failure"] != "cross_project_source_denied" {
		t.Errorf("crossProject %v, failure %v, want the default deny", got["crossProject"], got["failure"])
	}
	entry := got["trace"].([]any)[0].(map[string]any)
	if entry["matched"] != true || entry["contributes"] != false {
		t.Errorf("the trace entry = %v: matched, and contributes nothing", entry)
	}
	// An opt-in on the first match; the later mapping stays out of the evaluation (first match per tier).
	body := e.do(a, "GET", path+"/first", nil).JSON(t)
	body["options"] = map[string]any{"crossProjectSource": "read_only"}
	body["grants"] = grants(inline("npm-internal", []string{"*"}, "read"))
	wantStatus(t, e.do(a, "PUT", path+"/first", body, "If-Match", "*"), 200, "")
	got = evaluate()
	if got["failure"] != nil || got["scope"] != "read:npm-internal" {
		t.Errorf("failure %v, scope %v: with read_only the token holds read items only", got["failure"], got["scope"])
	}
}

func TestR_CI_009_ADisabledMappingIsNotEvaluated(t *testing.T) {
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	e.issuer(t, a, "gitlab-main", false)
	e.stack.Repository("npm-internal", "local", "npm", "{}")
	path := mappingsOf("gitlab-main")
	wantStatus(t, e.do(a, "POST", path, mapping("narrow", 10, all(eq("project_id", "1")),
		grants(inline("npm-internal", []string{"*"}, "read")), map[string]any{"enabled": false})), 201, "")
	wantStatus(t, e.do(a, "POST", path, mapping("broad", 20, all(eq("project_id", "1")), grants(inline("npm-internal", []string{"*"}, "read", "annotate")))), 201, "")
	r := e.do(a, "POST", issuersPath+"/gitlab-main/evaluate", map[string]any{"claims": map[string]any{"project_id": "1"}})
	wantStatus(t, r, 200, "")
	got := r.JSON(t)
	if sel := strs(got["selected"]); len(sel) != 1 || sel[0] != "broad" {
		t.Errorf("selected = %v: a disabled mapping lets the jobs fall through to the next one", sel)
	}
	if len(got["trace"].([]any)) != 1 {
		t.Errorf("trace = %v: a disabled mapping is not looked at", got["trace"])
	}
}
