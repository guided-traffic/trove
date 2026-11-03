package engine

import (
	"net/http"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/mainclient"
)

// TestR_BAS_014_R_BAS_025_AProbeTakesTheSlotClassOfTheCaller: with anonymous_share 0 a caller that presented a
// credential still gets the refusal of its write (the probe takes an authenticated slot), a caller without one
// is held to the anonymous class, which has no slot, and no probe ever counts as anonymous in flight for the
// credentialed one (Q-BAS-75, security finding B2).
func TestR_BAS_014_R_BAS_025_AProbeTakesTheSlotClassOfTheCaller(t *testing.T) {
	m := &fakeMain{}
	s := newSiteHarness(t, options{main: m})
	m.budget = mainclient.NewBudget(mainclient.BudgetOptions{
		MaxConcurrent: 4, Replicas: 1, AnonymousShare: 0, QueueTimeout: 20 * time.Millisecond, Metrics: s.m,
	})
	w := s.do(http.MethodPut, "/api/npm/npm-all/acme-sdk", nil, "Authorization", bearer(s.full))
	if w.Code != http.StatusForbidden || problemCode(w) != "bastion_read_only" {
		t.Fatalf("a credentialed write: %d %s", w.Code, w.Body)
	}
	if p := m.peakAnon.Load(); p != 0 {
		t.Fatalf("the probe of a credentialed caller counted as anonymous (%d)", p)
	}
	w = s.do(http.MethodPost, "/api/npm/npm-all/acme-sdk", nil)
	if w.Code != http.StatusServiceUnavailable || problemCode(w) != "bastion_busy" {
		t.Fatalf("a write without a credential, no anonymous slot: %d %s", w.Code, w.Body)
	}
	if got := m.requests()[0].Class; got != mainclient.Authenticated {
		t.Fatalf("the probe's class is %q", got)
	}
}
