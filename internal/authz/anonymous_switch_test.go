package authz

import (
	"context"
	"errors"
	"testing"
)

// TestR_AUTH_037_AnonymousSwitchIsReadOncePerRequest: the runtime switch is read
// with the request's context, once for a request that decides several
// coordinates, and a switch that cannot be read is 503, not 401.
func TestR_AUTH_037_AnonymousSwitchIsReadOncePerRequest(t *testing.T) {
	f := newFixture().repo("a-repo", "local", "generic", "")
	f.policy(policyDef{name: "anon", selectors: []string{"a-repo"}, entries: map[string][]string{AnonymousID: {"read"}}})

	type ctxKey struct{}
	reads, sawRequestContext := 0, false
	var failure error
	e := newTestEngine(t, f, func(o *Options) {
		o.AnonymousAccess = func(ctx context.Context) (bool, error) {
			reads++
			sawRequestContext = ctx.Value(ctxKey{}) != nil
			return true, failure
		}
	})
	ctx := context.WithValue(t.Context(), ctxKey{}, "request")

	t.Run("one read for a request with several coordinates, on the request's context", func(t *testing.T) {
		idx, err := e.Begin(ctx, anonSub, "a-repo")
		if err != nil {
			t.Fatal(err)
		}
		reads = 0
		if d := e.DecideOn(ctx, idx, anonSub, read("a-repo", "x", "")); d.Outcome != Allow {
			t.Fatalf("decision = %+v", d)
		}
		if reads != 1 || !sawRequestContext {
			t.Fatalf("reads = %d, request context seen = %v", reads, sawRequestContext)
		}
		reads = 0
		if got, err := e.PathsAllowed(ctx, anonSub, read("a-repo", "", ""), Read, []string{"a", "b", "c"}); err != nil || len(got) != 3 || reads != 1 {
			t.Fatalf("paths %v err %v reads %d", got, err, reads)
		}
	})
	t.Run("a switch that cannot be read is 503, not 401", func(t *testing.T) {
		failure = errors.New("database unavailable")
		d := e.Decide(ctx, anonSub, read("a-repo", "x", ""))
		if d.Status() != 503 {
			t.Fatalf("decision = %+v, status %d", d, d.Status())
		}
		if _, err := e.Readable(ctx, anonSub, []string{"a-repo"}); err == nil {
			t.Fatal("Readable answered without the switch")
		}
	})
}
