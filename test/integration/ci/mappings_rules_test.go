//go:build integration

package ci

import (
	"testing"
)

// The save-time rules of R-CI-005, R-CI-010 to R-CI-014 over HTTP, the shape of a violation (Q-CI-30) and the
// binding of the policies (R-CI-013, Q-CI-35).

func deepCondition() map[string]any {
	// all → any → not → all → leaf: five levels, one more than R-CI-010 allows.
	return map[string]any{"all": []any{map[string]any{"any": []any{map[string]any{"not": map[string]any{"all": []any{eq("project_id", "1")}}}}}}}
}

func TestR_CI_010_TheSaveTimeRulesOverHTTP(t *testing.T) {
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	e.issuer(t, a, "gitlab-main", false)  // private: unpinned_write applies
	e.issuer(t, a, "gitlab-public", true) // public: pin_required applies
	e.stack.Repository("npm-internal", "local", "npm", "{}")
	pinned := all(eq("project_id", "4242"), eq("ref_protected", true))
	read := grants(inline("it/npm-internal", []string{"*"}, "read"))

	for name, tc := range map[string]struct {
		issuer string
		body   map[string]any
		code   string // the problem's code
		field  string // "pointer code" in errors[]
	}{
		"R-CI-010 a condition without a leaf is a catch-all": {
			"gitlab-main", mapping("mm", 1, map[string]any{}, read), "catch_all_condition", "/match catch_all_condition",
		},
		"R-CI-010 a public issuer's condition needs a pin": {
			"gitlab-public", mapping("mm", 1, all(eq("ref_type", "tag")), read), "pin_required", "/match pin_required",
		},
		"R-CI-010 a condition deeper than 4 levels": {
			"gitlab-main", mapping("mm", 1, deepCondition(), read), "condition_invalid", "",
		},
		"R-CI-011 deploy needs ref_protected": {
			"gitlab-main", mapping("mm", 1, all(eq("project_id", "4242")), grants(inline("it/npm-internal", []string{"@a/*"}, "deploy"))),
			"unprotected_write", "/match unprotected_write",
		},
		"R-CI-010 a write on a private issuer without a pin and without bind_project": {
			"gitlab-main", mapping("mm", 1, all(eq("ref_protected", true)),
				grants(map[string]any{"repositories": []string{"it/npm-internal"}, "packages": map[string]any{"include": []string{"@a/*"}}, "actions": []string{"deploy"}, "ownership": "none"})),
			"unpinned_write", "/match unpinned_write",
		},
		"R-CI-013 manage is never grantable": {
			"gitlab-main", mapping("mm", 1, pinned, grants(inline("it/npm-internal", []string{"*"}, "manage"))),
			"action_not_allowed", "/grants/inline/0/actions/0 action_not_allowed",
		},
		"R-CI-013 an empty grant specification": {
			"gitlab-main", mapping("mm", 1, pinned, map[string]any{}), "empty_grant", "/grants empty_grant",
		},
		"R-CI-014 a template with a syntax error": {
			"gitlab-main", mapping("mm", 1, pinned, grants(inline("it/npm-internal", []string{"{{ namespace_path"}, "read"))),
			"template_invalid", "/grants/inline/0/packages/include/0 template_invalid",
		},
		"R-CI-014 a repositories template that a claim value alone can select": {
			"gitlab-main", mapping("mm", 1, pinned, map[string]any{"inline": []any{map[string]any{
				"repositories": []string{"{{ project_path | slug }}"}, "packages": map[string]any{"include": []string{"*"}}, "actions": []string{"read"},
			}}}),
			"template_unanchored", "/grants/inline/0/repositories/0 template_unanchored",
		},
		// R-CI-022 is P2 and not built: the refusal of its subject kind is no evidence of it.
		"the job token kind is not available": {
			"gitlab-main", mapping("mm", 1, pinned, read, map[string]any{"subjectKinds": []string{"job_token"}}),
			"job_token_fallback_not_available", "/subjectKinds/0 job_token_fallback_not_available",
		},
		"R-CI-013 a policy that does not exist": {
			"gitlab-main", mapping("mm", 1, pinned, map[string]any{"policies": []string{"nope"}}), "unknown_policy", "/grants/policies/0 unknown_policy",
		},
		"R-CI-013 a group that does not exist": {
			"gitlab-main", mapping("mm", 1, pinned, map[string]any{"groups": []string{"nobody"}}), "unknown_group", "/grants/groups/0 unknown_group",
		},
		"R-CI-009 a name outside the pattern": {
			"gitlab-main", mapping("Bad Name", 1, pinned, read), "", "/name invalid_name",
		},
		"R-CI-009 a priority outside 1 to 1000000": {
			"gitlab-main", mapping("mm", 0, pinned, read), "", "/priority out_of_range",
		},
		"R-CI-009 max_ttl_seconds above ci.token.max_ttl": {
			"gitlab-main", mapping("mm", 1, pinned, read, map[string]any{"maxTtlSeconds": 86400}), "", "/maxTtlSeconds out_of_range",
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := e.do(a, "POST", mappingsOf(tc.issuer), tc.body)
			wantStatus(t, r, 422, orValidation(tc.code))
			if tc.field != "" {
				found := false
				for _, f := range problemFields(t, r) {
					found = found || f == tc.field
				}
				if !found {
					t.Errorf("errors[] = %v, want %q", problemFields(t, r), tc.field)
				}
			}
			if e.number("SELECT count(*) FROM ci_mappings") != 0 || e.number("SELECT count(*) FROM principal WHERE kind = 'ci_mapping'") != 0 {
				t.Error("a refused save stored a mapping or a principal")
			}
		})
	}

	t.Run("Q-CI-30 a request that breaks several rules lists every one with its pointer", func(t *testing.T) {
		r := e.do(a, "POST", mappingsOf("gitlab-main"), mapping("mm", 1, pinned, map[string]any{"policies": []string{"nope"}, "groups": []string{"nobody"}}))
		wantStatus(t, r, 422, "unknown_policy")
		if f := problemFields(t, r); len(f) != 2 {
			t.Errorf("errors[] = %v, want the unknown policy and the unknown group", f)
		}
	})

	t.Run("R-CI-009 a body member that is no member of a mapping is 400 with its pointer", func(t *testing.T) {
		body := mapping("mm", 1, pinned, read, map[string]any{"colour": "red"})
		wantStatus(t, e.do(a, "POST", mappingsOf("gitlab-main"), body), 400, "")
		wantStatus(t, e.do(a, "POST", mappingsOf("gitlab-main"), map[string]any{"name": "mm", "priority": 1, "grants": read}), 422, "")
	})
}

