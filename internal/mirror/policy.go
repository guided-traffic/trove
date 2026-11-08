package mirror

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math/rand/v2"
	"time"

	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// The pure rules of the engine: the clock and the jitter it draws from (the seams of its tests), the back-offs, the decision
// between a full and an incremental pass, the versions a pass stores and the fingerprint of the patterns.

// Clock is the engine's time: every decision and every wait of the engine goes through it, so that a test runs a pass of
// hours in milliseconds. Sleep returns early with the context's error when ctx ends.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

// Jitter is the engine's source of randomness: Float64 returns a number in [0, 1). A test supplies a fixed one.
type Jitter interface {
	Float64() float64
}

// SystemClock is the clock of production.
type SystemClock struct{}

// Now returns the time of the system.
func (SystemClock) Now() time.Time { return time.Now() }

// Sleep waits for d or for the end of ctx.
func (SystemClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// SystemJitter is the randomness of production.
type SystemJitter struct{}

// Float64 returns a number in [0, 1).
func (SystemJitter) Float64() float64 { return rand.Float64() } //nolint:gosec // a spread of retries, no secret

// The constants of the back-offs (R-REPO-053 errors, R-REPO-054).
const (
	// FailureBackoffBase is the wait after the first failed pass attempt; it doubles per failure in a row.
	FailureBackoffBase = 60 * time.Second
	// FailureBackoffCap caps the wait after a failed pass attempt, before the jitter.
	FailureBackoffCap = 6 * time.Hour
	// FailureJitter is the share by which the wait after a failure varies up and down.
	FailureJitter = 0.2
	// RateLimitBackoffCap caps the wait after a rate-limit answer without a hint.
	RateLimitBackoffCap = 300 * time.Second
	// LongWait is the wait beyond which a pass ends its job instead of holding the slot (R-REPO-054).
	LongWait = 300 * time.Second
	// MaxRateLimitWait caps a wait a hint asks for (R-REPO-054).
	MaxRateLimitWait = 6 * time.Hour
	// NotifyEvery is the interval at which a repository's mirror.rate_limited event is written at most.
	NotifyEvery = time.Hour
	// OfflineRecheck bounds the wait before a pass that ended offline is tried again.
	OfflineRecheck = 5 * time.Minute
	// PreemptRetry is how long a package whose fetch a client pre-empted waits before it is tried again.
	PreemptRetry = 5 * time.Second
)

// FailureBackoff is the wait before the next pass attempt after the failures-th failure in a row: min(60 s × 2^(failures−1),
// 6 h), before the jitter (R-REPO-053 errors, Q-REPO-136).
func FailureBackoff(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	d := FailureBackoffBase
	for i := 1; i < failures; i++ {
		d *= 2
		if d >= FailureBackoffCap {
			return FailureBackoffCap
		}
	}
	return min(d, FailureBackoffCap)
}

// JitterFactor maps u in [0, 1) to the factor in [0.8, 1.2) that scales a failure back-off.
func JitterFactor(u float64) float64 { return 1 - FailureJitter + 2*FailureJitter*u }

// RateLimitBackoff is the wait after the k-th rate-limit answer in a row that named no wait: drawn uniformly from [d/2, d]
// with d = min(300 s, 1 s × 2^(k−1)); u in [0, 1) is the draw (R-REPO-054).
func RateLimitBackoff(k int, u float64) time.Duration {
	if k < 1 {
		k = 1
	}
	d := time.Second
	for i := 1; i < k && d < RateLimitBackoffCap; i++ {
		d *= 2
	}
	d = min(d, RateLimitBackoffCap)
	return d/2 + time.Duration(u*float64(d/2))
}

// DecideKind decides whether a pass that starts now is full or incremental, from what is known before the method is
// chosen (R-REPO-051): a full pass when there is no cursor of a complete feed pass, when the patterns changed since the
// last complete pass (the fingerprint), when fullPassInterval is not 0 and the last full pass is older, and when a full
// pass is requested. The method of the pass makes it full as well, see FinalKind.
func DecideKind(s State, fingerprint []byte, fullPassInterval time.Duration, now time.Time, requested bool) discover.PassKind {
	switch {
	case requested || s.PassRequestedFull,
		len(s.FeedCursor) == 0,
		string(s.PatternsFingerprint) != string(fingerprint),
		fullPassInterval > 0 && (s.LastFullPassAt.IsZero() || now.Sub(s.LastFullPassAt) >= fullPassInterval):
		return discover.PassFull
	}
	return discover.PassIncremental
}

// FinalKind applies the method to the decided kind: only a feed has an incremental pass, and only when the method is the one
// of the last complete pass's cursor (R-REPO-051).
func FinalKind(kind discover.PassKind, method discover.Method, s State) discover.PassKind {
	if kind == discover.PassIncremental && (method != discover.MethodFeed || s.FeedMethod != method) {
		return discover.PassFull
	}
	return kind
}

// Fingerprint is the fingerprint of the include and exclude patterns kept in mirror_state.
func Fingerprint(include, exclude []string) []byte {
	h := sha256.New()
	var n [8]byte
	for _, list := range [][]string{include, exclude} {
		binary.BigEndian.PutUint64(n[:], uint64(len(list)))
		h.Write(n[:])
		for _, p := range list {
			binary.BigEndian.PutUint64(n[:], uint64(len(p)))
			h.Write(n[:])
			h.Write([]byte(p))
		}
	}
	return h.Sum(nil)
}

// SelectItems applies the version bounds of R-REPO-051 to a plan's items (newest first): the prerelease versions go when
// includePrereleases is false, the newest max versions of the rest stay (0: all), and a pinned version always stays besides them
// (Q-NPM-71). The bounds count versions, not items: the items of one version (an image's manifests, configs and layers, Q-OCI-180)
// are kept or dropped together, decided at the first item of the version, and a version is pinned when any of its items is. The
// order is the plan's.
func SelectItems(items []discover.Item, includePrereleases bool, max int) []discover.Item {
	out := make([]discover.Item, 0, len(items))
	kept := 0
	decided := map[string]bool{}
	for _, it := range items {
		keep, ok := decided[it.Version]
		if !ok {
			candidate := includePrereleases || !it.Prerelease
			within := candidate && (max <= 0 || kept < max)
			if within {
				kept++
			}
			keep = within || it.Pinned
			decided[it.Version] = keep
		} else if it.Pinned {
			keep = true
		}
		if keep {
			out = append(out, it)
		}
	}
	return out
}
