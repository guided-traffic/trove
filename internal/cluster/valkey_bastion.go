package cluster

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The Valkey side of the trove Bastion's shared state (R-BAS-034): the block list, the failed-authentication
// timeouts, the signed answer records and the sweep lease. The Bastion's fetch locks are the FlightLock of
// valkey_flight.go (FlightKeyFromHash), its counters the limiters of limiter.go (Limiter.Estimate). Like every
// mechanism, this one hands out typed methods and no key (Q-ARCH-16); the Bastion's client is made with
// [WithoutKeyspace], so a key is <key_prefix><mechanism>:v1:<part>... with the key_prefix
// trove-bastion:<valkey.key_prefix>: of the Bastion.

// The longest expiries of the Bastion's key families.
const (
	// MaxBlockTTL bounds bastion.blocking.max_duration (at most 720 h).
	MaxBlockTTL = 720 * time.Hour
	// MaxBlockLevelTTL is the 24 h in which a further block doubles the duration (R-BAS-037).
	MaxBlockLevelTTL = 24 * time.Hour
	// MaxTimeoutTTL bounds bastion.failed_auth.timeout (at most 24 h).
	MaxTimeoutTTL = 24 * time.Hour
	// MaxAnswerTTL bounds the remaining validity of an answer: its period (at most 1 h) plus the grace period
	// (at most 24 h).
	MaxAnswerTTL = 48 * time.Hour
	// MaxSweepLeaseTTL bounds two sweep intervals (the interval is at most 24 h).
	MaxSweepLeaseTTL = 48 * time.Hour
)

// MarkKind selects the family of an address mark.
type MarkKind string

// The address marks of the Bastion.
const (
	// MarkBlock is a block of the address: the key holds the Bastion's record of it (block:<address>, TTL =
	// the remaining duration).
	MarkBlock MarkKind = "block"
	// MarkLevel remembers the level of the address's most recent block for 24 h.
	MarkLevel MarkKind = "level"
	// MarkTimeout is the failed-authentication timeout of the address.
	MarkTimeout MarkKind = "timeout"
)

func (k MarkKind) family() *KeyFamily {
	switch k {
	case MarkBlock:
		return blockFamily
	case MarkLevel:
		return blockLevelFamily
	case MarkTimeout:
		return timeoutFamily
	}
	return nil
}

// BastionStore is the Bastion's typed access to its Valkey keys. Every method returns [ErrUnavailable]
// without network I/O while Valkey cannot be used, and every failure counts for
// trove_valkey_degraded_total{mechanism="bastion_state"}; the caller runs its local form.
type BastionStore struct{ v *Valkey }

// NewBastionStore returns the store of a client made with [WithoutKeyspace]. With a nil client every call
// returns [ErrUnavailable].
func NewBastionStore(v *Valkey) *BastionStore { return &BastionStore{v: v} }

var errNoBastionClient = fmt.Errorf("cluster: no Valkey client: %w", ErrUnavailable)

func (s *BastionStore) fail(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if cerr := callerEnded(ctx); cerr != nil {
		return cerr
	}
	s.v.CountDegraded(MechanismBastionState)
	return err
}

// AddressPart turns a client address as the guard keys it (an IPv4 address, or the /64 prefix of an IPv6
// address, for example 2001:db8:1:2::/64) into a key part: ':' becomes '_', '.' and '/' become '-'. The
// mapping is injective for these inputs, so no two addresses share a key, and the key stays readable.
func AddressPart(addr string) (string, error) {
	if addr == "" || len(addr) > 64 {
		return "", errors.New("cluster: address of 1 to 64 characters expected")
	}
	var b strings.Builder
	for _, r := range addr {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
			b.WriteRune(r)
		case r == ':':
			b.WriteByte('_')
		case r == '.', r == '/':
			b.WriteByte('-')
		default:
			return "", fmt.Errorf("cluster: %q is not a client address key", addr)
		}
	}
	return b.String(), nil
}

func (s *BastionStore) markKey(kind MarkKind, ttl time.Duration, addr string) (Key, error) {
	f := kind.family()
	if f == nil {
		return Key{}, fmt.Errorf("cluster: unknown mark kind %q", kind)
	}
	part, err := AddressPart(addr)
	if err != nil {
		return Key{}, err
	}
	if ttl <= 0 {
		ttl = f.maxTTL
	}
	return s.v.formKeyTTL(f, min(ttl, f.maxTTL), part)
}

// Mark stores value under the mark of the address for ttl.
func (s *BastionStore) Mark(ctx context.Context, kind MarkKind, addr, value string, ttl time.Duration) error {
	if s.v == nil {
		return errNoBastionClient
	}
	if ttl <= 0 {
		return errors.New("cluster: a mark needs a positive expiry")
	}
	k, err := s.markKey(kind, ttl, addr)
	if err != nil {
		return err
	}
	return s.fail(ctx, s.v.set(ctx, k, value))
}

