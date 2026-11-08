package registry_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/api/dispatch"
	"github.com/guided-traffic/trove/internal/api/registry"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// R-OCI-005, unit level: the end-to-end proof is in test/integration/registry.
func TestTheChallengeNamesNoScopeForAnInvalidImageName(t *testing.T) {
	disp, err := dispatch.New(dispatch.Options{Host: stubHost{}, Decider: stubDecider{}, Repos: stubRepos{}})
	if err != nil {
		t.Fatal(err)
	}
	e, err := registry.NewEnforcer(registry.EnforcerOptions{Matcher: disp, WriteError: writeError, ValidName: func(n string) bool { return n == "app" }})
	if err != nil {
		t.Fatal(err)
	}
	var oci formats.Format
	for _, f := range formats.List() {
		if f.Descriptor().RegistryRoot {
			oci = f
		}
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	if err := e.Mount(httpserver.NewClientRouter(), []string{"/v2/{rest...}"}, ok, registry.RepositoryRoutes(oci.Routes(), "/v2/web/docker-local/")...); err != nil {
		t.Fatal(err)
	}
	p := httpserver.Principal{Kind: httpserver.KindUser, Credential: httpserver.Credential{Registry: &httpserver.RegistryGrant{}}}
	for path, want := range map[string]string{
		"/v2/web/docker-local/app/manifests/1":       `scope="repository:web/docker-local/app:pull"`,
		"/v2/web/docker-local/ap%0d%0ap/manifests/1": "",
	} {
		r := withPrincipal(httptest.NewRequestWithContext(context.Background(), "GET", path, nil), p)
		ee, _ := errs.As(e.Check(r))
		if ee == nil || ee.Code != errs.CodeOCIInsufficientScope {
			t.Fatalf("%s: %v", path, ee)
		}
		if want != "" && !strings.Contains(ee.Challenge, want) || want == "" && strings.Contains(ee.Challenge, "scope=") {
			t.Errorf("%s: challenge %q", path, ee.Challenge)
		}
		if want == "" && e.Scope(r) != "" {
			t.Errorf("%s: Scope %q", path, e.Scope(r))
		}
	}
}

type unavailableRepos struct{ repository.Reader }

func (unavailableRepos) ByName(context.Context, qname.Repo) (repository.Repository, error) {
	return repository.Repository{}, errs.E(errs.Unavailable, errs.CodeInternal, "the store is away")
}

// A failed lookup of the bound repository is the service's trouble: a registry token is answered with it (503), not sent to
// fetch another token (W3-4).
// R-OCI-005, unit level: the end-to-end proof is in test/integration/registry.
func TestAFailedBindingLookupIsNotAMissingScope(t *testing.T) {
	disp, err := dispatch.New(dispatch.Options{Host: stubHost{}, Decider: stubDecider{}, Repos: unavailableRepos{}})
	if err != nil {
		t.Fatal(err)
	}
	e, err := registry.NewEnforcer(registry.EnforcerOptions{Matcher: disp, WriteError: writeError})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequestWithContext(context.Background(), "GET", "/v2/library/alpine/manifests/3", nil)
	ctx := httpserver.WithResolved(r.Context(), httpserver.RequestInfo{BaseURL: "https://trove.test", RootRepository: hub})
	ctx = httpserver.WithPrincipal(ctx, httpserver.Principal{Kind: httpserver.KindUser, Credential: httpserver.Credential{Registry: &httpserver.RegistryGrant{}}})
	if ee, ok := errs.As(e.Check(r.WithContext(ctx))); !ok || ee.Kind != errs.Unavailable {
		t.Fatalf("%v", ee)
	}
}
