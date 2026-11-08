package registry_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/api/dispatch"
	"github.com/guided-traffic/trove/internal/api/registry"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/oci"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

func withPrincipal(r *http.Request, p httpserver.Principal) *http.Request {
	ctx := httpserver.WithResolved(r.Context(), httpserver.RequestInfo{BaseURL: "https://trove.test"})
	return r.WithContext(httpserver.WithPrincipal(ctx, p))
}

// R-OCI-003, unit level: the end-to-end proof is in test/integration/registry.
func TestTheVersionCheckAnswersAccordingToTheCredential(t *testing.T) {
	h := registry.NewVersion(registry.VersionOptions{WriteError: writeError})
	user := httpserver.Principal{Kind: httpserver.KindUser, ID: alice.ID, Name: "alice"}
	anonToken := httpserver.AnonymousPrincipal()
	anonToken.Credential.Registry = &httpserver.RegistryGrant{}
	for name, c := range map[string]struct {
		method string
		p      httpserver.Principal
		code   int
	}{
		"a principal":                        {"GET", user, 200},
		"a principal, HEAD":                  {"HEAD", user, 200},
		"the anonymous registry token":       {"GET", anonToken, 200},
		"no credential":                      {"GET", httpserver.AnonymousPrincipal(), 401},
		"no credential, HEAD":                {"HEAD", httpserver.AnonymousPrincipal(), 401},
		"a method the version check lacks":   {"POST", user, 405},
		"a method it lacks, no credential":   {"DELETE", httpserver.AnonymousPrincipal(), 405},
		"a registry token of a principal":    {"GET", httpserver.Principal{Kind: httpserver.KindUser, ID: alice.ID, Credential: httpserver.Credential{Registry: &httpserver.RegistryGrant{}}}, 200},
		"an anonymous caller with a grant":   {"GET", httpserver.Principal{Anonymous: true, Credential: httpserver.Credential{Registry: &httpserver.RegistryGrant{Catalog: true}}}, 200},
		"a principal that is also anonymous": {"GET", httpserver.Principal{Anonymous: true}, 401},
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, withPrincipal(httptest.NewRequestWithContext(context.Background(), c.method, "/v2/", nil), c.p))
			if w.Code != c.code || w.Header().Get(registry.APIVersionHeader) != "registry/2.0" {
				t.Fatalf("%d %v", w.Code, w.Header())
			}
			switch c.code {
			case 200:
				if c.method == "GET" && w.Body.String() != "{}" || c.method == "HEAD" && w.Body.Len() != 0 {
					t.Fatalf("body %q", w.Body.String())
				}
			case 401:
				if want := `Bearer realm="https://trove.test/v2/_token",service="trove.test"`; w.Header().Get("WWW-Authenticate") != want {
					t.Fatalf("challenge %q", w.Header().Get("WWW-Authenticate"))
				}
			}
		})
	}
}

// catalogDecider reads: it knows the repositories a subject may read and, per repository, the package names it may read.
type catalogDecider struct {
	authz.Decider
	repos map[qname.Repo]func(name string) bool
	seen  *[]string // the paths the catalog asked about
}

func (d catalogDecider) Begin(context.Context, authz.Subject, ...qname.Repo) (*authz.Index, error) {
	return nil, nil
}

func (d catalogDecider) ReadableOn(_ context.Context, _ *authz.Index, _ authz.Subject, keys []qname.Repo) ([]qname.Repo, error) {
	var out []qname.Repo
	for _, k := range keys {
		if _, ok := d.repos[k]; ok {
			out = append(out, k)
		}
	}
	return out, nil
}

func (d catalogDecider) PathsAllowedOn(_ context.Context, _ *authz.Index, _ authz.Subject, base authz.Coordinate, _ authz.Action, paths []string, classify authz.Classifier) ([]bool, error) {
	out := make([]bool, len(paths))
	for i, p := range paths {
		if d.seen != nil {
			*d.seen = append(*d.seen, p)
		}
		pkg, _ := classify(p)
		out[i] = d.repos[base.Repository](pkg)
	}
	return out, nil
}

type catalogRepos struct {
	repository.Reader
	list []repository.Repository
}

func (r catalogRepos) List(context.Context) ([]repository.Repository, error) { return r.list, nil }

