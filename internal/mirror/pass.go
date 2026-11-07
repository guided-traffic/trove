package mirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/repository"
)

// A pass attempt: the claim, the discovery and the fill, the stops and the end (R-REPO-053). One pass runs in one goroutine
// that discovers and, while it fills, in as many workers as its ceiling; every write of it is conditional on the fence.

// halt says why a pass attempt stops taking new work; the first reason wins.
type halt int

const (
	haltNone     halt = iota
	haltPause         // paused, or the repository is disabled: the pass stays for the resume
	haltMode          // the mode changed, or the repository is gone: the pass is cleared
	haltOffline       // offline mode or the global switch: the pass stays, no failure
	haltBound         // the byte bound was reached: the pending names stay
	haltDeferred      // a rate-limit wait longer than LongWait: the job ends, the dispatch continues the pass
	haltFatal         // an error of the pass itself or a lost fence
	haltShutdown      // the host says the process is shutting down: the job is released, nothing is written
)

// counters are the counters of the pass in progress as the row holds them.
type counters struct {
	names, done, failed, items, bytes int64
}

type pass struct {
	e   *Engine
	log *slog.Logger
	res ResultSetter
	m   repoMetrics

	id    int64
	fence Fence
	args  PassArgs

	// What the pass uses, set up by prepare.
	lister      discover.Lister
	up          discover.Upstream
	admits      func(string) bool
	fingerprint []byte
	host        string
	th          *throttle
	ceiling     int
	bound       *byteBound
	cancel      context.CancelFunc

	mu         sync.Mutex
	repo       repository.Repository
	settings   repository.RemoteSettings
	state      State
	c          counters
	kind       discover.PassKind
	method     discover.Method
	done       bool // discovery has ended
	halted     halt
	err        error
	deferUntil time.Time
	deferred   map[string]time.Time
	rlEvent    bool
	usedBytes  int64
	// admitVersion is the config version of the repository document the admission was built from; a newer snapshot rebuilds it.
	admitVersion int64
	// incomplete says that discovery stopped before its end, with the reason in the state's discovery note.
	incomplete bool
}

// RunPass is the handler of the job mirror-pass: one attempt of the pass of the repository the arguments name. A pass
// outlives its jobs (Q-REPO-135); this attempt claims it, continues it and ends it, or stops where it was told to.
func (e *Engine) RunPass(ctx context.Context, jobID string, args json.RawMessage, res ResultSetter) error {
	var a PassArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return jobs.Permanent(fmt.Errorf("mirror: the arguments of the pass: %w", err))
		}
	}
	p, skipped, err := e.begin(ctx, jobID, a, res)
	if err != nil || p == nil {
		if err == nil {
			err = res.SetResult(map[string]string{"skipped": skipped})
		}
		return err
	}
	return p.run(ctx)
}

