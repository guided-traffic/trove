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
// that name no registration count as failures of the caller's address, and from the budget on the address is
// refused 429 without a lookup, while another address is not slowed down and the Bastion's own interface
// requests, which come from its egress address, still pass.
func TestR_BAS_008_AFloodOfBastionTokensFromOneAddressIsThrottledBeforeTheTokenStore(t *testing.T) {
	e := newEnv(t)
	s := e.newSite(siteOpts{failures: 20, endpointAnonymous: true})
	bst := s.registered("partners", e.registration("partners"))
	v := strconv.Itoa(iface.InterfaceVersion)
	flood := func(addr string) reply {
		raw, err := token.Generate(token.TypeBST)
		if err != nil {
			t.Fatal(err)
		}
		return s.do(packageRequest("/api/npm/npm-all/lodash", append(bastionForwarded(addr), iface.HeaderAuthorization, "Bearer "+raw, iface.HeaderInterface, v)...))
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
}
