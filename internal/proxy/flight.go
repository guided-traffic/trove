package proxy

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
)

// The reasons a fetch goes on without the cross-replica lock
// (trove_flight_lock_bypass_total, R-ARCH-033).
const (
	bypassValkey      = "valkey_unavailable" //nolint:gosec // G101: a metric label, not a credential
	bypassWaitTimeout = "wait_timeout"
	bypassDraining    = "draining"
)

// The roles of the flight_role field of the upstream log (R-OPS-011, Q-REPO-92). The
// role says how the request that sent the upstream request got to be the one that
// sends it.
const (
	// roleLeader: it fetches without having joined or waited for another request's
	// flight first: it holds the cross-replica lock, or no lock was needed or
	// available.
	roleLeader = "leader"
	// roleFollower: it first joined a flight of this replica, which ended without an
	// entry to read, and then fetched itself.
	roleFollower = "follower"
	// roleWaiter: it first waited for another replica's flight, which ended without
	// an entry (the holder failed, its lock expired, or the wait bound ran out), and
	// then fetched itself.
	roleWaiter = "waiter"
)

// maxRounds bounds how often a request joins a new flight after the previous one
// ended with nothing for it to read.
const maxRounds = 3

// flightResult is what a flight hands to everybody who waited for it.
type flightResult struct {
	// doc is the metadata document the flight stored, or served unstored.
	doc *Document
	// negative: the upstream answered 404 or 410 and a negative entry was recorded.
	negative bool
	// stored: an artifact entry exists now; the requests read it.
	stored bool
	// err: the fetch failed; every request applies its own fallback (stale copy).
	err error
	// status is the upstream status of a failed fetch, for Cache-Status.
	status int
}

// usable reports whether the result answers the requests that waited for it. An
// empty result (an artifact streamed to its leader's client only, a refresh that
// could not be stored) leaves the followers to fetch themselves.
func (r flightResult) usable() bool { return r.err != nil || r.doc != nil || r.negative || r.stored }

// flight is the fetch of one cache entry on this replica: one request leads, the
// others on this replica join it (R-REPO-026 step 2).
type flight struct {
	done   chan struct{}
	joined atomic.Int64
	res    flightResult
}

// reqState is what a request carries through the collapsing protocol.
type reqState struct {
	// role is the flight_role of the request's upstream request.
	role string
	// collapsed: the response comes from another request's fetch.
	collapsed bool
	// firstValidatedUs is the validated_at the request read at step 4, 0 when it
	// found no entry. An entry whose validated_at differs counts as fresh for the
	// request whatever its period (R-REPO-027).
	firstValidatedUs int64
	// restoredAt is the restore bound of EnsureCurrent; zero for none.
	restoredAt time.Time
	// refresh bounds the client's wait for a refresh when it has a stale copy to serve
	// (R-REPO-027); zero waits for the flight.
	refresh time.Duration
	// stream is the leader's own client for an artifact; nil when it gets its answer
	// from the stored entry.
	stream *stream
}

type waitEnd uint8

const (
	endDone waitEnd = iota
	// endTimeout: the refresh timeout ran out; serve the stale copy.
	endTimeout
	// endGone: the client's context ended.
	endGone
)

func (e *Engine) joinOrLead(key string) (*flight, bool) {
	e.fmu.Lock()
	defer e.fmu.Unlock()
	if fl, ok := e.flights[key]; ok {
		return fl, false
	}
	fl := &flight{done: make(chan struct{})}
	e.flights[key] = fl
	return fl, true
}

func (e *Engine) endFlight(key string, fl *flight) {
	e.fmu.Lock()
	if e.flights[key] == fl {
		delete(e.flights, key)
	}
	e.fmu.Unlock()
	close(fl.done)
}

// await waits for a flight, for the end of the refresh timeout, or for the client.
func (e *Engine) await(ctx context.Context, fl *flight, deadline time.Time) waitEnd {
	var timeout <-chan time.Time
	if !deadline.IsZero() {
		t := time.NewTimer(time.Until(deadline))
		defer t.Stop()
		timeout = t.C
	}
	select {
	case <-fl.done:
		return endDone
	case <-timeout:
		return endTimeout
	case <-ctx.Done():
		return endGone
	}
}

