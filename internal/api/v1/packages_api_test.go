package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	_ "github.com/guided-traffic/trove/internal/formats/all"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/packages/view"
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
		"/api/v1/repositories/npm-local/client-setup?client=npm",
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

func (f packagesReader) ByKey(_ context.Context, key string) (repository.Repository, error) {
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

func (packagesDecider) Begin(context.Context, authz.Subject, ...string) (*authz.Index, error) {
	return nil, nil
}

func (f packagesDecider) CheckOn(_ *authz.Index, _ authz.Subject, items []authz.CheckItem) ([]bool, error) {
	out := make([]bool, len(items))
	for i := range items {
		out[i] = f.deploy
	}
	return out, nil
}

func (packagesDecider) ReadableOn(context.Context, *authz.Index, authz.Subject, []string) ([]string, error) {
	return nil, nil
}

type packagesScoper struct{}

func (packagesScoper) ReadScopeOn(context.Context, *authz.Index, authz.Subject, []string) (authz.ReadScope, error) {
	return nil, nil
}

func setupServer(t *testing.T, baseURL string, deploy bool) http.Handler {
	t.Helper()
	doc, err := LoadDocument(EmbeddedYAML())
	if err != nil {
		t.Fatal(err)
	}
	repos := packagesReader{repos: map[string]repository.Repository{
		"npm-local":  {Key: "npm-local", Class: repository.Local, Format: "npm"},
		"npm-remote": {Key: "npm-remote", Class: repository.Remote, Format: "npm"},
		"files":      {Key: "files", Class: repository.Local, Format: "generic"},
	}}
	router := httpserver.NewClientRouter()
	New(Options{
		Document: doc, Identity: Identity{Replica: "r", Role: "web"}, Ready: func() bool { return true },
		Access: allowAll{}, Replicas: fakeReplicas{}, Facts: fakeFacts{},
		Packages: PackageServices{
			View: &view.Service{}, Decider: packagesDecider{deploy: deploy}, Scoper: packagesScoper{}, Reader: repos,
			TokenEnvVar: "MY_TOKEN", TokenTTL: 36 * 3600 * 1e9,
		},
	}).Register(router)
	res, err := httpserver.NewResolver(nil, baseURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	chain := httpserver.Chain{Router: router, Resolver: res, Authenticate: authed, Replica: "r", ReplicaHeader: httpserver.ReplicaAuthenticated}
	return chain.Handler()
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

func TestR_API_030_ClientSetupAnswer(t *testing.T) {
	h := setupServer(t, "https://trove.example.com", true)
	w := get(h, "/api/v1/repositories/npm-local/client-setup?client=pnpm&credentials=env&package=a(b)&version=1.0.0")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var out gen.ClientSetup
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.RegistryUrl != "https://trove.example.com/api/npm/npm-local/" || out.EndpointPath != "//trove.example.com/api/npm/npm-local/" ||
		out.Username == nil || *out.Username != "alice" || out.PublishTarget == nil || *out.PublishTarget != "npm-local" || out.TokenTtlSeconds != 36*3600 {
		t.Fatalf("%+v", out)
	}
	got := map[string]string{}
	for _, s := range out.Snippets {
		got[s.Id] = s.Content
	}
	if got["pnpm.install"] != "pnpm add 'a(b)@1.0.0'\n" {
		t.Errorf("install %q", got["pnpm.install"])
	}
	if got["pnpm.configure.npmrc"] != "registry=https://trove.example.com/api/npm/npm-local/\n//trove.example.com/api/npm/npm-local/:_authToken=${MY_TOKEN}\n" {
		t.Errorf("npmrc %q", got["pnpm.configure.npmrc"])
	}
	if strings.Contains(w.Body.String(), "TROVE_TOKEN") {
		t.Error("the reference of the template survived")
	}
}

func TestR_API_030_PublishTargetFollowsTheDeployRight(t *testing.T) {
	for deploy, want := range map[bool]bool{true: true, false: false} {
		w := get(setupServer(t, "https://trove.example.com", deploy), "/api/v1/repositories/npm-local/client-setup?client=npm")
		var out gen.ClientSetup
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if (out.PublishTarget != nil) != want {
			t.Errorf("deploy=%v: %v", deploy, out.PublishTarget)
		}
	}
	// A remote repository takes no publish.
	w := get(setupServer(t, "https://trove.example.com", true), "/api/v1/repositories/npm-remote/client-setup?client=npm")
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
	req := httptest.NewRequest("GET", "/api/v1/repositories/npm-local/client-setup?client=npm", nil)
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
		"client=npm": "files", // the generic format has no setup templates
	} {
		w := get(h, "/api/v1/repositories/"+repo+"/client-setup?"+q)
		if w.Code != 400 {
			t.Errorf("%s: %d %s", q, w.Code, w.Body.String())
		}
	}
	if w := get(h, "/api/v1/repositories/ghost/client-setup?client=npm"); w.Code != 404 {
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
		{"npm", "npm-local", "@team-a/ui/-/ui-1.4.0.tgz", "https://t.example/api/npm/npm-local/@team-a/ui/-/ui-1.4.0.tgz"},
		{"npm", "npm-local", "left-pad/-/left-pad-1.0.0.tgz", "https://t.example/api/npm/npm-local/left-pad/-/left-pad-1.0.0.tgz"},
		{"npm", "npm-local", "a b/-/a%b.tgz", "https://t.example/api/npm/npm-local/a%20b/-/a%25b.tgz"},
		{"generic", "files", "tools/widget 2.3/w.bin", "https://t.example/files/files/tools/widget%202.3/w.bin"},
	} {
		if got := downloadURL("https://t.example/", tc.format, tc.repo, tc.path); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestR_API_029_FilterParsing(t *testing.T) {
	spec := ListSpec{Filters: []string{"format", "repository", "q", "namePrefix", "keyword", "createdFrom", "createdTo"}, Sorts: []string{"name", "createdAt", "updatedAt"}, DefaultSort: "name"}
	parse := func(query string) (ListQuery, error) {
		return ParseList(httptest.NewRequest("GET", "/api/v1/packages?"+query, nil), spec, apiCfg())
	}
	lq, err := parse("format=npm&format=nuget&repository=a,b&q=x&createdFrom=2026-10-01T00:00:00Z&sort=-updatedAt")
	if err != nil {
		t.Fatal(err)
	}
	f, err := packageFilter(lq)
	if err != nil || len(f.Formats) != 2 || !f.HasRepositories || len(f.Repositories) != 2 || f.Q != "x" || f.CreatedFrom == nil || f.CreatedTo != nil {
		t.Fatalf("%+v %v", f, err)
	}
	if lq.Sort != "updatedAt" || !lq.Desc {
		t.Fatalf("sort %q desc %v", lq.Sort, lq.Desc)
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
