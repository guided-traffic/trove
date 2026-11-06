//go:build integration

package ci

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The helpers of the mapping tests (work package W3): issuers without a network, the bodies of mappings,
// the reads of the audit events and the answers.

// issuerBody is the create request of a CI issuer that fetches nothing: the tests of the mappings need the
// row, not the keys.
func issuerBody(name string, public bool) map[string]any {
	return map[string]any{
		"name": name, "kind": "gitlab", "issuer": "https://" + name + ".example.com", "publicIssuer": public, "validate": false,
	}
}

func (e *env) issuer(t *testing.T, r *testenv.Replica, name string, public bool) {
	t.Helper()
	wantStatus(t, e.do(r, "POST", issuersPath, issuerBody(name, public)), 201, "")
}

func mappingsOf(issuer string) string { return issuersPath + "/" + issuer + "/mappings" }

// addOwner makes the user an Owner of the project (the row of project_member, Q-AUTHZ-208) and announces the change the
// way the commit of the members route does. It returns the authz epoch every replica must have reached before the
// owner's request (the header Trove-Authz-Min-Epoch). The membership itself is the members route's to test
// (test/integration/projects); here it is the starting point of what an Owner does with CI mappings.
func (e *env) addOwner(project, userID string) string { return e.addMember(project, userID, "owner") }

// addMember is addOwner for any role of Q-AUTHZ-208.
func (e *env) addMember(project, userID, role string) string {
	e.t.Helper()
	e.exec(`INSERT INTO project_member (project_id, principal_id, role) SELECT id, $2::uuid, $3 FROM project WHERE key = $1`, project, userID, role)
	e.bump("authz", nil)
	return strconv.FormatInt(e.epoch("authz"), 10)
}

func projectMappings(project string) string { return "/api/v1/projects/" + project + "/ci/mappings" }

// eq is the leaf {claim, equals}.
func eq(claim string, value any) map[string]any {
	return map[string]any{"claim": claim, "equals": value}
}

func all(leaves ...map[string]any) map[string]any {
	list := make([]any, 0, len(leaves))
	for _, l := range leaves {
		list = append(list, l)
	}
	return map[string]any{"all": list}
}

// inline is one inline grant over a repository with package patterns.
func inline(repo string, include []string, actions ...string) map[string]any {
	return map[string]any{
		"repositories": []string{repo}, "packages": map[string]any{"include": include}, "actions": actions,
	}
}

func grants(inl ...map[string]any) map[string]any {
	list := make([]any, 0, len(inl))
	for _, g := range inl {
		list = append(list, g)
	}
	return map[string]any{"inline": list}
}

// mapping is the request body of a mapping.
func mapping(name string, priority int, match, g map[string]any, extra ...map[string]any) map[string]any {
	b := map[string]any{"name": name, "priority": priority, "match": match, "grants": g}
	for _, m := range extra {
		for k, v := range m {
			b[k] = v
		}
	}
	return b
}

// readOnlyMapping is a mapping that grants read over a repository: no write rule applies to it.
func readOnlyMapping(name string, priority int, match map[string]any, repo string) map[string]any {
	return mapping(name, priority, match, grants(inline(repo, []string{"*"}, "read")))
}

// publishMapping grants read and deploy on a repository to the protected pipelines of a project.
func publishMapping(name string, priority int, repo string) map[string]any {
	return mapping(name, priority, all(eq("project_id", "4242"), eq("ref_protected", true)),
		grants(inline(repo, []string{"@acme/*"}, "read", "deploy")))
}

// warningCodes lists the codes of the warnings of a save.
func warningCodes(t *testing.T, a testenv.Answer) []string {
	t.Helper()
	var out []string
	list, _ := a.JSON(t)["warnings"].([]any)
	for _, w := range list {
		out = append(out, w.(map[string]any)["code"].(string))
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// names lists the member `name` of the items of a list answer.
func names(t *testing.T, a testenv.Answer) []string {
	t.Helper()
	var out []string
	items, _ := a.JSON(t)["items"].([]any)
	for _, i := range items {
		out = append(out, i.(map[string]any)["name"].(string))
	}
	return out
}

// strs reads a JSON array of strings out of a decoded body.
func strs(v any) []string {
	var out []string
	list, _ := v.([]any)
	for _, x := range list {
		out = append(out, x.(string))
	}
	return out
}

// eventDetails returns the details of the newest event of an action.
func (e *env) eventDetails(action string) map[string]any {
	e.t.Helper()
	raw := e.text("SELECT coalesce((SELECT details::text FROM audit_event WHERE action = $1 ORDER BY occurred_at DESC, id DESC LIMIT 1), '')", action)
	if raw == "" {
		e.t.Fatalf("no event %s", action)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		e.t.Fatal(err)
	}
	return m
}

// journal counts the rows of the revocation journal of an event type.
func (e *env) journal(event string) int64 {
	e.t.Helper()
	return e.number("SELECT count(*) FROM revocation_outbox WHERE event = $1", event)
}

// seedGroup creates a group with a member and gives it actions on a repository through a policy of its own.
func (e *env) seedGroup(group, policy, repo string, actions ...string) {
	e.t.Helper()
	e.stack.User("member-of-"+group, group)
	e.stack.Policy(policy, nil, []string{repo})
	e.exec(`INSERT INTO authz_policy_entry (policy_id, principal_id, actions)
	        SELECT pol.id, g.id, $3::text[] FROM authz_policy pol, principal g WHERE pol.name = $1 AND g.kind = 'group' AND g.name = $2`,
		policy, group, actions)
}

// seedMappingIssuance writes the record of an exchange through a mapping (R-CI-020).
func (e *env) seedMappingIssuance(issuer, mapping string) {
	e.t.Helper()
	e.exec(`INSERT INTO ci_token_issuances (token_id, issuer_id, mapping_ids, mapping_names, subject_kind, project_id, job_id, claims, grants,
	                                        granted_scope, issued_at, expires_at)
	        SELECT gen_random_uuid(), i.id, ARRAY[m.id], ARRAY[m.name], 'id_token', '7', '9', '{}'::jsonb, '{}'::jsonb, 'read', now(), now() + interval '1 hour'
	          FROM ci_issuers i JOIN ci_mappings m ON m.issuer_id = i.id WHERE i.name = $1 AND m.name = $2`, issuer, mapping)
}
