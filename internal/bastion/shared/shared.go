// Package shared is the state the replicas of a Bastion may share when Valkey is configured and keep per
// replica when it is not (R-BAS-034, Q-BAS-11): the block list, the rate-limit and failed-authentication
// counters, the fetch locks, the sweep lease and the signed answer records. Its consumers (the guard, the
// engine, the cache sweeps) hold the interfaces below and never know which implementation runs; losing Valkey
// costs performance, never correctness (INV-3, R-BAS-034).
//
// [NewLocal] is the implementation without Valkey: it works per replica, with the rates and thresholds divided
// by the chart's replica count (bastion.replicas, Q-BAS-34), so the sum over replicas stays near the configured
// value. Work package 6 adds the Valkey implementation behind the same interfaces with the key families of
// R-BAS-034 (prefix trove-bastion:<valkey.key_prefix>:), degrading to a local one while Valkey is unavailable
// (trove_valkey_available 0, R-ARCH-031), and owns the tests of both.
//
// This package imports nothing of the main installation. The Valkey implementation may use the Valkey client
// of internal/cluster; that package is on the allow-list of the archtest rule for it (R-BAS-001, Q-BAS-51).
package shared

import (
	"context"
	"time"
)

// Rate is a token bucket: a sustained rate per second and a burst (R-BAS-035).
type Rate struct {
	PerSecond float64
	Burst     int
}

// Limiter holds the limiters of R-ARCH-034 under the names bastion.<name> (R-BAS-034): token buckets for the
// per-address request limits and sliding-window counters for the misses, the failed authentications and the
// refusals that lead to a block. The key is the client address (an IPv6 address counted as its /64, R-BAS-019).
type Limiter interface {
	// Allow takes one token of the bucket (name, key). It returns whether the request is admitted and, when
	// not, how long until a token is available. A refused request takes nothing (Q-BAS-127).
	Allow(ctx context.Context, name, key string, rate Rate) (ok bool, retryAfter time.Duration, err error)
	// Add counts one event in the sliding window of (name, key) and returns the number of events inside it,
	// this one included, in the units of the configured threshold: while the counters are per replica (no
	// Valkey, or Valkey unavailable) the replica's count is multiplied by [Limiter.Replicas], so a caller
	// compares the number with the configured value and the total over the replicas stays near it (Q-BAS-34).
	// The window is between one second and twelve hours; a shorter or longer one is held to that range
	// (the configuration refuses a longer one, Q-BAS-128).
	Add(ctx context.Context, name, key string, window time.Duration) (count int, err error)
	// AddIfWithin is Add for a limit: the event is counted only if the count, this event included, stays within
	// limit. A refused event takes nothing from the budget of the key, so a client slightly over its limit is
	// admitted up to the limit and not starved (Q-BAS-127). It returns whether the event was admitted and the
	// count with it, in the units of Add.
	AddIfWithin(ctx context.Context, name, key string, window time.Duration, limit int) (admitted bool, count int, err error)
	// Count returns the number of events inside the window without adding one, in the same units.
	Count(ctx context.Context, name, key string, window time.Duration) (count int, err error)
	// Replicas is bastion.replicas: the divisor the local forms apply (Q-BAS-34). The callers need not use
	// it; Allow, Add and Count have applied it.
	Replicas() int
}

// Block is an address on the block list.
type Block struct {
	Until time.Time
	// Level counts the blocks within 24 h, which doubles the duration up to the maximum (R-BAS-037).
	Level int
}

// BlockList is the list of blocked addresses (R-BAS-037). With Valkey a block holds on every replica at its
// next request.
type BlockList interface {
	// Blocked returns the block of an address, if it is blocked now.
	Blocked(ctx context.Context, addr string) (Block, bool, error)
	// Set blocks an address for the duration; level is the new level.
	Set(ctx context.Context, addr string, d time.Duration, level int) error
	// Level returns the level of the address's most recent block within 24 h, 0 for none.
	Level(ctx context.Context, addr string) (int, error)
	// Size is the number of addresses blocked now (trove_bastion_blocked_addresses).
	Size(ctx context.Context) (int, error)
}

// FlightLock collapses concurrent fetches of one cache key across replicas (R-BAS-025, the FlightLock of
// R-ARCH-033 over the hex SHA-256 of the cache key, 30 s, extended by the holder). A lost lock or an
// unavailable Valkey only makes a replica fetch itself.
type FlightLock interface {
	// TryAcquire takes the lock; held is false when another holder has it. When Valkey cannot be asked, held is
	// true and err names why: the caller fetches itself, as the lock is an optimization (R-BAS-034).
	TryAcquire(ctx context.Context, keyHash string) (held bool, release func(), err error)
}

// SweepLease picks the one replica that sweeps a bucket (R-BAS-031, twice the sweep interval). Without Valkey,
// and while it is unavailable, every replica holds the lease and sweeps at a randomized interval, which is safe
// because a sweep only deletes.
type SweepLease interface {
	TryAcquire(ctx context.Context, ttl time.Duration) (held bool, release func(), err error)
}

// AnswerKind keys the families of signed answer records (R-BAS-034): ans:<kind>:<sha256 of the key>.
type AnswerKind string

// The kinds of stored answers.
const (
	AnswerCredential AnswerKind = "credential"
	AnswerShape      AnswerKind = "shape"
	AnswerURL        AnswerKind = "url"
	AnswerNegative   AnswerKind = "negative"
)

// AnswerStore keeps answer records (R-BAS-020, R-BAS-024, R-BAS-032): with Valkey, shared by the replicas;
// without, the replica's memory. The records are opaque to the caller. The Valkey form signs a record with
// cache.Signer under its kind and key on Put and verifies it on Get: a record that fails is not returned, is
// deleted and is counted (bad_signature), so a caller never sees what Valkey returned beyond a valid
// signature. The caller still judges the content (answers.State) whatever the store returns. A record signed
// is larger than the record by 65 bytes; one that does not fit the 1 KiB of R-ARCH-030 is refused by Put.
type AnswerStore interface {
	Get(ctx context.Context, kind AnswerKind, keyHash string) (record []byte, ok bool, err error)
	Put(ctx context.Context, kind AnswerKind, keyHash string, record []byte, ttl time.Duration) error
	Delete(ctx context.Context, kind AnswerKind, keyHash string) error
}

// State bundles the shared state the Bastion's packages use.
type State struct {
	Limiter Limiter
	Blocks  BlockList
	// Timeouts is the list of the failed-authentication timeouts (R-BAS-036): the same mechanism as the block
	// list, a family of its own; the level is not used.
	Timeouts BlockList
	Flights  FlightLock
	Sweep    SweepLease
	Answers  AnswerStore
	// Shared reports whether the state is shared across replicas (Valkey configured and available).
	Shared func() bool
}

// NewLocal returns the state without Valkey, for a Bastion of the given replica count (bastion.replicas).
// The clock is injectable for tests; nil means time.Now.
func NewLocal(replicas int, now func() time.Time) *State {
	if replicas < 1 {
		replicas = 1
	}
	if now == nil {
		now = time.Now
	}
	l := newLocal(replicas, now)
	return &State{
		Limiter: l, Blocks: newMemBlocks(now), Timeouts: newMemBlocks(now), Flights: newLocalFlights(),
		Sweep: localSweep{}, Answers: newMemAnswers(now), Shared: func() bool { return false },
	}
}