// collapse is steps 2 to 6 of R-REPO-026 for a request that has found nothing it can
// serve: it joins the flight of the replica, or leads one. It returns when the
// flight has ended and its result is the request's answer, or earlier when the
// refresh timeout ends or the client goes.
func (e *Engine) collapse(ctx context.Context, rq Request, st *reqState) (flightResult, waitEnd) {
	f := cluster.NewFlightKey(rq.Remote.id, rq.Path, rq.Variant)
	key := f.String()
	var deadline time.Time
	if st.refresh > 0 {
		deadline = time.Now().Add(st.refresh)
	}
	for round := 0; ; round++ {
		fl, leader := e.joinOrLead(key)
		if leader {
			go e.runFlight(ctx, f, key, fl, rq, st)
			end := e.await(ctx, fl, deadline)
			if end == endDone {
				return fl.res, end
			}
			return flightResult{}, end
		}
		st.collapsed = true
		fl.joined.Add(1)
		if end := e.await(ctx, fl, deadline); end != endDone {
			return flightResult{}, end
		}
		if fl.res.usable() {
			return fl.res, endDone
		}
		// The flight served its own client only (pass-through) or stored nothing: look
		// again, and fetch ourselves when there is still nothing to read.
		res, ok, err := e.recheck(ctx, rq, st)
		if err != nil {
			return flightResult{err: err}, endDone
		}
		if ok {
			return res, endDone
		}
		st.role = roleFollower
		if round+1 >= maxRounds {
			return e.fetchDirect(ctx, rq, st), endDone
		}
	}
}

// fetchDirect fetches outside any flight: the request has waited for flights that
// left nothing to read and nothing is shared afterwards.
func (e *Engine) fetchDirect(ctx context.Context, rq Request, st *reqState) flightResult {
	fctx, cancel := e.detached(ctx)
	defer cancel()
	return e.fetch(fctx, rq, st, &flight{done: make(chan struct{})})
}

// detached returns a context that outlives the client but ends at Quiesce: the
// fetch is the flight's, not the request's (R-REPO-026 "detached fetch").
func (e *Engine) detached(ctx context.Context) (context.Context, context.CancelFunc) {
	c, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(e.base, cancel)
	return c, func() { stop(); cancel() }
}

// runFlight is the flight: it runs detached from the request that leads it.
func (e *Engine) runFlight(reqCtx context.Context, f cluster.FlightKey, key string, fl *flight, rq Request, st *reqState) {
	ctx, cancel := e.detached(reqCtx)
	defer cancel()
	defer e.endFlight(key, fl)
	fl.res = e.lead(ctx, f, rq, st, fl)
}

// lead is the cross-replica part of the protocol (R-REPO-026 steps 3 to 6): it
// acquires the flight or waits for the holder, repeats the checks of R-REPO-025
// steps 4 to 8 after every wait, and fetches when it holds the flight or has to go
// on without it.
func (e *Engine) lead(ctx context.Context, f cluster.FlightKey, rq Request, st *reqState, fl *flight) flightResult {
	waitUntil := time.Now().Add(e.o.Config.FetchWaitTimeout())
	var (
		hold   *cluster.FlightHold
		bypass string
	)
	// The checks of step 4 to 8 once more under the flight, for a flight that began
	// after another one committed.
	if res, ok, err := e.recheck(ctx, rq, st); err != nil {
		return flightResult{err: err}
	} else if ok {
		return res
	}
loop:
	for {
		if e.draining.Load() {
			// A draining replica leads no new cross-replica flight (Q-REPO-7): it joins
			// the flight another replica holds, and fetches under its own single-flight
			// when none does.
			res, ok, stop := e.waitOnce(ctx, f, rq, st, waitUntil)
			if ok {
				return res
			}
			bypass = bypassDraining
			if stop != "" {
				bypass = stop
			}
			break loop
		}
		h, ok, err := e.o.Flights.Acquire(ctx, f)
		switch {
		case err != nil && errors.Is(err, cluster.ErrUnavailable):
			bypass = bypassValkey
			break loop
		case err != nil:
			e.log.Warn("acquiring a flight lock failed; fetching without it", "error", err)
			bypass = bypassValkey
			break loop
		case ok:
			hold = h
			break loop
		}
		st.role = roleWaiter // another replica holds the flight: whatever follows, we waited
		res, decided, stop := e.waitOnce(ctx, f, rq, st, waitUntil)
		if decided {
			return res
		}
		if stop != "" {
			bypass = stop
			break loop
		}
		if ctx.Err() != nil {
			return flightResult{err: ctx.Err()}
		}
		// The holder failed or its lock expired: acquire it ourselves.
	}
	if hold != nil {
		if res, ok, err := e.recheck(ctx, rq, st); err != nil {
			e.release(hold, cluster.OutcomeFailed)
			return flightResult{err: err}
		} else if ok {
			e.release(hold, cluster.OutcomeStored)
			return res
		}
	} else {
		e.m.bypass.WithLabelValues(bypass).Inc()
	}
	res := e.fetch(ctx, rq, st, fl)
	if hold != nil {
		o := cluster.OutcomeFailed
		if res.stored || res.doc != nil || res.negative {
			o = cluster.OutcomeStored
		}
		e.release(hold, o)
	}
	return res
}

