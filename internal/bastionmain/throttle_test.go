package bastionmain

import (
	"context"
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/errs"
)

type countingThrottle struct {
	exceeded bool
	asked    int
	counted  int
}

func (c *countingThrottle) Exceeded(context.Context) bool { c.asked++; return c.exceeded }
func (c *countingThrottle) Count(context.Context)         { c.counted++ }

// TestR_BAS_008_TheRecognitionAppliesTheFailedCredentialThrottle: the stage hands the throttle of the address to
// the authenticator, which asks it only for a token the replica does not hold (Q-BAS-65); the stage itself asks
// nothing, so a known Bastion token costs no read of the shared counters. A refused token counts as a failure of
// the address, a refusal for the throttle is 429 with Retry-After and counts nothing, and a valid token counts
// nothing.
func TestR_BAS_008_TheRecognitionAppliesTheFailedCredentialThrottle(t *testing.T) {
	th := &countingThrottle{}
	tokens := &fakeTokens{}
	o := RecognizeOptions{
		Tokens: tokens, Throttle: func(*http.Request) Throttle { return th },
		Source: func(*http.Request) (string, string) { return "198.51.100.20", "trove-bastion/0.15.0" },
	}
	if w, _ := run(t, o, forwarded("/api/npm/web/npm-all/x", iface.HeaderAuthorization, "Bearer "+tokRevoked, iface.HeaderInterface, version())); w.Code != http.StatusUnauthorized || th.counted != 1 {
		t.Errorf("a revoked token: %d, counted %d", w.Code, th.counted)
	}
	th.exceeded = true
	th.asked = 0
	w, seen := run(t, o, forwarded("/api/npm/web/npm-all/x", iface.HeaderAuthorization, "Bearer "+tokValid, iface.HeaderInterface, version()))
	if w.Code != http.StatusOK || seen == nil || th.counted != 1 || th.asked != 0 {
		t.Errorf("a valid token the replica holds, from a throttled address: %d, counted %d, throttle asked %d", w.Code, th.counted, th.asked)
	}
	w, seen = run(t, o, forwarded("/api/npm/web/npm-all/x", iface.HeaderAuthorization, "Bearer "+tokRevoked, iface.HeaderInterface, version()))
	if w.Code != http.StatusTooManyRequests || seen != nil || th.counted != 1 || w.Header().Get("Retry-After") == "" {
		t.Errorf("a token the replica does not hold, from a throttled address: %d, counted %d, Retry-After %q", w.Code, th.counted, w.Header().Get("Retry-After"))
	}
	if problem(t, w)["code"] != errs.CodeRateLimited {
		t.Errorf("body %s", w.Body)
	}
	for _, req := range tokens.reqs {
		if req.Throttle == nil || req.ClientAddr != "198.51.100.20" || req.UserAgent != "trove-bastion/0.15.0" {
			t.Errorf("the authenticator was not given the request's throttle and source: %+v", req)
		}
	}
	// A request without a Bastion token is not the stage's business: it neither counts nor is refused.
	if w, _ := run(t, o, forwarded("/api/npm/web/npm-all/x", iface.HeaderAuthorization, "Bearer "+pat)); w.Code != http.StatusOK {
		t.Errorf("an ordinary request from a throttled address: %d", w.Code)
	}
}
