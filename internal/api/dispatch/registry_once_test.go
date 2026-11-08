package dispatch

import (
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/api/registry/route"
)

// A request is matched once (W3-2): with a memo in its context the second read, by the handler behind the enforcement point,
// is the first one's answer: the handler serves the route that was matched even when the binding is gone by then.
// R-OCI-005, unit level: the end-to-end proof is in test/integration/registry.
func TestARequestIsMatchedOnce(t *testing.T) {
	r := newOCIRig(t)
	req := route.WithMemo(httptestRequest("GET", "/v2/library/alpine/manifests/3", ociHub))
	first, err := r.d.RegistryMatch(req)
	if err != nil || first.Repo != ociHub || !first.Bound {
		t.Fatalf("%+v %v", first, err)
	}
	// The binding disappears between the check and the handler: the handler still serves what was checked.
	delete(r.repos.byKey, ociHub)
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	if len(r.labels) != 1 || r.labels[0] != "oci.manifest.get.tag" {
		t.Fatalf("the handler served %v, not the matched route (%d)", r.labels, rec.Code)
	}
	// Without a memo every read asks.
	if _, ok := r.d.RegistryTarget(httptestRequest("GET", "/v2/library/alpine/manifests/3", ociHub)); ok {
		t.Fatal("the removed binding still matched without a memo")
	}
}
