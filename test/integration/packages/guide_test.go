//go:build integration

package packages

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// guideSections returns the sections of a guide answer by id.
func guideSections(t testing.TB, a testenv.Answer) map[string]map[string]any {
	t.Helper()
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	out := map[string]map[string]any{}
	for _, s := range a.JSON(t)["sections"].([]any) {
		m := s.(map[string]any)
		out[m["id"].(string)] = m
	}
	return out
}

func sectionSnippets(sec map[string]any) map[string]string {
	out := map[string]string{}
	for _, s := range sec["snippets"].([]any) {
		m := s.(map[string]any)
		out[m["id"].(string)] = m["content"].(string)
	}
	return out
}

// TestR_API_053_TheGuideThroughTheStack: the guide of a repository over the real chain
// shows the client setup's snippets for the same caller, follows the caller's rights, and
// names no token; the guide of a format answers every caller that may list the formats.
func TestR_API_053_TheGuideThroughTheStack(t *testing.T) {
	e := newEnv(t)
	shape := regexp.MustCompile(`trv_(pat|sat|cit)_`)

	t.Run("the configure and install snippets are the client setup's with credentials=env", func(t *testing.T) {
		for _, client := range []string{"npm", "pnpm", "yarn", "yarn-classic"} {
			g := guideSections(t, e.get(e.tokA, "/api/v1/repositories/npm-local/guide?client="+client))
			setup := snippets(t, e.setup(e.tokA, "npm-local", "client="+client+"&credentials=env"))
			for _, tab := range []string{"configure", "install"} {
				got := sectionSnippets(g["npm."+tab])
				if len(got) == 0 {
					t.Fatalf("%s: no %s snippets", client, tab)
				}
				for id, content := range got {
					if setup[id] != content {
						t.Errorf("%s %s: guide %q, setup %q", client, id, content, setup[id])
					}
				}
			}
		}
	})
	t.Run("a caller who may deploy gets the target, one who may not is told", func(t *testing.T) {
		a := e.get(e.tokA, "/api/v1/repositories/npm-local/guide?client=npm")
		if v := a.JSON(t); v["publishTarget"] != "npm-local" || v["credentials"] != "env" || v["registryUrl"] != "http://trove.test/api/npm/npm-local/" {
			t.Fatalf("%v", v)
		}
		b := e.get(e.tokB, "/api/v1/repositories/npm-local/guide?client=npm")
		if b.JSON(t)["publishTarget"] != nil {
			t.Fatalf("%s", b.Body)
		}
		if html, _ := guideSections(t, b)["npm.publish"]["html"].(string); !strings.Contains(html, "you do not hold the deploy permission") {
			t.Fatalf("%s", html)
		}
	})
	t.Run("a caller who may not read the repository gets the 404 of a missing one", func(t *testing.T) {
		missing := e.get(e.tokD, "/api/v1/repositories/npm-nowhere/guide?client=npm")
		hidden := e.get(e.tokD, "/api/v1/repositories/npm-local/guide?client=npm")
		if missing.Status != 404 || hidden.Status != 404 || missing.Code(t) != hidden.Code(t) {
			t.Fatalf("missing %d %s, hidden %d %s", missing.Status, missing.Body, hidden.Status, hidden.Body)
		}
	})
	t.Run("the guide of a format answers every caller who may list the formats", func(t *testing.T) {
		for _, tok := range []string{e.tokA, e.tokD} {
			a := e.get(tok, "/api/v1/formats/npm/guide?class=virtual&client=yarn")
			g := guideSections(t, a)
			if v := a.JSON(t); v["repository"] != nil || v["registryUrl"] != "http://trove.test/api/npm/<repository>/" || g["npm.virtual.what"] == nil {
				t.Fatalf("%v", v)
			}
		}
		if a := e.get("", "/api/v1/formats/npm/guide?class=virtual&client=yarn"); a.Status != 401 {
			t.Fatalf("anonymous with anonymous access off: %d", a.Status)
		}
	})
	t.Run("the formats list names the clients", func(t *testing.T) {
		for _, f := range e.get(e.tokA, "/api/v1/formats").JSON(t)["formats"].([]any) {
			m := f.(map[string]any)
			var ids []string
			for _, c := range m["clients"].([]any) {
				ids = append(ids, c.(map[string]any)["id"].(string))
			}
			want := map[string][]string{"npm": {"npm", "pnpm", "yarn", "yarn-classic"}, "generic": nil}[m["name"].(string)]
			if !slices.Equal(ids, want) {
				t.Errorf("%s: clients %v", m["name"], ids)
			}
		}
	})
	t.Run("no guide names a token", func(t *testing.T) {
		for _, tok := range []string{e.tokA, e.tokB, e.tokE} {
			for _, client := range []string{"npm", "pnpm", "yarn", "yarn-classic"} {
				for _, p := range []string{"/api/v1/repositories/npm-local/guide?client=" + client, "/api/v1/formats/npm/guide?class=remote&client=" + client} {
					if a := e.get(tok, p); a.Status != 200 || shape.Match(a.Body) {
						t.Fatalf("%s: %d", p, a.Status)
					}
				}
			}
		}
	})
}

// TestR_API_053_TheGuideOnAnEndpointWithRules: on an endpoint with exposure rules the
// guide carries the endpoint note and the endpoint's own addresses.
func TestR_API_053_TheGuideOnAnEndpointWithRules(t *testing.T) {
	e := newEnv(t)
	e.st.Exec(`INSERT INTO endpoint_exposure_rule (id, endpoint, name, repo_id, packages, versions)
		SELECT gen_random_uuid(), 'partners', 'ui', id, '{"include":["@team-a/*"],"exclude":[]}', '{"include":[],"exclude":[],"range":">=1.4.0"}'
		  FROM repositories WHERE key = 'npm-local'`)
	endpoints := `{"partners":{"listen":"127.0.0.1:0","base_url":"https://partners.example.test","surface":["api"]}}`
	r := e.st.Start(t, testenv.ReplicaOptions{Name: "web2", Endpoints: endpoints})
	a := e.st.DoAt(r.Addr("partners"), "partners.example.test", "GET", "/api/v1/repositories/npm-local/guide?client=npm", e.tokBot, nil)
	g := guideSections(t, a)
	v := a.JSON(t)
	if v["exposureRules"] != true || v["registryUrl"] != "https://partners.example.test/api/npm/npm-local/" || g["npm.endpoint"] == nil {
		t.Fatalf("%v", v)
	}
	if html := g["npm.endpoint"]["html"].(string); !strings.Contains(html, "endpoint_read_only") {
		t.Fatalf("%s", html)
	}
	if g := guideSections(t, e.get(e.tokE, "/api/v1/repositories/npm-local/guide?client=npm")); g["npm.endpoint"] != nil {
		t.Fatal("the default endpoint has no exposure rules and got the note")
	}
}