// TestR_AUTHZ_005_AnInlineGrantsUpperCasePathPatternOnNuGetIsRefused: NuGet stores lower-case paths and the path
// dimension is not folded, so an inline grant's path pattern with an upper-case letter over a NuGet repository would match
// nothing, and an exclude written so would admit what it names; the save refuses it at the pattern (Q-AUTHZ-253). The
// repository exists before the replica starts, so the decision point's index holds it.
func TestR_AUTHZ_005_AnInlineGrantsUpperCasePathPatternOnNuGetIsRefused(t *testing.T) {
	e := newEnv(t, options{valkey: true})
	e.stack.Repository("nuget-internal", "local", "nuget", "{}")
	a := e.web("web-a")
	e.issuer(t, a, "gitlab-main", false)
	pinned := all(eq("project_id", "4242"), eq("ref_protected", true))
	body := func(pattern string) map[string]any {
		return mapping("mm", 1, pinned, map[string]any{"inline": []any{map[string]any{
			"repositories": []string{"it/nuget-internal"}, "paths": map[string]any{"exclude": []string{pattern}}, "actions": []string{"read"},
		}}})
	}
	r := e.do(a, "POST", mappingsOf("gitlab-main"), body("Acme.Secret/**"))
	wantStatus(t, r, 422, "invalid_pattern")
	found := false
	for _, f := range problemFields(t, r) {
		found = found || f == "/grants/inline/0/paths/exclude/0 invalid_pattern"
	}
	if !found {
		t.Errorf("errors[] = %v, want the pattern's pointer", problemFields(t, r))
	}
	wantStatus(t, e.do(a, "POST", mappingsOf("gitlab-main"), body("acme.secret/**")), 201, "")
}

