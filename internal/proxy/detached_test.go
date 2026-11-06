package proxy

import (
	"context"
	"testing"
)

type ctxKey struct{}

func TestDetachedContext(t *testing.T) {
	t.Run("R-REPO-026 R-ARCH-005 a detached fetch does not carry what belongs to the request, and ends at Quiesce, not with the request", func(t *testing.T) {
		base, stop := context.WithCancel(context.Background())
		defer stop()
		e := &Engine{base: base, o: Options{Detach: func(ctx context.Context) context.Context {
			return context.WithValue(ctx, ctxKey{}, nil)
		}}}
		req, cancelReq := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "ticket"))
		c, cancel := e.detached(req)
		defer cancel()
		if c.Value(ctxKey{}) != nil {
			t.Fatal("the detached context still carries the request's value")
		}
		cancelReq()
		if c.Err() != nil {
			t.Fatal("the fetch ended with the request")
		}
		stop()
		<-c.Done()
	})
	t.Run("R-REPO-026 without a Detach hook the request's values are kept", func(t *testing.T) {
		e := &Engine{base: t.Context()}
		c, cancel := e.detached(context.WithValue(context.Background(), ctxKey{}, "v"))
		defer cancel()
		if c.Value(ctxKey{}) != "v" {
			t.Fatal("the value was dropped")
		}
	})
}
