package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/proxy/upstream"
)

// The background request of a mirror's pass (repositories-and-proxy.md R-REPO-053 to R-REPO-055, Q-REPO-149 to Q-REPO-152): a
// Request whose Priority is PriorityBackground. It takes a background slot (slots.go), never the clients' queue; its outcomes
// neither move nor reset the circuit of R-REPO-029 and it never probes; a rate-limit answer is a [*upstream.RateLimitError]
// and not the client classification; a client fetch can pre-empt it. The outcomes only the background request itself
// understands travel as flightResult.own, so that a client that joined the flight is never handed one of them.

// ErrPreempted is the error of a background request that a client fetch pre-empted (R-REPO-055), or that the yield of another
// process pre-empted: its upstream request was cancelled, nothing was stored and no negative entry was written. The mirror
// repeats the item later; it is neither a failure nor a rate limit.
var ErrPreempted = errors.New("proxy: the background fetch was pre-empted by a client fetch")

// The reasons of a [BackgroundWait].
const (
	// WaitCircuitOpen: the circuit of the repository is open (R-REPO-029, R-REPO-054): nothing was sent.
	WaitCircuitOpen = "circuit_open"
	// WaitSpoolBusy: the spool refused admission (R-STOR-004, R-REPO-052): nothing was stored.
	WaitSpoolBusy = "spool_busy"
)

// spoolBusyWait is how long a background request that the spool refused waits (R-REPO-052).
const spoolBusyWait = 5 * time.Second

// circuitWaitMax caps the wait for an open circuit: a client's success may close it at any time, so the mirror looks again at
// least this often (Q-REPO-150).
const circuitWaitMax = 5 * time.Second

// BackgroundWait is the error of a background request that was not sent, or whose body the spool refused, and that can be
// repeated after a wait: the circuit is open (the request reads it and never probes), or the spool refused admission.
type BackgroundWait struct {
	// Reason is [WaitCircuitOpen] or [WaitSpoolBusy].
	Reason string
	// After is how long to wait before the request is repeated.
	After time.Duration
}

func (e *BackgroundWait) Error() string {
	return "proxy: the background request waits (" + e.Reason + ") for " + e.After.String()
}

// circuitWait is the wait for an open circuit that stays open for retry: until its next state change, at most
// [circuitWaitMax], at least a second.
func circuitWait(retry time.Duration) *BackgroundWait {
	return &BackgroundWait{Reason: WaitCircuitOpen, After: min(max(retry, time.Second), circuitWaitMax)}
}

// circuitOpenError is the error of a request that finds the circuit open (R-REPO-025 step 7): a client is told that the
// upstream is assumed offline, a background request is told to wait.
func circuitOpenError(rq Request, retry time.Duration) error {
	if rq.Priority == PriorityBackground {
		return circuitWait(retry)
	}
	return assumedOffline(retry)
}

// backgroundOutcome reports whether err is an outcome only a background request understands.
func backgroundOutcome(err error) bool {
	var w *BackgroundWait
	var rl *upstream.RateLimitError
	return errors.Is(err, ErrPreempted) || errors.As(err, &w) || errors.As(err, &rl)
}

// resultOf is the flightResult of an error: an outcome of a background request goes to the flight's leader alone, any other
// error is the flight's for everybody who waited for it.
func resultOf(err error) flightResult {
	if backgroundOutcome(err) {
		return flightResult{own: err}
	}
	return flightResult{err: err}
}

// background reports whether the request is a mirror's.
func (r Request) background() bool { return r.Priority == PriorityBackground }

// rateLimited classifies the answer of the upstream to a background request as a rate limit (R-REPO-054): the typed error
// instead of the client classification. It reads at most [upstream.SlowDownSniffBytes] of the body of a 503. A client request
// never comes here, so a client's 503 SlowDown stays upstream_error.
func (f *fetcher) rateLimited(resp *http.Response) (flightResult, bool) {
	var body []byte
	switch resp.StatusCode {
	case http.StatusServiceUnavailable:
		body, _ = io.ReadAll(io.LimitReader(resp.Body, upstream.SlowDownSniffBytes))
	case http.StatusTooManyRequests, http.StatusForbidden:
	default:
		return flightResult{}, false
	}
	rl, ok := upstream.RateLimitOf(resp.StatusCode, resp.Header, body, f.e.now())
	if !ok {
		return flightResult{}, false
	}
	return flightResult{own: &upstream.RateLimitError{RateLimit: rl, Repo: f.rq.Remote.Repo.Name.String()}, status: resp.StatusCode}, true
}

// roleMirror is the flight_role of the upstream log record of a background request (R-REPO-057).
const roleMirror = "mirror"

// The signal mirror_yield (R-REPO-055, Q-ARCH-286).

// Signals is what the engine needs of the signal path: raise mirror_yield and listen to it. *cluster.Signals implements it.
type Signals interface {
	Publish(area, payload string)
	Subscribe(ctx context.Context, area string, h cluster.SignalHandler) (cancel func(), err error)
}

