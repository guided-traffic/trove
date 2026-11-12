//go:build integration

package search

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// fixture is the installation of the worked rows of R-AUTHZ-035 that R-API-061's acceptance criteria name:
//
//	web            public
//	  npm-public   artifacts public, wiki internal
//	  npm-internal artifacts and wiki internal
//	  npm-private  private, hideExistence false
//	  npm-all      virtual, internal, over the three
//	  npm, npm-local, my-npm   private (the ranks of R-API-062)
//	mobile         private, with npm-local
//
// Each local repository of the worked rows holds @web/button. The callers:
//
//	frank   internal, no grant
//	erin    flagged external, read on npm-private
//	carol   internal, read on npm-private for the packages @web/* only (a repository read in part)
//	ada     the system role admin
//	bot     a service account that reads the worked rows, its token bound to the endpoint customers, whose exposure rule
//	        exposes the packages @web/* of npm-public only
type fixture struct {
	t  *testing.T
	st *testenv.Stack
	r  *testenv.Replica

	frank, erin, carol, ada, bot                        testenv.Person
	tokFrank, tokFrankMobile, tokErin, tokCarol, tokAda string
	tokBot                                              string
}

// customers is the endpoint with exposure rules of the fixture.
const customers = `{"customers":{"listen":"127.0.0.1:0","base_url":"https://customers.example.test","surface":["api"]}}`

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "search"})
	f := &fixture{t: t, st: st}
	st.Project("web")
	st.Exec(`UPDATE project SET visibility = 'public' WHERE key = 'web'`)
	for _, k := range []string{"npm-public", "npm-internal", "npm", "npm-local", "my-npm"} {
		st.RepositoryIn("web", k, "local", "npm", `{}`)
	}
	st.RepositoryIn("web", "npm-private", "local", "npm", `{"hideExistence":"false"}`)
	st.RepositoryIn("web", "npm-all", "virtual", "npm", `{}`)
	st.RepositoryIn("mobile", "npm-local", "local", "npm", `{}`)
	for i, m := range []string{"npm-public", "npm-internal", "npm-private"} {
		st.Exec(`INSERT INTO virtual_members (virtual_id, position, member_id)
			SELECT v.id, $2, m.id FROM repositories v, repositories m, project p
			 WHERE v.key = 'npm-all' AND m.key = $1 AND v.project_id = p.id AND m.project_id = p.id AND p.key = 'web'`, m, i)
	}
	f.levels("npm-public", "public", "internal")
	f.levels("npm-internal", "internal", "internal")
	f.levels("npm-all", "internal", "internal")
	for _, k := range []string{"npm-public", "npm-internal", "npm-private"} {
		f.pkg("web/"+k, "@web/button")
	}
	f.pkg("web/npm-private", "@other/button")

	f.frank, f.erin, f.carol, f.ada = st.User("frank"), st.User("erin"), st.User("carol"), st.User("ada", "web-leads")
	st.Exec(`UPDATE app_user SET external = true, display_name = 'Erin External' WHERE principal_id = $1::uuid`, f.erin.ID)
	st.Exec(`UPDATE app_user SET display_name = 'Frank Reader' WHERE principal_id = $1::uuid`, f.frank.ID)
	st.Bind("admin", f.ada)
	st.Policy("erin-private", []string{"read"}, []string{"web/npm-private"}, f.erin)
	f.narrowed("carol-web", "npm-private", f.carol, "@web/*")
	f.bot = st.ServiceAccount("bot")
	st.Policy("bot-read", []string{"read"}, []string{"web/npm-public", "web/npm-internal", "web/npm-private"}, f.bot)
	st.Exec(`INSERT INTO endpoint_exposure_rule (id, endpoint, name, repo_id, packages, versions)
		SELECT gen_random_uuid(), 'customers', 'web', r.id, '{"include":["@web/*"],"exclude":[]}', '{"include":[],"exclude":[]}'
		  FROM repositories r JOIN project p ON p.id = r.project_id WHERE p.key = 'web' AND r.key = 'npm-public'`)

	f.tokFrank = st.Token(f.frank, scope.Default())
	f.tokFrankMobile = st.Token(f.frank, scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"mobile/*"}, Actions: []string{"read"}}}})
	f.tokErin, f.tokCarol = st.Token(f.erin, scope.Default()), st.Token(f.carol, scope.Default())
	f.tokAda = st.Token(f.ada, scope.Scope{Inherit: true, Admin: true})
	f.tokBot = st.Token(f.bot, scope.Scope{Inherit: true, Endpoints: []string{"customers"}})
	f.r = st.Start(t, testenv.ReplicaOptions{Name: "web1", Endpoints: customers})
	return f
}

