package proxy

import (
	"context"
	"errors"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/vuln/enforce"
)

// FillOutcome is the outcome of a [Engine.Fill] that did not fail.
type FillOutcome struct {
	// Present: the artifact was stored already, or another request's fetch stored it while this one waited for it; this
	// request sent no upstream request.
	Present bool
	// Bytes is the size of the artifact this request's fetch stored; 0 when Present.
	Bytes int64
}

// Fill stores one artifact for a mirror's pass: steps 4 to 10 of R-REPO-025 for a background request (rq.Priority is
// PriorityBackground), without a client to answer. A stored artifact is Present and costs no upstream request. The errors tell
// the pass what to do with the item (docs/developer/mirror.md): ErrPreempted (a client fetch took the slot), a
// [*BackgroundWait] (the circuit is open, the spool refused admission), a [*upstream.RateLimitError] (the upstream
// rate-limited the request); any other error is a failure of the item. Offline, a negative entry, a refused populate check and a
// blocked version of the vulnerability scanning are failures of the item too: every rule of a client's fill applies.
func (e *Engine) Fill(ctx context.Context, rq Request) (FillOutcome, error) {
	if !rq.background() {
		return FillOutcome{}, errs.E(errs.Internal, errs.CodeInternal, "proxy: Fill is the request path of a background request")
	}
	if err := e.validate(rq, Artifact); err != nil {
		return FillOutcome{}, err
	}
	if rq.Mode != Normal {
		return FillOutcome{}, errs.E(errs.Internal, errs.CodeInternal, "proxy: a background request contacts the upstream")
	}
	rem := rq.Remote
	if blocked, err := e.goneGate(ctx, rq); err != nil || blocked != nil {
		return FillOutcome{}, firstError(err, blocked)
	}
	_, state, err := e.lookupHit(ctx, rq, true)
	if err != nil {
		return FillOutcome{}, err
	}
	switch state {
	case hitStored:
		return FillOutcome{Present: true}, nil
	case hitEntryOnly:
		return FillOutcome{}, errs.E(errs.NotFound, errs.CodeNotFound, "The item does not exist.")
	}
	// hitDamaged is a miss: the pass fills the item again, and its commit writes the object again (R-STOR-027, Q-REPO-202).
	lost := state == hitDamaged
	if e.offline(rem) {
		return FillOutcome{}, offlineMiss(rem)
	}
	open, retry, err := e.circuitPeek(ctx, rem)
	if err != nil {
		return FillOutcome{}, err
	}
	if open {
		return FillOutcome{}, circuitWait(retry)
	}
	if _, hit := e.neg.lookup(ctx, rem, rq.Path, rq.Variant); hit {
		return FillOutcome{}, errs.E(errs.NotFound, errs.CodeNotFound, "The item does not exist upstream.", errs.WithAbsent())
	}
	if !e.mayFill(ctx, rq) {
		return FillOutcome{}, e.denyFill(ctx, rq)
	}
	if err := e.o.Enforce.GateMiss(ctx, enforce.Miss{
		Repo: rem.Repo, Addressed: rq.Addressed, Package: rq.Package, Version: rq.Version, Path: rq.Path,
		Actor: auditActor(rq.Caller), Source: rq.Caller.Source, Endpoint: rq.Caller.Endpoint,
	}); err != nil {
		return FillOutcome{}, err
	}
	if left, ok := e.backedOff(rq); lost && ok {
		return FillOutcome{}, unrepairedError(left) // the last fill could not restore it (Q-REPO-205)
	}
	// The slot comes first: a background request that may not run yet holds neither the flight nor a client's wait (Q-REPO-179).
	rq, sctx, release, err := e.holdBackground(ctx, rq)
	if err != nil {
		return FillOutcome{}, err
	}
	defer release()
	st := &reqState{role: roleLeader, lost: lost}
	res, end := e.collapse(sctx, rq, st)
	if st.collapsed && !st.fetched.Load() && end == endDone && repeatable(res) {
		// The flight was a client's (this request fetched nothing itself) and failed with the client's classification (R-REPO-029): a 429 would be an item failure
		// here, with no wait and no halving. The circuit says what happened to the upstream; when it is closed the pass fetches
		// once for itself (Q-REPO-180).
		if open, retry, perr := e.circuitPeek(ctx, rem); perr == nil && open {
			return FillOutcome{}, circuitWait(retry)
		}
		st = &reqState{role: roleLeader, lost: lost}
		res, end = e.collapse(sctx, rq, st)
	}
	switch {
	case end == endGone && errors.Is(context.Cause(sctx), ErrPreempted):
		return FillOutcome{}, ErrPreempted
	case end == endGone:
		return FillOutcome{}, ctx.Err()
	case res.own != nil:
		return FillOutcome{}, res.own
	case res.err != nil:
		return FillOutcome{}, res.err
	case res.negative:
		return FillOutcome{}, errs.E(errs.NotFound, errs.CodeNotFound, "The item does not exist upstream.", errs.WithAbsent())
	case res.stored:
		return e.filled(ctx, rq, st.collapsed, lost)
	}
	return FillOutcome{}, errs.E(errs.Unavailable, errs.CodeObjectStoreUnavail,
		"The artifact could not be stored; retry.", errs.WithRetryAfter(5*time.Second))
}

// filled is the outcome of a pass's fill whose flight stored the artifact: Present when another request's flight stored it,
// else the bytes this one stored. An item that was lost before the flight is read again whichever flight fetched it: a commit
// that kept the recorded digest (the upstream serves other bytes now, R-REPO-041) left it lost, which is a failure of the item
// and starts the backoff, never Present (Q-REPO-202, Q-REPO-205).
func (e *Engine) filled(ctx context.Context, rq Request, collapsed, lost bool) (FillOutcome, error) {
	if collapsed && !lost {
		return FillOutcome{Present: true}, nil // a client's fetch or an earlier flight stored it
	}
	it, found, err := e.o.Items.Live(ctx, e.liveRef(rq.Remote.Repo, rq.Facts), rq.Path)
	switch {
	case err != nil:
		return FillOutcome{}, err
	case !found:
		return FillOutcome{}, errs.E(errs.NotFound, errs.CodeNotFound, "The item does not exist.")
	case items.ContentError(it) != nil:
		e.noteUnrepaired(rq)
		return FillOutcome{}, unrepairedError(unrepairedBackoff)
	case collapsed:
		return FillOutcome{Present: true}, nil // a client's refill restored it
	}
	return FillOutcome{Bytes: it.Size}, nil
}
