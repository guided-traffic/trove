package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/packages/view"
	"github.com/guided-traffic/trove/internal/project"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln/report"
)

// memberProjects is a project service with recording member operations: the real rules (the last Owner, the
// audit event, the epoch) are the project package's and tested there.
type memberProjects struct {
	fakeProjects
	got *memberCall
}

type memberCall struct {
	op     string
	key    string
	ref    string
	role   authz.ProjectRole
	bypass bool
	exists bool
}

func (m memberProjects) Members(_ context.Context, key string) ([]project.Member, error) {
	p, err := m.Get(context.Background(), key)
	return p.Members, err
}

func (m memberProjects) AddMember(_ context.Context, _ authz.Subject, key, ref string, role project.Role, bypass bool) (project.Member, bool, int64, error) {
	exists := m.got.exists
	*m.got = memberCall{op: "add", key: key, ref: ref, role: role, bypass: bypass, exists: exists}
	if strings.HasSuffix(ref, "demoted") {
		return project.Member{}, false, 0, errs.E(errs.Conflict, "last_owner", "The last Owner cannot be demoted.")
	}
	kind, name, _ := strings.Cut(ref, ":")
	return project.Member{Project: key, Principal: project.PrincipalRef{Kind: project.PrincipalKind(kind), Name: name}, Role: role}, !m.got.exists, 41, nil
}

func (m memberProjects) RemoveMember(_ context.Context, _ authz.Subject, key, ref string, bypass bool) (int64, error) {
	*m.got = memberCall{op: "remove", key: key, ref: ref, bypass: bypass}
	if ref == "user:ghost" {
		return 0, errs.E(errs.NotFound, errs.CodeNotFound, "The principal is no member.")
	}
	return 42, nil
}

func memberSetup(t *testing.T, c projectCaller) (setup, *memberCall) {
	t.Helper()
	got := &memberCall{}
	return newSetup(t, func(o *Options) {
		o.Access = c
		o.Authz.Projects = memberProjects{fakeProjects: sampleProjects(), got: got}
		o.API.Config = apiConfigForTest()
		o.API.Config.MaxRequestBodyBytes = config.MiB
	}, authed), got
}

