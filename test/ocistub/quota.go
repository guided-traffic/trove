package ocistub

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// PullQuota models Docker Hub's pull limit: only manifest GET requests count (HEAD, tags lists and blobs do not), every manifest
// answer carries RateLimit-Limit and RateLimit-Remaining with the window ("76;w=21600"), and a request over the limit is answered 429
// with Remaining 0 and no Retry-After. The window starts with the first counted request after the quota was set and a new one
// begins when it has passed.
type PullQuota struct {
	// Limit is the number of manifest GET requests per window; 0 means HubLimit.
	Limit int `json:"limit,omitempty"`
	// WindowSeconds is the length of the window; 0 means HubWindowSeconds.
	WindowSeconds int `json:"window_seconds,omitempty"`
}

// Validate rejects a quota the stub does not implement.
func (q PullQuota) Validate() error {
	if q.Limit < 0 || q.WindowSeconds < 0 || q.WindowSeconds > 7*24*3600 {
		return fmt.Errorf("pull_quota %+v out of range", q)
	}
	return nil
}

func (q PullQuota) limit() int {
	if q.Limit > 0 {
		return q.Limit
	}
	return HubLimit
}

func (q PullQuota) window() int {
	if q.WindowSeconds > 0 {
		return q.WindowSeconds
	}
	return HubWindowSeconds
}

// QuotaState is the answer of GET /-/stub/quota.
type QuotaState struct {
	Limit     int `json:"limit"`
	Used      int `json:"used"`
	Remaining int `json:"remaining"`
	Window    int `json:"window_seconds"`
}

type quotaState struct {
	mu    sync.Mutex
	start time.Time
	used  int
}

// reset starts the count afresh.
func (q *quotaState) reset() {
	q.mu.Lock()
	q.used, q.start = 0, time.Time{}
	q.mu.Unlock()
}

// take counts a manifest request against the quota and sets the headers; it reports false for a request over the limit, which
// the caller answers with the 429. A HEAD only shows the remainder.
func (q *quotaState) take(pq PullQuota, now time.Time, h http.Header, count bool) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	window := time.Duration(pq.window()) * time.Second
	if !q.start.IsZero() && now.Sub(q.start) >= window {
		q.used, q.start = 0, time.Time{}
	}
	if count && q.used >= pq.limit() {
		hubHeaders(h, pq.limit(), 0, pq.window())
		return false
	}
	if count {
		if q.start.IsZero() {
			q.start = now
		}
		q.used++
	}
	hubHeaders(h, pq.limit(), max(pq.limit()-q.used, 0), pq.window())
	return true
}

func (q *quotaState) state(pq PullQuota) QuotaState {
	q.mu.Lock()
	defer q.mu.Unlock()
	return QuotaState{Limit: pq.limit(), Used: q.used, Remaining: max(pq.limit()-q.used, 0), Window: pq.window()}
}

// Quota returns the state of the pull quota; the zero value when no quota is set.
func (s *Server) Quota() QuotaState {
	pq := s.Mode().PullQuota
	if pq == nil {
		return QuotaState{}
	}
	return s.quota.state(*pq)
}
