package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/project"
)

// fakeProjects is a project.Service that serves a fixed list; the methods it does
// not define answer through the nil embedded interface and are not reached.
type fakeProjects struct {
	project.Service
	all []project.Project
}

func (f fakeProjects) List(context.Context) ([]project.Project, error) { return f.all, nil }

func (f fakeProjects) Get(_ context.Context, key string) (project.Project, error) {
	for _, p := range f.all {
		if p.Key == key {
			return p, nil
		}
	}
	return project.Project{}, errs.E(errs.NotFound, errs.CodeNotFound, "no such project")
}

func (f fakeProjects) LookupPrincipals(_ context.Context, prefix string, _ project.PrincipalKind, limit int) ([]project.PrincipalRef, error) {
	if limit == 0 {
		limit = project.MaxLookup
	}
	return []project.PrincipalRef{{Kind: project.KindUser, Name: prefix + "-x"}}[:min(limit, 1)], nil
}

func sampleProjects() fakeProjects {
	mk := func(key, name string) project.Project {
		return project.Project{
			ID: "id-" + key, Key: key, DisplayName: name, Version: 3,
			Rights: project.DefaultRights(), Admins: []project.PrincipalRef{{Kind: project.KindGroup, Name: key + "-leads"}},
		}
	}
	return fakeProjects{all: []project.Project{mk("docs", "Documentation"), mk("web", "Web platform"), mk("web2", "Second web")}}
}

func projectsSetup(t *testing.T, c projectCaller) setup {
	t.Helper()
	return newSetup(t, func(o *Options) {
		o.Access = c
		o.Authz.Projects = sampleProjects()
		o.API.Config = apiConfigForTest()
	}, authed)
}

type projectPage struct {
	Items []struct {
		Key     string   `json:"key"`
		Admins  []string `json:"admins"`
		Version int64    `json:"version"`
	} `json:"items"`
	NextCursor *string `json:"nextCursor"`
}

func decodePage(t *testing.T, body string) projectPage {
	t.Helper()
	var p projectPage
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return p
}

func keysOf(p projectPage) string {
	var ks []string
	for _, it := range p.Items {
		ks = append(ks, it.Key)
	}
	return strings.Join(ks, ",")
}

func TestR_API_047_ListProjectsNarrowsToTheCaller(t *testing.T) {
	auditor := projectCaller{caps: map[string]bool{"projects:read": true}}
	web := projectCaller{projects: []authz.AdministeredProject{{Key: "web", ManagePolicies: true}, {Key: "docs"}}}

	t.Run("R-API-047 a holder of projects:read sees every project", func(t *testing.T) {
		w := projectsSetup(t, auditor).get("/api/v1/projects")
		if w.Code != 200 || keysOf(decodePage(t, w.Body.String())) != "docs,web,web2" {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	})
	t.Run("R-API-047 an administrator without the capability sees its own projects only", func(t *testing.T) {
		w := projectsSetup(t, web).get("/api/v1/projects")
		if w.Code != 200 || keysOf(decodePage(t, w.Body.String())) != "docs,web" {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	})
	t.Run("R-API-047 q, sort and the cursor page by key", func(t *testing.T) {
		s := projectsSetup(t, auditor)
		if w := s.get("/api/v1/projects?q=SECOND"); keysOf(decodePage(t, w.Body.String())) != "web2" {
			t.Errorf("q on the display name: %s", w.Body)
		}
		if w := s.get("/api/v1/projects?sort=-key"); keysOf(decodePage(t, w.Body.String())) != "web2,web,docs" {
			t.Errorf("descending: %s", w.Body)
		}
		first := decodePage(t, s.get("/api/v1/projects?limit=2").Body.String())
		if keysOf(first) != "docs,web" || first.NextCursor == nil {
			t.Fatalf("first page: %+v", first)
		}
		second := decodePage(t, s.get("/api/v1/projects?limit=2&cursor="+*first.NextCursor).Body.String())
		if keysOf(second) != "web2" || second.NextCursor != nil {
			t.Errorf("second page: %+v", second)
		}
	})
}

func TestR_AUTHZ_026_GetProjectIsNotFoundForAnotherProject(t *testing.T) {
	web := projectCaller{projects: []authz.AdministeredProject{{Key: "web"}}}
	s := projectsSetup(t, web)
	t.Run("R-AUTHZ-026 an administrator reads its project with an ETag and gets 304 for the current tag", func(t *testing.T) {
		w := s.get("/api/v1/projects/web")
		if w.Code != 200 || w.Header().Get("ETag") != `"3"` {
			t.Fatalf("%d %v", w.Code, w.Header())
		}
		w = s.do("GET", "/api/v1/projects/web", "", map[string]string{"If-None-Match": `"3"`})
		if w.Code != http.StatusNotModified {
			t.Errorf("If-None-Match: %d", w.Code)
		}
	})
	t.Run("R-AUTHZ-026 a project the caller does not administer answers 404, existing or not", func(t *testing.T) {
		for _, key := range []string{"docs", "nothing"} {
			if w := s.get("/api/v1/projects/" + key); w.Code != 404 {
				t.Errorf("%s: %d", key, w.Code)
			}
		}
	})
}

func TestR_AUTHZ_026_TheLookupBoundsItsInput(t *testing.T) {
	web := projectCaller{projects: []authz.AdministeredProject{{Key: "web", ManagePolicies: true}}}
	s := projectsSetup(t, web)
	if w := s.get("/api/v1/projects/web/principals?q=al"); w.Code != 200 || !strings.Contains(w.Body.String(), `"principal":"user:al-x"`) {
		t.Errorf("a valid lookup: %d %s", w.Code, w.Body)
	}
	if w := s.get("/api/v1/projects/web/principals?q=al&limit=21"); w.Code != 422 {
		t.Errorf("limit above the maximum: %d", w.Code)
	}
	if w := s.get("/api/v1/projects/web/principals?q=" + strings.Repeat("a", 129)); w.Code != 422 {
		t.Errorf("a prefix of 129 characters: %d", w.Code)
	}
	if w := s.get("/api/v1/projects/docs/principals?q=al"); w.Code != 404 {
		t.Errorf("another project: %d", w.Code)
	}
}