// begin reads the repository, checks that a pass may start and claims it. skipped names why no pass started.
func (e *Engine) begin(ctx context.Context, jobID string, a PassArgs, res ResultSetter) (p *pass, skipped string, err error) {
	if err := e.o.Host.Ready(ctx); err != nil {
		// The host cannot serve passes: the job fails with the cause and the dispatch tries again (Q-ARCH-312).
		return nil, "", fmt.Errorf("mirror: the host is not ready: %w", err)
	}
	// The pass holds the repository by its id: a move changes the name (R-REPO-060), the arguments carry the name of the
	// enqueue only for the logs.
	repo, err := e.o.Repos.ByID(ctx, a.RepositoryID)
	switch {
	case err != nil && notFound(err):
		return nil, "repository_gone", nil
	case err != nil:
		return nil, "", err
	}
	rs, ok := mirrored(repo)
	switch {
	case !ok:
		if _, loaded := repo.Remote(); !loaded {
			// The stored document does not load (the snapshot disabled the repository): a pause, not a mode change (Q-REPO-191).
			return nil, "invalid_document", nil
		}
		return nil, "not_mirror", nil
	case repo.Disabled || rs.Mirror.Paused:
		return nil, "paused", nil
	}
	if _, ok := e.o.Host.Lister(repo.Format); !ok {
		return nil, "", fmt.Errorf("mirror: the %s format has no discovery", repo.Format)
	}
	include, exclude, err := patterns(repo)
	if err != nil {
		return nil, "", err
	}
	fence := Fence{RepoID: a.RepositoryID, JobID: jobID, Holder: e.o.Holder}
	now := e.o.Clock.Now()
	if err := e.o.Store.EnsureStates(ctx, []int64{fence.RepoID}); err != nil {
		return nil, "", err
	}
	st, ok, err := e.o.Store.State(ctx, fence.RepoID)
	if err != nil || !ok {
		return nil, "", errors.Join(err, errors.New("mirror: the repository has no state row"))
	}
	if st.RateLimitedUntil.After(now) {
		// A wait of an earlier attempt still lies ahead: nothing is sent before it is over; the dispatch continues the pass then
		// (R-REPO-054, Q-REPO-187).
		return nil, "rate_limited", nil
	}
	fp := Fingerprint(include, exclude)
	kind := DecideKind(st, fp, time.Duration(rs.Mirror.FullPassIntervalSeconds)*time.Second, now, a.Full)
	if claimed, err := e.o.Store.Claim(ctx, Claim{Fence: fence, Full: a.Full, KindIfNew: kind, Now: now}); err != nil {
		return nil, "", err
	} else if !claimed {
		return nil, "repository_gone", nil
	}
	st, ok, err = e.o.Store.State(ctx, fence.RepoID)
	if err != nil || !ok {
		return nil, "", errors.Join(err, errors.New("mirror: the state row is gone after the claim"))
	}
	p = &pass{
		e: e, res: res, id: fence.RepoID, fence: fence, args: a, repo: repo, settings: rs, state: st,
		fingerprint: fp, kind: st.PassKind, method: st.PassMethod, done: st.PassDiscoveryDone, deferred: map[string]time.Time{},
		c: counters{names: st.PassNames, done: st.PassPackagesDone, failed: st.PassPackagesFailed, items: st.PassItemsStored, bytes: st.PassBytesStored},
		m: e.o.Metrics.of(repo.Name.String()),
	}
	p.log = e.o.Log.With("component", "mirror", "repository", repo.Name, "job", jobID)
	p.m.touch()
	if u, err := url.Parse(rs.URL); err == nil {
		p.host = u.Hostname()
	}
	return p, "", nil
}

// patterns returns the include and exclude patterns of a repository as stored.
func patterns(r repository.Repository) (include, exclude []string, err error) {
	var s repository.Settings
	if err := json.Unmarshal(r.Config, &s); err != nil {
		return nil, nil, fmt.Errorf("mirror: the document of %s: %w", r.Name, err)
	}
	return s.IncludePatterns, s.ExcludePatterns, nil
}

// run executes the attempt and ends it.
func (p *pass) run(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	p.cancel = cancel
	defer p.m.window(0)
	execErr := p.execute(ctx)
	if parent.Err() != nil {
		// A release at shutdown or a lost lease: nothing is written, the next job continues from the pending names.
		return context.Cause(parent)
	}
	return p.finish(parent, execErr)
}

