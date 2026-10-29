package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// MaxTokenSeenWindow is the longest throttle-exemption window a marker may have.
// auth.rate_limit.recent_token_window is validated against it.
const MaxTokenSeenWindow = time.Hour

// TokenSeen is the throttle-exemption marker of R-AUTH-038. A successful
// validation of a credential from PostgreSQL marks it for the window, under a
// key derived from the hash of its secret hash (R-ARCH-030): the key proves
// nothing about the secret and the value is the constant "1". A marker lets a
// throttled address validate the credential; it never authenticates anyone, and
// Valkey being unavailable only narrows the exemption to the replica's own cache
// (Q-AUTH-11).
type TokenSeen struct {
	v      *Valkey
	window time.Duration
}

// NewTokenSeen creates the marker store. window is auth.rate_limit.recent_token_window,
// at most [MaxTokenSeenWindow]. A nil v gives a store that never marks and never
// finds a marker.
func NewTokenSeen(v *Valkey, window time.Duration) (*TokenSeen, error) {
	if window <= 0 || window > MaxTokenSeenWindow {
		return nil, errors.New("cluster: the token marker window must be positive and at most one hour")
	}
	return &TokenSeen{v: v, window: window}, nil
}

// markerKey forms the key. The part is the SHA-256 of the credential's secret
// hash, so the key is neither the lookup key of the database nor derivable
// from a token without knowing it.
func (t *TokenSeen) markerKey(secretSHA256 []byte) (Key, error) {
	sum := sha256.Sum256(secretSHA256)
	return t.v.formKeyTTL(tokSeenFamily, t.window, hex.EncodeToString(sum[:]))
}

// Mark records that the credential was validated from PostgreSQL. The error is
// for metrics and logs; a caller continues without a marker.
func (t *TokenSeen) Mark(ctx context.Context, secretSHA256 []byte) error {
	if t == nil || t.v == nil {
		return ErrUnavailable
	}
	k, err := t.markerKey(secretSHA256)
	if err != nil {
		return err
	}
	return t.v.set(ctx, k, "1")
}

// Seen reports whether the credential carries a marker. Any error, Valkey
// unavailable included, is "not seen": the caller falls back to its own cache.
func (t *TokenSeen) Seen(ctx context.Context, secretSHA256 []byte) bool {
	if t == nil || t.v == nil {
		return false
	}
	k, err := t.markerKey(secretSHA256)
	if err != nil {
		return false
	}
	val, found, err := t.v.get(ctx, k)
	return err == nil && found && val == "1"
}
