package dispatch

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/api/registry/route"
	_ "github.com/guided-traffic/trove/internal/formats/all" // registers the OCI format, whose routes the match reads
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/qname"
)

// The dispatcher's match below /v2/ is the one the enforcement point of the registry tokens' grants asks (Q-OCI-56).
// R-OCI-005 ("Use"): named in the test name by the mount of /v2/ once it runs.
func TestTheRegistryMatchIsTheDispatchers(t *testing.T) {
	r := newRig(t)
	d, err := New(Options{Host: fakeHost{begun: &r.calls}, Decider: r.decider, Repos: r.repos})
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("ab", 32)
	local := qname.Repo{Project: "web", Key: "docker-local"}
	hub := qname.Repo{Project: "web", Key: "docker-hub"}
	for _, c := range []struct {
		method, path string
		bound        qname.Repo
		want         route.Target
	}{
		{"GET", "/v2/", qname.Repo{}, route.Target{Route: route.NameVersion}},
		{"HEAD", "/v2", qname.Repo{}, route.Target{Route: route.NameVersion}},
		{"GET", "/v2/_catalog", qname.Repo{}, route.Target{Route: route.NameCatalog}},
		{"POST", "/v2/_token", qname.Repo{}, route.Target{Route: route.NameToken}},
		{"GET", "/v2/web/docker-local/library/nginx/manifests/1", qname.Repo{}, route.Target{Route: "oci.manifest.get.tag", Repo: local, Image: "library/nginx"}},
		{"PUT", "/v2/web/docker-local/library/nginx/manifests/1", qname.Repo{}, route.Target{Route: "oci.manifest.put", Repo: local, Image: "library/nginx"}},
		{"POST", "/v2/web/docker-local/app/blobs/uploads/", qname.Repo{}, route.Target{Route: "oci.upload.start", Repo: local, Image: "app"}},
		{"GET", "/v2/web/docker-hub~cache/library/alpine/blobs/" + digest, qname.Repo{}, route.Target{Route: "oci.blob.get", Repo: hub, Image: "library/alpine", CacheView: true}},
		{"GET", "/v2/library/alpine/manifests/3", hub, route.Target{Route: "oci.manifest.get.tag", Repo: hub, Image: "library/alpine", Bound: true}},
	} {
		req := httptest.NewRequestWithContext(context.Background(), c.method, c.path, nil)
		if !c.bound.IsZero() {
			req = req.WithContext(httpserver.WithResolved(req.Context(), httpserver.RequestInfo{RootRepository: c.bound}))
		}
		got, ok := d.RegistryTarget(req)
		if !ok || got != c.want {
			t.Errorf("%s %s: %+v %v, want %+v", c.method, c.path, got, ok, c.want)
		}
	}
	for _, path := range []string{
		"/v2/web", "/v2/web/docker-local", "/v2/Web/docker-local/app/manifests/1", "/v2/web%2Fx/docker-local/app/manifests/1",
		"/v2/web/docker-local//app/manifests/1", "/v2/web/docker-local/app/nothing", "/api/oci/web/docker-local/app/manifests/1",
	} {
		if got, ok := d.RegistryTarget(httptest.NewRequestWithContext(context.Background(), "GET", path, nil)); ok {
			t.Errorf("%s matched %+v", path, got)
		}
	}
}
