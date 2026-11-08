package proxy

import (
	"context"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
)

// slots are the fetch slots of R-REPO-040: per-replica counters, no coordination
// with another replica and none with Valkey. proxy.max_concurrent_fetches bounds the
// replica, and limits.maxConcurrentFetches is a budget for the whole installation
// that each replica takes ceil(budget / n) of, n being the live replicas that fill
// client misses: packages, web and all, their pools included, and not api or worker
// (Q-REPO-97, Q-ARCH-297).
//
// Background fetches of a mirror (R-REPO-055) count in the same replica limit and repository share, and in a counter
// of their own per repository, which acquireBackground holds at the background share. A client fetch that finds no free slot
// pre-empts the youngest background fetch that qualifies and takes its slot at once; a yield of another process
// ([slots.yield]) pre-empts every background fetch of a repository that no client of this replica joined and keeps the
// background from starting for [yieldFor].
//
// A hold has two parts (R-REPO-040, Q-REPO-235): the repository's share, and for a background fetch its background count, end
// with the upstream exchange ([slotHold.exchanged]); the replica's slot ends with the fetch ([slotHold.release]).
type slots struct {
	max      int
	replicas func() int
	// now is the clock of the yield window; time.Now unless a test sets another.
	now func() time.Time
	// onWait is called, outside the lock, when a client fetch of a repository has to wait for a slot; the engine raises the
	// signal mirror_yield with it (R-REPO-055). Nil does nothing.
	onWait func(repoID int64)

	mu    sync.Mutex
	inUse int
	repo  map[int64]int
	bg    map[int64]int
	wake  chan struct{}
	// runs are the running background fetches of a repository in start order; a pre-empted one is removed at once.
	runs map[int64][]*bgRun
	seq  uint64
	// waiting counts the client fetches of a repository that wait for a slot.
	waiting map[int64]int
	// yielding holds, per repository, the end of the window in which no background fetch starts; yieldingAll is the same for a
	// signal that named no repository (a coarse batch).
	yielding    map[int64]time.Time
	yieldingAll time.Time
}

// yieldFor is how long after the last signal mirror_yield no background fetch of the repository starts (R-REPO-055).
const yieldFor = 30 * time.Second

// bgRun is a running background fetch as the pre-emption sees it.
type bgRun struct {
	repo int64
	seq  uint64
	// cancel ends the fetch's context with the cause ErrPreempted.
	cancel context.CancelCauseFunc
	// joined reports whether a follower of this replica joined the fetch's flight: such a fetch is not pre-empted.
	joined func() bool
	// gone is true once the fetch was pre-empted: its slot was given up then, and its release does nothing.
	gone bool
}

// slotHold is a fetch's hold on the slots, in two parts that end at different moments (R-REPO-040, Q-REPO-235):
//
//   - exchanged returns the repository's share, and for a background fetch the background count; the fetch also leaves the
//     running background fetches that a client fetch or the signal pre-empts. It does not end the context the fetch runs under.
//   - release returns what exchanged has not returned yet, and the replica's slot. For a background fetch it ends the fetch's
//     context with no cause.
//
// Both are idempotent and safe for concurrent use. A background fetch that was pre-empted gave all three counters up at the
// pre-emption: for it both do nothing to the counters. A nil hold does nothing.
type slotHold struct {
	s    *slots
	repo int64
	// run is the background fetch of the hold, nil for a client's.
	run *bgRun
	// cancel ends the context of a background hold; nil for a client's.
	cancel context.CancelCauseFunc
	// wait is how long acquiring the hold took.
	wait time.Duration
	// exchangedDone and released are guarded by s.mu.
	exchangedDone, released bool
}

// exchanged returns the repository's share (and the background count) of the hold.
func (h *slotHold) exchanged() {
	if h == nil {
		return
	}
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	h.exchangeLocked()
}

// exchangeLocked is exchanged with mu held.
func (h *slotHold) exchangeLocked() {
	if h.exchangedDone {
		return
	}
	h.exchangedDone = true
	s := h.s
	if h.run != nil {
		if h.run.gone {
			return // the pre-emption returned every counter
		}
		s.forget(h.run)
		if s.bg[h.repo]--; s.bg[h.repo] <= 0 {
			delete(s.bg, h.repo)
		}
	}
	if s.repo[h.repo]--; s.repo[h.repo] <= 0 {
		delete(s.repo, h.repo)
	}
	s.broadcast()
}

// release returns the hold's remaining parts and the replica's slot.
func (h *slotHold) release() {
	if h == nil {
		return
	}
	s := h.s
	s.mu.Lock()
	if !h.released {
		h.released = true
		h.exchangeLocked()
		if h.run == nil || !h.run.gone {
			s.inUse--
		}
		s.broadcast()
	}
	s.mu.Unlock()
	if h.cancel != nil {
		h.cancel(nil)
	}
}

