package v1

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

func TestR_API_008_ParseRequirementForms(t *testing.T) {
	good := []struct {
		name string
		raw  map[string]any
		want Requirement
	}{
		{"a capability", map[string]any{"permission": "policies:write"}, Requirement{Permission: "policies:write"}},
		{"a role", map[string]any{"role": "admin"}, Requirement{Role: "admin"}},
		{
			"an action on a repository from the query",
			map[string]any{"action": "deploy", "repository": "query.repository"},
			Requirement{Action: authz.Deploy, Repository: "query.repository"},
		},
		{
			"an action with a path",
			map[string]any{"action": "read", "repository": "path.key", "path": "query.path"},
			Requirement{Action: authz.Read, Repository: "path.key", Path: "query.path"},
		},
		{
			"an action with a package and a path",
			map[string]any{"action": "delete", "repository": "path.repoKey", "package": "path.name", "path": "query.path"},
			Requirement{Action: authz.Delete, Repository: "path.repoKey", Package: "path.name", Path: "query.path"},
		},
	}
	for _, tc := range good {
		t.Run("R-API-008 accepts "+tc.name, func(t *testing.T) {
			got, err := parseRequirement(tc.raw)
			if err != nil || got != tc.want {
				t.Fatalf("got %+v, %v", got, err)
			}
			if got.String() == "nothing (deny)" {
				t.Error("String does not render the form")
			}
		})
	}
	bad := []struct {
		name string
		raw  map[string]any
	}{
		{"an unknown role", map[string]any{"role": "owner"}},
		{"an empty role", map[string]any{"role": ""}},
		{"a role with another member", map[string]any{"role": "admin", "permission": "settings:read"}},
		{"a permission that is no capability", map[string]any{"permission": "system.read"}},
		{"an unknown action", map[string]any{"action": "publish", "repository": "query.repository"}},
		{"an action without a repository", map[string]any{"action": "deploy"}},
		{"a repository that is no value expression", map[string]any{"action": "deploy", "repository": "npm-internal"}},
		{"a source of an unknown origin", map[string]any{"action": "deploy", "repository": "body.repository"}},
		{"an unknown member", map[string]any{"action": "deploy", "repository": "query.repository", "version": "query.v"}},
		{"an action with a permission", map[string]any{"action": "deploy", "repository": "query.repository", "permission": "settings:read"}},
	}
	for _, tc := range bad {
		t.Run("R-API-008 refuses "+tc.name, func(t *testing.T) {
			if _, err := parseRequirement(tc.raw); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// actionAccess records what the action and role forms ask.
type actionAccess struct {
	coordinate authz.Coordinate
	permission string
	err        error
}

func (a *actionAccess) Require(_ context.Context, permission string) error {
	a.permission = permission
	return a.err
}

func (a *actionAccess) RequireAction(_ context.Context, c authz.Coordinate) error {
	a.coordinate = c
	return a.err
}

func TestR_API_008_DecideActionAndRoleForms(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/things/npm-internal?repository=npm-internal&path=a/b.tgz&package=%40web%2Fbutton", nil)
	req.SetPathValue("key", "npm-internal")
	withUser := func(r *http.Request) *http.Request {
		return r.WithContext(httpserver.WithPrincipal(r.Context(), httpserver.Principal{Kind: httpserver.KindUser, ID: "u", Name: "alice"}))
	}
	t.Run("R-API-008 the action form builds the coordinate from the request", func(t *testing.T) {
		acc := &actionAccess{}
		s := New(Options{Access: acc})
		q := Requirement{Action: authz.Deploy, Repository: "query.repository", Package: "query.package", Path: "query.path"}
		if err := s.decide(withUser(req), q); err != nil {
			t.Fatal(err)
		}
		c := acc.coordinate
		if c.Repository != "npm-internal" || c.Path != "a/b.tgz" || c.Package != "@web/button" || c.Kind != authz.KindWrite ||
			len(c.Actions) != 1 || c.Actions[0] != authz.Deploy {
			t.Fatalf("coordinate = %+v", c)
		}
	})
	t.Run("R-API-008 a route wildcard is a source and a read is a metadata request", func(t *testing.T) {
		acc := &actionAccess{}
		s := New(Options{Access: acc})
		if err := s.decide(withUser(req), Requirement{Action: authz.Read, Repository: "path.key"}); err != nil {
			t.Fatal(err)
		}
		if acc.coordinate.Repository != "npm-internal" || acc.coordinate.Kind != authz.KindMetadata {
			t.Fatalf("coordinate = %+v", acc.coordinate)
		}
	})
	t.Run("R-API-008 a source the request lacks names no repository", func(t *testing.T) {
		acc := &actionAccess{}
		s := New(Options{Access: acc})
		_ = s.decide(withUser(req), Requirement{Action: authz.Read, Repository: "query.nothing"})
		if acc.coordinate.Repository != "" {
			t.Fatalf("coordinate = %+v", acc.coordinate)
		}
	})
	t.Run("R-API-008 the refusal of the evaluator is the answer", func(t *testing.T) {
		acc := &actionAccess{err: authz.Decision{Outcome: authz.DenyHidden}.Err()}
		s := New(Options{Access: acc})
		err := s.decide(withUser(req), Requirement{Action: authz.Read, Repository: "query.repository"})
		if ee, _ := errs.As(err); ee == nil || ee.Status() != 404 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-API-008 the role form asks Require for the role", func(t *testing.T) {
		acc := &actionAccess{}
		s := New(Options{Access: acc})
		if err := s.decide(withUser(req), Requirement{Role: "admin"}); err != nil {
			t.Fatal(err)
		}
		if acc.permission != "role:admin" {
			t.Fatalf("Require(%q)", acc.permission)
		}
	})
	t.Run("R-API-008 without an Access the forms refuse: 401 anonymous, 403 otherwise", func(t *testing.T) {
		s := New(Options{})
		for _, q := range []Requirement{{Role: "admin"}, {Action: authz.Read, Repository: "query.repository"}} {
			if ee, _ := errs.As(s.decide(withUser(req), q)); ee == nil || ee.Status() != 403 {
				t.Errorf("%s: authenticated: %v", q, ee)
			}
			if ee, _ := errs.As(s.decide(req, q)); ee == nil || ee.Status() != 401 {
				t.Errorf("%s: anonymous: %v", q, ee)
			}
		}
	})
	t.Run("R-API-008 an Access that cannot decide actions refuses the action form", func(t *testing.T) {
		s := New(Options{Access: accessFunc(func(context.Context, string) error { return nil })})
		if ee, _ := errs.As(s.decide(withUser(req), Requirement{Action: authz.Read, Repository: "query.repository"})); ee == nil || ee.Status() != 403 {
			t.Fatalf("err = %v", ee)
		}
	})
	t.Run("R-API-008 the requirement renders for error texts", func(t *testing.T) {
		for _, q := range []Requirement{{Role: "admin"}, {Action: authz.Deploy, Repository: "query.repository"}, {Permission: "settings:read"}} {
			if strings.Contains(q.String(), "nothing") {
				t.Errorf("%+v renders as %q", q, q.String())
			}
		}
	})
}
