package mirror

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// call sends one request of the pass through the window (R-REPO-054): it waits while a wait is in force and while the window
// is full, repeats the request after a rate-limit answer (with the window halved) and after a *WaitError (without counting
// anything), and returns every other error. A pre-emption comes back as ErrPreempted for the caller to defer the item.
// After the pass stopped taking work it returns errHalted.
func (p *pass) call(ctx context.Context, fn func(context.Context) error) error {
	for {
		tk, err := p.th.acquire(ctx)
		if err != nil {
			return err
		}
		err = fn(ctx)
		if err == nil {
			p.th.success(tk)
			return nil
		}
		if errors.Is(err, ErrShuttingDown) {
			// The process is shutting down: the pass stops as if its job's context had ended and writes nothing (R-REPO-053).
			p.th.abandoned(tk)
			p.setHalt(haltShutdown, nil)
			return errHalted
		}
		if ctx.Err() != nil {
			p.th.abandoned(tk)
			return ctx.Err()
		}
		var wait *WaitError
		switch {
		case errors.Is(err, ErrPreempted):
			p.th.abandoned(tk)
			return err
		case errors.As(err, &wait):
			p.th.abandoned(tk)
			if serr := p.e.o.Clock.Sleep(ctx, wait.After); serr != nil {
				return serr
			}
			continue
		}
		if rl, ok := discover.AsRateLimited(err); ok {
			hint, hinted := rl.RetryAfter()
			waitFor, started := p.th.rateLimited(tk, hint, hinted)
			p.onRateLimit(ctx, rl, waitFor, hinted, started)
			continue
		}
		p.th.failed(tk)
		return err
	}
}

// callDiscovery is call for a request of discovery (the methods, a page, a deleted package's document): a client that
// pre-empted it makes the pass wait PreemptRetry and send it again, and never fails the pass (R-REPO-055, Q-REPO-184).
func (p *pass) callDiscovery(ctx context.Context, fn func(context.Context) error) error {
	for {
		err := p.call(ctx, fn)
		if !errors.Is(err, ErrPreempted) {
			return err
		}
		p.m.preempted()
		if serr := p.e.o.Clock.Sleep(ctx, PreemptRetry); serr != nil {
			return serr
		}
	}
}

// onRateLimit counts a rate-limit answer. An answer that begins a wait is also recorded in the state and, at most once per
// pass and hour, in the audit trail; a wait longer than LongWait ends the attempt (R-REPO-054).
func (p *pass) onRateLimit(ctx context.Context, rl discover.RateLimited, wait time.Duration, hinted, started bool) {
	p.m.rateLimited(rl.RateLimitReason())
	if !started {
		return
	}
	p.m.waited(wait.Seconds())
	now := p.e.o.Clock.Now()
	if !p.wrote(p.e.o.Store.RecordRateLimit(ctx, p.fence, now, wait, rateLimitStatus(rl))) {
		return
	}
	p.eventRateLimited(ctx, rl, int64(wait.Seconds()), hinted)
	if wait > LongWait {
		p.deferLong(ctx, now.Add(wait))
	}
}

// deferLong stores the end of a long wait and stops the attempt: no job slot is held through the wait, the dispatch enqueues
// the next job when it is over.
func (p *pass) deferLong(ctx context.Context, until time.Time) {
	if !p.wrote(p.e.o.Store.Defer(ctx, p.fence, until)) {
		return
	}
	p.mu.Lock()
	p.deferUntil = until
	p.mu.Unlock()
	p.setHalt(haltDeferred, nil)
}

// record writes an asynchronous event of the pass: the system is the actor, the repository its subject (R-REPO-057).
func (p *pass) record(ctx context.Context, typ string, outcome audit.Outcome, reason string, details map[string]any) {
	d, _ := json.Marshal(details)
	repo, _ := p.current() // the name as the snapshot holds it now: a move renames the repository under a running pass
	name := repo.Name
	p.e.o.Audit.Record(context.WithoutCancel(ctx), audit.Event{
		Type: typ, Actor: audit.Actor{Kind: "system", Name: "mirror"}, Outcome: outcome, Reason: reason,
		Subject: audit.Subject{Kind: "repository", Name: name.String()}, Repository: name.String(), Project: name.Project, Details: d,
	})
}