// levels sets the artifacts and wiki levels of a repository of web.
func (f *fixture) levels(key, artifacts, wiki string) {
	f.t.Helper()
	f.st.Exec(`UPDATE repositories r SET visibility_artifacts = $2, visibility_wiki = $3 FROM project p
	            WHERE p.id = r.project_id AND p.key = 'web' AND r.key = $1`, key, artifacts, wiki)
}

// pkg stores a package with one live version in a repository named <project>/<key>.
func (f *fixture) pkg(repo, name string) {
	f.t.Helper()
	project, key, _ := strings.Cut(repo, "/")
	id := f.st.Number(`INSERT INTO packages (repo_id, name, display_name)
		SELECT r.id, $3, $3 FROM repositories r JOIN project p ON p.id = r.project_id WHERE p.key = $1 AND r.key = $2 RETURNING id`, project, key, name)
	f.st.Exec(`INSERT INTO package_versions (package_id, version, display_version) VALUES ($1, '1.0.0', '1.0.0')`, id)
}

// narrowed grants read on a repository of web for the packages matching one include pattern: a repository read in part.
func (f *fixture) narrowed(name, repo string, who testenv.Person, include string) {
	f.t.Helper()
	id := f.st.Text("INSERT INTO authz_policy (id, name) VALUES (gen_random_uuid(), $1) RETURNING id::text", name)
	f.st.Exec(`INSERT INTO authz_policy_selector (policy_id, ordinal, kind, repository_id)
		SELECT $1::uuid, 0, 'key', r.id FROM repositories r JOIN project p ON p.id = r.project_id WHERE p.key = 'web' AND r.key = $2`, id, repo)
	f.st.Exec("INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) VALUES ($1::uuid, 'package', 'include', $2)", id, include)
	f.st.Exec("INSERT INTO authz_policy_entry (policy_id, principal_id, actions) VALUES ($1::uuid, $2::uuid, ARRAY['read'])", id, who.ID)
}

// suggestions is the answer of GET /api/v1/search/suggestions.
type suggestions struct {
	Repositories *struct {
		Items []struct{ Project, Key, Format, Class string } `json:"items"`
		More  bool                                           `json:"more"`
	} `json:"repositories"`
	Packages *struct {
		Items []struct{ ID, Format, Name, Project, Repository string } `json:"items"`
		More  bool                                                     `json:"more"`
	} `json:"packages"`
	Projects *struct {
		Items []struct{ Key, DisplayName string } `json:"items"`
		More  bool                                `json:"more"`
	} `json:"projects"`
	Users *struct {
		Items []struct {
			Username    string  `json:"username"`
			DisplayName *string `json:"displayName"`
			Disabled    bool    `json:"disabled"`
		} `json:"items"`
		More bool `json:"more"`
	} `json:"users"`
	Groups *struct {
		Items []struct{ Name, Description string } `json:"items"`
		More  bool                                 `json:"more"`
	} `json:"groups"`
}

// repos names the suggested repositories <project>/<key>.
func (s suggestions) repos() []string {
	var out []string
	if s.Repositories != nil {
		for _, i := range s.Repositories.Items {
			out = append(out, i.Project+"/"+i.Key)
		}
	}
	return out
}

// pkgs names the suggested packages <name>@<project>/<repository>.
func (s suggestions) pkgs() []string {
	var out []string
	if s.Packages != nil {
		for _, i := range s.Packages.Items {
			out = append(out, i.Name+"@"+i.Project+"/"+i.Repository)
		}
	}
	return out
}

// get asks the suggestions of the default endpoint with a token ("" for none).
func (f *fixture) get(token, query string) testenv.Answer {
	f.t.Helper()
	return f.st.Do(f.r, "GET", "/api/v1/search/suggestions?"+query, token, nil)
}

// suggest asks the suggestions and decodes the 200 answer.
func (f *fixture) suggest(token, q string, extra ...string) suggestions {
	f.t.Helper()
	a := f.get(token, "q="+url.QueryEscape(q)+strings.Join(extra, ""))
	if a.Status != 200 {
		f.t.Fatalf("q=%s%s: %d %s", q, strings.Join(extra, ""), a.Status, a.Body)
	}
	var s suggestions
	if err := json.Unmarshal(a.Body, &s); err != nil {
		f.t.Fatal(err)
	}
	return s
}