func newSlots(max int, replicas func() int) *slots {
	if replicas == nil {
		replicas = func() int { return 1 }
	}
	return &slots{
		max: max, replicas: replicas, now: time.Now, repo: map[int64]int{}, bg: map[int64]int{}, wake: make(chan struct{}),
		runs: map[int64][]*bgRun{}, waiting: map[int64]int{}, yielding: map[int64]time.Time{},
	}
}

// repoLimit is this replica's share of a repository's budget.
func (s *slots) repoLimit(budget int) int {
	n := max(1, s.replicas())
	return max(1, (budget+n-1)/n)
}

// fits reports whether a client fetch of the repository has a free slot. The caller holds mu.
func (s *slots) fits(repoID int64, budget int) bool {
	return s.inUse < s.max && s.repo[repoID] < s.repoLimit(budget)
}

// acquire takes a slot for a fetch of repository repoID, waiting up to timeout for
// one to be free. The returned hold gives the slot back, its share when the upstream exchange ends and the rest when the
// fetch ends; wait is how long the fetch queued. A timeout is 503 with Retry-After 5 (R-REPO-040).
//
// A fetch that finds no free slot first pre-empts a background fetch when one qualifies (R-REPO-055: the youngest of the
// repository, or of any repository when only the replica's limit is exhausted; never one a follower joined) and takes its
// slot at once. Without such a fetch it queues, counts as waiting for the repository, which keeps background fetches of it
// from starting, and tells the engine through onWait.
func (s *slots) acquire(ctx context.Context, repoID int64, budget int, timeout time.Duration) (hold *slotHold, wait time.Duration, err error) {
	start := time.Now()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond) // n may change while a fetch queues
	defer tick.Stop()
	queued := false
	defer func() {
		if queued {
			s.mu.Lock()
			if s.waiting[repoID]--; s.waiting[repoID] <= 0 {
				delete(s.waiting, repoID)
			}
			s.broadcast() // a background fetch of the repository may start now
			s.mu.Unlock()
		}
	}()
	for {
		s.mu.Lock()
		if !s.fits(repoID, budget) {
			s.preempt(repoID, budget)
		}
		if s.fits(repoID, budget) {
			s.inUse++
			s.repo[repoID]++
			s.mu.Unlock()
			return &slotHold{s: s, repo: repoID, wait: time.Since(start)}, time.Since(start), nil
		}
		first := false
		if !queued {
			queued, first = true, true
			s.waiting[repoID]++
		}
		ch := s.wake
		s.mu.Unlock()
		if first && s.onWait != nil {
			s.onWait(repoID)
		}
		select {
		case <-ch:
		case <-tick.C:
		case <-ctx.Done():
			return nil, time.Since(start), ctx.Err()
		case <-deadline.C:
			return nil, time.Since(start), errs.E(errs.Unavailable, errs.CodeFetchQueueTimeout,
				"All upstream fetch slots stayed busy; retry shortly.", errs.WithRetryAfter(5*time.Second))
		}
	}
}

// preempt frees a slot for a client fetch of repoID by pre-empting a background fetch (R-REPO-055): the youngest of the
// repository, or, when the repository's own share has room and only the replica's limit is exhausted, the youngest of any
// repository. A fetch that a follower joined is left. It reports whether it pre-empted one. The caller holds mu.
func (s *slots) preempt(repoID int64, budget int) bool {
	if v := youngest(s.runs[repoID]); v != nil {
		s.take(v)
		return true
	}
	if s.repo[repoID] < s.repoLimit(budget) && s.inUse >= s.max {
		var victim *bgRun
		for _, rs := range s.runs {
			if v := youngest(rs); v != nil && (victim == nil || v.seq > victim.seq) {
				victim = v
			}
		}
		if victim != nil {
			s.take(victim)
			return true
		}
	}
	return false
}

// youngest returns the background fetch started last that no follower joined, nil for none.
func youngest(rs []*bgRun) *bgRun {
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i].joined == nil || !rs[i].joined() {
			return rs[i]
		}
	}
	return nil
}

// take pre-empts a background fetch: its context ends with the cause ErrPreempted and its slot is given up at once, so that
// the client fetch that asked takes it without waiting for the cancelled request to unwind. The caller holds mu.
func (s *slots) take(r *bgRun) {
	r.gone = true
	r.cancel(ErrPreempted)
	s.forget(r)
	s.inUse--
	if s.repo[r.repo]--; s.repo[r.repo] <= 0 {
		delete(s.repo, r.repo)
	}
	if s.bg[r.repo]--; s.bg[r.repo] <= 0 {
		delete(s.bg, r.repo)
	}
}