type namesStore struct {
	mu    sync.Mutex
	names map[int64][]string
	calls int
}

func (s *namesStore) CatalogNames(_ context.Context, id int64, after string, limit int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	var out []string
	for _, n := range s.names[id] {
		if n > after && len(out) < limit {
			out = append(out, n)
		}
	}
	return out, nil
}

// R-OCI-017, unit level: the end-to-end proof is in test/integration/registry.
func TestTheCatalogPagesOverRepositoriesInByteOrder(t *testing.T) {
	a := qname.Repo{Project: "web", Key: "a"}
	ab := qname.Repo{Project: "web", Key: "a-b"} // "web/a-b/" sorts before "web/a/": '-' is below '/'
	virt := qname.Repo{Project: "web", Key: "virt"}
	hidden := qname.Repo{Project: "web", Key: "hidden"}
	gone := qname.Repo{Project: "web", Key: "gone"}
	repos := catalogRepos{list: []repository.Repository{
		{ID: "1", Name: a, Class: repository.Local, Format: "oci"},
		{ID: "2", Name: ab, Class: repository.Remote, Format: "oci"},
		{ID: "3", Name: virt, Class: repository.Virtual, Format: "oci"},
		{ID: "4", Name: hidden, Class: repository.Local, Format: "oci"},
		{ID: "5", Name: qname.Repo{Project: "web", Key: "files"}, Class: repository.Local, Format: "generic"},
		{ID: "6", Name: gone, Class: repository.Local, Format: "oci", Lifecycle: repository.Deleting},
		{ID: "7", Name: qname.Repo{Project: "web", Key: "off"}, Class: repository.Local, Format: "oci", Disabled: true},
		{ID: "x", Name: qname.Repo{Project: "web", Key: "nonum"}, Class: repository.Local, Format: "oci"},
	}}
	store := &namesStore{names: map[int64][]string{1: {"app", "team/x", "team/y", "zed"}, 2: {"lib"}, 3: {"nope"}, 4: {"secret"}, 6: {"dead"}}}
	var asked []string
	decider := catalogDecider{seen: &asked, repos: map[qname.Repo]func(string) bool{
		a:    func(n string) bool { return n != "team/y" }, // a grant scoped to a package pattern: one name of the repository is hidden
		ab:   func(string) bool { return true },
		virt: func(string) bool { return true }, gone: func(string) bool { return true },
	}}
	var bound qname.Repo
	c, err := registry.NewCatalog(registry.CatalogOptions{
		Repos: repos, Decider: decider, Store: store, Format: "oci", Documents: registry.NewDocuments(oci.New()), WriteError: writeError,
		Bound: func(*http.Request) (qname.Repo, bool, error) { return bound, !bound.IsZero(), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	get := func(query string) (names []string, link string, code int) {
		w := httptest.NewRecorder()
		c.ServeHTTP(w, withPrincipal(httptest.NewRequestWithContext(context.Background(), "GET", "/v2/_catalog"+query, nil), httpserver.Principal{Kind: httpserver.KindUser, ID: alice.ID}))
		var body struct {
			Repositories []string `json:"repositories"`
		}
		if w.Code == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Repositories == nil {
				t.Fatalf("body %q: %v", w.Body.String(), err)
			}
		}
		return body.Repositories, w.Header().Get("Link"), w.Code
	}
	all := []string{"web/a-b/lib", "web/a/app", "web/a/team/x", "web/a/zed"}
	t.Run("the names the caller may read, in byte order; virtual, other-format, deleting, disabled and hidden repositories give none", func(t *testing.T) {
		if got, link, code := get(""); code != 200 || !slices.Equal(got, all) || link != "" {
			t.Fatalf("%d %v %q", code, got, link)
		}
	})
	t.Run("R-OCI-017 each name is decided on the path of its package document, the tags list", func(t *testing.T) {
		asked = nil // the decision of Q-OCI-140: the package document, not the bare name
		if got, _, code := get(""); code != 200 || !slices.Equal(got, all) {
			t.Fatalf("%d %v", code, got)
		}
		for _, p := range []string{"app/_taglist", "team/x/_taglist", "zed/_taglist"} {
			if !slices.Contains(asked, p) {
				t.Errorf("the catalog did not decide %q (decided: %v)", p, asked)
			}
		}
		for _, p := range asked {
			if !strings.HasSuffix(p, "/_taglist") {
				t.Errorf("the catalog decided the path %q, which is no package document", p)
			}
		}
	})
	t.Run("every page boundary continues exactly after its last name", func(t *testing.T) {
		for n := 1; n <= 5; n++ {
			var seen []string
			q := "?n=" + string(rune('0'+n))
			for i := 0; i < 10; i++ {
				got, link, _ := get(q)
				seen = append(seen, got...)
				if link == "" {
					break
				}
				last := got[len(got)-1]
				if want := `</v2/_catalog?last=` + last + `&n=` + string(rune('0'+n)) + `>; rel="next"`; link != want {
					t.Fatalf("n=%d: link %q, want %q", n, link, want)
				}
				q = "?n=" + string(rune('0'+n)) + "&last=" + last
			}
			if !slices.Equal(seen, all) {
				t.Errorf("n=%d: pages %v", n, seen)
			}
		}
	})
	t.Run("last may name a name that does not exist, or a repository that is not listed", func(t *testing.T) {
		for last, want := range map[string][]string{
			"web/a/b":        {"web/a/team/x", "web/a/zed"},
			"web/a-b/lib":    all[1:],
			"web/a-b/":       all,
			"web":            all,
			"web/zzz/x":      nil,
			"web/a/zed":      nil,
			"web/hidden/aaa": all[:0],
		} {
			if got, _, code := get("?last=" + last); code != 200 || len(got) != len(want) {
				t.Errorf("last=%s: %d %v, want %v", last, code, got, want)
			}
		}
	})
	t.Run("n is capped at 1000, n=0 is empty, anything but a non-negative integer is 400", func(t *testing.T) {
		if got, link, _ := get("?n=0"); len(got) != 0 || link != "" {
			t.Fatalf("n=0 %v %q", got, link)
		}
		if got, _, _ := get("?n=100000"); len(got) != len(all) {
			t.Fatalf("n=100000 %v", got)
		}
		for _, q := range []string{"?n=-1", "?n=x", "?n=1&n=2", "?last=a&last=b", "?n="} {
			if _, _, code := get(q); code != 400 {
				t.Errorf("%s: %d", q, code)
			}
		}
	})
	t.Run("on a bound host the bound repository's names are listed without a prefix", func(t *testing.T) {
		bound = a
		defer func() { bound = qname.Repo{} }()
		if got, _, _ := get(""); !slices.Equal(got, []string{"app", "team/x", "zed"}) {
			t.Fatalf("%v", got)
		}
		if got, _, _ := get("?last=app&n=1"); !slices.Equal(got, []string{"team/x"}) {
			t.Fatalf("%v", got)
		}
		bound = virt // a virtual repository is not expanded
		if got, _, _ := get(""); len(got) != 0 {
			t.Fatalf("%v", got)
		}
	})
	t.Run("a port without a page answers 501", func(t *testing.T) {
		noStore, _ := registry.NewCatalog(registry.CatalogOptions{
			Repos: repos, Decider: decider, Format: "oci", Documents: registry.NewDocuments(oci.New()), WriteError: writeError,
			Bound: func(*http.Request) (qname.Repo, bool, error) { return qname.Repo{}, false, nil },
		})
		w := httptest.NewRecorder()
		noStore.ServeHTTP(w, withPrincipal(httptest.NewRequestWithContext(context.Background(), "GET", "/v2/_catalog", nil), httpserver.Principal{Kind: httpserver.KindUser}))
		if w.Code != http.StatusNotImplemented {
			t.Fatalf("%d", w.Code)
		}
	})
	t.Run("a method other than GET and HEAD is 405", func(t *testing.T) {
		w := httptest.NewRecorder()
		c.ServeHTTP(w, withPrincipal(httptest.NewRequestWithContext(context.Background(), "POST", "/v2/_catalog", nil), httpserver.Principal{Kind: httpserver.KindUser}))
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%d", w.Code)
		}
	})
	if _, err := registry.NewCatalog(registry.CatalogOptions{}); err == nil {
		t.Fatal("a catalog without its parts was created")
	}
}

func TestEnforcerScopeIsTheDispatchersMatchWithTheMountedAction(t *testing.T) {
	m := newMount(t, nil, "")
	scope := func(method, path string) string {
		return m.enforcer.Scope(withPrincipal(httptest.NewRequestWithContext(context.Background(), method, path, nil), httpserver.AnonymousPrincipal()))
	}
	for _, c := range []struct{ method, path, want string }{
		{"GET", "/v2/web/docker-local/library/app/manifests/1", "repository:web/docker-local/library/app:pull"},
		{"PUT", "/v2/web/docker-local/app/manifests/1", "repository:web/docker-local/app:push"},
		{"DELETE", "/v2/web/docker-local/app/manifests/1", "repository:web/docker-local/app:delete"},
		{"GET", "/v2/web/docker-hub~cache/lib/blobs/sha256:" + strings.Repeat("a", 64), "repository:web/docker-hub~cache/lib:pull"},
		{"GET", "/v2/_catalog", "registry:catalog:*"},
		{"GET", "/v2/", ""},
		{"GET", "/v2/_token", ""},
		{"GET", "/v2/web", ""},
		{"TRACE", "/v2/web/docker-local/app/manifests/1", ""},
	} {
		if got := scope(c.method, c.path); got != c.want {
			t.Errorf("%s %s: %q, want %q", c.method, c.path, got, c.want)
		}
	}
	var none *registry.Enforcer
	if none.Scope(httptest.NewRequestWithContext(context.Background(), "GET", "/v2/_catalog", nil)) != "" || none.UncleanedPrefixes() != nil {
		t.Fatal("a nil enforcer has a scope")
	}
}

func TestRegisterMountsOnePatternBehindTheEnforcer(t *testing.T) {
	var oci formats.Format
	for _, f := range formats.List() {
		if f.Descriptor().RegistryRoot {
			oci = f
		}
	}
	repos := &catalogRepos{list: nil}
	disp, err := dispatch.New(dispatch.Options{Host: stubHost{}, Decider: stubDecider{}, Repos: repos})
	if err != nil {
		t.Fatal(err)
	}
	e, err := registry.NewEnforcer(registry.EnforcerOptions{Matcher: disp, WriteError: oci.WriteError})
	if err != nil {
		t.Fatal(err)
	}
	router := httpserver.NewClientRouter()
	core := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	o := registry.RegisterOptions{Router: router, Enforcer: e, Dispatcher: disp, Format: oci, Version: core, Catalog: core, Token: core}
	for name, broken := range map[string]func(*registry.RegisterOptions){
		"no router": func(o *registry.RegisterOptions) { o.Router = nil }, "no enforcer": func(o *registry.RegisterOptions) { o.Enforcer = nil },
		"no dispatcher": func(o *registry.RegisterOptions) { o.Dispatcher = nil }, "no format": func(o *registry.RegisterOptions) { o.Format = nil },
		"no version check": func(o *registry.RegisterOptions) { o.Version = nil }, "no catalog": func(o *registry.RegisterOptions) { o.Catalog = nil },
		"no token service": func(o *registry.RegisterOptions) { o.Token = nil },
	} {
		bad := o
		broken(&bad)
		if err := registry.Register(bad); err == nil {
			t.Errorf("%s: registered", name)
		}
	}
	if len(router.Patterns()) != 2 { // the router's own row and nothing yet
		t.Fatalf("a refused registration left patterns: %v", router.Patterns())
	}
	if err := registry.Register(o); err != nil {
		t.Fatal(err)
	}
	if got := e.Patterns(); !slices.Equal(got, []string{registry.BarePattern, registry.Pattern}) {
		t.Fatalf("patterns %v", got)
	}
	if got := router.UncleanedPrefixes(); !slices.Equal(got, []string{"/v2/"}) || !slices.Equal(got, e.UncleanedPrefixes()) {
		t.Fatalf("uncleaned %v / %v", got, e.UncleanedPrefixes())
	}
	if v := httpserver.CheckRouter(router, e.Patterns(), e.UncleanedPrefixes(), nil); len(v) != 0 {
		t.Fatalf("the route table refuses what Register mounted: %v", v)
	}
	names := map[string]bool{}
	for _, r := range e.Routes() {
		names[r.Name] = true
	}
	for _, r := range oci.Routes() {
		if !names[r.Name] {
			t.Errorf("route %s is not registered", r.Name)
		}
	}
}
