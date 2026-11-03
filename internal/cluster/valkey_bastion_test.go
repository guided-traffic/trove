package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// bastionFixture is a client without a keyspace on the in-memory Valkey, the way a Bastion runs.
type bastionFixture struct {
	srv   *memServer
	v     *Valkey
	reg   *prometheus.Registry
	store *BastionStore
}

const bastionPrefix = "trove-bastion:packages.example.com:"

func newBastionFixture(t *testing.T) *bastionFixture {
	t.Helper()
	srv := newMemServer(t)
	cfg := standaloneCfg(srv.addr())
	cfg.KeyPrefix = bastionPrefix
	cfg.CommandTimeout = time.Second
	reg := prometheus.NewRegistry()
	v, err := NewValkey(t.Context(), cfg, "bastion-0", "bastion", nil, reg, WithoutKeyspace())
	if err != nil {
		t.Fatalf("NewValkey: %v", err)
	}
	t.Cleanup(v.Close)
	eventually(t, "availability", 5*time.Second, v.Available)
	return &bastionFixture{srv: srv, v: v, reg: reg, store: NewBastionStore(v)}
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestR_BAS_034_TheKeysOfTheBastionCarryItsPrefixAndNoKeyspace(t *testing.T) {
	f := newBastionFixture(t)
	ctx := t.Context()
	h := hashOf("/api/npm/npm-all/lodash")
	if err := f.store.Mark(ctx, MarkBlock, "203.0.113.7", "1:42", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Mark(ctx, MarkBlock, "2001:db8:1:2::/64", "1:42", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Mark(ctx, MarkTimeout, "203.0.113.7", "1:42", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Mark(ctx, MarkLevel, "203.0.113.7", "1:42", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := f.store.PutAnswer(ctx, "credential", h, []byte("record"), time.Minute); err != nil {
		t.Fatal(err)
	}
	held, release, err := f.store.AcquireSweep(ctx, time.Minute)
	if err != nil || !held {
		t.Fatalf("sweep: %v %v", held, err)
	}
	defer release()
	for _, k := range []string{
		bastionPrefix + "block:v1:203-0-113-7",
		bastionPrefix + "block:v1:2001_db8_1_2__-64",
		bastionPrefix + "timeout:v1:203-0-113-7",
		bastionPrefix + "block:level:v1:203-0-113-7",
		bastionPrefix + "ans:v1:credential:" + h,
		bastionPrefix + "sweep:v1",
	} {
		if _, ok := f.srv.value(k); !ok {
			t.Errorf("key %s does not exist", k)
		}
	}
}

func TestR_BAS_037_AMarkIsReadBackAndExpires(t *testing.T) {
	f := newBastionFixture(t)
	ctx := t.Context()
	if _, found, err := f.store.Marked(ctx, MarkBlock, "203.0.113.7"); err != nil || found {
		t.Fatalf("an unset mark: %v %v", found, err)
	}
	if err := f.store.Mark(ctx, MarkBlock, "203.0.113.7", "2:99", 200*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if v, found, err := f.store.Marked(ctx, MarkBlock, "203.0.113.7"); err != nil || !found || v != "2:99" {
		t.Fatalf("mark = %q %v %v", v, found, err)
	}
	eventually(t, "the mark's expiry", 3*time.Second, func() bool {
		_, found, _ := f.store.Marked(ctx, MarkBlock, "203.0.113.7")
		return !found
	})
	for _, bad := range []string{"", "ABC", "a b", "203.0.113.7\n", strings.Repeat("1", 65)} {
		if err := f.store.Mark(ctx, MarkBlock, bad, "x", time.Minute); err == nil {
			t.Errorf("address %q accepted", bad)
		}
	}
	if err := f.store.Mark(ctx, MarkKind("nope"), "203.0.113.7", "x", time.Minute); err == nil {
		t.Error("unknown kind accepted")
	}
	if err := f.store.Mark(ctx, MarkBlock, "203.0.113.7", "x", 0); err == nil {
		t.Error("a mark without an expiry accepted")
	}
}

func TestR_BAS_034_AddressPartIsInjective(t *testing.T) {
	seen := map[string]string{}
	for _, a := range []string{"203.0.113.7", "203.0.113.70", "2001:db8::/64", "2001:db8:0:1::/64", "::/64", "10.0.0.1", "fe80::/64"} {
		p, err := AddressPart(a)
		if err != nil {
			t.Fatalf("%s: %v", a, err)
		}
		if other, dup := seen[p]; dup {
			t.Fatalf("%s and %s share the part %s", a, other, p)
		}
		seen[p] = a
		if !partPattern.MatchString(p) {
			t.Errorf("part %q of %s is not a valid key part", p, a)
		}
	}
}

func TestR_BAS_034_AnswerRecordsRoundTripAndCanBeDeletedOnlyAsTheyAre(t *testing.T) {
	f := newBastionFixture(t)
	ctx := t.Context()
	h := hashOf("k")
	if _, found, err := f.store.GetAnswer(ctx, "shape", h); err != nil || found {
		t.Fatalf("empty: %v %v", found, err)
	}
	if err := f.store.PutAnswer(ctx, "shape", h, []byte("a\x00b\nc"), time.Minute); err != nil {
		t.Fatal(err)
	}
	got, found, err := f.store.GetAnswer(ctx, "shape", h)
	if err != nil || !found || string(got) != "a\x00b\nc" {
		t.Fatalf("record = %q %v %v", got, found, err)
	}
	if _, found, _ := f.store.GetAnswer(ctx, "url", h); found {
		t.Fatal("a record of another kind was found")
	}
	// A record replaced after it was read is not deleted by the delete of the old one.
	if err := f.store.PutAnswer(ctx, "shape", h, []byte("new"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DeleteAnswer(ctx, "shape", h, got); err != nil {
		t.Fatal(err)
	}
	if cur, found, _ := f.store.GetAnswer(ctx, "shape", h); !found || string(cur) != "new" {
		t.Fatalf("the replacement was deleted: %q %v", cur, found)
	}
	if err := f.store.DeleteAnswer(ctx, "shape", h, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := f.store.GetAnswer(ctx, "shape", h); found {
		t.Fatal("the record was not deleted")
	}
	if err := f.store.PutAnswer(ctx, "shape", h, make([]byte, MaxValueSize+1), time.Minute); !errors.Is(err, ErrValueTooLarge) {
		t.Fatalf("a record above the limit: %v", err)
	}
	for _, c := range []struct{ kind, hash string }{{"Shape", h}, {"", h}, {"shape", "abc"}, {"shape", strings.ToUpper(h)}, {"sh:ape", h}} {
		if err := f.store.PutAnswer(ctx, c.kind, c.hash, []byte("x"), time.Minute); err == nil {
			t.Errorf("(%q, %q) accepted", c.kind, c.hash)
		}
	}
	if err := f.store.PutAnswer(ctx, "shape", h, []byte("x"), 0); err == nil {
		t.Error("an answer without validity accepted")
	}
}

func TestR_BAS_031_OneReplicaHoldsTheSweepLease(t *testing.T) {
	f := newBastionFixture(t)
	ctx := t.Context()
	held, release, err := f.store.AcquireSweep(ctx, time.Minute)
	if err != nil || !held {
		t.Fatalf("first: %v %v", held, err)
	}
	if again, _, err := f.store.AcquireSweep(ctx, time.Minute); err != nil || again {
		t.Fatalf("second holder: %v %v", again, err)
	}
	release()
	if again, rel, err := f.store.AcquireSweep(ctx, time.Minute); err != nil || !again {
		t.Fatalf("after release: %v %v", again, err)
	} else {
		rel()
	}
	if _, _, err := f.store.AcquireSweep(ctx, 0); err == nil {
		t.Fatal("a lease without an expiry accepted")
	}
}

func TestR_BAS_025_AFlightIsNamedByTheHashOfTheCacheKey(t *testing.T) {
	f := newBastionFixture(t)
	key, err := FlightKeyFromHash(hashOf("/api/npm/npm-all/lodash"))
	if err != nil {
		t.Fatal(err)
	}
	lock := NewFlightLock(f.v, FlightLockOptions{TTL: 3 * time.Second})
	hold, ok, err := lock.Acquire(t.Context(), key)
	if err != nil || !ok {
		t.Fatalf("acquire: %v %v", ok, err)
	}
	if _, ok := f.srv.value(bastionPrefix + "lock:fetch:v1:" + key.String()); !ok {
		t.Fatal("the lock key is not below the Bastion's prefix")
	}
	if _, ok, _ := lock.Acquire(t.Context(), key); ok {
		t.Fatal("a second holder")
	}
	_ = hold.Release(t.Context(), OutcomeStored, "")
	for _, bad := range []string{"", "xyz", strings.ToUpper(hashOf("a")), hashOf("a")[:63]} {
		if _, err := FlightKeyFromHash(bad); err == nil {
			t.Errorf("hash %q accepted", bad)
		}
	}
}

func TestR_BAS_034_WithoutValkeyEveryCallIsUnavailableAndCounted(t *testing.T) {
	none := NewBastionStore(nil)
	ctx := t.Context()
	if _, _, err := none.Marked(ctx, MarkBlock, "203.0.113.7"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Marked: %v", err)
	}
	if err := none.Mark(ctx, MarkBlock, "203.0.113.7", "x", time.Minute); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Mark: %v", err)
	}
	if _, _, err := none.GetAnswer(ctx, "shape", hashOf("a")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("GetAnswer: %v", err)
	}
	if _, _, err := none.AcquireSweep(ctx, time.Minute); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("AcquireSweep: %v", err)
	}

	// A client that cannot reach Valkey counts the degradation under its own mechanism.
	cfg := standaloneCfg("127.0.0.1:1")
	cfg.KeyPrefix = bastionPrefix
	cfg.ConnectTimeout = 100 * time.Millisecond
	reg := prometheus.NewRegistry()
	v, err := NewValkey(ctx, cfg, "bastion-0", "bastion", nil, reg, WithoutKeyspace())
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	store := NewBastionStore(v)
	if _, _, err := store.Marked(ctx, MarkBlock, "203.0.113.7"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Marked on an unreachable Valkey: %v", err)
	}
	if got := degradedCount(t, reg, MechanismBastionState); got < 1 {
		t.Fatalf("trove_valkey_degraded_total{mechanism=bastion_state} = %v", got)
	}
}

func degradedCount(t *testing.T, reg *prometheus.Registry, mechanism string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "trove_valkey_degraded_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			if labelValue(m, "mechanism") == mechanism {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func TestR_BAS_034_EstimateCountsInTheSlidingWindowAndNeverFallsBack(t *testing.T) {
	store := newMemWindows()
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	ls, err := NewLimiterSet(LimiterOptions{store: store, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	l := ls.NewLimiter("bastion.failed_auth", ScopeCluster, 1000/600.0, 1000) // a window of 10 minutes
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		n, err := l.Estimate(ctx, "a", true)
		if err != nil || n != i {
			t.Fatalf("estimate %d = %d, %v", i, n, err)
		}
	}
	if n, err := l.Estimate(ctx, "a", false); err != nil || n != 3 {
		t.Fatalf("a read = %d, %v", n, err)
	}
	if n, _ := l.Estimate(ctx, "b", false); n != 0 {
		t.Fatalf("another bucket = %d", n)
	}
	now = now.Add(15 * time.Minute) // half a window into the next one
	if n, _ := l.Estimate(ctx, "a", false); n != 2 {
		t.Fatalf("after half a window = %d, want ceil(3 x 0.5)", n)
	}
	// Valkey unusable: no answer, the limiter reports its fallback, the caller counts in memory.
	store.down = true
	if _, err := l.Estimate(ctx, "a", true); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable store: %v", err)
	}
	if !l.inFallback.Load() {
		t.Fatal("the limiter does not report its fallback")
	}
	store.down, store.fail = false, true
	if _, err := l.Estimate(ctx, "a", true); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("failing store: %v", err)
	}
	store.fail = false
	if _, err := l.Estimate(ctx, "a", true); err != nil {
		t.Fatal(err)
	}
	if l.inFallback.Load() {
		t.Fatal("the fallback flag stays after Valkey answered again")
	}
	replica := ls.NewLimiter("bastion.local", ScopeReplica, 1, 1)
	if _, err := replica.Estimate(ctx, "a", true); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("replica scope: %v", err)
	}
}
