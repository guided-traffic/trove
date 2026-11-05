package mirror

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
)

// The end of a pass attempt (R-REPO-053 step 4, the stops and the errors): what is written, counted and recorded for each of
// the ways it can end.

// endOpts say how an end changes the row.
type endOpts struct {
	next         time.Time
	keep         bool
	clearPending bool
	advance      bool
	resetFails   bool
	bound        string
	clearBound   bool
}

// finish ends the attempt after the work stopped: execErr is an error of the pass itself, else the reason it halted tells
// what the end is.
func (p *pass) finish(ctx context.Context, execErr error) error {
	p.mu.Lock()
	h, herr := p.halted, p.err
	p.mu.Unlock()
	if execErr == nil && h == haltFatal {
		execErr = herr
	}
	switch {
	case errors.Is(execErr, ErrFenceLost):
		return p.fenceLost()
	case execErr != nil:
		return p.fail(ctx, execErr)
	}
	now := p.e.o.Clock.Now()
	switch h {
	case haltDeferred:
		return p.deferredEnd()
	case haltPause:
		return p.end(ctx, OutcomeStopped, endOpts{keep: true, next: now})
	case haltMode:
		return p.end(ctx, OutcomeStopped, endOpts{clearPending: true, next: now})
	case haltOffline:
		recheck := min(time.Duration(p.settings.Mirror.IntervalSeconds)*time.Second, OfflineRecheck)
		return p.end(ctx, OutcomeOffline, endOpts{keep: true, next: now.Add(recheck)})
	case haltBound:
		return p.end(ctx, OutcomeBoundReached, endOpts{next: p.nextPass(now), advance: p.done, resetFails: true, bound: BoundBytes})
	}
	outcome := OutcomeComplete
	p.mu.Lock()
	if p.c.failed > 0 {
		outcome = OutcomePartial
	}
	p.mu.Unlock()
	return p.end(ctx, outcome, endOpts{next: p.nextPass(now), advance: true, resetFails: true, clearBound: true})
}

// nextPass is when the pass after this one is due: its start plus the interval, or now when that has passed (R-REPO-053).
func (p *pass) nextPass(now time.Time) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	next := p.state.PassStartedAt.Add(time.Duration(p.settings.Mirror.IntervalSeconds) * time.Second)
	if next.Before(now) {
		return now
	}
	return next
}

// summary is the summary of the attempt for the status and the event.
func (p *pass) summary(outcome string, now time.Time) passSummary {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := passSummary{
		Kind: string(p.kind), StartedAt: p.state.PassStartedAt.UTC().Format(time.RFC3339), FinishedAt: now.UTC().Format(time.RFC3339),
		Outcome: outcome, Packages: p.c.done, PackagesFailed: p.c.failed, ItemsStored: p.c.items, BytesStored: p.c.bytes,
		DiscoveryComplete: p.state.PassDiscoveryComplete,
	}
	if p.method != "" {
		m := string(p.method)
		s.Method = &m
	}
	if p.state.PassDiscoveryNote != "" {
		n := p.state.PassDiscoveryNote
		s.DiscoveryNote = &n
	}
	return s
}

