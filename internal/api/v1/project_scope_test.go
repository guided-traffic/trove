package v1

import (
	"context"
	"errors"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
)

// projectCaller is an Access of a caller that holds the listed capabilities and
// administers the listed projects; repositories maps a repository key to the
// project it belongs to.
type projectCaller struct {
	caps         map[string]bool
	projects     []authz.AdministeredProject
	repositories map[string]string
}

func (c projectCaller) Require(_ context.Context, permission string) error {
	if c.caps[permission] {
		return nil
	}
	return errs.E(errs.Forbidden, errs.CodeForbidden, "The operation is not permitted.")
}

func (c projectCaller) Holds(_ context.Context, permission string) (bool, error) {
	return c.caps[permission], nil
}

func (c projectCaller) AdministeredProjects(context.Context) ([]authz.AdministeredProject, error) {
	return c.projects, nil
}

func (c projectCaller) AdministersRepository(_ context.Context, repository string, right authz.ProjectRight) (string, bool, error) {
	key := c.repositories[repository]
	for _, p := range c.projects {
		if p.Key == key && p.Holds(right) {
			return key, true, nil
		}
	}
	return "", false, nil
}

func projectServer(c projectCaller) *Server { return &Server{o: Options{Access: c}} }

func codeOf(t *testing.T, err error) (errs.Kind, string) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("not a problem error: %v", err)
	}
	return e.Kind, e.Code
}

func TestR_AUTHZ_026_TheDeclarationOfAProjectAdministrator(t *testing.T) {
	t.Run("R-AUTHZ-026 parseRequirement reads permission with projectAdmin and refuses another right", func(t *testing.T) {
		q, err := parseRequirement(map[string]any{"permission": "policies:write", "projectAdmin": "managePolicies"})
		if err != nil || q.Permission != "policies:write" || q.ProjectAdmin != authz.ProjectManagePolicies {
			t.Fatalf("%+v %v", q, err)
		}
		if _, err := parseRequirement(map[string]any{"permission": "policies:write", "projectAdmin": "everything"}); err == nil {
			t.Error("an unknown right was accepted")
		}
		if _, err := parseRequirement(map[string]any{"role": "admin", "projectAdmin": "any"}); err == nil {
			t.Error("projectAdmin next to a role was accepted")
		}
	})

	call := func(c projectCaller) int {
		s := newSetup(t, func(o *Options) { o.Access = c }, authed)
		return s.get("/api/v1/projects").Code
	}
	t.Run("R-AUTHZ-026 the shared step admits the capability and an administrator of a project, and refuses the rest", func(t *testing.T) {
		// The project service is not wired in this setup: an admitted caller reaches the handler, which answers 503.
		if got := call(projectCaller{caps: map[string]bool{"projects:read": true}}); got != 503 {
			t.Errorf("capability: %d", got)
		}
		if got := call(projectCaller{projects: []authz.AdministeredProject{{Key: "web"}}}); got != 503 {
			t.Errorf("project administrator: %d", got)
		}
		if got := call(projectCaller{}); got != 403 {
			t.Errorf("neither: %d", got)
		}
	})
	t.Run("R-AUTHZ-026 the right of the declaration is the switch the administrator needs", func(t *testing.T) {
		lookup := func(c projectCaller) int {
			s := newSetup(t, func(o *Options) { o.Access = c }, authed)
			return s.get("/api/v1/projects/web/principals?q=al").Code
		}
		if got := lookup(projectCaller{projects: []authz.AdministeredProject{{Key: "web", ManagePolicies: true}}}); got != 503 {
			t.Errorf("switch on: %d", got)
		}
		if got := lookup(projectCaller{projects: []authz.AdministeredProject{{Key: "web", ManageRepositories: true}}}); got != 403 {
			t.Errorf("switch off: %d", got)
		}
	})
	t.Run("R-AUTHZ-026 an anonymous caller gets 401", func(t *testing.T) {
		s := newSetup(t, func(o *Options) { o.Access = projectCaller{} }, nil)
		if got := s.get("/api/v1/projects").Code; got != 401 && got != 403 {
			t.Errorf("anonymous: %d", got)
		}
	})
}

