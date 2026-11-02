package v1

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
)

// The narrowing of the repository and service-account operations to a project
// administrator's project (R-AUTHZ-026, Q-AUTHZ-173, Q-AUTHZ-177, Q-AUTHZ-184),
// against a test double of the access interface: the decisions of the engine are
// tested in internal/authz and in the integration suites.

func ptrTo(s string) *string { return &s }

func TestR_AUTHZ_026_RepositoryOperationsOfAProjectAdministrator(t *testing.T) {
	web := authz.AdministeredProject{Key: "web", ManagePolicies: true, ManageRepositories: true}
	lab := authz.AdministeredProject{Key: "lab", ManagePolicies: true} // manageRepositories is off
	caller := projectCaller{
		projects:     []authz.AdministeredProject{web, lab},
		repositories: map[string]string{"web-npm": "web", "lab-npm": "lab", "shop-npm": "shop", "plain": ""},
	}
	s := projectServer(caller)
	ctx := context.Background()

	t.Run("R-AUTHZ-026 a create names a project the caller administers with manageRepositories", func(t *testing.T) {
		if got, err := s.createDelegate(ctx, ptrTo("web")); err != nil || got != "web" {
			t.Fatalf("%q %v", got, err)
		}
		_, err := s.createDelegate(ctx, ptrTo("lab"))
		if kind, code := codeOf(t, err); kind != errs.Forbidden || code != errs.CodeProjectSwitchOff {
			t.Errorf("switch off: %v %s", kind, code)
		}
		_, err = s.createDelegate(ctx, ptrTo("shop"))
		if kind, _ := codeOf(t, err); kind != errs.NotFound {
			t.Errorf("another project: %v", kind)
		}
		for _, p := range []*string{nil, ptrTo("")} {
			_, err = s.createDelegate(ctx, p)
			if kind, code := codeOf(t, err); kind != errs.Forbidden || code != errs.CodeForbidden {
				t.Errorf("no project: %v %s", kind, code)
			}
		}
	})
	t.Run("R-AUTHZ-026 a holder of repositories:write-config acts without a project", func(t *testing.T) {
		holder := projectServer(projectCaller{caps: map[string]bool{capabilityWriteConfig: true}})
		for _, p := range []*string{nil, ptrTo("any")} {
			if got, err := holder.createDelegate(ctx, p); err != nil || got != "" {
				t.Errorf("%q %v", got, err)
			}
		}
		if got, err := holder.writeDelegate(ctx, "plain"); err != nil || got != "" {
			t.Errorf("%q %v", got, err)
		}
		if got, err := holder.requireAnyRepositoriesSwitch(ctx); err != nil || got != "" {
			t.Errorf("%q %v", got, err)
		}
	})
	t.Run("R-AUTHZ-026 Q-AUTHZ-184 a write of an existing repository needs its project and the switch", func(t *testing.T) {
		if got, err := s.writeDelegate(ctx, "web-npm"); err != nil || got != "web" {
			t.Fatalf("%q %v", got, err)
		}
		_, err := s.writeDelegate(ctx, "lab-npm")
		if kind, code := codeOf(t, err); kind != errs.Forbidden || code != errs.CodeProjectSwitchOff {
			t.Errorf("switch off: %v %s", kind, code)
		}
		for _, key := range []string{"shop-npm", "plain", "unknown"} { // the same answer as an unknown repository
			_, err = s.writeDelegate(ctx, key)
			if kind, code := codeOf(t, err); kind != errs.NotFound || code != errs.CodeNotFound {
				t.Errorf("%s: %v %s", key, kind, code)
			}
		}
	})
	t.Run("R-AUTHZ-026 the unsaved upstream test needs any project with the switch", func(t *testing.T) {
		if got, err := s.requireAnyRepositoriesSwitch(ctx); err != nil || got != "web" {
			t.Fatalf("%q %v", got, err) // the project with the switch: its rules apply to the section (Q-REPO-127)
		}
		off := projectServer(projectCaller{projects: []authz.AdministeredProject{lab}})
		_, err := off.requireAnyRepositoriesSwitch(ctx)
		if kind, code := codeOf(t, err); kind != errs.Forbidden || code != errs.CodeProjectSwitchOff {
			t.Errorf("switch off: %v %s", kind, code)
		}
		none := projectServer(projectCaller{})
		_, err = none.requireAnyRepositoriesSwitch(ctx)
		if kind, code := codeOf(t, err); kind != errs.Forbidden || code != errs.CodeForbidden {
			t.Errorf("no project: %v %s", kind, code)
		}
	})
	t.Run("Q-AUTHZ-184 the full document goes to the administrators of the repository's project", func(t *testing.T) {
		for key, want := range map[string]bool{"web-npm": true, "lab-npm": true, "shop-npm": false, "plain": false} {
			if got, err := s.readsConfig(ctx, key); err != nil || got != want {
				t.Errorf("%s: %v %v", key, got, err)
			}
		}
		holder := projectServer(projectCaller{caps: map[string]bool{capabilityReadConfig: true}})
		if got, _ := holder.readsConfig(ctx, "plain"); !got {
			t.Error("a holder of repositories:read-config reads every configuration")
		}
	})
}