func orValidation(code string) string {
	if code == "" {
		return "validation_failed"
	}
	return code
}

func TestR_CI_010_TheWarningsOfASave(t *testing.T) {
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	e.issuer(t, a, "gitlab-main", false)
	e.stack.Repository("npm-internal", "local", "npm", "{}")
	path := mappingsOf("gitlab-main")

	t.Run("R-CI-010 a private issuer's mapping without a pin warns unpinned_condition", func(t *testing.T) {
		r := e.do(a, "POST", path, readOnlyMapping("loose", 1, all(eq("namespace_path", "acme")), "it/npm-internal"))
		wantStatus(t, r, 201, "")
		if !contains(warningCodes(t, r), "unpinned_condition") {
			t.Errorf("warnings = %v", warningCodes(t, r))
		}
	})
	t.Run("R-CI-013 an explicit ownership none on a writing grant saves with shared_ownership", func(t *testing.T) {
		g := grants(map[string]any{"repositories": []string{"it/npm-internal"}, "packages": map[string]any{"include": []string{"@a/*"}}, "actions": []string{"read", "deploy"}, "ownership": "none"})
		r := e.do(a, "POST", path, mapping("shared", 2, all(eq("project_id", "4242"), eq("ref_protected", true), map[string]any{"claim": "pipeline_source", "in": []string{"push"}}), g))
		wantStatus(t, r, 201, "")
		if !contains(warningCodes(t, r), "shared_ownership") {
			t.Errorf("warnings = %v", warningCodes(t, r))
		}
		got := e.do(a, "GET", path+"/shared", nil).JSON(t)["grants"].(map[string]any)["inline"].([]any)[0].(map[string]any)
		if got["ownership"] != "none" {
			t.Errorf("the explicit none reads back %v", got["ownership"])
		}
	})
	t.Run("R-CI-011 and R-CI-012 the visible opt-ins and a write without a pipeline_source", func(t *testing.T) {
		r := e.do(a, "POST", path, mapping("optin", 3, all(eq("project_id", "4242")), grants(inline("it/npm-internal", []string{"@a/*"}, "deploy")),
			map[string]any{"options": map[string]any{"allowUnprotectedRefs": true, "crossProjectSource": "read_only"}}))
		wantStatus(t, r, 201, "")
		codes := warningCodes(t, r)
		for _, want := range []string{"unprotected_refs_allowed", "cross_project_read_only", "write_without_pipeline_source"} {
			if !contains(codes, want) {
				t.Errorf("warnings %v lack %s", codes, want)
			}
		}
	})
	t.Run("R-CI-010 a condition on a mutable user claim and on sub", func(t *testing.T) {
		r := e.do(a, "POST", path, readOnlyMapping("users", 4, all(eq("project_id", "4242"), eq("user_login", "mallory"),
			map[string]any{"claim": "sub", "glob": "project_path:acme/*"}), "it/npm-internal"))
		wantStatus(t, r, 201, "")
		codes := warningCodes(t, r)
		if !contains(codes, "mutable_user_claim") || !contains(codes, "sub_pattern") {
			t.Errorf("warnings = %v", codes)
		}
	})
	t.Run("R-CI-013 an inline grant with deploy and no ownership reads back bind_project", func(t *testing.T) {
		wantStatus(t, e.do(a, "POST", path, publishMapping("bound", 5, "it/npm-internal")), 201, "")
		got := e.do(a, "GET", path+"/bound", nil).JSON(t)["grants"].(map[string]any)["inline"].([]any)[0].(map[string]any)
		if got["ownership"] != "bind_project" {
			t.Errorf("ownership = %v", got["ownership"])
		}
	})
}