func TestR_AUTHZ_026_TheHandlersNarrowToTheProject(t *testing.T) {
	web := projectCaller{
		projects:     []authz.AdministeredProject{{Key: "web", ManagePolicies: true}},
		repositories: map[string]string{"web-npm": "web", "npm-internal": ""},
	}
	t.Run("R-AUTHZ-026 requireProject lets the capability and the administered project pass", func(t *testing.T) {
		ctx := context.Background()
		if err := projectServer(web).requireProject(ctx, "policies:write", "web", authz.ProjectManagePolicies); err != nil {
			t.Errorf("administered project: %v", err)
		}
		holder := projectCaller{caps: map[string]bool{"policies:write": true}}
		if err := projectServer(holder).requireProject(ctx, "policies:write", "docs", authz.ProjectManagePolicies); err != nil {
			t.Errorf("capability: %v", err)
		}
	})
	t.Run("R-AUTHZ-026 another project answers 404, a switch that is off 403 project_switch_off", func(t *testing.T) {
		ctx := context.Background()
		kind, _ := codeOf(t, projectServer(web).requireProject(ctx, "policies:write", "docs", authz.ProjectManagePolicies))
		if kind != errs.NotFound {
			t.Errorf("another project: %v", kind)
		}
		kind, code := codeOf(t, projectServer(web).requireProject(ctx, "repositories:write-config", "web", authz.ProjectManageRepositories))
		if kind != errs.Forbidden || code != errs.CodeProjectSwitchOff {
			t.Errorf("switch off: %v %s", kind, code)
		}
		if err := refuseProject(DelegatedScope{Projects: []authz.AdministeredProject{{Key: "web"}}}, "web", authz.ProjectAny); err != nil {
			t.Errorf("an administrator holds the right any: %v", err)
		}
	})
	t.Run("R-AUTHZ-026 delegatedScope says what the caller reaches", func(t *testing.T) {
		d, err := projectServer(web).delegatedScope(context.Background(), "policies:read")
		if err != nil || d.Global || !d.Sees("web") || d.Sees("docs") || !d.May("web", authz.ProjectManagePolicies) || d.May("web", authz.ProjectManageRepositories) {
			t.Errorf("%+v %v", d, err)
		}
		d, err = projectServer(projectCaller{caps: map[string]bool{"policies:read": true}}).delegatedScope(context.Background(), "policies:read")
		if err != nil || !d.Global || !d.May("anything", authz.ProjectManageRepositories) {
			t.Errorf("%+v %v", d, err)
		}
	})
	t.Run("R-AUTHZ-026 requireProjectOfRepository names the project, and a repository outside answers 404", func(t *testing.T) {
		ctx := context.Background()
		s := projectServer(projectCaller{
			projects:     []authz.AdministeredProject{{Key: "web", ManageRepositories: true}},
			repositories: web.repositories,
		})
		project, err := s.requireProjectOfRepository(ctx, "repositories:write-config", "web-npm", authz.ProjectManageRepositories)
		if err != nil || project != "web" {
			t.Fatalf("%q %v", project, err)
		}
		kind, _ := codeOf(t, errOf(s.requireProjectOfRepository(ctx, "repositories:write-config", "npm-internal", authz.ProjectManageRepositories)))
		if kind != errs.NotFound {
			t.Errorf("repository outside: %v", kind)
		}
		// Administered with the switch off: 403 with the reason.
		kind, code := codeOf(t, errOf(projectServer(web).requireProjectOfRepository(ctx, "repositories:write-config", "web-npm", authz.ProjectManageRepositories)))
		if kind != errs.Forbidden || code != errs.CodeProjectSwitchOff {
			t.Errorf("switch off: %v %s", kind, code)
		}
		// A holder of the capability needs no project.
		holder := projectServer(projectCaller{caps: map[string]bool{"repositories:write-config": true}})
		if project, err := holder.requireProjectOfRepository(ctx, "repositories:write-config", "npm-internal", authz.ProjectManageRepositories); err != nil || project != "" {
			t.Errorf("capability: %q %v", project, err)
		}
	})
}

// errOf drops the first result of a call, for the helpers that return a value and an error.
func errOf(_ string, err error) error { return err }

func TestR_AUTHZ_026_TheProjectOperationsAnswer503WithoutAService(t *testing.T) {
	s := newSetup(t, func(o *Options) { o.Access = allowAll{} }, authed)
	for _, path := range []string{"/api/v1/projects", "/api/v1/projects/web"} {
		if w := s.get(path); w.Code != 503 {
			t.Errorf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
	}
}
