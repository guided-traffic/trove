package nugetstub

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"
)

// Throttling: the stub as an upstream that limits the request rate, in the answers the mirror of R-REPO-054 recognizes
// (R-REPO-059). It is self-contained: the state, the control methods and the gate are in this file; the server calls
// throttleGate in front of its routes and serveControl routes /-/stub/throttle here.
//
// A throttle answers requests with a rate-limit response, chosen by Mode, either for the n-th request counted from the moment
// the throttle was set (Nth, with Count of them in a row) or for a period (ForMS). A delay per response applies to every
// request while the throttle is set, throttled or not. The control API itself is never throttled or counted.

// Throttle modes: the answer of a throttled request.
const (
	// ThrottleTooManyRequests answers 429 with Retry-After in seconds (RetryAfterSeconds).
	ThrottleTooManyRequests = "429"
	// ThrottleTooManyRequestsDate answers 429 with Retry-After as an HTTP date, RetryAfterSeconds from now.
	ThrottleTooManyRequestsDate = "429-date"
	// ThrottleUnavailable answers 503 with Retry-After in seconds.
	ThrottleUnavailable = "503"
	// ThrottleSlowDown answers 503 with the body an object store sends, <Error><Code>SlowDown</Code>..., and no Retry-After.
	ThrottleSlowDown = "503-slowdown"
	// ThrottleQuota403 answers 403 with X-RateLimit-Remaining: 0 and X-RateLimit-Reset, the time in seconds since the epoch
	// that is RetryAfterSeconds from now (left out when RetryAfterSeconds is 0).
	ThrottleQuota403 = "403-quota"
	// ThrottleQuota429 answers 429 with X-RateLimit-Remaining: 0, X-RateLimit-Reset as above and no Retry-After.
	ThrottleQuota429 = "429-quota"
)

// SlowDownBody is the body of ThrottleSlowDown: what an S3-compatible store answers when its request rate is exceeded.
const SlowDownBody = `<Error><Code>SlowDown</Code><Message>Please reduce your request rate.</Message></Error>`

// MaxThrottleSeconds bounds RetryAfterSeconds and the period, so that a throttle forgotten by a test ends.
const MaxThrottleSeconds = 3600

// Throttle is the throttling switch. The zero value throttles nothing and delays nothing.
type Throttle struct {
	// Mode is one of the Throttle constants; empty: no request is throttled (the delay still applies).
	Mode string `json:"mode,omitempty"`
	// RetryAfterSeconds is the wait the answer names (Retry-After, or the reset header of the quota modes).
	RetryAfterSeconds int `json:"retry_after_seconds,omitempty"`
	// Nth throttles the n-th request counted from the moment the throttle was set (1 is the next one), and Count of them
	// in a row (0 or 1: only that one, a negative Count: every request from there on). Not together with ForMS.
	Nth   int `json:"nth,omitempty"`
	Count int `json:"count,omitempty"`
	// ForMS throttles every request for this many milliseconds from the moment the throttle was set. With neither Nth nor
	// ForMS every request is throttled until the throttle is cleared.
	ForMS int `json:"for_ms,omitempty"`
	// DelayMS delays every response by this long while the throttle is set.
	DelayMS int `json:"delay_ms,omitempty"`
}

// Validate rejects a throttle the stub does not implement.
func (t Throttle) Validate() error {
	if t.Mode != "" && !slices.Contains([]string{ThrottleTooManyRequests, ThrottleTooManyRequestsDate, ThrottleUnavailable, ThrottleSlowDown, ThrottleQuota403, ThrottleQuota429}, t.Mode) {
		return fmt.Errorf("throttle mode %q: want 429, 429-date, 503, 503-slowdown, 403-quota or 429-quota", t.Mode)
	}
	if t.RetryAfterSeconds < 0 || t.RetryAfterSeconds > MaxThrottleSeconds {
		return fmt.Errorf("retry_after_seconds %d out of range", t.RetryAfterSeconds)
	}
	if t.ForMS < 0 || t.ForMS > MaxThrottleSeconds*1000 || t.DelayMS < 0 || t.DelayMS > int(MaxHang/time.Millisecond) {
		return fmt.Errorf("for_ms %d or delay_ms %d out of range", t.ForMS, t.DelayMS)
	}
	if t.Nth < 0 || t.Nth > 0 && t.ForMS > 0 {
		return fmt.Errorf("nth %d: not negative, and not together with for_ms", t.Nth)
	}
	return nil
}

