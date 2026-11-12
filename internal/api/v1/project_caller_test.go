package v1

import (
	"context"
	"errors"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
)

// projectCaller is an Access of a caller that holds the listed capabilities and is a member of the listed
// projects with the role; owns lists the projects it may administer (an Owner through a session).
type projectCaller struct {
	caps    map[string]bool
	members map[string]authz.ProjectRole
	owns    map[string]bool
	// anonymous: the caller sent no credential, so the engine answers Unauthenticated where a signed-in caller gets Forbidden.
	anonymous bool
}

func (c projectCaller) Require(_ context.Context, permission string) error {
	if c.caps[permission] {
		return nil
	}
	if c.anonymous {
		return errs.E(errs.Unauthenticated, errs.CodeUnauthenticated, "Authentication is required.")
	}
	return errs.E(errs.Forbidden, errs.CodeForbidden, "The operation is not permitted.")
}

func (c projectCaller) Holds(_ context.Context, permission string) (bool, error) {
	return c.caps[permission], nil
}

func (c projectCaller) Memberships(context.Context) ([]authz.Membership, error) {
	var out []authz.Membership
	for k, r := range c.members {
		out = append(out, authz.Membership{Key: k, Role: r})
	}
	return out, nil
}

func (c projectCaller) Owns(_ context.Context, project string) (bool, error) {
	return c.owns[project], nil
}

func codeOf(t *testing.T, err error) (errs.Kind, string) {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("not a problem error: %v", err)
	}
	return e.Kind, e.Code
}

func TestR_AUTHZ_026_TheDeclarationOfAProjectRole(t *testing.T) {
	t.Run("R-AUTHZ-026 parseRequirement reads permission with project and projectRole and refuses another role", func(t *testing.T) {
		q, err := parseRequirement(map[string]any{"permission": "identities:write", "project": "path.projectKey", "projectRole": "owner"})
		if err != nil || q.Permission != "identities:write" || q.ProjectRole != "owner" || q.Project != "path.projectKey" {
			t.Fatalf("%+v %v", q, err)
		}
		if q, err = parseRequirement(map[string]any{"permission": "projects:read", "projectRole": "reader"}); err != nil || q.ProjectRole != "reader" || q.Project != "" {
			t.Fatalf("%+v %v", q, err)
		}
		if _, err := parseRequirement(map[string]any{"permission": "policies:write", "projectRole": "everything"}); err == nil {
			t.Error("an unknown role was accepted")
		}
		if _, err := parseRequirement(map[string]any{"permission": "policies:write", "project": "projectKey", "projectRole": "owner"}); err == nil {
			t.Error("a project that is no value expression was accepted")
		}
		if _, err := parseRequirement(map[string]any{"role": "admin", "projectRole": "owner"}); err == nil {
			t.Error("projectRole next to a role was accepted")
		}
	})
	t.Run("R-AUTHZ-026 the action form needs a project source", func(t *testing.T) {
		q, err := parseRequirement(map[string]any{"action": "read", "repository": "path.repoKey", "project": "path.projectKey"})
		if err != nil || q.Project != "path.projectKey" || q.Repository != "path.repoKey" {
			t.Fatalf("%+v %v", q, err)
		}
		if _, err := parseRequirement(map[string]any{"action": "read", "repository": "path.repoKey"}); err == nil {
			t.Error("an action declaration without a project was accepted")
		}
	})

	call := func(c projectCaller, path string) int {
		s := newSetup(t, func(o *Options) { o.Access = c }, authed)
		return s.get(path).Code
	}
	t.Run("R-AUTHZ-026 the shared step admits the capability and a member of a project, and refuses the rest", func(t *testing.T) {
		// The project service is not wired in this setup: an admitted caller reaches the handler, which answers 503. The members
		// list keeps the declaration; the project list and the project itself are public and decide in the handler (R-AUTHZ-035 rule 8).
		const path = "/api/v1/projects/web/members"
		if got := call(projectCaller{caps: map[string]bool{"projects:read": true}}, path); got != 503 {
			t.Errorf("capability: %d", got)
		}
		if got := call(projectCaller{members: map[string]authz.ProjectRole{"web": authz.ProjectRoleReader}}, path); got != 503 {
			t.Errorf("member: %d", got)
		}
		if got := call(projectCaller{}, path); got != 404 {
			t.Errorf("neither: %d, want 404: a project the caller is no member of answers like a missing one", got)
		}
	})
	t.Run("R-AUTHZ-026 the owner role of the declaration asks for the administration of the addressed project", func(t *testing.T) {
		const path = "/api/v1/projects/web/principals?q=al"
		if got := call(projectCaller{members: map[string]authz.ProjectRole{"web": authz.ProjectRoleOwner}, owns: map[string]bool{"web": true}}, path); got != 503 {
			t.Errorf("owner: %d", got)
		}
		if got := call(projectCaller{members: map[string]authz.ProjectRole{"web": authz.ProjectRoleMaintainer}}, path); got != 403 {
			t.Errorf("maintainer: %d", got)
		}
		if got := call(projectCaller{members: map[string]authz.ProjectRole{"shop": authz.ProjectRoleOwner}, owns: map[string]bool{"shop": true}}, path); got != 404 {
			t.Errorf("owner of another project: %d, want 404: a project the caller is no member of answers like a missing one", got)
		}
	})
	t.Run("R-AUTHZ-026 a holder of projects:read, the auditor, keeps 403 where a non-member gets 404", func(t *testing.T) {
		const path = "/api/v1/projects/web/service-accounts"
		if got := call(projectCaller{caps: map[string]bool{"projects:read": true}}, path); got != 403 {
			t.Errorf("auditor: %d, want 403: it reads every project, so the project is not hidden from it", got)
		}
		if got := call(projectCaller{}, path); got != 404 {
			t.Errorf("non-member: %d, want 404", got)
		}
	})
	t.Run("R-AUTHZ-026 an anonymous caller gets 401", func(t *testing.T) {
		s := newSetup(t, func(o *Options) { o.Access = projectCaller{anonymous: true} }, nil)
		if got := s.get("/api/v1/projects/web/members").Code; got != 401 {
			t.Errorf("anonymous: %d", got)
		}
	})
}

func TestR_AUTHZ_026_TheProjectOperationsAnswer503WithoutAService(t *testing.T) {
	s := newSetup(t, func(o *Options) { o.Access = allowAll{} }, authed)
	for _, path := range []string{"/api/v1/projects", "/api/v1/projects/web"} {
		if w := s.get(path); w.Code != 503 {
			t.Errorf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
	}
}
