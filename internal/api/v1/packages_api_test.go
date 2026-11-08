package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	_ "github.com/guided-traffic/trove/internal/formats/all"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/packages/readme"
	"github.com/guided-traffic/trove/internal/packages/view"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestR_API_029_WithoutTheReadModelEveryOperationAnswers503: a replica that cannot
// decide what a caller may read answers the package operations 503, never an empty
// list.
func TestR_API_029_WithoutTheReadModelEveryOperationAnswers503(t *testing.T) {
	s := newSetup(t, nil, authed)
	for _, path := range []string{
		"/api/v1/packages",
		"/api/v1/packages/lookup?format=npm&name=%40team-a%2Fui",
		"/api/v1/packages/" + view.ID("npm", "left-pad"),
		"/api/v1/packages/" + view.ID("npm", "left-pad") + "/versions",
		"/api/v1/packages/" + view.ID("npm", "left-pad") + "/versions/1.0.0",
		"/api/v1/packages/" + view.ID("npm", "left-pad") + "/versions/1.0.0/readme",
		"/api/v1/projects/it/repositories/npm-local/client-setup?client=npm",
	} {
		w := s.get(path)
		var p errs.Problem
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || w.Code != 503 || p.Code != errs.CodeUnavailable {
			t.Errorf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
}

type packagesReader struct {
	repos map[string]repository.Repository
}

func (f packagesReader) ByName(_ context.Context, name qname.Repo) (repository.Repository, error) {
	key := name.Key
	if r, ok := f.repos[key]; ok {
		return r, nil
	}
	return repository.Repository{}, errs.E(errs.NotFound, errs.CodeNotFound, "no such repository")
}

func (f packagesReader) List(context.Context) ([]repository.Repository, error) {
	var out []repository.Repository
	for _, r := range f.repos {
		out = append(out, r)
	}
	return out, nil
}

// packagesDecider answers the questions the client setup asks: nothing is readable by an
// anonymous caller, and the caller may deploy where deploy is true.
type packagesDecider struct {
	authz.Decider
	deploy bool
}

func (packagesDecider) Begin(context.Context, authz.Subject, ...qname.Repo) (*authz.Index, error) {
	return nil, nil
}

func (f packagesDecider) CheckOn(_ *authz.Index, _ authz.Subject, items []authz.CheckItem) ([]bool, error) {
	out := make([]bool, len(items))
	for i := range items {
		out[i] = f.deploy
	}
	return out, nil
}

func (packagesDecider) ReadableOn(context.Context, *authz.Index, authz.Subject, []qname.Repo) ([]qname.Repo, error) {
	return nil, nil
}

type packagesScoper struct{}

func (packagesScoper) ReadScopeOn(context.Context, *authz.Index, authz.Subject, qname.Repo, []qname.Repo) (authz.ReadScope, error) {
	return nil, nil
}

func setupServer(t *testing.T, baseURL string, deploy bool) http.Handler {
	t.Helper()
	return setupServerAs(t, baseURL, deploy, authed)
}

// setupServerAs is setupServer for a caller authn produces; nil is anonymous.
func setupServerAs(t *testing.T, baseURL string, deploy bool, authn func(*http.Request) (httpserver.Principal, error)) http.Handler {
	t.Helper()
	return setupServerWith(t, baseURL, deploy, authn, nil)
}

// setupServerWith is setupServerAs with a change to the services before the server is built.
func setupServerWith(t *testing.T, baseURL string, deploy bool, authn func(*http.Request) (httpserver.Principal, error), change func(*PackageServices)) http.Handler {
	t.Helper()
	guides, err := readme.New(readme.Options{MaxSourceBytes: 256 << 10})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := LoadDocument(EmbeddedYAML())
	if err != nil {
		t.Fatal(err)
	}
	local := repository.Repository{Name: qr("npm-local"), Class: repository.Local, Format: "npm"}
	remote := repository.Repository{Name: qr("npm-remote"), Class: repository.Remote, Format: "npm"}
	repos := packagesReader{repos: map[string]repository.Repository{
		"npm-local":   local,
		"npm-remote":  remote,
		"files":       {Name: qr("files"), Class: repository.Local, Format: "generic"},
		"nuget-local": {Name: qr("nuget-local"), Class: repository.Local, Format: "nuget"},
		"oci-local":   {Name: qr("oci-local"), Class: repository.Local, Format: "oci"},
		"npm-all": repository.Assemble(repository.Repository{Name: qr("npm-all"), Class: repository.Virtual, Format: "npm"},
			[]repository.Repository{local, remote}, &local, 0),
		"npm-readonly": repository.Assemble(repository.Repository{Name: qr("npm-readonly"), Class: repository.Virtual, Format: "npm"},
			[]repository.Repository{remote}, nil, 0),
	}}
	services := PackageServices{
		View: &view.Service{}, Decider: packagesDecider{deploy: deploy}, Scoper: packagesScoper{}, Reader: repos, Guides: guides,
		TokenEnvVar: "MY_TOKEN", TokenTTL: 36 * 3600 * 1e9,
	}
	if change != nil {
		change(&services)
	}
	router := httpserver.NewClientRouter()
	New(Options{
		Document: doc, Identity: Identity{Replica: "r", Role: "web"}, Ready: func() bool { return true },
		Access: allowAll{}, Replicas: fakeReplicas{}, Facts: fakeFacts{},
		Packages: services,
	}).Register(router)
	res, err := httpserver.NewResolver(nil, baseURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	chain := httpserver.Chain{Router: router, Resolver: res, Authenticate: authn, Replica: "r", ReplicaHeader: httpserver.ReplicaAuthenticated}
	return chain.Handler()
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

func TestR_API_030_ClientSetupAnswer(t *testing.T) {
	h := setupServer(t, "https://trove.example.com", true)
	w := get(h, "/api/v1/projects/it/repositories/npm-local/client-setup?client=pnpm&credentials=env&package=a(b)&version=1.0.0")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var out gen.ClientSetup
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.RegistryUrl != "https://trove.example.com/api/npm/it/npm-local/" || out.EndpointPath != "//trove.example.com/api/npm/it/npm-local/" ||
		out.Username == nil || *out.Username != "alice" || out.PublishTarget == nil || *out.PublishTarget != "it/npm-local" || out.TokenTtlSeconds != 36*3600 {
		t.Fatalf("%+v", out)
	}
	got := map[string]string{}
	for _, s := range out.Snippets {
		got[s.Id] = s.Content
	}
	if got["pnpm.install"] != "pnpm add 'a(b)@1.0.0'\n" {
		t.Errorf("install %q", got["pnpm.install"])
	}
	if got["pnpm.configure.npmrc"] != "registry=https://trove.example.com/api/npm/it/npm-local/\n//trove.example.com/api/npm/it/npm-local/:_authToken=${MY_TOKEN}\n" {
		t.Errorf("npmrc %q", got["pnpm.configure.npmrc"])
	}
	if strings.Contains(w.Body.String(), "TROVE_TOKEN") {
		t.Error("the reference of the template survived")
	}
}

// TestR_GENERIC_016_TheClientSetupOfCurl: the curl snippets of a generic repository
// in every credential mode; the publish snippet only for a caller who may deploy.
func TestR_GENERIC_016_TheClientSetupOfCurl(t *testing.T) {
	function := func(header string) string {
		return "trove_curl() {\n  curl --silent --show-error --config - \"$@\" <<EOF\n" + header + "EOF\n}\n"
	}
	const (
		install = "trove_curl --fail --remote-name \"https://trove.example.com/files/it/files/<path>\"\n"
		publish = "trove_curl --fail --upload-file <file> \"https://trove.example.com/files/it/files/<path>\"\n"
	)
	for _, c := range []struct {
		credentials string
		deploy      bool
		want        map[string]string
	}{
		{"env", true, map[string]string{
			"curl.configure.function": function("header = \"Authorization: Bearer ${MY_TOKEN}\"\n"), "curl.install": install, "curl.publish": publish,
		}},
		{"placeholder", true, map[string]string{
			"curl.configure.function": function("header = \"Authorization: Bearer {{token}}\"\n"), "curl.install": install, "curl.publish": publish,
		}},
		{"none", true, map[string]string{"curl.configure.function": function(""), "curl.install": install, "curl.publish": publish}},
		{"env", false, map[string]string{"curl.configure.function": function("header = \"Authorization: Bearer ${MY_TOKEN}\"\n"), "curl.install": install}},
	} {
		w := get(setupServer(t, "https://trove.example.com", c.deploy), "/api/v1/projects/it/repositories/files/client-setup?client=curl&credentials="+c.credentials)
		var out gen.ClientSetup
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
			t.Fatalf("%s: %d %s", c.credentials, w.Code, w.Body.String())
		}
		got := map[string]string{}
		for _, sn := range out.Snippets {
			got[sn.Id] = sn.Content
			if sn.Language != gen.Shell || sn.FileName != nil {
				t.Errorf("%s: %+v", sn.Id, sn)
			}
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s, deploy %v:\n%q\nwant\n%q", c.credentials, c.deploy, got, c.want)
		}
		if out.RegistryUrl != "https://trove.example.com/files/it/files/" || out.Format != "generic" {
			t.Errorf("%+v", out)
		}
	}
}

func TestR_API_030_PublishTargetFollowsTheDeployRight(t *testing.T) {
	for deploy, want := range map[bool]bool{true: true, false: false} {
		w := get(setupServer(t, "https://trove.example.com", deploy), "/api/v1/projects/it/repositories/npm-local/client-setup?client=npm")
		var out gen.ClientSetup
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if (out.PublishTarget != nil) != want {
			t.Errorf("deploy=%v: %v", deploy, out.PublishTarget)
		}
	}
	// A remote repository takes no publish.
	w := get(setupServer(t, "https://trove.example.com", true), "/api/v1/projects/it/repositories/npm-remote/client-setup?client=npm")
	var out gen.ClientSetup
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.PublishTarget != nil {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

// TestR_API_030_AddressesNeverComeFromTheHostOfTheRequest: without a configured base
// URL the answer is 503 and not an address built from the request.
func TestR_API_030_AddressesNeverComeFromTheHostOfTheRequest(t *testing.T) {
	h := setupServer(t, "", true)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/projects/it/repositories/npm-local/client-setup?client=npm", nil)
	req.Host = "evil.example"
	h.ServeHTTP(w, req)
	if w.Code != 503 || strings.Contains(w.Body.String(), "evil.example") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestR_API_030_ClientSetupRefusesWhatItCannotAnswer(t *testing.T) {
	h := setupServer(t, "https://trove.example.com", true)
	for q, repo := range map[string]string{
		"client=bun":                                     "npm-local",
		"client=npm&credentials=token":                   "npm-local",
		"client=npm&package=trv_pat_abcdefgh":            "npm-local",
		"client=npm&scope=trv_sat_abcdefgh":              "npm-local",
		"client=npm&version=trv_cit_x&package=x":         "npm-local",
		"client=npm&package=" + url.QueryEscape("a\nb"):  "npm-local",
		"client=npm&package=" + strings.Repeat("x", 257): "npm-local",
		"client=npm&scope=%40a%2Fb":                      "npm-local",
		// Q-NPM-50: a package or version a client would read as an option, and a package
		// that is no npm name.
		"client=npm&package=" + url.QueryEscape("--registry=https://evil.example"): "npm-local",
		"client=npm&package=-g":                               "npm-local",
		"client=npm&package=x&version=--ignore-scripts=false": "npm-local",
		"client=npm&package=" + url.QueryEscape("a/b"):        "npm-local",
		"client=npm&package=" + url.QueryEscape("@scope"):     "npm-local",
		"client=npm&package=" + url.QueryEscape("a%2fb"):      "npm-local",
		"client=npm":            "files", // the generic format offers curl only
		"client=curl&package=x": "files", // and has no package names
		// Q-API-235: a format without a scoped variant refuses a scope instead of ignoring it.
		"client=curl&scope=acme":   "files",
		"client=docker&scope=acme": "oci-local",
	} {
		w := get(h, "/api/v1/projects/it/repositories/"+repo+"/client-setup?"+q)
		if w.Code != 400 {
			t.Errorf("%s: %d %s", q, w.Code, w.Body.String())
		}
		if strings.Contains(q, "scope=acme") && !strings.Contains(w.Body.String(), `parameter \"scope\" is not offered`) {
			t.Errorf("%s: the detail does not name scope: %s", q, w.Body.String())
		}
	}
	if w := get(h, "/api/v1/projects/it/repositories/ghost/client-setup?client=npm"); w.Code != 404 {
		t.Errorf("a missing repository: %d", w.Code)
	}
}

func TestR_API_029_TaggedAnswers(t *testing.T) {
	answer := func(v any, ifNoneMatch string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/x", nil)
		if ifNoneMatch != "" {
			r.Header.Set("If-None-Match", ifNoneMatch)
		}
		w := httptest.NewRecorder()
		writeTagged(w, r, v)
		return w
	}
	a := answer(map[string]int{"n": 1}, "")
	tag := a.Header().Get("ETag")
	if a.Code != 200 || len(tag) < 4 || tag[0] != '"' || strings.HasPrefix(tag, "W/") {
		t.Fatalf("%d %q", a.Code, tag)
	}
	if b := answer(map[string]int{"n": 1}, tag); b.Code != 304 || b.Body.Len() != 0 || b.Header().Get("ETag") != tag {
		t.Fatalf("%d", b.Code)
	}
	if c := answer(map[string]int{"n": 2}, tag); c.Code != 200 || c.Header().Get("ETag") == tag {
		t.Fatalf("a changed answer keeps its tag: %d %q", c.Code, c.Header().Get("ETag"))
	}
}

func TestR_API_029_DownloadURLs(t *testing.T) {
	for _, tc := range []struct{ format, repo, path, want string }{
		{"npm", "it/npm-local", "@team-a/ui/-/ui-1.4.0.tgz", "https://t.example/api/npm/it/npm-local/@team-a/ui/-/ui-1.4.0.tgz"},
		{"npm", "it/npm-local", "left-pad/-/left-pad-1.0.0.tgz", "https://t.example/api/npm/it/npm-local/left-pad/-/left-pad-1.0.0.tgz"},
		{"npm", "it/npm-local", "a b/-/a%b.tgz", "https://t.example/api/npm/it/npm-local/a%20b/-/a%25b.tgz"},
		{"generic", "it/files", "tools/widget 2.3/w.bin", "https://t.example/files/it/files/tools/widget%202.3/w.bin"},
	} {
		if got := downloadURL("https://t.example/", tc.format, mustRepo(t, tc.repo), tc.path); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestR_API_029_FilterParsing(t *testing.T) {
	spec := ListSpec{Filters: []string{"format", "project", "repository", "q", "namePrefix", "keyword", "createdFrom", "createdTo"}, Sorts: []string{"name", "createdAt", "updatedAt"}, DefaultSort: "name"}
	parse := func(query string) (ListQuery, error) {
		return ParseList(httptest.NewRequest("GET", "/api/v1/packages?"+query, nil), spec, apiCfg())
	}
	lq, err := parse("format=npm&format=nuget&repository=it/aa,it/bb,bare&q=x&createdFrom=2026-10-01T00:00:00Z&sort=-updatedAt")
	if err != nil {
		t.Fatal(err)
	}
	f, err := packageFilter(lq)
	if err != nil || len(f.Formats) != 2 || !f.HasRepositories || len(f.Repositories) != 2 || f.Repositories[1] != (qname.Repo{Project: "it", Key: "bb"}) || f.Q != "x" || f.CreatedFrom == nil || f.CreatedTo != nil {
		t.Fatalf("%+v %v", f, err)
	}
	if lq.Sort != "updatedAt" || !lq.Desc {
		t.Fatalf("sort %q desc %v", lq.Sort, lq.Desc)
	}
	lq, _ = parse("project=team-a")
	if f, _ := packageFilter(lq); f.Project != "team-a" || f.HasRepositories {
		t.Fatalf("the project filter is kept apart from the repository filter: %+v", f)
	}
	lq, _ = parse("")
	if f, _ := packageFilter(lq); f.HasRepositories {
		t.Fatal("no repository filter is a filter that names nothing")
	}
	lq, _ = parse("repository=")
	if f, _ := packageFilter(lq); !f.HasRepositories || len(f.Repositories) != 0 {
		t.Fatal("an empty repository filter lists nothing")
	}
	lq, _ = parse("createdTo=yesterday")
	if _, err := packageFilter(lq); err == nil {
		t.Fatal("a date that is none")
	}
	for _, bad := range []string{"sort=downloads", "colour=red"} {
		if _, err := parse(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

// TestR_API_030_NpmPublishSnippets: the npm templates deliver the publish snippets of
// R-UI-017 for every client, for a local repository and for a virtual one with a
// deployment target; the snippets address the repository the caller chose, whose
// credential the configure snippet binds (Q-API-180). Without the deploy right, for a
// virtual repository without a target and for a remote one there are none.
func TestR_API_030_NpmPublishSnippets(t *testing.T) {
	const local, all = "https://trove.example.com/api/npm/it/npm-local/", "https://trove.example.com/api/npm/it/npm-all/"
	publish := func(deploy bool, repo, client string) (map[string]gen.ClientSnippet, gen.ClientSetup) {
		t.Helper()
		w := get(setupServer(t, "https://trove.example.com", deploy), "/api/v1/projects/it/repositories/"+repo+"/client-setup?client="+client+"&credentials=env")
		var out gen.ClientSetup
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
			t.Fatalf("%s %s: %d %s", repo, client, w.Code, w.Body.String())
		}
		got := map[string]gen.ClientSnippet{}
		for _, sn := range out.Snippets {
			if sn.Tab == gen.ClientSnippetTabPublish {
				got[sn.Id] = sn
			}
		}
		return got, out
	}
	for _, c := range []struct {
		repo, url, client, id, content string
		lang                           gen.ClientSnippetLanguage
		file                           string
	}{
		{"npm-local", local, "npm", "npm.publish", "npm publish --registry " + local + "\n", gen.Shell, ""},
		{"npm-local", local, "pnpm", "pnpm.publish", "pnpm publish --registry " + local + "\n", gen.Shell, ""},
		{"npm-local", local, "yarn-classic", "yarn-classic.publish", "yarn publish --registry " + local + "\n", gen.Shell, ""},
		{"npm-local", local, "yarn", "yarn.publish", "npmPublishRegistry: \"" + local + "\"\n", gen.Yaml, ".yarnrc.yml"},
		{"npm-all", all, "npm", "npm.publish", "npm publish --registry " + all + "\n", gen.Shell, ""},
		{"npm-all", all, "yarn", "yarn.publish", "npmPublishRegistry: \"" + all + "\"\n", gen.Yaml, ".yarnrc.yml"},
	} {
		got, out := publish(true, c.repo, c.client)
		sn, ok := got[c.id]
		if !ok || len(got) != 1 {
			t.Errorf("%s %s: publish snippets %v", c.repo, c.client, got)
			continue
		}
		if sn.Content != c.content || sn.Language != c.lang || (sn.FileName == nil) != (c.file == "") || (c.file != "" && *sn.FileName != c.file) {
			t.Errorf("%s: %+v, want %q", c.id, sn, c.content)
		}
		if out.PublishTarget == nil || *out.PublishTarget != "it/npm-local" {
			t.Errorf("%s: publishTarget %v", c.repo, out.PublishTarget)
		}
	}
	for _, c := range []struct {
		deploy bool
		repo   string
	}{{false, "npm-local"}, {false, "npm-all"}, {true, "npm-readonly"}, {true, "npm-remote"}} {
		if got, out := publish(c.deploy, c.repo, "npm"); len(got) != 0 || out.PublishTarget != nil {
			t.Errorf("%s deploy=%v: %v %v", c.repo, c.deploy, got, out.PublishTarget)
		}
	}
}

func (f packagesReader) ListProject(context.Context, string) ([]repository.Repository, error) {
	return nil, nil
}

func (f packagesReader) ByID(context.Context, int64) (repository.Repository, error) {
	return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "no repository")
}

func mustRepo(t *testing.T, text string) qname.Repo {
	t.Helper()
	n, err := qname.ParseRepo(text)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