// ThrottleState is the answer of GET /-/stub/throttle: the switch, the requests counted since it was set and the answers it
// gave.
type ThrottleState struct {
	Throttle  Throttle `json:"throttle"`
	Seen      int      `json:"seen"`
	Throttled int      `json:"throttled"`
}

// throttleState is the throttle of a server with its own lock, so that the gate does not take the server's.
type throttleState struct {
	mu        sync.Mutex
	set       Throttle
	since     time.Time
	seen      int
	throttled int
}

// SetThrottle replaces the throttle and restarts its count and period; the zero Throttle clears it.
func (s *Server) SetThrottle(t Throttle) error {
	if err := t.Validate(); err != nil {
		return err
	}
	s.throttle.mu.Lock()
	defer s.throttle.mu.Unlock()
	s.throttle.set, s.throttle.since, s.throttle.seen, s.throttle.throttled = t, s.cfg.Now(), 0, 0
	return nil
}

// ThrottleState returns the throttle with the requests it counted and throttled.
func (s *Server) ThrottleState() ThrottleState {
	s.throttle.mu.Lock()
	defer s.throttle.mu.Unlock()
	return ThrottleState{Throttle: s.throttle.set, Seen: s.throttle.seen, Throttled: s.throttle.throttled}
}

// clearThrottle is what ResetAll does to the throttle.
func (s *Server) clearThrottle() {
	s.throttle.mu.Lock()
	defer s.throttle.mu.Unlock()
	s.throttle.set, s.throttle.seen, s.throttle.throttled = Throttle{}, 0, 0
}

// throttleGate counts the request, delays it and, when the throttle says so, answers it with the rate-limit response. It
// reports true when it answered, so that the request goes no further.
func (s *Server) throttleGate(w http.ResponseWriter, r *http.Request) bool {
	now := s.cfg.Now()
	st := &s.throttle
	st.mu.Lock()
	t := st.set
	if t == (Throttle{}) {
		st.mu.Unlock()
		return false
	}
	st.seen++
	n := st.seen
	hit := t.Mode != "" && t.applies(n, now.Sub(st.since))
	if hit {
		st.throttled++
	}
	st.mu.Unlock()

	if t.DelayMS > 0 {
		select {
		case <-time.After(time.Duration(t.DelayMS) * time.Millisecond):
		case <-r.Context().Done():
			return true
		}
	}
	if !hit {
		return false
	}
	t.respond(w, now)
	return true
}

// applies reports whether the n-th request, elapsed after the throttle was set, is throttled.
func (t Throttle) applies(n int, elapsed time.Duration) bool {
	switch {
	case t.Nth > 0:
		count := max(t.Count, 1)
		return n >= t.Nth && (t.Count < 0 || n < t.Nth+count)
	case t.ForMS > 0:
		return elapsed < time.Duration(t.ForMS)*time.Millisecond
	}
	return true
}

// respond writes the rate-limit answer of the mode.
func (t Throttle) respond(w http.ResponseWriter, now time.Time) {
	h := w.Header()
	wait := time.Duration(t.RetryAfterSeconds) * time.Second
	switch t.Mode {
	case ThrottleTooManyRequests, ThrottleUnavailable:
		h.Set("Retry-After", strconv.Itoa(t.RetryAfterSeconds))
	case ThrottleTooManyRequestsDate:
		h.Set("Retry-After", now.Add(wait).UTC().Format(http.TimeFormat))
	case ThrottleSlowDown:
		h.Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(SlowDownBody))
		return
	case ThrottleQuota403, ThrottleQuota429:
		h.Set("X-RateLimit-Remaining", "0")
		if t.RetryAfterSeconds > 0 {
			h.Set("X-RateLimit-Reset", strconv.FormatInt(now.Add(wait).Unix(), 10))
		}
	}
	status := http.StatusTooManyRequests
	switch t.Mode {
	case ThrottleUnavailable:
		status = http.StatusServiceUnavailable
	case ThrottleQuota403:
		status = http.StatusForbidden
	}
	writeError(w, status, "rate limit exceeded")
}
