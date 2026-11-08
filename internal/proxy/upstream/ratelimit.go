package upstream

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// The error of a rate-limit answer is what the mirror's contract finds (discover.AsRateLimited).
var _ discover.RateLimited = (*RateLimitError)(nil)

// The reasons of a rate-limit answer (repositories-and-proxy.md R-REPO-054, Q-REPO-137): the label `reason` of
// trove_mirror_rate_limited_total.
const (
	// ReasonTooManyRequests is a 429.
	ReasonTooManyRequests = "too_many_requests"
	// ReasonSlowDown is a 503 whose body is an S3 error with the code SlowDown.
	ReasonSlowDown = "slow_down"
	// ReasonRetryAfter is a 503 with a Retry-After header.
	ReasonRetryAfter = "retry_after"
	// ReasonQuotaHeader is a 403, 429 or 503 whose RateLimit-Remaining or X-RateLimit-Remaining is 0.
	ReasonQuotaHeader = "quota_header"
)

// MaxRateLimitWait caps the wait an upstream may ask for: a wrong header cannot park a mirror for days (R-REPO-054).
const MaxRateLimitWait = 6 * time.Hour

// SlowDownSniffBytes is how much of a 503's body RateLimitOf looks at for the S3 code SlowDown.
const SlowDownSniffBytes = 4096

// RateLimit is the typed outcome of an upstream's rate-limit answer (R-REPO-054). The mirror waits for it, halves its
// window and continues; a client request keeps the classification of R-REPO-029 (classifyStatus in internal/proxy).
type RateLimit struct {
	// Status is the HTTP status of the answer.
	Status int
	// Reason is one of the Reason constants.
	Reason string
	// Wait is the wait the upstream asked for, capped at MaxRateLimitWait; Hinted is false when it named none, and the
	// caller's back-off applies.
	Wait   time.Duration
	Hinted bool
}

// RateLimitOf classifies an answer as a rate limit (R-REPO-054): 429; 503 with a Retry-After header or with the S3 code
// SlowDown in body (the first SlowDownSniffBytes of the body; nil when it was not read); 403, 429 or 503 whose
// RateLimit-Remaining or X-RateLimit-Remaining is 0. The wait comes from Retry-After (delta seconds or an HTTP date, RFC
// 9110 section 10.2.3), else from RateLimit-Reset (seconds) or X-RateLimit-Reset (seconds since the epoch); a value in
// the past or one that does not parse counts as absent. now is the clock the dates are read against.
func RateLimitOf(status int, h http.Header, body []byte, now time.Time) (RateLimit, bool) {
	quota := remainingZero(h)
	var rl RateLimit
	switch {
	case status == http.StatusTooManyRequests:
		rl = RateLimit{Status: status, Reason: ReasonTooManyRequests}
		if quota {
			rl.Reason = ReasonQuotaHeader
		}
	case status == http.StatusServiceUnavailable && slowDown(body):
		rl = RateLimit{Status: status, Reason: ReasonSlowDown}
	case status == http.StatusServiceUnavailable && h.Get("Retry-After") != "":
		rl = RateLimit{Status: status, Reason: ReasonRetryAfter}
	case (status == http.StatusForbidden || status == http.StatusServiceUnavailable) && quota:
		rl = RateLimit{Status: status, Reason: ReasonQuotaHeader}
	default:
		return RateLimit{}, false
	}
	if d, ok := retryAfter(h.Get("Retry-After"), now); ok {
		rl.Wait, rl.Hinted = d, true
	} else if d, ok := resetOf(h, now); ok {
		rl.Wait, rl.Hinted = d, true
	}
	if rl.Wait > MaxRateLimitWait {
		rl.Wait = MaxRateLimitWait
	}
	return rl, true
}

func remainingZero(h http.Header) bool {
	for _, k := range []string{"RateLimit-Remaining", "X-RateLimit-Remaining"} {
		if n, ok := QuotaRemaining(h.Get(k)); ok && n <= 0 {
			return true
		}
	}
	return false
}

// QuotaRemaining reads the value of a RateLimit-Remaining header by its integer before the first ';': a registry sends the
// quota with its window as a parameter, "0;w=21600" (R-REPO-054, R-OCI-025). ok is false for an empty value and for one whose
// first element is no integer.
func QuotaRemaining(v string) (n int64, ok bool) {
	first, _, _ := strings.Cut(v, ";")
	first = strings.TrimSpace(first)
	if first == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(first, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func slowDown(body []byte) bool {
	if len(body) > SlowDownSniffBytes {
		body = body[:SlowDownSniffBytes]
	}
	return bytes.Contains(body, []byte("<Code>SlowDown</Code>"))
}

// ParseRetryAfter reads a Retry-After value (RFC 9110 section 10.2.3): delta seconds, or an HTTP date as the time left until it,
// which is not above zero for a date that has passed. ok is false for an empty or unreadable value and for a negative delta. It
// caps nothing and decides nothing about "absent": the client classification (R-REPO-029) and RateLimitOf (R-REPO-054) each apply
// their own rule to what it returns.
func ParseRetryAfter(v string, now time.Time) (d time.Duration, ok bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n < 0 {
			return 0, false
		}
		return time.Duration(min(n, maxRetryAfterSeconds)) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return t.Sub(now), true
	}
	return 0, false
}

// maxRetryAfterSeconds keeps a delta from overflowing a Duration.
const maxRetryAfterSeconds = 1 << 31

// retryAfter reads the hint of a rate-limit answer: a delta, or a date that lies ahead; a date in the past counts as absent.
func retryAfter(v string, now time.Time) (time.Duration, bool) {
	d, ok := ParseRetryAfter(v, now)
	if !ok || (d <= 0 && !isDelta(v)) {
		return 0, false
	}
	if d > MaxRateLimitWait {
		d = MaxRateLimitWait
	}
	return d, true
}

func isDelta(v string) bool {
	_, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	return err == nil
}

// resetOf reads RateLimit-Reset (delta seconds) and X-RateLimit-Reset (seconds since the epoch).
func resetOf(h http.Header, now time.Time) (time.Duration, bool) {
	if v := strings.TrimSpace(h.Get("RateLimit-Reset")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			return min(time.Duration(n)*time.Second, MaxRateLimitWait), true
		}
	}
	if v := strings.TrimSpace(h.Get("X-RateLimit-Reset")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			if t := time.Unix(n, 0); t.After(now) {
				return min(t.Sub(now), MaxRateLimitWait), true
			}
		}
	}
	return 0, false
}

// RateLimitError is the error a background request of a mirror returns for a rate-limit answer. Its method set is the
// one of discover.RateLimited, so the mirror engine and the formats find it with discover.AsRateLimited without this
// package being imported by them.
type RateLimitError struct {
	RateLimit
	// Repo is the key of the repository whose upstream answered.
	Repo string
}

func (e *RateLimitError) Error() string {
	msg := "the upstream of " + e.Repo + " is rate limiting trove (" + strconv.Itoa(e.Status) + ", " + e.Reason + ")"
	if e.Hinted {
		msg += "; it asked for a wait of " + e.Wait.String()
	}
	return msg
}

// RetryAfter is the wait the upstream asked for and whether it named one.
func (e *RateLimitError) RetryAfter() (time.Duration, bool) { return e.Wait, e.Hinted }

// HTTPStatus is the status of the answer; the engine of the mirror reads it through an interface of its own.
func (e *RateLimitError) HTTPStatus() int { return e.Status }

// RateLimitReason is the reason label of the answer.
func (e *RateLimitError) RateLimitReason() string { return e.Reason }
