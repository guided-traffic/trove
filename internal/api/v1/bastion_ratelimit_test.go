package v1

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// TestR_BAS_008_TheInterfaceOfABastionIsCountedAgainstItsTokenInTheReadClass: the check and the report of downloads
// are POSTs, and an interface request is of the registration and not of the address it came from.
func TestR_BAS_008_TheInterfaceOfABastionIsCountedAgainstItsTokenInTheReadClass(t *testing.T) {
	bastion := httpserver.Principal{
		Kind: httpserver.KindBastion, ID: "0192f1c4-0000-7000-8000-0000000000b1", Name: "partners",
		Credential: httpserver.Credential{TokenID: "0192f1c4-0000-7000-8000-000000000001"},
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, iface.PathHandshake}, {http.MethodPost, iface.PathCheck}, {http.MethodGet, iface.PathFeed}, {http.MethodPost, iface.PathDownloads},
	} {
		r := httptest.NewRequest(c.method, c.path, nil)
		r = r.WithContext(httpserver.WithPrincipal(r.Context(), bastion))
		limiter, bucket := classify(r)
		if limiter != LimiterRead || bucket != "token:0192f1c4-0000-7000-8000-000000000001" {
			t.Errorf("%s %s: %s %s", c.method, c.path, limiter, bucket)
		}
	}
	// A person's POST stays in the write class.
	user := httpserver.Principal{Kind: httpserver.KindUser, ID: "u1", Name: "alice", Credential: httpserver.Credential{TokenID: "t1"}}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/tokens", nil)
	r = r.WithContext(httpserver.WithPrincipal(r.Context(), user))
	if limiter, _ := classify(r); limiter != LimiterWrite {
		t.Errorf("a user's POST: %s", limiter)
	}
}
