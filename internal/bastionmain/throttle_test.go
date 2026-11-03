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
	counted  int
}

func (c *countingThrottle) Exceeded(context.Context) bool { return c.exceeded }
func (c *countingThrottle) Count(context.Context)         { c.counted++ }

// TestR_BAS_008_TheRecognitionAppliesTheFailedCredentialThrottle: a refused Bastion token counts as a failure of
// the address, a throttled address is refused 429 before the authenticator is asked, a valid token counts nothing.
func TestR_BAS_008_TheRecognitionAppliesTheFailedCredentialThrottle(t *testing.T) {
	th := &countingThrottle{}
	tokens := &fakeTokens{}
	o := RecognizeOptions{Tokens: tokens, Throttle: func(*http.Request) Throttle { return th }}
	if w, _ := run(t, o, forwarded("/api/npm/npm-all/x", iface.HeaderAuthorization, "Bearer "+tokRevoked, iface.HeaderInterface, version())); w.Code != http.StatusUnauthorized || th.counted != 1 {
		t.Errorf("a revoked token: %d, counted %d", w.Code, th.counted)
	}
	if w, _ := run(t, o, forwarded("/api/npm/npm-all/x", iface.HeaderAuthorization, "Bearer "+tokValid, iface.HeaderInterface, version())); w.Code != http.StatusOK || th.counted != 1 {
		t.Errorf("a valid token: %d, counted %d", w.Code, th.counted)
	}
	th.exceeded = true
	asked := tokens.calls
	w, seen := run(t, o, forwarded("/api/npm/npm-all/x", iface.HeaderAuthorization, "Bearer "+tokValid, iface.HeaderInterface, version()))
	if w.Code != http.StatusTooManyRequests || seen != nil || tokens.calls != asked || w.Header().Get("Retry-After") == "" {
		t.Errorf("a throttled address: %d, lookups %d -> %d, Retry-After %q", w.Code, asked, tokens.calls, w.Header().Get("Retry-After"))
	}
	if problem(t, w)["code"] != errs.CodeRateLimited {
		t.Errorf("body %s", w.Body)
	}
	// A request without a Bastion token is not the stage's business: it neither counts nor is refused.
	if w, _ := run(t, o, forwarded("/api/npm/npm-all/x", iface.HeaderAuthorization, "Bearer "+pat)); w.Code != http.StatusOK {
		t.Errorf("an ordinary request from a throttled address: %d", w.Code)
	}
}
