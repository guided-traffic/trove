package create

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

func actor() audit.Actor { return audit.Actor{Kind: "system", Name: "backup"} }

func marshal(v any) (json.RawMessage, error) { return json.Marshal(v) }

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// newRunID returns a UUIDv7 (RFC 9562): 48 bits of Unix milliseconds, then random bits.
func newRunID(at time.Time) (string, error) {
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		return "", err
	}
	ms := uint64(at.UnixMilli()) //nolint:gosec // after 1970
	for i := range 6 {
		b[i] = byte(ms >> (8 * (5 - i))) //nolint:gosec // a byte of the timestamp
	}
	b[6] = b[6]&0x0f | 0x70
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// classify gives an error from a dependency the exit code of R-BAK-013. Errors that already carry one, and the sentinels
// internal/backup maps, pass through; a database or target that cannot be reached is temporary (75); anything else is a
// failure of the run (1).
func classify(err error) error {
	if err == nil {
		return nil
	}
	var ee *backup.ExitError
	if errors.As(err, &ee) {
		return err
	}
	if backup.ExitCode(err) != backup.ExitFailed {
		return err
	}
	if errors.Is(err, backup.ErrSuperseded) || errors.Is(err, backup.ErrHoldLost) || errors.Is(err, backup.ErrPoolDamaged) || errors.Is(err, backup.ErrInvariantViolation) {
		return err
	}
	if temporary(err) {
		return backup.Fail(backup.ExitTemporary, backup.CodeTemporary, err)
	}
	return err
}

// temporary reports an unreachable database or target: retrying later is useful.
func temporary(err error) bool {
	var ne net.Error
	switch {
	case postgres.IsUnavailable(err), blob.IsTransient(err):
		return true
	case errors.As(err, &ne), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.ECONNREFUSED),
		errors.Is(err, syscall.EPIPE), errors.Is(err, context.DeadlineExceeded):
		return true
	}
	return false
}

// group runs functions and cancels the rest when one fails; Wait returns the first error.
type group struct {
	cancel context.CancelCauseFunc
	wg     sync.WaitGroup
	once   sync.Once
	err    error
	ctx    context.Context
}

func newGroup(ctx context.Context) (*group, context.Context) {
	ctx, cancel := context.WithCancelCause(ctx)
	return &group{cancel: cancel, ctx: ctx}, ctx
}

func (g *group) Go(f func(context.Context) error) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		if err := f(g.ctx); err != nil {
			g.once.Do(func() { g.err = err; g.cancel(err) })
		}
	}()
}

func (g *group) Wait() error {
	g.wg.Wait()
	g.cancel(nil)
	return g.err
}

// limiter caps the bytes per second of a run (backup.copy.bytes_per_second). The zero rate is unlimited.
type limiter struct {
	mu   sync.Mutex
	rate float64
	next time.Time
}

func newLimiter(bytesPerSecond int64) *limiter { return &limiter{rate: float64(bytesPerSecond)} }

// wait reserves n bytes and sleeps until the reservation is due, with a burst of one second.
func (l *limiter) wait(ctx context.Context, n int64) error {
	if l == nil || l.rate <= 0 || n <= 0 {
		return nil
	}
	l.mu.Lock()
	now := time.Now()
	start := l.next
	if start.Before(now) {
		start = now
	}
	l.next = start.Add(time.Duration(float64(n) / l.rate * float64(time.Second)))
	delay := start.Sub(now) - time.Second
	l.mu.Unlock()
	if delay <= 0 {
		return nil
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-t.C:
		return nil
	}
}
