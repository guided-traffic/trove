package sync

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// bookkeepingTimeout bounds the write of a failure: the run's context may be the very thing that ended.
const bookkeepingTimeout = 15 * time.Second

// asError turns the error of a run into the *errs.Error whose code and detail the problem object carries.
func asError(err error) *errs.Error {
	var f *failure
	if errors.As(err, &f) {
		return errs.E(errs.UpstreamFailed, f.code, f.detail, errs.WithCause(f.cause))
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

// recordFailure records a failed run: the previous data stays, last_error holds the problem, the consecutive failures rise and
// the run is counted failed (R-VULN-004 "Failure"). The audit event vulnerability.source.failed is written at most once an hour
// per source and ecosystem (R-VULN-024); the time of the last one lives in last_error, so that the limit holds across
// worker replicas.
func (s *Service) recordFailure(ctx context.Context, source, eco, kind string, cause error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bookkeepingTimeout)
	defer cancel()
	s.log.Error("advisory sync failed", "source", source, "ecosystem", eco, "kind", kind, "error", cause)
	s.countRun(source, eco, kind, "failed")
	now := s.now()
	var code string
	emit := false
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
		var last time.Time
		var old struct {
			EventAt string `json:"eventAt"`
		}
		if json.Unmarshal(prev.LastError, &old) == nil && old.EventAt != "" {
			last, _ = time.Parse(time.RFC3339, old.EventAt)
		}
		eventAt := last
		if last.IsZero() || now.Sub(last) >= failedEventEvery {
			eventAt, emit = now, true
		}
		var body json.RawMessage
		body, code = problemJSON(cause, now, eventAt)
		return s.st.Failure(ctx, db, source, eco, body)
	})
	if err != nil {
		s.log.Error("the failure of an advisory sync could not be recorded", "source", source, "ecosystem", eco, "error", err)
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
