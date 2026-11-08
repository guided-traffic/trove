package registry_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/api/dispatch"
	"github.com/guided-traffic/trove/internal/api/registry"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// R-OCI-004 step 1 (unit level): the generic challenge of authz.Unauthenticated, which a caller without a credential meets for a private
// repository where anonymous access is on, becomes the registry's, with the scope the route needs.
func TestABareChallengeBelowV2BecomesTheRegistrys(t *testing.T) {
	disp, err := dispatch.New(dispatch.Options{Host: stubHost{}, Decider: stubDecider{}, Repos: stubRepos{}})
	if err != nil {
		t.Fatal(err)
	}
	e, err := registry.NewEnforcer(registry.EnforcerOptions{Matcher: disp, WriteError: writeError, ValidName: func(string) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	var oci formats.Format
	for _, f := range formats.List() {
		if f.Descriptor().RegistryRoot {
			oci = f
		}
	}
	unauthenticated := func(w http.ResponseWriter, _ *http.Request) {
		err := authz.Unauthenticated()
		w.Header().Set("WWW-Authenticate", err.Challenge)
		w.WriteHeader(http.StatusUnauthorized)
	}
	router := httpserver.NewClientRouter()
	if err := e.Mount(router, []string{"/v2/{rest...}"}, http.HandlerFunc(unauthenticated), registry.RepositoryRoutes(oci.Routes(), "/v2/web/docker-local/")...); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"/v2/web/docker-local/app/manifests/1":    `Bearer realm="https://trove.test/v2/_token",service="trove.test",scope="repository:web/docker-local/app:pull"`,
		"/v2/web/docker-local/app/blobs/uploads/": `Bearer realm="https://trove.test/v2/_token",service="trove.test",scope="repository:web/docker-local/app:push"`,
	} {
		method := http.MethodGet
		if path == "/v2/web/docker-local/app/blobs/uploads/" {
			method = http.MethodPost
		}
		r := withPrincipal(httptest.NewRequestWithContext(context.Background(), method, path, nil), httpserver.AnonymousPrincipal())
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if got := w.Header().Get("WWW-Authenticate"); w.Code != http.StatusUnauthorized || got != want {
			t.Errorf("%s: %d %q, want %q", path, w.Code, got, want)
		}
	}
}
