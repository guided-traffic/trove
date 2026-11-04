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
// of their own per repository, which acquireBackground holds at the background share.
type slots struct {
	max      int
	replicas func() int

	mu    sync.Mutex
	inUse int
	repo  map[int64]int
	bg    map[int64]int
	wake  chan struct{}
}

func newSlots(max int, replicas func() int) *slots {
	if replicas == nil {
		replicas = func() int { return 1 }
	}
	return &slots{max: max, replicas: replicas, repo: map[int64]int{}, bg: map[int64]int{}, wake: make(chan struct{})}
}

// repoLimit is this replica's share of a repository's budget.
func (s *slots) repoLimit(budget int) int {
	n := max(1, s.replicas())
	return max(1, (budget+n-1)/n)
}

// acquire takes a slot for a fetch of repository repoID, waiting up to timeout for
// one to be free. The returned function gives the slot back; wait is how long the
// fetch queued. A timeout is 503 with Retry-After 5 (R-REPO-040).
func (s *slots) acquire(ctx context.Context, repoID int64, budget int, timeout time.Duration) (release func(), wait time.Duration, err error) {
	start := time.Now()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond) // n may change while a fetch queues
	defer tick.Stop()
	for {
		s.mu.Lock()
		if s.inUse < s.max && s.repo[repoID] < s.repoLimit(budget) {
			s.inUse++
			s.repo[repoID]++
			s.mu.Unlock()
			return func() { s.release(repoID) }, time.Since(start), nil
		}
		ch := s.wake
		s.mu.Unlock()
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

// acquireBackground takes a background slot for a fetch of a mirror's pass (R-REPO-055): at most ceiling background
// fetches of repository repoID at a time on this replica, within the replica limit and the repository's per-replica
// share. It never joins the clients' queue and has no queue timeout: it waits outside the slots until one is free or ctx
// ends. The returned context is ctx, cancelled when a client fetch pre-empts this one; the fetch runs under it, and
// release gives the slot back.
//
// Contract wave: the slot accounting is in place and the client path is unchanged; the pre-emption of R-REPO-055 (a
// client that finds no free slot cancels the youngest background fetch) and the cross-replica yield are work package C
// of the mirror (Q-ARCH-288), so the returned context is never cancelled by a client yet.
func (s *slots) acquireBackground(ctx context.Context, repoID int64, budget, ceiling int) (context.Context, func(), error) {
	ceiling = max(1, ceiling)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		s.mu.Lock()
		if s.inUse < s.max && s.repo[repoID] < s.repoLimit(budget) && s.bg[repoID] < ceiling {
			s.inUse++
			s.repo[repoID]++
			s.bg[repoID]++
			s.mu.Unlock()
			bctx, cancel := context.WithCancel(ctx)
			return bctx, func() { cancel(); s.releaseBackground(repoID) }, nil
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

func (s *slots) releaseBackground(repoID int64) {
	s.mu.Lock()
	if s.bg[repoID]--; s.bg[repoID] <= 0 {
		delete(s.bg, repoID)
	}
	s.mu.Unlock()
	s.release(repoID)
}

// background returns the background slots in use for a repository, for tests and the status of a pass.
func (s *slots) background(repoID int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bg[repoID]
}

func (s *slots) release(repoID int64) {
	s.mu.Lock()
	s.inUse--
	if s.repo[repoID]--; s.repo[repoID] <= 0 {
		delete(s.repo, repoID)
	}
	close(s.wake)
	s.wake = make(chan struct{})
	s.mu.Unlock()
}

// held returns the slots in use, for tests and the drain.
func (s *slots) held() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inUse
}