// forget removes a background fetch from the running ones. The caller holds mu.
func (s *slots) forget(r *bgRun) {
	rs := s.runs[r.repo]
	for i, x := range rs {
		if x == r {
			rs = append(rs[:i:i], rs[i+1:]...)
			break
		}
	}
	if len(rs) == 0 {
		delete(s.runs, r.repo)
		return
	}
	s.runs[r.repo] = rs
}

// yield is the signal mirror_yield for a repository (R-REPO-055): the running background fetches of it that no client of this
// replica joined are pre-empted, and none starts for [yieldFor] after the last signal. It returns how many it pre-empted.
func (s *slots) yield(repoID int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.purgeYield(now)
	// A message names any id: the map keeps the windows of the last 30 s and at most maxYielding of them, so a forged flood
	// costs a bounded amount of memory (Q-REPO-182). A window beyond the bound is not opened; the running fetches are still
	// pre-empted.
	if _, known := s.yielding[repoID]; known || len(s.yielding) < maxYielding {
		s.yielding[repoID] = now.Add(yieldFor)
	}
	return s.yieldRuns(repoID)
}

// maxYielding bounds the yield windows kept at a time.
const maxYielding = 4096

// purgeYield drops the windows that have passed. The caller holds mu.
func (s *slots) purgeYield(now time.Time) {
	for id, until := range s.yielding {
		if !now.Before(until) {
			delete(s.yielding, id)
		}
	}
}

// yieldAll is the signal of a batch that named no repository: it counts for every repository.
func (s *slots) yieldAll() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.yieldingAll = s.now().Add(yieldFor)
	n := 0
	for id := range s.runs {
		n += s.yieldRuns(id)
	}
	return n
}

// yieldRuns pre-empts the unjoined background fetches of a repository. The caller holds mu.
func (s *slots) yieldRuns(repoID int64) int {
	n := 0
	for _, r := range append([]*bgRun(nil), s.runs[repoID]...) {
		if r.joined == nil || !r.joined() {
			s.take(r)
			n++
		}
	}
	if n > 0 {
		s.broadcast()
	}
	return n
}

// yieldOpen reports whether the yield window of a repository is open at now, and drops one that has passed. The caller holds mu.
func (s *slots) yieldOpen(repoID int64, now time.Time) bool {
	if now.Before(s.yieldingAll) {
		return true
	}
	if until, ok := s.yielding[repoID]; ok {
		if now.Before(until) {
			return true
		}
		delete(s.yielding, repoID)
	}
	return false
}

// broadcast wakes every waiter. The caller holds mu.
func (s *slots) broadcast() {
	close(s.wake)
	s.wake = make(chan struct{})
}

// acquireBackground takes a background slot for a fetch of a mirror's pass (R-REPO-055): at most ceiling background
// fetches of repository repoID at a time on this replica, within the replica limit and the repository's per-replica
// share. It never joins the clients' queue and has no queue timeout: it waits outside the slots until one is free or ctx
// ends, and it does not start while a client fetch of the repository waits for a slot or the yield window of a signal is
// open. The returned context is ctx, cancelled with the cause ErrPreempted when a client fetch pre-empts this one; the fetch
// runs under it, and the hold gives the slot back.
func (s *slots) acquireBackground(ctx context.Context, repoID int64, budget, ceiling int) (context.Context, *slotHold, error) {
	return s.acquireBackgroundJoined(ctx, repoID, budget, ceiling, nil)
}

// acquireBackgroundJoined is acquireBackground for a fetch that leads a flight: joined reports whether a follower of this
// replica has joined the flight, which exempts the fetch from pre-emption. The hold's wait is how long the fetch waited for
// its slot, the yield windows included.
func (s *slots) acquireBackgroundJoined(ctx context.Context, repoID int64, budget, ceiling int, joined func() bool) (context.Context, *slotHold, error) {
	ceiling = max(1, ceiling)
	start := time.Now()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		s.mu.Lock()
		if s.waiting[repoID] == 0 && !s.yieldOpen(repoID, s.now()) && s.fits(repoID, budget) && s.bg[repoID] < ceiling {
			s.inUse++
			s.repo[repoID]++
			s.bg[repoID]++
			bctx, cancel := context.WithCancelCause(ctx)
			s.seq++
			r := &bgRun{repo: repoID, seq: s.seq, cancel: cancel, joined: joined}
			s.runs[repoID] = append(s.runs[repoID], r)
			s.mu.Unlock()
			return bctx, &slotHold{s: s, repo: repoID, run: r, cancel: cancel, wait: time.Since(start)}, nil
		}
		ch := s.wake
		s.mu.Unlock()
		select {
		case <-ch:
		case <-tick.C:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
}

// background returns the background slots in use for a repository, for tests and the status of a pass.
func (s *slots) background(repoID int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bg[repoID]
}

// held returns the slots in use, for tests and the drain.
func (s *slots) held() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inUse
}
