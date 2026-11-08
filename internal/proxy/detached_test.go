package proxy

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
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

// A fetch that Quiesce cancelled reports the shutdown, whichever step of the flight saw the cancellation first (F-79): the mirror
// pass reads errs.CodeShuttingDown as "stop and write nothing", and a raw context error as a failure of the item with a retry a
// minute later.
func TestQuiescedClassifiesTheShutdown(t *testing.T) {
	raw := fmt.Errorf("checking the entry: %w", context.Canceled)
	t.Run("R-REPO-053 R-ARCH-024 a context error of a flight whose engine was quiesced is a shutting_down error", func(t *testing.T) {
		base, stop := context.WithCancel(context.Background())
		e := &Engine{base: base}
		stop()
		got := e.quiesced(flightResult{err: raw})
		if ee, ok := errs.As(got.err); !ok || ee.Code != errs.CodeShuttingDown {
			t.Fatalf("the error is %v, want code %s", got.err, errs.CodeShuttingDown)
		}
	})
	t.Run("R-REPO-053 a context error of a flight whose engine runs is left as it is, and so is any other error and a result without one", func(t *testing.T) {
		e := &Engine{base: t.Context()}
		if got := e.quiesced(flightResult{err: raw}); got.err != raw { //nolint:errorlint // identity
			t.Errorf("the error is %v", got.err)
		}
		other := errors.New("boom")
		cancelled, stop := context.WithCancel(context.Background())
		stop()
		q := &Engine{base: cancelled}
		if got := q.quiesced(flightResult{err: other}); got.err != other { //nolint:errorlint // identity
			t.Errorf("an unrelated error became %v", got.err)
		}
		if got := q.quiesced(flightResult{stored: true}); got.err != nil || !got.stored {
			t.Errorf("a result without an error changed: %+v", got)
		}
	})
}
