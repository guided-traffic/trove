package blob

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/config"
)

// Guard decorates an ObjectStore with the circuit breaker of R-STOR-033 and the
// request metrics. After circuit_breaker.failure_threshold consecutive failures
// (network errors, timeouts, 5xx, credential errors; not 404 or other 4xx) the
// circuit opens for circuit_breaker.open_duration, during which every call
// fails at once with ErrCircuitOpen; then one probe request goes through and a
// success closes the circuit.
//
// The breaker is per replica and keeps no state anywhere else.
type Guard struct {
	inner     ObjectStore
	threshold int
	openFor   time.Duration
	now       func() time.Time
	metrics   *Metrics

	mu        sync.Mutex
	failures  int
	openUntil time.Time
	open      bool
	probing   bool
}

// GuardOption configures a Guard.
type GuardOption func(*Guard)

// WithGuardMetrics sets the metrics.
func WithGuardMetrics(m *Metrics) GuardOption { return func(g *Guard) { g.metrics = m } }

// WithGuardClock replaces the clock; tests use it.
func WithGuardClock(now func() time.Time) GuardOption { return func(g *Guard) { g.now = now } }

// NewGuard wraps inner.
func NewGuard(inner ObjectStore, cfg config.StorageS3Breaker, opts ...GuardOption) *Guard {
	g := &Guard{inner: inner, threshold: max(1, cfg.FailureThreshold), openFor: cfg.OpenDuration, now: time.Now}
	for _, o := range opts {
		o(g)
	}
	return g
}

var _ ObjectStore = (*Guard)(nil)

// Open reports whether the circuit is open (or probing).
func (g *Guard) Open() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.open
}

// admit decides whether a call may go to the store.
func (g *Guard) admit() (probe bool, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.open {
		return false, nil
	}
	if g.probing || g.now().Before(g.openUntil) {
		return false, ErrCircuitOpen
	}
	g.probing = true
	return true, nil
}

// settle records the outcome of a call that was admitted.
func (g *Guard) settle(probe bool, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if probe {
		g.probing = false
	}
	if err != nil && (errors.Is(err, context.Canceled) || (errors.Is(err, context.DeadlineExceeded) && !IsTransient(err))) {
		// The caller gave up or its deadline ended the call: no evidence either
		// way. A deadline that ended a call inside the transport arrives as a
		// transient StoreError and counts.
		return
	}
	if err == nil || !IsTransient(err) {
		// The store answered; a 404 or another 4xx is an answer too.
		g.failures = 0
		if g.open {
			g.open = false
			g.metrics.circuit(false)
		}
		return
	}
	g.failures++
	if probe || g.failures >= g.threshold {
		g.open = true
		g.openUntil = g.now().Add(g.openFor)
		g.metrics.circuit(true)
	}
}

func requestOutcome(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrCircuitOpen):
		return "circuit_open"
	}
	return "error"
}

// call runs f under the breaker and records metrics for op.
func (g *Guard) call(op string, f func() error) error {
	probe, err := g.admit()
	if err != nil {
		g.metrics.request(op, "circuit_open", 0)
		return err
	}
	start := g.now()
	err = f()
	g.settle(probe, err)
	g.metrics.request(op, requestOutcome(err), g.now().Sub(start).Seconds())
	return err
}

// Put implements ObjectStore.
func (g *Guard) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, sum Checksum) error {
	err := g.call("put", func() error { return g.inner.Put(ctx, key, body, size, sum) })
	if err == nil {
		g.metrics.bytes("up", size)
	}
	return err
}

// CreateMultipart implements ObjectStore.
func (g *Guard) CreateMultipart(ctx context.Context, key, algorithm string) (id string, err error) {
	err = g.call("create_multipart", func() (e error) { id, e = g.inner.CreateMultipart(ctx, key, algorithm); return })
	return id, err
}

// UploadPart implements ObjectStore.
func (g *Guard) UploadPart(ctx context.Context, key, uploadID string, number int, body io.ReadSeeker, size int64, sum Checksum) (p Part, err error) {
	err = g.call("upload_part", func() (e error) {
		p, e = g.inner.UploadPart(ctx, key, uploadID, number, body, size, sum)
		return
	})
	if err == nil {
		g.metrics.bytes("up", size)
	}
	return p, err
}

// CompleteMultipart implements ObjectStore.
func (g *Guard) CompleteMultipart(ctx context.Context, key, uploadID string, parts []Part) error {
	return g.call("complete_multipart", func() error { return g.inner.CompleteMultipart(ctx, key, uploadID, parts) })
}

// AbortMultipart implements ObjectStore.
func (g *Guard) AbortMultipart(ctx context.Context, key, uploadID string) error {
	return g.call("abort_multipart", func() error { return g.inner.AbortMultipart(ctx, key, uploadID) })
}

// Head implements ObjectStore.
func (g *Guard) Head(ctx context.Context, key string) (info ObjectInfo, err error) {
	err = g.call("head", func() (e error) { info, e = g.inner.Head(ctx, key); return })
	return info, err
}

// Get implements ObjectStore. The breaker judges the request and the response
// headers; a failure in the middle of the body is the reader's error.
func (g *Guard) Get(ctx context.Context, key string, rng *ByteRange) (body io.ReadCloser, info ObjectInfo, err error) {
	err = g.call("get", func() (e error) { body, info, e = g.inner.Get(ctx, key, rng); return })
	if err != nil {
		return nil, info, err
	}
	return &countingBody{ReadCloser: body, m: g.metrics}, info, nil
}

type countingBody struct {
	io.ReadCloser
	m *Metrics
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.m.bytes("down", int64(n))
	return n, err
}

// Delete implements ObjectStore.
func (g *Guard) Delete(ctx context.Context, key string) error {
	return g.call("delete", func() error { return g.inner.Delete(ctx, key) })
}

// DeleteMany implements ObjectStore.
func (g *Guard) DeleteMany(ctx context.Context, keys []string) (failed []DeleteFailure, err error) {
	err = g.call("delete_many", func() (e error) { failed, e = g.inner.DeleteMany(ctx, keys); return })
	return failed, err
}

// List implements ObjectStore.
func (g *Guard) List(ctx context.Context, prefix string, opts ListOptions) (page ListPage, err error) {
	err = g.call("list", func() (e error) { page, e = g.inner.List(ctx, prefix, opts); return })
	return page, err
}

// ListMultipart implements ObjectStore.
func (g *Guard) ListMultipart(ctx context.Context, prefix string) (ups []MultipartUpload, err error) {
	err = g.call("list_multipart", func() (e error) { ups, e = g.inner.ListMultipart(ctx, prefix); return })
	return ups, err
}

// Presign implements ObjectStore. Presigning is local work and does not go
// through the breaker.
func (g *Guard) Presign(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return g.inner.Presign(ctx, key, ttl)
}
