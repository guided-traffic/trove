//go:build integration

package bastioniface

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/errs"
)

// TestR_BAS_008_AFloodOfBastionTokensFromOneAddressIsThrottledBeforeTheTokenStore: the recognition runs before the
// authentication stage, so it applies the failed-credential throttle of R-AUTH-038 itself: well-formed tokens
// that name no registration count as failures of the address that sent them, and from the budget on that address
// is refused 429 without a lookup, while another address is not slowed down, and a Bastion token the replica
// holds still passes from the throttled address (Q-BAS-65). The address is the sender's as the main
// installation's ingress reports it (loopback plays the ingress here): what the sender writes in its own
// forwarded headers does not move it to another bucket (Q-BAS-116).
func TestR_BAS_008_AFloodOfBastionTokensFromOneAddressIsThrottledBeforeTheTokenStore(t *testing.T) {
	e := newEnv(t)
	s := e.newSite(siteOpts{failures: 20, endpointAnonymous: true, trusted: []string{"127.0.0.1/32", "::1/128"}, egress: []string{"198.51.100.20/32"}})
	bst := s.registered("partners", e.registration("partners"))
	v := strconv.Itoa(iface.InterfaceVersion)
	// sent is a request as the ingress passes it on: the sender's own entry, then the address the ingress saw.
	sent := func(sender, raw string, extra ...string) reply {
		h := append([]string{"X-Forwarded-For", "192.0.2.77, " + sender, iface.HeaderAuthorization, "Bearer " + raw, iface.HeaderInterface, v}, extra...)
		return s.do(packageRequest("/api/npm/npm-all/lodash", h...))
	}
	flood := func(sender string) reply {
		raw, err := token.Generate(token.TypeBST)
		if err != nil {
			t.Fatal(err)
		}
		return sent(sender, raw)
	}
	unauthorized, throttled := 0, 0
	for range 30 {
		switch flood("203.0.113.60").Status {
		case http.StatusUnauthorized:
			unauthorized++
		case http.StatusTooManyRequests:
			throttled++
		}
	}
	if unauthorized != 20 || throttled != 10 {
		t.Fatalf("%d refused as invalid and %d throttled of 30, want 20 and 10", unauthorized, throttled)
	}
	before := s.bastion.asked()
	rp := flood("203.0.113.60")
	if rp.Status != http.StatusTooManyRequests || rp.code() != errs.CodeRateLimited || rp.Header.Get("Retry-After") == "" || s.bastion.asked() != before {
		t.Errorf("a throttled address: %d %s (lookups %d -> %d)", rp.Status, rp.Body, before, s.bastion.asked())
	}
	if rp := flood("203.0.113.61"); rp.Status != http.StatusUnauthorized {
		t.Errorf("another address: %d %s", rp.Status, rp.Body)
	}
	if rp := s.iface(bst, http.MethodGet, iface.PathHandshake, nil); rp.Status != http.StatusOK {
		t.Errorf("the handshake: %d %s", rp.Status, rp.Body)
	}
	// Recognized: the answer is the anonymous caller's (no caller credential, anonymous access off), not the
	// throttle's.
	if rp := sent("203.0.113.60", bst); rp.Status == http.StatusTooManyRequests || rp.code() != errs.CodeCredentialsRequired {
		t.Errorf("a Bastion token the replica holds, from the throttled address: %d %s", rp.Status, rp.Body)
	}
}