// prepare builds what the attempt works with.
func (p *pass) prepare(ctx context.Context) error {
	var ok bool
	p.lister, ok = p.e.o.Host.Lister(p.repo.Format)
	if !ok {
		return &passError{code: CodeNotApplicable, err: fmt.Errorf("the %s format has no discovery", p.repo.Format)}
	}
	var err error
	if p.admits, err = p.e.o.Host.Admits(ctx, p.repo); err != nil {
		return &passError{code: CodeInternal, err: err}
	}
	p.ceiling = p.e.background(p.settings)
	if p.up, err = p.e.o.Host.Upstream(p.repo, Pass{JobID: p.fence.JobID, Ceiling: p.ceiling}); err != nil {
		return &passError{code: CodeUpstream, err: err}
	}
	p.th = newThrottle(p.e.o.Clock, p.e.o.Jitter, p.ceiling, p.m.window)
	if p.settings.Mirror.MaxBytes != nil {
		p.bound = newByteBound(p.e.o.Store, p.e.o.Clock, p.id, *p.settings.Mirror.MaxBytes)
	}
	p.admitVersion = p.repo.Version
	return nil
}

// admitFn returns the current admission check (patterns, claims, name rule).
func (p *pass) admitFn() func(string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.admits
}

// admitNow reports whether the current admission lets a name through (R-REPO-051: a name that is refused is never asked for).
func (p *pass) admitNow(name string) bool { return p.admitFn()(name) }

// isShutdown reports whether the host said the process is shutting down.
func (p *pass) isShutdown() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.halted == haltShutdown
}

// rebuildAdmits builds the admission again from a newer document of the repository (R-REPO-051, Q-REPO-188).
func (p *pass) rebuildAdmits(ctx context.Context, repo repository.Repository) {
	admits, err := p.e.o.Host.Admits(ctx, repo)
	if err != nil {
		if ctx.Err() == nil {
			p.fatal(&passError{code: CodeInternal, err: err})
		}
		return
	}
	p.mu.Lock()
	p.admits = admits
	p.mu.Unlock()
}

// dropRefused deletes the pending rows that the current admission refuses, so that a name discovered under earlier patterns is
// never asked for (R-REPO-051, Q-REPO-188).
func (p *pass) dropRefused(ctx context.Context) error {
	admits, after := p.admitFn(), ""
	for {
		rows, err := p.e.o.Store.NextPending(ctx, p.id, p.e.o.Clock.Now(), after, pageSize)
		if err != nil {
			return &passError{code: CodeInternal, err: err}
		}
		if len(rows) == 0 {
			return nil
		}
		var refused []string
		for _, r := range rows {
			after = r.Name
			if !admits(r.Name) {
				refused = append(refused, r.Name)
			}
		}
		if err := p.e.o.Store.DropPending(ctx, p.fence, refused); err != nil {
			if errors.Is(err, ErrFenceLost) {
				return err
			}
			return &passError{code: CodeInternal, err: err}
		}
	}
}

// execute runs the steps after the claim: the offline and bound checks, then discovery and fill until the pass is done or
// halted. It returns an error of the pass itself.
func (p *pass) execute(ctx context.Context) error {
	if err := p.prepare(ctx); err != nil {
		return err
	}
	if p.offline() {
		p.setHalt(haltOffline, nil)
		return nil
	}
	if err := p.dropRefused(ctx); err != nil {
		return err
	}
	if p.state.BoundReached != "" {
		holds, err := p.boundHolds(ctx)
		if err != nil {
			return &passError{code: CodeInternal, err: err}
		}
		if holds {
			p.setHalt(haltBound, nil)
			return nil
		}
	}
	for !p.isHalted() {
		if !p.done {
			if err := p.discover(ctx); err != nil {
				return err
			}
		}
		if p.isHalted() {
			break
		}
		handled, err := p.fill(ctx)
		if err != nil {
			return err
		}
		if p.done || p.isHalted() {
			break
		}
		if handled == 0 && p.pendingFull(ctx) {
			break
		}
	}
	return nil
}

// pendingFull reports whether the pending table is at its bound with nothing a pass can take from it: names that failed three
// times stay and count, so discovery cannot go on. The attempt ends partial, with the reason in the discovery note.
func (p *pass) pendingFull(ctx context.Context) bool {
	n, err := p.e.o.Store.PendingCount(ctx, p.id)
	if err != nil || n < MaxPending {
		return false
	}
	p.mu.Lock()
	p.incomplete = true
	p.state.PassDiscoveryComplete = false
	p.state.PassDiscoveryNote = "discovery stopped: the pending names are at their bound of 10,000 and none can be taken"
	p.mu.Unlock()
	return true
}

