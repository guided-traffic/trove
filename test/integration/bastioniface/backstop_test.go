//go:build integration

package bastioniface

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/errs"
)

// TestR_BAS_014_AForwardOutsideThePackageFamiliesReachesNoRoute: through the real chain, the real authentication
// stage and the real routes of the management API (Q-BAS-115). A forward - a request with the Bastion token in
// Trove-Bastion-Authorization - whose path the router decodes to a route of /api/v1/ answers 404 not_found and runs
// nothing with the caller's credential, also when the path decodes to an interface route; that answer is never
// bastion_token_invalid, which a Bastion would read as the refusal of its own token. The same paths without the
// forward header are routed as the router decodes them, which proves the escapes reach those routes.
func TestR_BAS_014_AForwardOutsideThePackageFamiliesReachesNoRoute(t *testing.T) {
	e := newEnv(t)
	s := e.newSite(siteOpts{})
	bst := s.registered("partners", e.registration("partners"))
	sa := s.serviceAccount("acme-full")
	caller := s.sat(sa).Secret
	v := strconv.Itoa(iface.InterfaceVersion)

	for _, path := range []string{"/api/v1/tokens", "/api/%76%31/tokens", "/api/%761/bastion/handshake", iface.PathFeed, "/api/v1/settings/auth"} {
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			rp := s.do(call{method: method, path: path, header: http.Header{
				"Authorization": {"Bearer " + caller}, iface.HeaderAuthorization: {"Bearer " + bst}, iface.HeaderInterface: {v},
			}})
			if rp.Status != http.StatusNotFound || rp.code() != errs.CodeNotFound {
				t.Errorf("%s %s forwarded: %d %s, want 404 not_found", method, path, rp.Status, rp.Body)
			}
			if rp.code() == errs.CodeBastionTokenInvalid {
				t.Errorf("%s %s forwarded: the answer refuses the Bastion's own token", method, path)
			}
		}
	}

	// The router decodes the escapes: without the forward header the same paths reach their routes.
	if rp := s.do(call{method: http.MethodGet, path: "/api/%76%31/tokens", header: http.Header{"Authorization": {"Bearer " + caller}}}); rp.Status == http.StatusNotFound {
		t.Errorf("GET /api/%%76%%31/tokens with the caller's own token: %d %s, want the token list's answer", rp.Status, rp.Body)
	}
	if rp := s.iface(bst, http.MethodGet, "/api/%761/bastion/handshake", nil); rp.Status != http.StatusOK {
		t.Errorf("the handshake on a path the router decodes to it, as an interface call: %d %s", rp.Status, rp.Body)
	}
	// A forward of a package family is served.
	if rp := s.do(packageRequest("/api/npm/npm-all/lodash", "Authorization", "Bearer "+caller, iface.HeaderAuthorization, "Bearer "+bst, iface.HeaderInterface, v)); rp.Status != http.StatusOK {
		t.Errorf("a forward of a package route: %d %s", rp.Status, rp.Body)
	}
}

// TestR_BAS_017_AnUnrecognizedRequestFromTheBastionsEgressForwardsNothing: the Bastion's egress is in
// bastions.trusted_proxies, not in server.trusted_proxies (Q-BAS-116). A request from that address without a valid
// Bastion token - any workload that shares the egress - cannot choose its client address, which the rate limits,
// the failed-credential throttle and the audit use, nor the response base URL.
func TestR_BAS_017_AnUnrecognizedRequestFromTheBastionsEgressForwardsNothing(t *testing.T) {
	e := newEnv(t)
	s := e.newSite(siteOpts{additional: []string{"https://packages.example.com"}, endpointAnonymous: true})
	bst := s.registered("partners", e.registration("partners"))
	on := true
	if _, err := s.setts.Update(context.Background(), admin, token.SettingsPatch{AnonymousAccess: &on, Expected: errs.AnyRevision}); err != nil {
		t.Fatal(err)
	}
	v := strconv.Itoa(iface.InterfaceVersion)
	var got echo
	rp := s.do(packageRequest("/api/npm/npm-all/lodash", bastionForwarded("203.0.113.7")...))
	rp.decode(t, &got)
	if rp.Status != http.StatusOK || got.Client == "203.0.113.7" || got.BaseURL == "https://packages.example.com" {
		t.Errorf("a request without a Bastion token chose its address or base URL: %d %+v", rp.Status, got)
	}
	// The same headers with the Bastion token count.
	rp = s.do(packageRequest("/api/npm/npm-all/lodash", append(bastionForwarded("203.0.113.7"), iface.HeaderAuthorization, "Bearer "+bst, iface.HeaderInterface, v)...))
	rp.decode(t, &got)
	if got.Client != "203.0.113.7" || got.BaseURL != "https://packages.example.com" {
		t.Errorf("a recognized request: %d %+v", rp.Status, got)
	}
}