// The binding of a policy is stored once, in the policy's principal list (R-CI-013).
func TestR_CI_013_PoliciesAreBoundThroughThePolicysPrincipalList(t *testing.T) {
	e := newEnv(t, options{valkey: true})
	a, b := e.web("web-a"), e.web("web-b")
	e.issuer(t, a, "gitlab-main", false)
	e.stack.Repository("npm-internal", "local", "npm", "{}")
	e.stack.Policy("ci-read", nil, []string{"npm-internal"})
	e.stack.Policy("ci-other", nil, []string{"npm-internal"})
	path := mappingsOf("gitlab-main")
	entries := func(policy string) map[string][]string {
		r := e.do(b, "GET", "/api/v1/permission-policies/"+policy, nil)
		wantStatus(t, r, 200, "")
		out := map[string][]string{}
		for _, en := range r.JSON(t)["entries"].([]any) {
			m := en.(map[string]any)
			out[m["principal"].(string)] = strs(m["actions"])
		}
		return out
	}
	policyVersion := func(policy string) int64 { return e.number("SELECT version FROM authz_policy WHERE name = $1", policy) }

	v0, epoch0 := policyVersion("ci-read"), e.epoch("authz")
	r := e.do(a, "POST", path, mapping("reader", 10, all(eq("project_id", "1")), map[string]any{"policies": []string{"ci-read"}}))
	wantStatus(t, r, 201, "")
	if got := entries("ci-read")["ci:gitlab-main/reader"]; len(got) != 1 || got[0] != "read" {
		t.Errorf("the policy's entry for the mapping = %v, want read (the save adds the least a policy can give)", got)
	}
	if policyVersion("ci-read") != v0+1 || e.epoch("authz") <= epoch0 {
		t.Error("adding the entry did not move the policy's version and the area authz")
	}
	if pol := strs(e.do(a, "GET", path+"/reader", nil).JSON(t)["grants"].(map[string]any)["policies"]); len(pol) != 1 || pol[0] != "ci-read" {
		t.Errorf("the mapping reads back the policies %v", pol)
	}

	t.Run("an administrator who widens the entry in the policy changes what the mapping lists nothing about", func(t *testing.T) {
		pol := e.do(a, "GET", "/api/v1/permission-policies/ci-read", nil)
		body := pol.JSON(t)
		for _, en := range body["entries"].([]any) {
			en.(map[string]any)["actions"] = []string{"read", "annotate"}
		}
		wantStatus(t, e.do(a, "PUT", "/api/v1/permission-policies/ci-read", body, "If-Match", etag(pol)), 200, "")
		// A PUT of the mapping keeps the entry's actions: the entry is the policy's.
		m := e.do(a, "GET", path+"/reader", nil).JSON(t)
		m["description"] = "edited"
		wantStatus(t, e.do(a, "PUT", path+"/reader", m, "If-Match", "*"), 200, "")
		if got := entries("ci-read")["ci:gitlab-main/reader"]; len(got) != 2 {
			t.Errorf("the entry after the mapping's PUT = %v, want read and annotate kept", got)
		}
	})

	t.Run("removing the mapping from the policy removes it from the mapping", func(t *testing.T) {
		pol := e.do(a, "GET", "/api/v1/permission-policies/ci-read", nil)
		body := pol.JSON(t)
		body["entries"] = []any{}
		wantStatus(t, e.do(a, "PUT", "/api/v1/permission-policies/ci-read", body, "If-Match", etag(pol)), 200, "")
		if pol := strs(e.do(b, "GET", path+"/reader", nil).JSON(t)["grants"].(map[string]any)["policies"]); len(pol) != 0 {
			t.Errorf("the mapping still lists %v: the binding is stored once, in the policy", pol)
		}
	})

	t.Run("a PUT that lists other policies moves the binding", func(t *testing.T) {
		m := e.do(a, "GET", path+"/reader", nil).JSON(t)
		m["grants"] = map[string]any{"policies": []string{"ci-read", "ci-other"}}
		wantStatus(t, e.do(a, "PUT", path+"/reader", m, "If-Match", "*"), 200, "")
		if len(entries("ci-read")) != 1 || len(entries("ci-other")) != 1 {
			t.Errorf("entries: %v %v", entries("ci-read"), entries("ci-other"))
		}
		m["grants"] = map[string]any{"policies": []string{"ci-other"}}
		wantStatus(t, e.do(a, "PUT", path+"/reader", m, "If-Match", "*"), 200, "")
		if len(entries("ci-read")) != 0 || len(entries("ci-other")) != 1 {
			t.Errorf("entries after the removal: %v %v", entries("ci-read"), entries("ci-other"))
		}
		// An update that changes nothing about the grants leaves a binding made through the policy API alone.
	})

	t.Run("deleting the mapping removes its entries and moves the policy", func(t *testing.T) {
		before := policyVersion("ci-other")
		wantStatus(t, e.do(a, "DELETE", path+"/reader", nil), 204, "")
		if len(entries("ci-other")) != 0 || policyVersion("ci-other") != before+1 {
			t.Errorf("entries %v, version %d -> %d", entries("ci-other"), before, policyVersion("ci-other"))
		}
	})
}