func TestR_AUTHZ_026_ProjectMemberOfARepositoryBody(t *testing.T) {
	t.Run("R-AUTHZ-026 the project member is taken out of the document and read apart", func(t *testing.T) {
		for body, want := range map[string]*string{
			`{"key":"a","project":"web"}`: ptrTo("web"),
			`{"key":"a","project":null}`:  ptrTo(""),
			`{"key":"a"}`:                 nil,
		} {
			rest, got, err := takeProject([]byte(body))
			if err != nil {
				t.Fatalf("%s: %v", body, err)
			}
			if (got == nil) != (want == nil) || (got != nil && *got != *want) {
				t.Errorf("%s: project %v, want %v", body, got, want)
			}
			if string(rest) == "" || (got != nil && string(rest) == body) {
				t.Errorf("%s: the member stays in %s", body, rest)
			}
		}
		if _, _, err := takeProject([]byte(`{"project":3}`)); err == nil {
			t.Error("a number is no project")
		}
	})
}

func TestR_AUTHZ_026_ServiceAccountOperationsOfAProjectAdministrator(t *testing.T) {
	web := authz.AdministeredProject{Key: "web", ManagePolicies: true}
	lab := authz.AdministeredProject{Key: "lab"} // managePolicies is off
	s := projectServer(projectCaller{projects: []authz.AdministeredProject{web, lab}})
	ctx := context.Background()

	t.Run("Q-AUTHZ-169 the list names a project the caller administers", func(t *testing.T) {
		if err := s.narrowAccountList(ctx, "web"); err != nil {
			t.Error(err)
		}
		if kind, code := codeOf(t, s.narrowAccountList(ctx, "")); kind != errs.Forbidden || code != errs.CodeForbidden {
			t.Errorf("no project: %v %s", kind, code)
		}
		if kind, _ := codeOf(t, s.narrowAccountList(ctx, "shop")); kind != errs.NotFound {
			t.Errorf("another project: %v", kind)
		}
		holder := projectServer(projectCaller{caps: map[string]bool{capabilityIdentitiesRead: true}})
		if err := holder.narrowAccountList(ctx, ""); err != nil {
			t.Errorf("a holder of identities:read lists every account: %v", err)
		}
	})
	t.Run("Q-AUTHZ-177 a create needs the project and managePolicies", func(t *testing.T) {
		if err := s.narrowAccountCreate(ctx, "web"); err != nil {
			t.Error(err)
		}
		if kind, code := codeOf(t, s.narrowAccountCreate(ctx, "lab")); kind != errs.Forbidden || code != errs.CodeProjectSwitchOff {
			t.Errorf("switch off: %v %s", kind, code)
		}
		if kind, code := codeOf(t, s.narrowAccountCreate(ctx, "")); kind != errs.Forbidden || code != errs.CodeForbidden {
			t.Errorf("no project: %v %s", kind, code)
		}
		if kind, _ := codeOf(t, s.narrowAccountCreate(ctx, "shop")); kind != errs.NotFound {
			t.Errorf("another project: %v", kind)
		}
		holder := projectServer(projectCaller{caps: map[string]bool{capabilityIdentitiesWrite: true}})
		for _, p := range []string{"", "web", "shop"} {
			if err := holder.narrowAccountCreate(ctx, p); err != nil {
				t.Errorf("a holder of identities:write creates for %q: %v", p, err)
			}
		}
	})
}

func TestR_AUTHZ_026_UpstreamTestsOfAProjectAdministrator(t *testing.T) {
	prober := &recordingProber{}
	build := func(c projectCaller) *Server {
		s := upstreamServer(t, prober)
		s.o.Access = c
		return s
	}
	body := `{"format":"npmlike","remote":{}}`
	t.Run("R-AUTHZ-026 the unsaved test answers 403 project_switch_off while the switch is off", func(t *testing.T) {
		s := build(projectCaller{projects: []authz.AdministeredProject{{Key: "lab"}}})
		w := post(s, s.TestUpstream, body, "application/json")
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), errs.CodeProjectSwitchOff) {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	})
	t.Run("R-AUTHZ-026 the unsaved test is open to an administrator with the switch", func(t *testing.T) {
		s := build(projectCaller{projects: []authz.AdministeredProject{{Key: "web", ManageRepositories: true}}})
		if w := post(s, s.TestUpstream, `{"format":"npmlike","remote":{"authMode":"none"}}`, "application/json"); w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	})
	t.Run("R-AUTHZ-026 the saved test of a repository outside the project answers 404", func(t *testing.T) {
		s := build(projectCaller{
			projects:     []authz.AdministeredProject{{Key: "web", ManageRepositories: true}},
			repositories: map[string]string{"shop-npm": "shop"},
		})
		r := httptest.NewRequest(http.MethodPost, "/api/v1/repositories/shop-npm/upstream-test", nil)
		w := httptest.NewRecorder()
		s.TestRepositoryUpstream(w, r, "shop-npm")
		if w.Code != http.StatusNotFound {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	})
}
