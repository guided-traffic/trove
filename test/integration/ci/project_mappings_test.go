//go:build integration

package ci

import (
	"fmt"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The CI mappings of a project (Q-CI-26, R-CI-019, R-AUTHZ-026): the tier, the bounds, the narrowing of the
// project routes and the delete check of the project.

// projectEnv is the installation of the project tests: the issuer gitlab-main, the projects web (bob is an
// Owner of it) and data, the repositories web-npm and web-nuget of web, other-npm of data, a global policy and a
// policy of web.
type projectEnv struct {
	*env
	a, b  *testenv.Replica
	epoch string // the authz epoch after bob became an administrator: the replicas' index is at least this fresh
}

func newProjectEnv(t *testing.T) *projectEnv {
	t.Helper()
	e := newEnv(t, options{valkey: true})
	p := &projectEnv{env: e, a: e.web("web-a"), b: e.web("web-b")}
	p.issuer(t, p.a, "gitlab-main", false)
	wantStatus(t, e.do(p.a, "POST", "/api/v1/projects", map[string]any{"key": "web", "displayName": "Web"}), 201, "")
	wantStatus(t, e.do(p.a, "POST", "/api/v1/projects", map[string]any{"key": "data", "displayName": "Data"}), 201, "")
	for _, repo := range []string{"web/web-npm", "web/web-nuget", "data/other-npm"} {
		format := "npm"
		if strings.HasSuffix(repo, "nuget") {
			format = "nuget"
		}
		e.stack.Repository(repo, "local", format, "{}")
	}
	e.stack.Policy("ci-read-global", nil, []string{"data/other-npm"})
	e.exec("INSERT INTO authz_policy (id, name, project_id) SELECT gen_random_uuid(), key || '/ci-read', id FROM project WHERE key = 'web'")
	p.epoch = e.addOwner("web", e.bobID)
	return p
}

// bob sends a request as the Owner of the project to either replica.
func (p *projectEnv) bob(r *testenv.Replica, method, path string, body any, hdr ...string) testenv.Answer {
	p.t.Helper()
	return p.as(r, p.bobPAT, method, path, body, append([]string{"Trove-Authz-Min-Epoch", p.epoch}, hdr...)...)
}

// webPublish is the mapping of the project web that publishes to its repositories.
func webPublish(name string, priority int) map[string]any {
	return mapping(name, priority, all(eq("project_id", "4242"), eq("ref_protected", true)),
		grants(inline("web-npm", []string{"@acme/*"}, "read", "deploy")), map[string]any{"issuer": "gitlab-main"})
}

func TestQ_CI_26_AProjectOwnerManagesTheMappingsOfItsProject(t *testing.T) {
	p := newProjectEnv(t)
	a, b := p.a, p.b
	path := projectMappings("web")

	r := p.bob(a, "POST", path, webPublish("web-publish", 100))
	wantStatus(t, r, 201, "")
	if loc := r.Header.Get("Location"); !strings.HasSuffix(loc, path+"/gitlab-main/web-publish") {
		t.Errorf("Location = %q", loc)
	}
	got := r.JSON(t)
	if got["tier"] != "project:web" || got["project"] != "web" || got["issuer"] != "gitlab-main" {
		t.Errorf("created mapping = %v", got)
	}
	if p.number("SELECT count(*) FROM ci_mappings m JOIN project pr ON pr.id = m.project_id WHERE pr.key = 'web' AND m.tier = 'project:web'") != 1 {
		t.Error("the mapping row names no project")
	}
	if p.number("SELECT count(*) FROM principal WHERE kind = 'ci_mapping' AND name = 'gitlab-main/web/web-publish'") != 1 {
		t.Error("no principal gitlab-main/web/web-publish (<issuer>/<project>/<mapping>, Q-AUTHZ-214)")
	}
	if p.number("SELECT count(*) FROM ci_mappings WHERE name = 'web-publish'") != 1 {
		t.Error("the mapping row holds the bare name")
	}
	if d := p.eventDetails("ci.mapping.created"); d["project"] != "web" || d["tier"] != "project:web" {
		t.Errorf("the event carries project %v, tier %v", d["project"], d["tier"])
	}

	t.Run("Q-CI-26 the bounds of a project mapping", func(t *testing.T) {
		pinned := all(eq("project_id", "4242"))
		for name, tc := range map[string]struct {
			body  map[string]any
			code  string
			field string
		}{
			"a qualified selector names another project, a project's mapping names bare keys": {
				mapping("web-qualified", 200, pinned, grants(inline("web/web-npm", []string{"*"}, "read")), map[string]any{"issuer": "gitlab-main"}),
				"", "/grants/inline/0/repositories/0 invalid_pattern",
			},
			"a pattern of projects is no pattern of a project": {
				mapping("web-allprojects", 200, pinned, grants(inline("*/web-*", []string{"*"}, "read")), map[string]any{"issuer": "gitlab-main"}),
				"", "/grants/inline/0/repositories/0 invalid_pattern",
			},
			"groups are global grants": {
				mapping("web-groups", 200, pinned, map[string]any{"groups": []string{"ci-publishers"}}, map[string]any{"issuer": "gitlab-main"}),
				"groups_not_allowed_in_project", "/grants/groups groups_not_allowed_in_project",
			},
			"a global policy is not the project's": {
				mapping("web-global", 200, pinned, map[string]any{"policies": []string{"ci-read-global"}}, map[string]any{"issuer": "gitlab-main"}),
				"policy_outside_project", "/grants/policies/0 policy_outside_project",
			},
			"a policy that does not exist answers the same, so a name of another project is no oracle": {
				mapping("web-ghostpol", 200, pinned, map[string]any{"policies": []string{"ghost-policy"}}, map[string]any{"issuer": "gitlab-main"}),
				"policy_outside_project", "/grants/policies/0 policy_outside_project",
			},
			"a repository of another project": {
				mapping("web-other", 200, pinned, grants(inline("other-npm", []string{"*"}, "read")), map[string]any{"issuer": "gitlab-main"}),
				"repository_outside_project", "/grants/inline/0/repositories/0 repository_outside_project",
			},
			"a repository that does not exist answers the same": {
				mapping("web-ghost", 200, pinned, grants(inline("ghost-npm", []string{"*"}, "read")), map[string]any{"issuer": "gitlab-main"}),
				"repository_outside_project", "/grants/inline/0/repositories/0 repository_outside_project",
			},
			"manage is never grantable": {
				mapping("web-manage", 200, pinned, grants(inline("web-npm", []string{"*"}, "manage")), map[string]any{"issuer": "gitlab-main"}),
				"action_not_allowed", "/grants/inline/0/actions/0 action_not_allowed",
			},
			"the issuer is named": {
				mapping("web-issuerless", 200, pinned, grants(inline("web-npm", []string{"*"}, "read"))),
				"", "/issuer required",
			},
		} {
			t.Run(name, func(t *testing.T) {
				r := p.bob(a, "POST", path, tc.body)
				wantStatus(t, r, 422, orValidation(tc.code))
				found := false
				for _, f := range problemFields(t, r) {
					found = found || f == tc.field
				}
				if !found {
					t.Errorf("errors[] = %v, want %q", problemFields(t, r), tc.field)
				}
			})
		}
		if p.number("SELECT count(*) FROM ci_mappings") != 1 {
			t.Error("a refused save stored a mapping")
		}
	})

	t.Run("Q-CI-26 @any and patterns mean the project's repositories, its own policies bind", func(t *testing.T) {
		body := mapping("web-reader", 300, all(eq("project_id", "4242")), map[string]any{
			"policies": []string{"ci-read"},
			"inline":   []any{map[string]any{"repositories": []string{"@any", "web-*"}, "packages": map[string]any{"include": []string{"*"}}, "actions": []string{"read"}}},
		}, map[string]any{"issuer": "gitlab-main"})
		wantStatus(t, p.bob(b, "POST", path, body), 201, "")
		if p.number(`SELECT count(*) FROM authz_project_policy_entry e JOIN authz_policy pol ON pol.id = e.policy_id JOIN principal pr ON pr.id = e.principal_id
		              WHERE pol.name = 'web/ci-read' AND pr.name = 'gitlab-main/web/web-reader'`) != 1 {
			t.Error("the project policy has no entry for the mapping")
		}
	})

	t.Run("Q-CI-26 tiers combine by union, so a priority is unique per tier and not per issuer", func(t *testing.T) {
		wantStatus(t, p.do(a, "POST", mappingsOf("gitlab-main"), readOnlyMapping("sys-read", 100, all(eq("project_id", "4242")), "data/other-npm")), 201, "")
		wantStatus(t, p.bob(a, "POST", path, webPublish("web-second", 100)), 409, "priority_taken")
		wantStatus(t, p.bob(a, "POST", path, webPublish("web-publish", 400)), 409, "already_exists")
		// A system mapping and the project's coexist; the dry run of an administrator unites both tiers.
		r := p.do(a, "POST", issuersPath+"/gitlab-main/evaluate", map[string]any{"claims": map[string]any{"project_id": "4242", "ref_protected": "true"}})
		wantStatus(t, r, 200, "")
		sel := strs(r.JSON(t)["selected"])
		if len(sel) != 2 || sel[0] != "sys-read" || sel[1] != "web/web-publish" {
			t.Errorf("selected = %v, want the system mapping and the project's: a token carries one match per tier", sel)
		}
	})

	t.Run("Q-CI-26 a project Owner sees only its tier", func(t *testing.T) {
		l := p.bob(b, "GET", path, nil)
		wantStatus(t, l, 200, "")
		if got := strings.Join(names(t, l), ","); got != "web-publish,web-reader" {
			t.Errorf("bob lists %s", got)
		}
		all := p.do(a, "GET", mappingsOf("gitlab-main"), nil)
		if got := strings.Join(names(t, all), ","); got != "sys-read,web-publish,web-reader" {
			t.Errorf("the administrator lists %s: the system tier first, then the projects'", got)
		}
		wantStatus(t, p.bob(a, "GET", path+"/gitlab-main/sys-read", nil), 404, "not_found")
		wantStatus(t, p.bob(a, "GET", path+"/gitlab-main/web-publish", nil), 200, "")
		wantStatus(t, p.bob(a, "GET", projectMappings("data"), nil), 404, "not_found")
		wantStatus(t, p.bob(a, "POST", projectMappings("data"), webPublish("data-publish", 1)), 404, "not_found")
		if r := p.as(a, p.alicePAT, "GET", path, nil); r.Status != 404 && r.Status != 403 {
			t.Errorf("a user who owns nothing got %d", r.Status)
		}
		for _, op := range []struct{ method, path string }{
			{"GET", mappingsOf("gitlab-main")},
			{"POST", mappingsOf("gitlab-main")},
			{"PUT", mappingsOf("gitlab-main") + "/web-publish"},
			{"DELETE", mappingsOf("gitlab-main") + "/web-publish"},
			{"POST", evaluatePath},
		} {
			if r := p.bob(a, op.method, op.path, map[string]any{}, "If-Match", "*"); r.Status != 403 {
				t.Errorf("%s %s answered %d to a project Owner", op.method, op.path, r.Status)
			}
		}
	})

	t.Run("Q-CI-26 the dry run of a project Owner traces its tier only", func(t *testing.T) {
		r := p.bob(a, "POST", "/api/v1/projects/web/ci/issuers/gitlab-main/evaluate", map[string]any{"claims": map[string]any{"project_id": "4242", "ref_protected": "true"}})
		wantStatus(t, r, 200, "")
		got := r.JSON(t)
		if sel := strs(got["selected"]); len(sel) != 1 || sel[0] != "web/web-publish" {
			t.Errorf("selected = %v", sel)
		}
		for _, e := range got["trace"].([]any) {
			if tier := e.(map[string]any)["tier"]; tier != "project:web" {
				t.Errorf("the trace shows the tier %v", tier)
			}
		}
		if strings.Contains(string(r.Body), "sys-read") {
			t.Error("the answer names a mapping of another tier")
		}
	})

	t.Run("Q-CI-26 a system administrator who edits a project's mapping keeps the bounds of its tier", func(t *testing.T) {
		// R-CI-009: a name is unique within a tier, so the issuer's routes (the system tier) do not address the project's mapping.
		wantStatus(t, p.do(a, "GET", mappingsOf("gitlab-main")+"/web-publish", nil), 404, "not_found")
		body := p.do(a, "GET", path+"/gitlab-main/web-publish", nil).JSON(t)
		body["grants"] = map[string]any{"groups": []string{"ci-publishers"}}
		wantStatus(t, p.do(a, "PUT", path+"/gitlab-main/web-publish", body, "If-Match", "*"), 422, "groups_not_allowed_in_project")
	})

	t.Run("Q-CI-26 replace and delete under the project route", func(t *testing.T) {
		body := p.bob(a, "GET", path+"/gitlab-main/web-publish", nil).JSON(t)
		body["description"] = "edited by the project Owner"
		wantStatus(t, p.bob(a, "PUT", path+"/gitlab-main/web-publish", body), 428, "precondition_required")
		put := p.bob(b, "PUT", path+"/gitlab-main/web-publish", body, "If-Match", "*")
		wantStatus(t, put, 200, "")
		if put.JSON(t)["tier"] != "project:web" {
			t.Errorf("the replacement moved the tier: %v", put.JSON(t)["tier"])
		}
		wantStatus(t, p.bob(a, "DELETE", path+"/gitlab-main/sys-read", nil), 404, "not_found")
		wantStatus(t, p.bob(a, "DELETE", path+"/gitlab-main/web-reader", nil), 204, "")
		if p.number("SELECT count(*) FROM authz_project_policy_entry") != 0 {
			t.Error("the policy entry of the deleted mapping remains")
		}
	})

	t.Run("Q-AUTHZ-208 only an Owner of the project reaches its CI routes, a lower role does not", func(t *testing.T) {
		p.epoch = p.addMember("web", p.aliceID, "maintainer")
		for _, op := range []struct{ method, path string }{{"GET", path}, {"POST", path}, {"GET", path + "/gitlab-main/web-publish"}} {
			r := p.as(a, p.alicePAT, op.method, op.path, webPublish("web-late", 500))
			if r.Status != 403 && r.Status != 404 {
				t.Errorf("%s %s answered %d to a maintainer", op.method, op.path, r.Status)
			}
		}
		// The holder of ci:read keeps its reach.
		wantStatus(t, p.do(a, "GET", path, nil), 200, "")
	})
}

func TestQ_CI_26_AProjectWithCIMappingsIsNotEmpty(t *testing.T) {
	p := newProjectEnv(t)
	a := p.a
	wantStatus(t, p.do(a, "POST", "/api/v1/projects", map[string]any{"key": "solo", "displayName": "Solo"}), 201, "")
	body := mapping("solo-reader", 10, all(eq("project_id", "9")), grants(inline("@any", []string{"*"}, "read")), map[string]any{"issuer": "gitlab-main"})
	wantStatus(t, p.do(a, "POST", projectMappings("solo"), body), 201, "")

	if n, err := a.App.CIMappings().CountIn(t.Context(), "solo"); err != nil || n != 1 {
		t.Errorf("CountIn = %d, %v, want 1", n, err)
	}
	r := p.do(a, "DELETE", "/api/v1/projects/solo", nil)
	wantStatus(t, r, 409, "project_not_empty")
	if !strings.Contains(fmt.Sprint(r.JSON(t)["detail"]), "1 CI mappings") {
		t.Errorf("detail = %v: it says the CI mappings keep the project", r.JSON(t)["detail"])
	}
	if p.number("SELECT count(*) FROM project WHERE key = 'solo'") != 1 {
		t.Error("the refused delete removed the project")
	}
	wantStatus(t, p.do(a, "DELETE", projectMappings("solo")+"/gitlab-main/solo-reader", nil), 204, "")
	wantStatus(t, p.do(a, "DELETE", "/api/v1/projects/solo", nil), 204, "")
	// A project that does not exist has no CI routes.
	wantStatus(t, p.do(a, "GET", projectMappings("solo"), nil), 404, "not_found")
	wantStatus(t, p.do(a, "POST", projectMappings("ghost"), body), 404, "not_found")
}

func TestQ_CI_26_ACreateAndTheDeleteOfAProjectSerialize(t *testing.T) {
	p := newProjectEnv(t)
	a := p.a
	wantStatus(t, p.do(a, "POST", "/api/v1/projects", map[string]any{"key": "race", "displayName": "Race"}), 201, "")
	body := mapping("race-reader", 10, all(eq("project_id", "9")), grants(inline("@any", []string{"*"}, "read")), map[string]any{"issuer": "gitlab-main"})
	done := make(chan testenv.Answer, 2)
	go func() { done <- p.do(a, "DELETE", "/api/v1/projects/race", nil) }()
	go func() { done <- p.do(p.b, "POST", projectMappings("race"), body) }()
	first, second := <-done, <-done
	// Either order is fine; what must never be left is a mapping of a project that is gone, or a 500.
	for _, r := range []testenv.Answer{first, second} {
		if r.Status >= 500 {
			t.Fatalf("answered %d: %s", r.Status, r.Body)
		}
	}
	projects := p.number("SELECT count(*) FROM project WHERE key = 'race'")
	mappings := p.number("SELECT count(*) FROM ci_mappings WHERE tier = 'project:race'")
	if projects == 0 && mappings != 0 {
		t.Errorf("the project is gone and %d mappings of its tier remain", mappings)
	}
}