// Marked returns the value of the mark of the address, and whether there is one.
func (s *BastionStore) Marked(ctx context.Context, kind MarkKind, addr string) (string, bool, error) {
	if s.v == nil {
		return "", false, errNoBastionClient
	}
	k, err := s.markKey(kind, 0, addr)
	if err != nil {
		return "", false, err
	}
	val, found, err := s.v.get(ctx, k)
	return val, found, s.fail(ctx, err)
}

func (s *BastionStore) answerKey(kind, hash string, ttl time.Duration) (Key, error) {
	if kind == "" || strings.Trim(kind, "abcdefghijklmnopqrstuvwxyz") != "" {
		return Key{}, fmt.Errorf("cluster: answer kind %q must be lower-case letters", kind)
	}
	if !isSHA256Hex(hash) {
		return Key{}, errors.New("cluster: an answer is keyed by the 64 hex digits of a SHA-256")
	}
	if ttl <= 0 {
		ttl = MaxAnswerTTL
	}
	return s.v.formKeyTTL(answerFamily, ttl, kind, hash)
}

func isSHA256Hex(h string) bool {
	b, err := hex.DecodeString(h)
	return err == nil && len(b) == 32 && h == strings.ToLower(h)
}

// PutAnswer stores a record of an answer under (kind, hash) for ttl, the remaining validity of the answer.
// The record is opaque here; a record above MaxValueSize is refused with [ErrValueTooLarge].
func (s *BastionStore) PutAnswer(ctx context.Context, kind, hash string, record []byte, ttl time.Duration) error {
	if s.v == nil {
		return errNoBastionClient
	}
	if ttl <= 0 {
		return errors.New("cluster: an answer needs a positive expiry")
	}
	k, err := s.answerKey(kind, hash, min(ttl, MaxAnswerTTL))
	if err != nil {
		return err
	}
	err = s.v.set(ctx, k, string(record))
	if errors.Is(err, ErrValueTooLarge) {
		return err
	}
	return s.fail(ctx, err)
}

// GetAnswer returns the record under (kind, hash).
func (s *BastionStore) GetAnswer(ctx context.Context, kind, hash string) ([]byte, bool, error) {
	if s.v == nil {
		return nil, false, errNoBastionClient
	}
	k, err := s.answerKey(kind, hash, 0)
	if err != nil {
		return nil, false, err
	}
	val, found, err := s.v.get(ctx, k)
	return []byte(val), found, s.fail(ctx, err)
}

// DeleteAnswer deletes the record under (kind, hash) if it still holds record: the ACL user has no DEL, and a
// record replaced meanwhile is not the one that failed (R-ARCH-031).
func (s *BastionStore) DeleteAnswer(ctx context.Context, kind, hash string, record []byte) error {
	if s.v == nil {
		return errNoBastionClient
	}
	k, err := s.answerKey(kind, hash, 0)
	if err != nil {
		return err
	}
	_, err = s.v.delIfEq(ctx, k, string(record))
	if errors.Is(err, ErrValueTooLarge) {
		return err
	}
	return s.fail(ctx, err)
}

// AcquireSweep takes the sweep lease for ttl (twice the sweep interval, R-BAS-031). held is false when another
// replica holds it. release gives the lease back if this holder still owns it.
func (s *BastionStore) AcquireSweep(ctx context.Context, ttl time.Duration) (held bool, release func(), err error) {
	if s.v == nil {
		return false, func() {}, errNoBastionClient
	}
	if ttl <= 0 {
		return false, func() {}, errors.New("cluster: a lease needs a positive expiry")
	}
	k, err := s.v.formKeyTTL(sweepFamily, min(ttl, MaxSweepLeaseTTL))
	if err != nil {
		return false, func() {}, err
	}
	token := s.v.instance + "." + randomToken()
	ok, err := s.v.setNX(ctx, k, token)
	if err != nil {
		return false, func() {}, s.fail(ctx, err)
	}
	if !ok {
		return false, func() {}, nil
	}
	return true, func() {
		_, _ = s.v.delIfEq(context.WithoutCancel(ctx), k, token)
	}, nil
}

// FlightKeyFromHash names the flight of a Bastion cache key by the lower-case hex SHA-256 of the key, which
// is what the lock key and the outcome channel carry (R-BAS-025). It is the key of [NewFlightLock] for a
// caller that has no repository id.
func FlightKeyFromHash(h string) (FlightKey, error) {
	if !isSHA256Hex(h) {
		return FlightKey{}, errors.New("cluster: a flight is named by the 64 hex digits of a SHA-256")
	}
	return FlightKey{hex: h}, nil
}
