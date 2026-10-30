package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MaxNegativeTTL is the longest expiry a negative entry may have: the longest
// notFoundTtlSeconds (24 h) plus the 5 s clock allowance of the readers
// (R-ARCH-030).
const MaxNegativeTTL = 24*time.Hour + 5*time.Second

// NegativeEntry is a remembered upstream 404 or 410 for one flight key
// (R-REPO-028). It can make trove answer 404 and nothing else: nothing is
// served, admitted or granted because of one (Q-ARCH-16).
type NegativeEntry struct {
	// Status is 404 or 410.
	Status int
	// SentAt is when the upstream request was sent, by the clock of the
	// replica that sent it.
	SentAt time.Time
}

// NegativeStore keeps negative entries in Valkey, shared by the replicas. It
// is the Valkey half of the negative cache: it checks that an entry is well
// formed and nothing else. The bounded per-replica L1 in front of it, the
// validity window and the invalidations that cover a path belong to the proxy
// (Q-ARCH-197), which alone knows the repository's notFoundTtlSeconds and the
// path.
//
// Every error wraps [ErrUnavailable] when Valkey cannot be used; the caller
// then answers from its L1 alone.
type NegativeStore interface {
	// Lookup returns the entry of (repoID, f). found is false when there is
	// none. A malformed entry is deleted and reported as not found.
	Lookup(ctx context.Context, repoID int64, f FlightKey) (e NegativeEntry, found bool, err error)
	// Record stores e for ttl, which is the repository's notFoundTtlSeconds.
	// A ttl of zero or less stores nothing (negative caching is off).
	Record(ctx context.Context, repoID int64, f FlightKey, e NegativeEntry, ttl time.Duration) error
}

// negativeCache is the cache label of trove_valkey_cache_lookups_total.
const negativeCache = "negative"

type negativeStore struct{ v *Valkey }

// NewNegativeStore creates the store. With v == nil every call returns
// [ErrUnavailable].
func NewNegativeStore(v *Valkey) NegativeStore { return negativeStore{v: v} }

// negativeValue is the stored form, {"s": 404 or 410, "c": Unix ms}
// (R-ARCH-030).
type negativeValue struct {
	S int   `json:"s"`
	C int64 `json:"c"`
}

func validNegativeStatus(s int) bool { return s == 404 || s == 410 }

func (negativeStore) encode(e NegativeEntry) (string, error) {
	if !validNegativeStatus(e.Status) {
		return "", fmt.Errorf("cluster: a negative entry records 404 or 410, not %d", e.Status)
	}
	ms := e.SentAt.UnixMilli()
	if ms <= 0 {
		return "", errors.New("cluster: a negative entry needs the time its request was sent")
	}
	b, err := json.Marshal(negativeValue{S: e.Status, C: ms})
	return string(b), err
}

// decode parses a stored value; ok is false for anything but exactly the form
// above.
func (negativeStore) decode(raw string) (e NegativeEntry, ok bool) {
	var nv negativeValue
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&nv); err != nil || dec.More() {
		return NegativeEntry{}, false
	}
	if !validNegativeStatus(nv.S) || nv.C <= 0 {
		return NegativeEntry{}, false
	}
	return NegativeEntry{Status: nv.S, SentAt: time.UnixMilli(nv.C)}, true
}

func (s negativeStore) key(repoID int64, f FlightKey, ttl time.Duration) (Key, error) {
	return s.v.formKeyTTL(negFamily, ttl, strconv.FormatInt(repoID, 10), f.hex)
}

func (s negativeStore) fail(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	if !errors.Is(err, ErrUnavailable) {
		err = fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	s.v.CountDegraded(MechanismNegativeCache)
	return err
}

func (s negativeStore) Lookup(ctx context.Context, repoID int64, f FlightKey) (NegativeEntry, bool, error) {
	if s.v == nil {
		return NegativeEntry{}, false, ErrUnavailable
	}
	if !f.valid() {
		return NegativeEntry{}, false, errInvalidFlightKey
	}
	k, err := s.key(repoID, f, MaxNegativeTTL)
	if err != nil {
		return s.lookupFailed(ctx, err)
	}
	raw, found, err := s.v.get(ctx, k)
	if err != nil {
		return s.lookupFailed(ctx, err)
	}
	return s.read(ctx, k, raw, found)
}

func (s negativeStore) lookupFailed(ctx context.Context, err error) (NegativeEntry, bool, error) {
	err = s.fail(ctx, err)
	if errors.Is(err, ErrUnavailable) {
		s.v.CountCacheLookup(negativeCache, "unavailable")
	}
	return NegativeEntry{}, false, err
}

// read judges the value of a successful GET.
func (s negativeStore) read(ctx context.Context, k Key, raw string, found bool) (NegativeEntry, bool, error) {
	if !found {
		s.v.CountCacheLookup(negativeCache, "miss")
		return NegativeEntry{}, false, nil
	}
	e, ok := s.decode(raw)
	if !ok {
		// Delete only the value that was read: a writer that replaced it
		// since keeps its entry. The key's own expiry is not trusted.
		_, _ = s.v.delIfEq(ctx, k, raw)
		s.v.CountCacheLookup(negativeCache, "rejected")
		return NegativeEntry{}, false, nil
	}
	s.v.CountCacheLookup(negativeCache, "hit")
	return e, true, nil
}

func (s negativeStore) Record(ctx context.Context, repoID int64, f FlightKey, e NegativeEntry, ttl time.Duration) error {
	if s.v == nil {
		return ErrUnavailable
	}
	if !f.valid() {
		return errInvalidFlightKey
	}
	if ttl <= 0 {
		return nil
	}
	if ttl > MaxNegativeTTL {
		return fmt.Errorf("cluster: a negative entry expires within %s, not %s", MaxNegativeTTL, ttl)
	}
	val, err := s.encode(e)
	if err != nil {
		return err
	}
	k, err := s.key(repoID, f, ttl)
	if err != nil {
		return s.fail(ctx, err)
	}
	if err := s.v.set(ctx, k, val); err != nil {
		return s.fail(ctx, err)
	}
	return nil
}