func TestR_AUTHZ_026_TheMembersOfAProject(t *testing.T) {
	reader := projectCaller{members: map[string]authz.ProjectRole{"web": authz.ProjectRoleReader}}
	t.Run("R-AUTHZ-026 a member reads the members of its project", func(t *testing.T) {
		s, _ := memberSetup(t, reader)
		w := s.get("/api/v1/projects/web/members")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"principal":"group:web-leads"`) || !strings.Contains(w.Body.String(), `"role":"owner"`) {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	})
	t.Run("R-AUTHZ-026 the holder of projects:read reads the members of any project", func(t *testing.T) {
		s, _ := memberSetup(t, projectCaller{caps: map[string]bool{"projects:read": true}})
		if w := s.get("/api/v1/projects/docs/members"); w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	})
	t.Run("R-AUTHZ-016 a project the caller is no member of answers like one that does not exist", func(t *testing.T) {
		s, _ := memberSetup(t, reader)
		existing := s.get("/api/v1/projects/docs/members")
		missing := s.get("/api/v1/projects/nothing/members")
		if existing.Code != 404 || missing.Code != 404 {
			t.Fatalf("existing %d, missing %d", existing.Code, missing.Code)
		}
		var a, b map[string]any
		_ = json.Unmarshal(existing.Body.Bytes(), &a)
		_ = json.Unmarshal(missing.Body.Bytes(), &b)
		if a["code"] != b["code"] || a["title"] != b["title"] || a["status"] != b["status"] {
			t.Errorf("the two answers differ: %v / %v", a, b)
		}
	})
}

func TestR_AUTHZ_026_SetAndRemoveAMember(t *testing.T) {
	owner := projectCaller{members: map[string]authz.ProjectRole{"web": authz.ProjectRoleOwner}, owns: map[string]bool{"web": true}}
	put := func(s setup, path, body string) *httptest.ResponseRecorder { return s.do("PUT", path, body, nil) }

	t.Run("R-AUTHZ-026 an Owner adds a member (201) and sets its role (200), with the epoch", func(t *testing.T) {
		s, got := memberSetup(t, owner)
		w := put(s, "/api/v1/projects/web/members/user:alice", `{"role":"developer"}`)
		if w.Code != http.StatusCreated || w.Header().Get(authz.HeaderEpoch) != "41" {
			t.Fatalf("%d %v %s", w.Code, w.Header(), w.Body)
		}
		if got.op != "add" || got.key != "web" || got.ref != "user:alice" || got.role != authz.ProjectRoleDeveloper || got.bypass {
			t.Errorf("call = %+v", *got)
		}
		got.exists = true
		if w := put(s, "/api/v1/projects/web/members/user:alice", `{"role":"maintainer"}`); w.Code != http.StatusOK {
			t.Errorf("an existing member: %d %s", w.Code, w.Body)
		}
	})
	t.Run("R-AUTHZ-026 the percent-encoded reference of a group reaches the service decoded", func(t *testing.T) {
		s, got := memberSetup(t, owner)
		if w := put(s, "/api/v1/projects/web/members/group%3Aweb-leads", `{"role":"owner"}`); w.Code != 201 || got.ref != "group:web-leads" {
			t.Errorf("%d ref %q", w.Code, got.ref)
		}
	})
	t.Run("Q-AUTHZ-208 a role outside the four is 422", func(t *testing.T) {
		s, got := memberSetup(t, owner)
		w := put(s, "/api/v1/projects/web/members/user:alice", `{"role":"admin"}`)
		if w.Code != 422 || got.op != "" {
			t.Errorf("%d %s (the service was asked: %q)", w.Code, w.Body, got.op)
		}
	})
	t.Run("Q-AUTHZ-217 the holder of projects:write may pass the last-Owner rule, an Owner may not", func(t *testing.T) {
		s, got := memberSetup(t, projectCaller{caps: map[string]bool{"projects:write": true}})
		if w := put(s, "/api/v1/projects/web/members/user:alice", `{"role":"owner"}`); w.Code != 201 || !got.bypass {
			t.Errorf("administrator: %d bypass %v", w.Code, got.bypass)
		}
		s, got = memberSetup(t, owner)
		w := put(s, "/api/v1/projects/web/members/user:demoted", `{"role":"reader"}`)
		problem(t, w, http.StatusConflict, "last_owner")
		if got.bypass {
			t.Error("an Owner passed the last-Owner rule")
		}
	})
	t.Run("R-AUTHZ-026 a member below Owner gets 403, a caller that is no member 404", func(t *testing.T) {
		s, got := memberSetup(t, projectCaller{members: map[string]authz.ProjectRole{"web": authz.ProjectRoleMaintainer}})
		if w := put(s, "/api/v1/projects/web/members/user:alice", `{"role":"reader"}`); w.Code != 403 {
			t.Errorf("maintainer: %d", w.Code)
		}
		if w := put(s, "/api/v1/projects/docs/members/user:alice", `{"role":"reader"}`); w.Code != 404 {
			t.Errorf("no member: %d", w.Code)
		}
		if got.op != "" {
			t.Errorf("the service was asked: %q", got.op)
		}
	})
	t.Run("R-AUTHZ-026 the removal answers 204 with the epoch, and 404 for a principal that is no member", func(t *testing.T) {
		s, got := memberSetup(t, owner)
		w := s.do("DELETE", "/api/v1/projects/web/members/user:alice", "", nil)
		if w.Code != http.StatusNoContent || w.Header().Get(authz.HeaderEpoch) != "42" || got.op != "remove" || got.ref != "user:alice" {
			t.Errorf("%d %v call %+v", w.Code, w.Header(), *got)
		}
		problem(t, s.do("DELETE", "/api/v1/projects/web/members/user:ghost", "", nil), http.StatusNotFound, "not_found")
	})
}

// TestR_REPO_060_TheTargetOfAMoveIsDecidedByTheCaller: the declaration admits the holder of
// repositories:write-config and an Owner of the source; the handler asks the second for the target.
func TestR_REPO_060_TheTargetOfAMoveIsDecidedByTheCaller(t *testing.T) {
	check := func(c projectCaller, target string) error {
		s := New(Options{Access: c})
		r := httptest.NewRequest("POST", "/api/v1/projects/web/repositories/npm-libs/move", nil)
		return s.checkMoveTarget(r, target)
	}
	if err := check(projectCaller{caps: map[string]bool{"repositories:write-config": true}}, "mobile"); err != nil {
		t.Errorf("the holder of the capability: %v", err)
	}
	if err := check(projectCaller{members: map[string]authz.ProjectRole{"mobile": authz.ProjectRoleOwner}, owns: map[string]bool{"mobile": true}}, "mobile"); err != nil {
		t.Errorf("an Owner of the target: %v", err)
	}
	kind, _ := codeOf(t, check(projectCaller{members: map[string]authz.ProjectRole{"mobile": authz.ProjectRoleDeveloper}}, "mobile"))
	if kind != errs.Forbidden {
		t.Errorf("a developer of the target: %v, want forbidden", kind)
	}
	kind, _ = codeOf(t, check(projectCaller{members: map[string]authz.ProjectRole{"web": authz.ProjectRoleOwner}, owns: map[string]bool{"web": true}}, "mobile"))
	if kind != errs.NotFound {
		t.Errorf("no member of the target: %v, want not found", kind)
	}
}

func TestR_REPO_060_TheMoveBodyNamesAProjectKey(t *testing.T) {
	s := newSetup(t, func(o *Options) {
		o.Access = projectCaller{caps: map[string]bool{"repositories:write-config": true}}
		o.Authz.Projects = sampleProjects()
		// The handler refuses the body before it asks the service for anything: an empty service stands in.
		o.Repositories.Service = &repository.Service{}
		o.API.Config.MaxRequestBodyBytes = 1 << 20
	}, authed)
	w := s.do("POST", "/api/v1/projects/web/repositories/npm-libs/move", `{"project":"Not A Key"}`, nil)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "/project") {
		t.Errorf("the problem does not point at /project: %s", w.Body)
	}
}

func TestR_AUTHZ_026_TheValidationOfAProjectPolicyIsDecidedByTheProject(t *testing.T) {
	web := "web"
	check := func(c projectCaller, project *string) error {
		s := New(Options{Access: c})
		return s.mayValidate(httptest.NewRequest("POST", "/api/v1/authz/validate-policy", nil), project)
	}
	if err := check(projectCaller{caps: map[string]bool{"policies:read": true}}, nil); err != nil {
		t.Errorf("policies:read, no project: %v", err)
	}
	if err := check(projectCaller{owns: map[string]bool{"web": true}, members: map[string]authz.ProjectRole{"web": authz.ProjectRoleOwner}}, &web); err != nil {
		t.Errorf("an Owner of the named project: %v", err)
	}
	kind, _ := codeOf(t, check(projectCaller{members: map[string]authz.ProjectRole{"web": authz.ProjectRoleDeveloper}}, &web))
	if kind != errs.Forbidden {
		t.Errorf("a developer of the project: %v", kind)
	}
	kind, _ = codeOf(t, check(projectCaller{members: map[string]authz.ProjectRole{"docs": authz.ProjectRoleOwner}, owns: map[string]bool{"docs": true}}, &web))
	if kind != errs.NotFound {
		t.Errorf("an Owner of another project: %v, want not found", kind)
	}
	kind, _ = codeOf(t, check(projectCaller{owns: map[string]bool{"web": true}}, nil))
	if kind != errs.Forbidden {
		t.Errorf("an Owner without a project in the body: %v, want forbidden", kind)
	}
}

func TestR_AUTHZ_026_AProjectPolicyIsNamedWithinItsProject(t *testing.T) {
	in := authz.Policy{PolicyInput: authz.PolicyInput{Name: "ci"}}
	stored := storedPolicy(in, "web")
	if stored.Name != "web/ci" || stored.Project == nil || *stored.Project != "web" {
		t.Fatalf("stored = %+v", stored.PolicyInput)
	}
	if got := viewPolicy(stored); got.Name != "ci" {
		t.Errorf("view name = %q", got.Name)
	}
	if got := viewPolicy(authz.Policy{PolicyInput: authz.PolicyInput{Name: "global"}}); got.Name != "global" {
		t.Errorf("a policy of no project keeps its name: %q", got.Name)
	}
	other := "docs"
	if err := projectPolicyBody(authz.Policy{PolicyInput: authz.PolicyInput{Project: &other}}, "web"); err == nil {
		t.Error("a body naming another project was accepted")
	}
	if err := projectPolicyBody(in, "web"); err != nil {
		t.Errorf("a body without a project: %v", err)
	}
}

func TestR_AUTHZ_026_ProjectPolicyRoutesAreDeclaredForOwners(t *testing.T) {
	// A project the caller is no member of is hidden before the policy store is asked, and a replica without the
	// store answers 503 to a caller that may reach the route.
	nonMember := projectCaller{members: map[string]authz.ProjectRole{"docs": authz.ProjectRoleOwner}, owns: map[string]bool{"docs": true}}
	s := newSetup(t, func(o *Options) { o.Access = nonMember; o.Authz.Projects = sampleProjects() }, authed)
	for _, path := range []string{"/api/v1/projects/web/policies", "/api/v1/projects/web/policies/ci"} {
		if w := s.get(path); w.Code != 404 {
			t.Errorf("GET %s: %d", path, w.Code)
		}
	}
	owner := projectCaller{members: map[string]authz.ProjectRole{"web": authz.ProjectRoleOwner}, owns: map[string]bool{"web": true}}
	s = newSetup(t, func(o *Options) { o.Access = owner; o.Authz.Projects = sampleProjects() }, authed)
	if w := s.get("/api/v1/projects/web/policies"); w.Code != 503 {
		t.Errorf("an Owner on a replica without the policy store: %d", w.Code)
	}
}

// TestR_VULN_020_TheProjectFilterOfTheFindings: project stands for the physical repositories of the project, a
// bare repository key is 400, and both filters intersect.
func TestR_VULN_020_TheProjectFilterOfTheFindings(t *testing.T) {
	repos := []repository.Repository{
		{Name: qname.Repo{Project: "web", Key: "npm-local"}, Class: repository.Local, Format: "npm"},
		{Name: qname.Repo{Project: "web", Key: "npm-remote"}, Class: repository.Remote, Format: "npm"},
		{Name: qname.Repo{Project: "docs", Key: "npm-local"}, Class: repository.Local, Format: "npm"},
		{Name: qname.Repo{Project: "web", Key: "npm-all"}, Class: repository.Virtual, Format: "npm"},
	}
	sn := &report.Snapshot{Repos: repos, Topo: view.NewTopology(repos)}
	list := func(query string) ([]string, bool, error) {
		r := httptest.NewRequest("GET", "/api/v1/vulnerabilities/findings?"+query, nil)
		lq, err := ParseList(r, findingListSpec(), apiConfigForTest())
		if err != nil {
			t.Fatal(err)
		}
		return findingRepoNames(lq, sn, report.Caller{})
	}
	got, has, err := list("project=web")
	if err != nil || !has || strings.Join(sortedCopy(got), ",") != "web/npm-local,web/npm-remote" {
		t.Errorf("project=web: %v %v %v", got, has, err)
	}
	if got, has, err = list("project=nothing"); err != nil || !has || len(got) != 0 {
		t.Errorf("a project without repositories gives an empty set, not an error: %v %v %v", got, has, err)
	}
	got, has, err = list("project=web&repository=docs/npm-local")
	if err != nil || !has || len(got) != 0 {
		t.Errorf("the two filters intersect: %v %v %v", got, has, err)
	}
	if got, has, err = list(""); err != nil || has || got != nil {
		t.Errorf("no filter: %v %v %v", got, has, err)
	}
	if _, _, err = list("repository=npm-local"); err == nil {
		t.Error("a bare repository key was accepted")
	}
	if _, _, err = list("project=Not+A+Key"); err == nil {
		t.Error("a project that is no key was accepted")
	}
	if projectOf("web/npm-local") != "web" {
		t.Error("projectOf")
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestR_AUTHZ_022_MeListsTheProjects(t *testing.T) {
	// The field is `projects`, the memberships with the role in force (R-AUTHZ-022, R-AUTHZ-026).
	out := authzMe{Memberships: []authz.Membership{{Key: "web", Role: authz.ProjectRoleOwner}}}
	body, err := json.Marshal(out)
	if err != nil || !strings.Contains(string(body), `"projects":[{"key":"web","role":"owner"}]`) {
		t.Errorf("%s %v", body, err)
	}
}
