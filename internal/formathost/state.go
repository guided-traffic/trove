package formathost

import (
	"context"
	"slices"
	"sync"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/meta"
)

// requestState is what the host keeps for one request, in formats.Request.HostState:
// the decisions that allowed a write, which the transaction of the write records
// (R-AUTHZ-029), and the spool files of the request, which Finish removes. It is
// the request's, not the host's: the host itself holds no request state
// (R-ARCH-032).
type requestState struct {
	mu      sync.Mutex
	allowed []allowedWrite
	spools  []*spooled
}

type allowedWrite struct {
	c authz.Coordinate
	d authz.Decision
}

// Prepare gives a request the state the host needs. The dispatcher calls it when it
// builds the request, before the handler runs.
func Prepare(rq *formats.Request) { rq.HostState = &requestState{} }

// Finish releases what the request holds: its spool files, on every path.
func Finish(rq *formats.Request) {
	st := stateOf(rq)
	if st == nil {
		return
	}
	st.mu.Lock()
	sp := st.spools
	st.spools = nil
	st.mu.Unlock()
	for _, s := range sp {
		_ = s.s.Close()
	}
}

func stateOf(rq *formats.Request) *requestState {
	st, _ := rq.HostState.(*requestState)
	return st
}

// allow remembers an allowed write decision; one coordinate is recorded once.
func (st *requestState) allow(c authz.Coordinate, d authz.Decision) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, a := range st.allowed {
		if sameCoordinate(a.c, c) {
			return
		}
	}
	st.allowed = append(st.allowed, allowedWrite{c: c, d: d})
}

func sameCoordinate(a, b authz.Coordinate) bool {
	if a.Repository != b.Repository || a.Path != b.Path || a.Package != b.Package || a.Version != b.Version || len(a.Actions) != len(b.Actions) {
		return false
	}
	for i := range a.Actions {
		if a.Actions[i] != b.Actions[i] {
			return false
		}
	}
	return true
}

func (st *requestState) track(s *spooled) {
	st.mu.Lock()
	st.spools = append(st.spools, s)
	st.mu.Unlock()
}

// recheck is the commit-time re-check of R-AUTHZ-015 for a format's write: every
// write decision the request was allowed is decided again on the index of this
// moment, before the transaction of the write opens (a stale index makes a
// decision wait, R-ARCH-019), and the request decides on that index from then on,
// so the ownership check and the auto claim inside the transaction see it too. A
// body may have taken minutes; a grant removed meanwhile stops the write, as it
// stops a deploy through /files/ (Q-ARCH-225). A refusal is the decision's error;
// the decisions recorded in the transaction are the new ones.
func (h *Host) recheck(ctx context.Context, rq *formats.Request) error {
	st := stateOf(rq)
	if st == nil {
		return errs.E(errs.Internal, errs.CodeInternal, "The request was not prepared by the format dispatcher.")
	}
	st.mu.Lock()
	allowed := append([]allowedWrite(nil), st.allowed...)
	st.mu.Unlock()
	if len(allowed) == 0 {
		return nil
	}
	var keys []string
	for _, a := range allowed {
		if !slices.Contains(keys, a.c.Repository) {
			keys = append(keys, a.c.Repository)
		}
	}
	idx, err := h.o.Decider.Begin(ctx, rq.Subject, keys...)
	if err != nil {
		return err
	}
	for i, a := range allowed {
		d := h.o.Decider.DecideOn(ctx, idx, rq.Subject, a.c)
		if !d.Allowed() {
			return d.Err()
		}
		allowed[i].d = d
	}
	st.mu.Lock()
	for _, a := range allowed {
		for j := range st.allowed {
			if sameCoordinate(st.allowed[j].c, a.c) {
				st.allowed[j].d = a.d
			}
		}
	}
	st.mu.Unlock()
	rq.Index = idx
	return nil
}

// record writes the allowed write decisions of the request into the transaction
// of the change. A request the host did not prepare is a defect: it fails the
// write instead of losing the audit of an allowed change.
func (st *requestState) record(ctx context.Context, tx meta.Tx, d authz.Decider, sub authz.Subject) error {
	if st == nil {
		return errs.E(errs.Internal, errs.CodeInternal, "The request was not prepared by the format dispatcher.")
	}
	st.mu.Lock()
	allowed := append([]allowedWrite(nil), st.allowed...)
	st.mu.Unlock()
	for _, a := range allowed {
		if err := d.RecordAllowed(ctx, tx, sub, a.c, a.d); err != nil {
			return err
		}
	}
	return nil
}
