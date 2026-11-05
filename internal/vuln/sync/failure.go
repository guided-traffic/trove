package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// bookkeepingTimeout bounds the write of a failure: the run's context may be the very thing that ended.
const bookkeepingTimeout = 15 * time.Second

// asError turns the error of a run into the *errs.Error whose code and detail the problem object carries.
func asError(err error) *errs.Error {
	var f *failure
	if errors.As(err, &f) {
		return errs.E(f.kind, f.code, f.detail, errs.WithCause(f.cause))
	}
	if e, ok := errs.As(err); ok {
		return e
	}
	return errs.E(errs.Internal, errs.CodeInternal, "the sync run failed", errs.WithCause(err))
}

// problemJSON renders the problem object of a failed run (R-ARCH-023) with the time it happened and, when the audit event of
// this failure was written, the time of that event. The event is rate limited by that member: a failure that follows another
// within the hour carries the earlier time forward and writes none.
func problemJSON(err error, at, eventAt time.Time) (json.RawMessage, string) {
	p, e := errs.ProblemOf(asError(err), "", "")
	if e.Kind == errs.Internal {
		// Internal detail is never written for a reader; the cause is in the log.
		p.Detail = "The sync run failed; see the log of the worker."
	}
	body, _ := json.Marshal(p)
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	m["at"] = at.UTC().Format(time.RFC3339)
	if !eventAt.IsZero() {
		m["eventAt"] = eventAt.UTC().Format(time.RFC3339)
	}
	out, _ := json.Marshal(m)
	return out, p.Code
}

// attempt is a run's mark on the row of the ecosystem it syncs: the attempted_at its attempt wrote, the zero time until that
// write committed (R-VULN-004 "Failure", Q-VULN-80). A failure is written only while the row still carries it.
type attempt struct{ at time.Time }

// interrupted is the error of a run that its context ended, other than by a drain's release: the problem says what ended it
// (R-VULN-004 "Failure", Q-VULN-80).
func interrupted(cause error) *failure {
	switch {
	case errors.Is(cause, jobs.ErrLeaseLost):
		return &failure{kind: errs.Unavailable, code: errs.CodeSyncLeaseLost, detail: "the worker lost the lease of the run's job and stopped the run", cause: cause}
	case errors.Is(cause, jobs.ErrCancelRequested):
		return &failure{kind: errs.Conflict, code: errs.CodeSyncCancelled, detail: "an operator cancelled the run's job", cause: cause}
	}
	detail := "the run stopped before it ended"
	if cause != nil {
		detail += ": " + cause.Error()
	}
	return &failure{kind: errs.Unavailable, code: errs.CodeSyncInterrupted, detail: detail, cause: cause}
}

// panicked is the error of a run whose handler panicked. Its kind is internal, so the problem object carries a fixed detail
// and the value of the panic stays in the log.
func panicked(v any) *failure {
	return &failure{kind: errs.Internal, code: errs.CodeSyncPanicked, detail: "the sync run panicked", cause: fmt.Errorf("panic: %v", v)}
}

// guarded runs the sync of one source and ecosystem. A panic of it is recorded as the ecosystem's failure and then goes on, so
// that the worker's own recovery ends the job failed as for any other kind (R-VULN-004 "Failure", Q-VULN-80).
func guarded[T any](ctx context.Context, s *Service, source, eco, kind string, at *attempt, sync func() (T, error)) (T, error) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("advisory sync panicked", "source", source, "ecosystem", eco, "panic", r, "stack", string(debug.Stack()))
			s.recordFailure(ctx, source, eco, kind, at.at, panicked(r))
			panic(r)
		}
	}()
	return sync()
}

// recordEnd records a run of one ecosystem that its context ended, other than by a drain's release: what ended it, on the
// row the run attempted. A run that ended before its attempt of the ecosystem committed has not begun it: no row carries a
// sync of it that did not end, and the job's own error says how the run ended.
func (s *Service) recordEnd(ctx context.Context, source, eco, kind string, at time.Time, cause error) {
	if at.IsZero() {
		s.log.Warn("advisory sync stopped before it began the ecosystem", "source", source, "ecosystem", eco, "error", cause)
		return
	}
	s.recordFailure(ctx, source, eco, kind, at, interrupted(cause))
}

// recordFailure records a failed run: the previous data stays, last_error holds the problem, the consecutive failures rise and
// the run is counted failed (R-VULN-004 "Failure"). The audit event vulnerability.source.failed is written at most once an hour
// per source and ecosystem (R-VULN-024); the time of the last one lives in last_error, so that the limit holds across
// worker replicas.
//
// A run whose job an operator cancelled (sync_cancelled) records the problem and leaves the consecutive failures as they are,
// is counted cancelled and writes no event: the cancel is the operator's act, and system.job.cancelled records it (Q-VULN-80).
//
// at is the attempted_at the run's attempt of the ecosystem wrote, the zero time when the run failed before that attempt
// committed. A row that carries another one was attempted by a newer run since, which has the row now: the failure of this
// run, which lost its lease to it, is logged and not written.
func (s *Service) recordFailure(ctx context.Context, source, eco, kind string, at time.Time, cause error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bookkeepingTimeout)
	defer cancel()
	cancelled := errors.Is(cause, jobs.ErrCancelRequested)
	if cancelled {
		s.log.Info("advisory sync cancelled", "source", source, "ecosystem", eco, "kind", kind)
		s.countRun(source, eco, kind, outcomeCancelled)
	} else {
		s.log.Error("advisory sync failed", "source", source, "ecosystem", eco, "kind", kind, "error", cause)
		s.countRun(source, eco, kind, outcomeFailed)
	}
	now := s.now()
	var code string
	emit, superseded := false, false
	err := s.o.DB.InTx(ctx, func(tx metaTx) error {
		db, err := store.DBOf(tx)
		if err != nil {
			return err
		}
		if err := s.st.Ensure(ctx, db, source, eco); err != nil {
			return err
		}
		prev, err := s.st.Lock(ctx, db, source, eco)
		if err != nil {
			return err
		}
		if !at.IsZero() && !prev.AttemptedAt.Equal(at) {
			superseded = true
			return nil
		}
		var last time.Time
		var old struct {
			EventAt string `json:"eventAt"`
		}
		if json.Unmarshal(prev.LastError, &old) == nil && old.EventAt != "" {
			last, _ = time.Parse(time.RFC3339, old.EventAt)
		}
		eventAt := last
		if !cancelled && (last.IsZero() || now.Sub(last) >= failedEventEvery) {
			eventAt, emit = now, true
		}
		var body json.RawMessage
		body, code = problemJSON(cause, now, eventAt)
		if cancelled {
			return s.st.Cancelled(ctx, db, source, eco, body)
		}
		return s.st.Failure(ctx, db, source, eco, body)
	})
	switch {
	case err != nil:
		s.log.Error("the failure of an advisory sync could not be recorded", "source", source, "ecosystem", eco, "error", err)
		return
	case superseded:
		s.log.Warn("the failure of an advisory sync is not recorded: a newer run has attempted the ecosystem since this one did",
			"source", source, "ecosystem", eco, "error", cause)
		return
	}
	if emit {
		d, _ := json.Marshal(map[string]string{"source": source, "ecosystem": eco, "code": code})
		s.o.Recorder.Record(ctx, audit.Event{
			Type: audit.EventVulnSourceFailed, Actor: audit.Actor{Kind: "system", Name: "vulnerability-sync"}, Outcome: audit.Failure,
			Reason: code, Subject: audit.Subject{Kind: "vulnerability_source", Name: source + ":" + eco}, Details: d,
		})
	}
}
