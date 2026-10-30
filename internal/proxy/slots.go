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
// that each replica takes ceil(budget / n) of, n being the live replicas that serve
// client traffic (Q-REPO-97).
type slots struct {
	max      int
	replicas func() int

	mu    sync.Mutex
	inUse int
	repo  map[int64]int
	wake  chan struct{}
}

func newSlots(max int, replicas func() int) *slots {
	if replicas == nil {
		replicas = func() int { return 1 }
	}
	return &slots{max: max, replicas: replicas, repo: map[int64]int{}, wake: make(chan struct{})}
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