// offline reports whether the repository or the installation is offline (R-REPO-030, R-REPO-031).
func (p *pass) offline() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.settings.Availability.Mode == repository.AvailabilityOffline || p.e.o.GlobalOffline()
}

// boundHolds reports whether the byte bound of an earlier pass still holds: the repository has a maxBytes and its bytes have
// not fallen below it. Such a pass ends at once, without an upstream request (R-REPO-052).
func (p *pass) boundHolds(ctx context.Context) (bool, error) {
	if p.bound == nil {
		return false, nil
	}
	used, err := p.bound.used(ctx)
	if err != nil {
		return false, err
	}
	p.mu.Lock()
	p.usedBytes = used
	p.mu.Unlock()
	return used >= p.bound.limit(), nil
}

// setHalt records why the attempt stops taking work; the first reason wins. A fatal one cancels the work in flight.
func (p *pass) setHalt(h halt, err error) {
	p.mu.Lock()
	first := p.halted == haltNone
	if first {
		p.halted, p.err = h, err
	}
	p.mu.Unlock()
	if !first {
		return
	}
	if p.th != nil {
		p.th.halt()
	}
	if h == haltFatal && p.cancel != nil {
		p.cancel()
	}
}

func (p *pass) isHalted() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.halted != haltNone
}

// fatal stops the attempt at an error of the pass itself.
func (p *pass) fatal(err error) { p.setHalt(haltFatal, err) }

// checkStop reads the repository from the snapshot before a package and halts the attempt when it was deleted, disabled,
// paused or turned back to pull-through, or went offline (R-REPO-053 stops). It returns whether the attempt is halted.
func (p *pass) checkStop(ctx context.Context) bool {
	if p.isHalted() {
		return true
	}
	repo, err := p.e.o.Repos.ByID(ctx, p.id)
	switch {
	case err != nil && notFound(err):
		p.setHalt(haltMode, nil)
	case err != nil:
		if ctx.Err() == nil {
			p.fatal(&passError{code: CodeInternal, err: err})
		}
	default:
		p.applySnapshot(ctx, repo)
	}
	return p.isHalted()
}

func (p *pass) applySnapshot(ctx context.Context, repo repository.Repository) {
	rs, ok := mirrored(repo)
	switch {
	case !ok:
		if _, loaded := repo.Remote(); !loaded {
			p.setHalt(haltPause, nil)
		} else {
			p.setHalt(haltMode, nil)
		}
		return
	case repo.Disabled || rs.Mirror.Paused:
		p.setHalt(haltPause, nil)
		return
	case rs.Availability.Mode == repository.AvailabilityOffline || p.e.o.GlobalOffline():
		p.setHalt(haltOffline, nil)
		return
	}
	p.mu.Lock()
	rebuild := repo.Version != p.admitVersion
	p.repo, p.settings = repo, rs
	if rebuild {
		p.admitVersion = repo.Version
	}
	p.mu.Unlock()
	if rebuild {
		p.rebuildAdmits(ctx, repo)
	}
	if p.bound != nil && rs.Mirror.MaxBytes != nil {
		p.bound.setMax(*rs.Mirror.MaxBytes)
	}
}

func (p *pass) current() (repository.Repository, repository.RemoteSettings) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.repo, p.settings
}

// store reports the outcome of a write of the pass: a lost fence or any other error halts the attempt.
func (p *pass) wrote(err error) bool {
	if err == nil {
		return true
	}
	p.fatal(err)
	return false
}

func (p *pass) add(f func(*counters)) {
	p.mu.Lock()
	f(&p.c)
	p.mu.Unlock()
}