// waitOnce waits for the holder of the flight once (R-REPO-026 step 4) and repeats
// the checks. decided says that they answer the request. stop is the bypass reason
// when the wait cannot go on: Valkey is unavailable or the wait bound is spent.
func (e *Engine) waitOnce(ctx context.Context, f cluster.FlightKey, rq Request, st *reqState, until time.Time) (res flightResult, decided bool, stop string) {
	remaining := time.Until(until)
	if remaining <= 0 {
		return flightResult{}, false, bypassWaitTimeout
	}
	wctx, cancel := context.WithTimeout(ctx, remaining)
	_, err := e.o.Flights.Wait(wctx, f)
	cancel()
	switch {
	case err == nil:
		// The holder released or its lock is gone: the entry is the answer.
	case errors.Is(err, cluster.ErrUnavailable):
		return flightResult{}, false, bypassValkey
	case ctx.Err() != nil:
		return flightResult{err: ctx.Err()}, true, ""
	default: // the wait bound ended
		return flightResult{}, false, bypassWaitTimeout
	}
	res, ok, rerr := e.recheck(ctx, rq, st)
	if rerr != nil {
		return flightResult{err: rerr}, true, ""
	}
	if ok {
		st.collapsed = true
	}
	return res, ok, ""
}

// release gives the flight back on a context of its own: a cancelled fetch must
// still release (R-ARCH-033).
func (e *Engine) release(h *cluster.FlightHold, o cluster.Outcome) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Release(ctx, o, ""); err != nil && !errors.Is(err, cluster.ErrUnavailable) {
		e.log.Warn("releasing a flight lock failed; it expires on its own", "error", err)
	}
}

// recheck repeats the checks of R-REPO-025 steps 4 to 8 for a request that waited:
// an entry that serves, an open circuit, a negative entry. ok is false when none
// decides and the request has to fetch.
func (e *Engine) recheck(ctx context.Context, rq Request, st *reqState) (res flightResult, ok bool, err error) {
	rem := rq.Remote
	en, found, err := e.getEntry(ctx, rem.id, rq.Path, rq.Variant)
	if err != nil {
		return flightResult{}, false, err
	}
	if found && !en.Gone() {
		if rq.Kind == Artifact {
			return flightResult{stored: true}, true, nil
		}
		if e.fresh(rem, rq.Path, en, st.restoredAt) || en.ValidatedAt.UnixMicro() != st.firstValidatedUs {
			return flightResult{doc: e.document(en, hitStatus(en.TTL))}, true, nil
		}
	}
	open, retry, err := e.circuitPeek(ctx, rem)
	if err != nil {
		return flightResult{}, false, err
	}
	if open {
		return flightResult{err: assumedOffline(retry)}, true, nil
	}
	if _, hit := e.neg.lookup(ctx, rem, rq.Path, rq.Variant); hit {
		return flightResult{negative: true}, true, nil
	}
	return flightResult{}, false, nil
}
