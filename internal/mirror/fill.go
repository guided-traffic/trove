package mirror

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// The fill of a pass (R-REPO-053 step 3): the pending names in name order, each package filled through the host under the
// window by one of as many workers as the ceiling, its row deleted and the counters raised in one statement.

// batch is the number of pending names the feeder reads at a time.
const batch = 100

// fill works off the pending names until none is left to take or the attempt is halted. A name that failed waits for its
// retry in the table, one that was pre-empted in memory; the pass waits for the earliest of them before it ends.
func (p *pass) fill(ctx context.Context) (handled int, err error) {
	for {
		n, err := p.fillRound(ctx)
		handled += n
		if err != nil {
			return handled, err
		}
		if p.isHalted() {
			return handled, nil
		}
		wake, err := p.nextWake(ctx)
		if err != nil {
			return handled, &passError{code: CodeInternal, err: err}
		}
		if n == 0 && wake.IsZero() {
			return handled, nil
		}
		if d := wake.Sub(p.e.o.Clock.Now()); !wake.IsZero() && d > 0 {
			if err := p.e.o.Clock.Sleep(ctx, d); err != nil {
				return handled, err
			}
		}
	}
}

// nextWake returns when the earliest waiting name becomes due, zero when none waits.
func (p *pass) nextWake(ctx context.Context) (time.Time, error) {
	now := p.e.o.Clock.Now()
	wake, err := p.e.o.Store.NextWake(ctx, p.id, now)
	if err != nil {
		return time.Time{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for name, at := range p.deferred {
		if !at.After(now) {
			delete(p.deferred, name)
			continue
		}
		if wake.IsZero() || at.Before(wake) {
			wake = at
		}
	}
	return wake, nil
}

// isDeferred reports whether a pre-empted name waits for its retry.
func (p *pass) isDeferred(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	at, ok := p.deferred[name]
	return ok && at.After(p.e.o.Clock.Now())
}

// fillRound reads the pending names that are due in name order and hands them to the workers; it returns the number of
// names it handed out.
func (p *pass) fillRound(ctx context.Context) (int, error) {
	names := make(chan string)
	var wg sync.WaitGroup
	for range p.ceiling {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for name := range names {
				p.fillPackage(ctx, name)
			}
		}()
	}
	n := p.feed(ctx, names)
	close(names)
	wg.Wait()
	p.mu.Lock()
	err := p.err
	p.mu.Unlock()
	if err != nil && !errors.Is(err, ErrFenceLost) {
		var pe *passError
		if !errors.As(err, &pe) {
			err = &passError{code: CodeInternal, err: err}
		}
		return n, err
	}
	return n, nil
}

// feed sends the due pending names to the workers in name order.
func (p *pass) feed(ctx context.Context, names chan<- string) int {
	n, after := 0, ""
	for !p.isHalted() {
		rows, err := p.e.o.Store.NextPending(ctx, p.id, p.e.o.Clock.Now(), after, batch)
		if err != nil {
			if ctx.Err() == nil {
				p.fatal(err)
			}
			return n
		}
		if len(rows) == 0 {
			return n
		}
		for _, r := range rows {
			after = r.Name
			if p.isDeferred(r.Name) {
				continue
			}
			select {
			case names <- r.Name:
				n++
			case <-ctx.Done():
				return n
			}
			if p.isHalted() {
				return n
			}
		}
	}
	return n
}

// pkgState is what one package's attempt did so far.
type pkgState struct {
	items, bytes int64
}

// fillPackage fills one package: its plan, then each artifact the version bounds keep (R-REPO-051). The package leaves the
// pending table when every artifact is stored or present; it stays, with its counters raised, when the attempt halted, a
// client pre-empted a fetch or the bound was reached, and is retried with one more attempt when it failed.
func (p *pass) fillPackage(ctx context.Context, name string) {
	if p.checkStop(ctx) {
		return
	}
	repo, rs := p.current()
	if !p.admitNow(name) {
		// The patterns or the claims changed after the name was discovered: it is not asked for, not even its document
		// (R-REPO-051); the row is done.
		p.finishPackage(ctx, name, pkgState{})
		return
	}
	var st pkgState
	plan, err := p.plan(ctx, name, &st)
	if err != nil {
		p.afterError(ctx, name, st, err)
		return
	}
	var transient, permanent error
	stored := map[string]bool{} // the paths this attempt stored or found: an artifact that two versions name is filled once
	for _, item := range SelectItems(plan.Items, rs.Mirror.IncludePrereleases, rs.Mirror.MaxVersionsPerPackage) {
		if p.isHalted() {
			p.keep(ctx, name, st)
			return
		}
		if stored[item.Path] {
			continue
		}
		if ok, err := p.admitsBytes(ctx, item); err != nil {
			p.fatal(&passError{code: CodeInternal, err: err})
			return
		} else if !ok {
			p.m.item(ResultBounded)
			p.setHalt(haltBound, nil)
			p.keep(ctx, name, st)
			return
		}
		var res FillResult
		err := p.call(ctx, func(c context.Context) (e error) {
			res, e = p.e.o.Host.Fill(c, repo, Pass{JobID: p.fence.JobID, Ceiling: p.ceiling}, item)
			return e
		})
		var refused *RefusedError
		var perm *PermanentError
		switch {
		case err == nil:
			stored[item.Path] = true
			p.count(&st, res)
		case errors.As(err, &refused):
			// Nothing outside the pass changed, so the artifact is not stored: counted, and the next item goes on (R-REPO-057).
			p.m.item(ResultRefused)
		case errors.Is(err, errHalted), errors.Is(err, ErrPreempted), ctx.Err() != nil:
			p.afterError(ctx, name, st, err)
			return
		case errors.As(err, &perm):
			// The other items go on; the package fails once, after them (R-REPO-041, R-REPO-053).
			p.m.item(ResultFailed)
			if permanent == nil {
				permanent = err
			}
		default:
			// A transient failure of one item does not keep the others from being stored: the package fails once, after them.
			p.m.item(ResultFailed)
			if transient == nil {
				transient = err
			}
		}
	}
	if ctx.Err() != nil {
		return
	}
	switch {
	case transient != nil:
		// An item that may succeed later keeps the package's retries, and its error is the reason (Q-REPO-252).
		p.failPackage(ctx, name, st, transient, false)
		return
	case permanent != nil:
		p.failPackage(ctx, name, st, permanent, true)
		return
	}
	p.finishPackage(ctx, name, st)
}

// finishPackage deletes the package's pending row and raises the counters.
func (p *pass) finishPackage(ctx context.Context, name string, st pkgState) {
	if p.wrote(p.e.o.Store.PackageDone(ctx, p.fence, PackageResult{Name: name, Items: st.items, Bytes: st.bytes, Now: p.e.o.Clock.Now()})) {
		p.add(func(c *counters) { c.done++; c.items += st.items; c.bytes += st.bytes })
	}
}

// plan asks the format for the package's plan under the window. An artifact the format's plan stores itself, to read what it names
// (discover.Upstream.Artifact), is counted in the package's tally like one the engine fills: the host reports it through
// [ReportStored]. A plan runs in one goroutine, so the tally needs no lock.
func (p *pass) plan(ctx context.Context, name string, st *pkgState) (plan discover.Plan, err error) {
	err = p.call(ctx, func(c context.Context) (e error) {
		c = withStoredTally(c, func(res FillResult) { p.count(st, res) })
		plan, e = p.lister.MirrorPlan(c, p.up, name)
		return e
	})
	return plan, err
}

// admitsBytes checks the byte bound before an artifact is fetched (R-REPO-052).
func (p *pass) admitsBytes(ctx context.Context, item discover.Item) (bool, error) {
	if p.bound == nil {
		return true, nil
	}
	ok, used, err := p.bound.admits(ctx, item.Size)
	if err == nil {
		p.mu.Lock()
		p.usedBytes = max(p.usedBytes, used)
		p.mu.Unlock()
	}
	return ok, err
}

// count counts the result of a fill.
func (p *pass) count(st *pkgState, res FillResult) {
	if res.Present {
		p.m.item(ResultPresent)
		return
	}
	p.m.item(ResultStored)
	p.m.bytes(res.Bytes)
	st.items++
	st.bytes += res.Bytes
	if p.bound != nil {
		p.bound.add(res.Bytes)
	}
}

// keep leaves the package pending without a failure and adds what the attempt stored to the counters.
func (p *pass) keep(ctx context.Context, name string, st pkgState) {
	if ctx.Err() != nil || p.isShutdown() {
		return
	}
	if p.wrote(p.e.o.Store.PackageKept(ctx, p.fence, PackageResult{Name: name, Items: st.items, Bytes: st.bytes, Now: p.e.o.Clock.Now()})) {
		p.add(func(c *counters) { c.items += st.items; c.bytes += st.bytes })
	}
}

// afterError decides what an error of a package's request means: the end of the attempt (a halt, the context), a pre-emption
// (the package comes back later, no failure), or a failure of the package (R-REPO-053 step 3).
func (p *pass) afterError(ctx context.Context, name string, st pkgState, err error) {
	switch {
	case errors.Is(err, errHalted):
		p.keep(ctx, name, st)
	case ctx.Err() != nil:
		// A release at shutdown, a lost lease or a fatal error elsewhere: nothing more is written for this package.
	case errors.Is(err, ErrPreempted):
		p.m.item(ResultPreempted)
		p.m.preempted()
		p.mu.Lock()
		p.deferred[name] = p.e.o.Clock.Now().Add(PreemptRetry)
		p.mu.Unlock()
		p.keep(ctx, name, st)
	default:
		p.m.item(ResultFailed)
		p.failPackage(ctx, name, st, err, false)
	}
}

// failPackage records one failed attempt of a package: one more attempt and a retry a minute later per attempt; the attempt
// that reaches the limit leaves it to the next pass and counts it failed (R-REPO-053 step 3).
func (p *pass) failPackage(ctx context.Context, name string, st pkgState, err error, permanent bool) {
	attempts, ferr := p.e.o.Store.PackageFailed(ctx, p.fence, PackageResult{
		Name: name, Items: st.items, Bytes: st.bytes, Now: p.e.o.Clock.Now(), Error: sanitize(err.Error()), Permanent: permanent,
	})
	if !p.wrote(ferr) {
		return
	}
	p.add(func(c *counters) {
		c.items += st.items
		c.bytes += st.bytes
		if attempts >= MaxPackageAttempts {
			c.failed++
		}
	})
	p.log.Warn("a package could not be filled", "attempts", attempts, "error", sanitize(err.Error()))
}
