package npmstub

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"
)

// The throttling modes of the counting stub upstreams (R-REPO-059, R-REPO-054): the answers a registry gives that rate-limits
// a client, each switchable for the n-th request or for a period, and a delay per response. This file is self-contained
// (the Server holds one throttleState, Mode one Throttle, the gate calls throttleGate), so that another stub can take its
// shape over.
//
// A Throttle is set with the rest of the Mode (SetMode, POST /-/stub/mode with {"throttle": {...}}); setting a mode
// starts its counters afresh. Examples as the control API takes them:
//
//	{"throttle": {"kind": "429", "class": "tarball", "from": 11, "retry_after_seconds": 7}}
//	    the eleventh tarball request, and only it, answers 429 with Retry-After: 7
//	{"throttle": {"kind": "503_slowdown", "count": 3}}
//	    the next three requests answer 503 with the S3 SlowDown body and no Retry-After
//	{"throttle": {"kind": "403_quota", "period_ms": 4000, "retry_after_seconds": 4}}
//	    every request for 4 s from the first answers 403 with X-RateLimit-Remaining: 0 and X-RateLimit-Reset 4 s ahead
//	{"throttle": {"class": "replication", "delay_ms": 300}}
//	    every response to the replication host is held 300 ms, none is refused

// The kinds of throttled answer.
const (
	// Throttle429 answers 429 with Retry-After in seconds.
	Throttle429 = "429"
	// Throttle429Date answers 429 with Retry-After as an HTTP date RetryAfterSeconds ahead.
	Throttle429Date = "429_date"
	// Throttle503 answers 503 with Retry-After in seconds.
	Throttle503 = "503"
	// Throttle503SlowDown answers 503 with the S3 SlowDown error body and no Retry-After.
	Throttle503SlowDown = "503_slowdown"
	// Throttle403Quota answers 403 with X-RateLimit-Remaining: 0 and X-RateLimit-Reset, seconds since the epoch.
	Throttle403Quota = "403_quota"
	// Throttle429Quota answers 429 with X-RateLimit-Remaining: 0 and X-RateLimit-Reset, without Retry-After.
	Throttle429Quota = "429_quota"
)

// SlowDownBody is the body of Throttle503SlowDown, the error an S3 backend answers with when it is asked too fast.
const SlowDownBody = `<Error><Code>SlowDown</Code><Message>Please reduce your request rate.</Message></Error>`

// Throttle describes which requests are throttled and how. The zero value throttles nothing.
type Throttle struct {
	// Kind is the answer: one of the Throttle* constants; empty for a delay only.
	Kind string `json:"kind,omitempty"`
	// Class limits the throttle to the requests of one kind (document, tarball, replication, audit, ping, other); empty:
	// every request but the control API's.
	Class string `json:"class,omitempty"`
	// From is the 1-based number of the matching request that is the first to be throttled; 0 means the first.
	From int `json:"from,omitempty"`
	// Count is how many matching requests, from the first throttled one, are throttled; 0 means one, unless PeriodMS is set.
	Count int `json:"count,omitempty"`
	// PeriodMS, when set, throttles every matching request for this long after the first throttled one instead of Count.
	PeriodMS int `json:"period_ms,omitempty"`
	// RetryAfterSeconds is the wait the answer names: Retry-After, or X-RateLimit-Reset ahead of now.
	RetryAfterSeconds int `json:"retry_after_seconds,omitempty"`
	// DelayMS holds every matching response, throttled or not, before its status line.
	DelayMS int `json:"delay_ms,omitempty"`
}

var throttleKinds = []string{"", Throttle429, Throttle429Date, Throttle503, Throttle503SlowDown, Throttle403Quota, Throttle429Quota}

// validate rejects a throttle the stub does not implement.
func (t Throttle) validate() error {
	if !slices.Contains(throttleKinds, t.Kind) {
		return fmt.Errorf("throttle kind %q: want one of %q", t.Kind, throttleKinds[1:])
	}
	if t.Class != "" && !slices.Contains([]string{KindDocument, KindTarball, KindReplication, KindAudit, KindPing, KindOther}, t.Class) {
		return fmt.Errorf("throttle class %q: want document, tarball, replication, audit, ping or other", t.Class)
	}
	if t.From < 0 || t.Count < 0 || t.PeriodMS < 0 || t.PeriodMS > int(MaxHang/time.Millisecond) ||
		t.DelayMS < 0 || t.DelayMS > int(MaxHang/time.Millisecond) || t.RetryAfterSeconds < 0 || t.RetryAfterSeconds > 24*3600 {
		return fmt.Errorf("throttle %+v: a number is out of range", t)
	}
	return nil
}

func (t Throttle) matches(kind string) bool { return t.Class == "" || t.Class == kind }

// throttleState is what a Throttle has done since its mode was set.
type throttleState struct {
	seen      int       // matching requests so far
	thrown    int       // of them throttled
	windowEnd time.Time // end of the period of PeriodMS, once it began
}

// throttleDecision counts the request and says whether it is throttled and how long its response is held.
func (s *Server) throttleDecision(t *Throttle, kind string) (delay time.Duration, throttled bool) {
	if t == nil || !t.matches(kind) {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delay = time.Duration(t.DelayMS) * time.Millisecond
	if t.Kind == "" {
		return delay, false
	}
	s.thr.seen++
	if s.thr.seen < max(t.From, 1) {
		return delay, false
	}
	switch {
	case t.PeriodMS > 0:
		now := time.Now()
		if s.thr.windowEnd.IsZero() {
			s.thr.windowEnd = now.Add(time.Duration(t.PeriodMS) * time.Millisecond)
		}
		throttled = now.Before(s.thr.windowEnd)
	default:
		throttled = s.thr.thrown < max(t.Count, 1)
	}
	if throttled {
		s.thr.thrown++
	}
	return delay, throttled
}

// Throttled is the number of requests answered with a throttled answer since the mode was set.
func (s *Server) Throttled() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.thr.thrown
}

// throttleGate holds the response of a matching request and, when the throttle says so, answers it; it reports whether it
// did, so that the request goes no further.
func (s *Server) throttleGate(w http.ResponseWriter, r *http.Request, kind string, t *Throttle) bool {
	delay, throttled := s.throttleDecision(t, kind)
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return true
		}
	}
	if !throttled {
		return false
	}
	wait := strconv.Itoa(t.RetryAfterSeconds)
	h := w.Header()
	status := http.StatusTooManyRequests
	switch t.Kind {
	case Throttle429:
		h.Set("Retry-After", wait)
	case Throttle429Date:
		h.Set("Retry-After", time.Now().Add(time.Duration(t.RetryAfterSeconds)*time.Second).UTC().Format(http.TimeFormat))
	case Throttle503:
		status = http.StatusServiceUnavailable
		h.Set("Retry-After", wait)
	case Throttle503SlowDown:
		h.Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(SlowDownBody))
		return true
	case Throttle403Quota, Throttle429Quota:
		if t.Kind == Throttle403Quota {
			status = http.StatusForbidden
		}
		h.Set("X-RateLimit-Remaining", "0")
		h.Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Duration(t.RetryAfterSeconds)*time.Second).Unix(), 10))
	}
	writeError(w, status, "throttled by the test upstream")
	return true
}