// end writes the end of the attempt, counts it, records the event and sets the job's result.
func (p *pass) end(ctx context.Context, outcome string, o endOpts) error {
	now := p.e.o.Clock.Now()
	sum := p.summary(outcome, now)
	boundNew, err := p.e.o.Store.EndPass(ctx, p.fence, End{
		Outcome: outcome, LastPass: sum.json(), Now: now, NextPassAt: o.next, KeepPass: o.keep, ClearPending: o.clearPending,
		Advance: o.advance, Fingerprint: p.fingerprint, ResetFailures: o.resetFails, Bound: o.bound, ClearBound: o.clearBound,
	})
	if errors.Is(err, ErrFenceLost) {
		return p.fenceLost()
	}
	if err != nil {
		return fmt.Errorf("mirror: the end of the pass is not written: %w", err)
	}
	p.m.pass(string(p.kind), outcome)
	method := ""
	if sum.Method != nil {
		method = *sum.Method
	}
	p.record(ctx, audit.EventMirrorPassCompleted, audit.Success, "", map[string]any{
		"kind": sum.Kind, "method": method, "outcome": outcome, "packages": sum.Packages, "packagesFailed": sum.PackagesFailed,
		"itemsStored": sum.ItemsStored, "bytesStored": sum.BytesStored, "discoveryComplete": sum.DiscoveryComplete, "job": p.fence.JobID,
	})
	if boundNew && p.bound != nil {
		p.mu.Lock()
		used := p.usedBytes
		p.mu.Unlock()
		p.record(ctx, audit.EventMirrorBoundReached, audit.Success, "", map[string]any{"bound": BoundBytes, "usedBytes": used, "limitBytes": p.bound.limit()})
	}
	return p.res.SetResult(map[string]any{
		"outcome": outcome, "kind": sum.Kind, "method": method, "packages": sum.Packages, "packagesFailed": sum.PackagesFailed,
		"itemsStored": sum.ItemsStored, "bytesStored": sum.BytesStored, "discoveryComplete": sum.DiscoveryComplete,
	})
}

// deferredEnd ends the job of an attempt that met a long rate-limit wait: the state was written when the wait began, the
// pass stays, and the result says until when (R-REPO-054).
func (p *pass) deferredEnd() error {
	p.mu.Lock()
	until := p.deferUntil
	p.mu.Unlock()
	p.m.pass(string(p.kind), OutcomeDeferred)
	return p.res.SetResult(map[string]string{"deferred": "rate_limited", "until": until.UTC().Format(time.RFC3339)})
}

// fenceLost ends the job of an attempt that another job has taken the pass from: nothing more is written (Q-ARCH-287).
func (p *pass) fenceLost() error {
	p.m.pass(string(p.kind), OutcomeStopped)
	p.log.Warn("the pass is owned by another job; this job stops without writing")
	return p.res.SetResult(map[string]string{"outcome": OutcomeStopped, "reason": "fence_lost"})
}

// fail records a failure of the pass itself: the error and the count in the state, the next attempt after the back-off, the
// event. The pass in progress stays; the job ends with the error (R-REPO-053 errors, Q-REPO-136).
func (p *pass) fail(ctx context.Context, cause error) error {
	code := CodeInternal
	var pe *passError
	if errors.As(cause, &pe) {
		code = pe.code
	}
	msg := sanitize(cause.Error())
	now := p.e.o.Clock.Now()
	f, err := p.e.o.Store.RecordFailure(ctx, p.fence, now, code, msg, JitterFactor(p.e.o.Jitter.Float64()))
	if errors.Is(err, ErrFenceLost) {
		return p.fenceLost()
	}
	out := fmt.Errorf("mirror pass failed (%s): %s", code, msg)
	if err != nil {
		p.log.Error("the failure of the pass is not recorded", "error", err)
		return out
	}
	p.m.pass(string(p.kind), OutcomeFailed)
	p.record(ctx, audit.EventMirrorPassFailed, audit.Failure, code, map[string]any{
		"code": code, "failures": f.Failures, "nextPassAt": f.NextPassAt.UTC().Format(time.RFC3339), "job": p.fence.JobID,
	})
	p.log.Warn("the pass failed", "code", code, "failures", f.Failures, "next", f.NextPassAt, "error", msg)
	return out
}

var urlPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>)\]]+`)

// sanitize removes what a message may not show: a URL becomes its host, so that no path, query or credential is kept
// (R-REPO-056: the message names hosts, never a URL or a credential).
func sanitize(msg string) string {
	return urlPattern.ReplaceAllStringFunc(msg, func(s string) string {
		if u, err := url.Parse(s); err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
		return "<url>"
	})
}