// yieldEvery bounds how often a replica raises the signal for one repository (R-REPO-055).
const yieldEvery = time.Second

// raiseYield tells the processes that run background fetches of repository repoID that a client fetch of it waits for a slot:
// at most once per second and repository. The signal is simply lost while Valkey is unavailable (the path of
// cluster.Signals); a replica without signals raises none.
func (e *Engine) raiseYield(repoID int64) {
	if e.o.Signals == nil {
		return
	}
	id := strconv.FormatInt(repoID, 10)
	if !e.throttle("mirror_yield\x00"+id, yieldEvery) {
		return
	}
	e.o.Signals.Publish(cluster.AreaMirrorYield, id)
}

// onYield is the handler of mirror_yield: a payload names a repository, a batch without one counts for all of them.
func (e *Engine) onYield(payloads []string, coarse bool) {
	if coarse {
		e.slots.yieldAll()
	}
	for _, p := range payloads {
		if id, err := strconv.ParseInt(p, 10, 64); err == nil {
			e.slots.yield(id)
		}
	}
}

// listenYield subscribes the engine to mirror_yield. A replica without signals, or whose subscription fails, runs without:
// the background keeps within its share (R-REPO-055).
func (e *Engine) listenYield() {
	if e.o.Signals == nil {
		return
	}
	cancel, err := e.o.Signals.Subscribe(e.base, cluster.AreaMirrorYield, e.onYield)
	if err != nil {
		e.log.Warn("subscribing to the mirror yield signal failed; background fetches yield through their share only", "error", err)
		return
	}
	e.stopYield = cancel
}

// bgSlot is the background slot a pass's request holds while it goes through the flights of R-REPO-026 (Q-REPO-179). It is taken
// before the request enters collapse, so that a request that may not run yet (the share is full, a client waits, the yield window of
// the signal is open) holds no cross-replica lock and no flight a client could join. The fetch that the request leads runs under the
// slot's fate: a pre-emption cancels it; the end of the pass's context cancels it unless a follower of this replica joined its flight
// (a client then waits for it, and it finishes).
type bgSlot struct {
	ctx context.Context
	// hold is the slot's hold; the fetch of the item the slot was taken for returns its share when its upstream exchange ends
	// (Q-REPO-235).
	hold *slotHold
	// path, variant and kind identify that item: a discovery document resolved under the same slot is another one.
	path    string
	variant string
	kind    Kind
	// waitTaken is set by the first upstream request sent under the slot, which reports the slot's wait; later ones report 0.
	waitTaken atomic.Bool
	mu        sync.Mutex
	fls       []*flight
}

// owns reports whether the request is the one the slot was taken for.
func (b *bgSlot) owns(rq Request) bool {
	return rq.Path == b.path && rq.Variant == b.variant && rq.Kind == b.kind
}

// takeWait returns the slot's wait for the first upstream request sent under it, 0 for every later one.
func (b *bgSlot) takeWait() time.Duration {
	if b.waitTaken.Swap(true) {
		return 0
	}
	return b.hold.wait
}

// joined reports whether a follower of this replica joined a flight the slot's request leads.
func (b *bgSlot) joined() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, fl := range b.fls {
		if fl.joined.Load() > 0 {
			return true
		}
	}
	return false
}

// link returns the context a fetch under the slot runs under, and the function that ends the link.
func (b *bgSlot) link(ctx context.Context, fl *flight) (context.Context, func()) {
	c, cancel := context.WithCancelCause(ctx)
	b.mu.Lock()
	b.fls = append(b.fls, fl)
	b.mu.Unlock()
	stop := context.AfterFunc(b.ctx, func() {
		cause := context.Cause(b.ctx)
		if errors.Is(cause, ErrPreempted) || fl.joined.Load() == 0 {
			cancel(cause)
		}
	})
	return c, func() {
		stop()
		b.mu.Lock()
		b.fls = slices.DeleteFunc(b.fls, func(x *flight) bool { return x == fl })
		b.mu.Unlock()
		cancel(nil)
	}
}

// holdBackground takes the background slot of a background request before it enters the flights; a request that holds one
// already (the discovery document a fetch resolves) and every client request pass through. The returned context is the one the
// request waits under: a pre-emption ends it with the cause ErrPreempted.
func (e *Engine) holdBackground(ctx context.Context, rq Request) (Request, context.Context, func(), error) {
	if !rq.background() || rq.slot != nil {
		return rq, ctx, func() {}, nil
	}
	rem := rq.Remote
	budget := rem.Settings.Limits.MaxConcurrentFetches
	ceiling := rq.Ceiling
	if ceiling <= 0 {
		ceiling = BackgroundShare(budget, rem.Settings.Mirror.Concurrency)
	}
	b := &bgSlot{path: rq.Path, variant: rq.Variant, kind: rq.Kind}
	sctx, hold, err := e.slots.acquireBackgroundJoined(ctx, rem.id, budget, ceiling, b.joined)
	if err != nil {
		return rq, ctx, nil, err
	}
	b.ctx, b.hold = sctx, hold
	rq.slot = b
	return rq, sctx, hold.release, nil
}
