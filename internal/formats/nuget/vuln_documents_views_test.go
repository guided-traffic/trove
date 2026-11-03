package nuget

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
)

// TestR_VULN_018_TheResourceIsNeitherListedNorServedOnAHiddenView (Q-VULN-71): the page names the packages of the installation's
// data minus its internal names, which on the cache view of a remote repository and on an endpoint with exposure rules would
// tell a caller which names a hidden package or a claim stands for; as the npm audit is not answered there (Q-VULN-19), the
// resource is not listed and its documents answer 404.
func TestR_VULN_018_TheResourceIsNeitherListedNorServedOnAHiddenView(t *testing.T) {
	data := newScanning()
	data.state = advisory.DataState{Complete: true, SyncedAt: time.Now(), Epoch: 1}
	h := vulnHost(t, formats.Remote, `{}`, data)
	serve := func(path string, mod func(*formats.Request)) int {
		t.Helper()
		rq, err := h.NewRequest(context.Background(), http.MethodGet, path, nil)
		if err != nil {
			t.Fatal(err)
		}
		mod(rq)
		return h.ServeRequest(rq, false).Code
	}
	for _, c := range []struct {
		name string
		mod  func(*formats.Request)
	}{
		{"the cache view", func(rq *formats.Request) { rq.CacheView = true }},
		{"an endpoint with exposure rules", func(rq *formats.Request) { rq.Endpoint = formats.EndpointFacts{Name: "customers", Rules: true} }},
	} {
		for _, path := range []string{"v3/vulnerabilities/index.json", "v3/vulnerabilities/trove.json"} {
			if got := serve(path, c.mod); got != http.StatusNotFound {
				t.Errorf("%s: %s answers %d, want 404", c.name, path, got)
			}
		}
	}
	// the default view still serves both.
	for _, path := range []string{"v3/vulnerabilities/index.json", "v3/vulnerabilities/trove.json"} {
		if got := serve(path, func(*formats.Request) {}); got != http.StatusOK {
			t.Errorf("the default view: %s answers %d", path, got)
		}
	}
}