// The write rules count the writes that a listed policy or group gives (R-CI-010, R-CI-011, R-CI-013).
func TestR_CI_010_TheWriteRulesCountWritesThroughPoliciesAndGroups(t *testing.T) {
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	e.issuer(t, a, "gitlab-main", false) // private: a write through a policy or a group needs a pin
	e.stack.Repository("npm-internal", "local", "npm", "{}")
	e.stack.Policy("ci-write", nil, []string{"npm-internal"})
	path := mappingsOf("gitlab-main")
	protected := all(eq("ref_protected", true), map[string]any{"claim": "pipeline_source", "in": []string{"push"}})

	t.Run("a group that writes", func(t *testing.T) {
		e.seedGroup("ci-publishers", "group-writes", "npm-internal", "read", "deploy")
		r := e.do(a, "POST", path, mapping("via-group", 1, protected, map[string]any{"groups": []string{"ci-publishers"}}))
		wantStatus(t, r, 422, "unpinned_write")
		wantStatus(t, e.do(a, "POST", path, mapping("via-group", 1, all(eq("project_id", "1"), eq("ref_protected", true)),
			map[string]any{"groups": []string{"ci-publishers"}})), 201, "")
		// Without a ref_protected leaf the group's write is a write like any other (R-CI-011).
		wantStatus(t, e.do(a, "POST", path, mapping("via-group-open", 2, all(eq("project_id", "1")), map[string]any{"groups": []string{"ci-publishers"}})), 422, "unprotected_write")
	})

	t.Run("a group that holds manage", func(t *testing.T) {
		e.seedGroup("ci-admins", "group-manages", "npm-internal", "manage")
		r := e.do(a, "POST", path, mapping("via-admins", 3, all(eq("project_id", "1")), map[string]any{"groups": []string{"ci-admins"}}))
		wantStatus(t, r, 422, "action_not_allowed")
	})

	t.Run("a policy whose entry for the mapping writes", func(t *testing.T) {
		wantStatus(t, e.do(a, "POST", path, mapping("via-policy", 4, all(eq("project_id", "1")), map[string]any{"policies": []string{"ci-write"}})), 201, "")
		pol := e.do(a, "GET", "/api/v1/permission-policies/ci-write", nil)
		body := pol.JSON(t)
		for _, en := range body["entries"].([]any) {
			en.(map[string]any)["actions"] = []string{"read", "deploy"}
		}
		wantStatus(t, e.do(a, "PUT", "/api/v1/permission-policies/ci-write", body, "If-Match", etag(pol)), 200, "")
		m := e.do(a, "GET", path+"/via-policy", nil).JSON(t)
		m["description"] = "now the policy writes"
		// The save sees the entry's deploy: the private issuer's mapping has no pin and no ref_protected.
		wantStatus(t, e.do(a, "PUT", path+"/via-policy", m, "If-Match", "*"), 422, "unprotected_write")
		m["match"] = all(eq("ref_protected", true))
		wantStatus(t, e.do(a, "PUT", path+"/via-policy", m, "If-Match", "*"), 422, "unpinned_write")
		m["match"] = all(eq("project_id", "1"), eq("ref_protected", true))
		wantStatus(t, e.do(a, "PUT", path+"/via-policy", m, "If-Match", "*"), 200, "")
	})
}
