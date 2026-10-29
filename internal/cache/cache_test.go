package cache

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/guided-traffic/trove/internal/observability"
)

type fakeEpochs struct {
	mu sync.Mutex
	m  map[string]int64
}

func (f *fakeEpochs) Applied(a string) int64 { f.mu.Lock(); defer f.mu.Unlock(); return f.m[a] }
func (f *fakeEpochs) set(a string, v int64)  { f.mu.Lock(); f.m[a] = v; f.mu.Unlock() }

func newRegistry(t *testing.T) (*Registry, *fakeEpochs, *prometheus.Registry) {
	t.Helper()
	ep := &fakeEpochs{m: map[string]int64{}}
	reg := prometheus.NewRegistry()
	r, err := NewRegistry(ep, reg)
	if err != nil {
		t.Fatal(err)
	}
	return r, ep, reg
}

func mustPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s did not panic", what)
		}
	}()
	fn()
}

func TestRegisterRules(t *testing.T) {
	r, _, _ := newRegistry(t)
	t.Run("R-ARCH-018 SafetyTTL is mandatory", func(t *testing.T) {
		mustPanic(t, "a cache without SafetyTTL", func() { Register[string, int](r, "nottl", Options[string, int]{MaxEntries: 1}) })
	})
	t.Run("R-ARCH-018 a cache is bounded by entries or bytes, not both and not neither", func(t *testing.T) {
		mustPanic(t, "no bound", func() { Register[string, int](r, "nobound", Options[string, int]{SafetyTTL: time.Minute}) })
		mustPanic(t, "two bounds", func() {
			Register[string, int](r, "twobounds", Options[string, int]{SafetyTTL: time.Minute, MaxEntries: 1, MaxBytes: 1, Weigh: func(string, int) int64 { return 1 }})
		})
		mustPanic(t, "bytes without Weigh", func() { Register[string, int](r, "noweigh", Options[string, int]{SafetyTTL: time.Minute, MaxBytes: 1}) })
	})
	t.Run("R-ARCH-018 names are unique and well formed", func(t *testing.T) {
		Register[string, int](r, "dup", Options[string, int]{SafetyTTL: time.Minute, MaxEntries: 1})
		mustPanic(t, "a duplicate", func() { Register[string, int](r, "dup", Options[string, int]{SafetyTTL: time.Minute, MaxEntries: 1}) })
		mustPanic(t, "a bad name", func() {
			Register[string, int](r, "Bad-Name", Options[string, int]{SafetyTTL: time.Minute, MaxEntries: 1})
		})
	})
}

func TestBoundsAndTTL(t *testing.T) {
	r, _, _ := newRegistry(t)
	now := time.Unix(1000, 0)
	c := Register[string, string](r, "bounded", Options[string, string]{MaxEntries: 2, SafetyTTL: time.Minute, Now: func() time.Time { return now }})

	t.Run("R-ARCH-018 the least recently used entry goes first", func(t *testing.T) {
		c.Set("a", "1")
		c.Set("b", "2")
		c.Get("a") // a is now the most recent
		c.Set("c", "3")
		if _, ok := c.Get("b"); ok {
			t.Error("b survived")
		}
		if _, ok := c.Get("a"); !ok {
			t.Error("a was evicted")
		}
		if c.Len() != 2 {
			t.Errorf("Len = %d", c.Len())
		}
	})
	t.Run("R-ARCH-018 SafetyTTL bounds the age of an entry", func(t *testing.T) {
		c.Set("old", "x")
		now = now.Add(59 * time.Second)
		if _, ok := c.Get("old"); !ok {
			t.Fatal("expired early")
		}
		now = now.Add(2 * time.Second)
		if _, ok := c.Get("old"); ok {
			t.Fatal("outlived SafetyTTL")
		}
	})
	t.Run("R-ARCH-018 a byte-bounded cache evicts by weight", func(t *testing.T) {
		b := Register[string, []byte](r, "bytes", Options[string, []byte]{MaxBytes: 10, SafetyTTL: time.Minute, Weigh: func(_ string, v []byte) int64 { return int64(len(v)) }})
		b.Set("a", make([]byte, 6))
		b.Set("b", make([]byte, 6))
		if _, ok := b.Get("a"); ok {
			t.Error("a stayed although 12 > 10 bytes")
		}
		b.Set("huge", make([]byte, 11))
		if _, ok := b.Get("huge"); ok {
			t.Error("an entry larger than the cache was stored")
		}
		if st := r.Stats(); len(st) == 0 {
			t.Error("no stats")
		}
	})
}

func TestFillRule(t *testing.T) {
	ctx := context.Background()
	load := func(v string) func(context.Context) (string, error) {
		return func(context.Context) (string, error) { return v, nil }
	}
	t.Run("R-ARCH-018 a fill whose area advanced during the query is not cached", func(t *testing.T) {
		r, ep, _ := newRegistry(t)
		c := Register[string, string](r, "tokens", Options[string, string]{MaxEntries: 10, SafetyTTL: time.Minute, Areas: []string{"auth"}})
		got, err := c.Load(ctx, "k", func(context.Context) (string, error) {
			ep.set("auth", 1) // the poll applied a change while the query ran
			return "stale", nil
		})
		if err != nil || got != "stale" {
			t.Fatalf("Load = %q, %v: the value must still serve the current request", got, err)
		}
		if _, ok := c.Get("k"); ok {
			t.Fatal("the stale value was cached")
		}
		if got, _ := c.Load(ctx, "k", load("fresh")); got != "fresh" {
			t.Fatalf("second Load = %q", got)
		}
		if v, ok := c.Get("k"); !ok || v != "fresh" {
			t.Fatalf("fresh value not cached: %q %v", v, ok)
		}
	})
	t.Run("R-ARCH-018 a fill is cached when no epoch moved", func(t *testing.T) {
		r, _, _ := newRegistry(t)
		c := Register[string, string](r, "ok", Options[string, string]{MaxEntries: 10, SafetyTTL: time.Minute, Areas: []string{"auth", "authz"}})
		c.Load(ctx, "k", load("v"))
		if v, ok := c.Get("k"); !ok || v != "v" {
			t.Fatal("not cached")
		}
	})
	t.Run("R-ARCH-018 an early eviction during the fill stops the insert", func(t *testing.T) {
		r, _, _ := newRegistry(t)
		c := Register[string, string](r, "evict", Options[string, string]{MaxEntries: 10, SafetyTTL: time.Minute, Areas: []string{"auth"}})
		c.Load(ctx, "k", func(context.Context) (string, error) {
			r.ApplyChange("auth", 1, json.RawMessage(`{}`)) // a Valkey message: the epoch does not move
			return "stale", nil
		})
		if _, ok := c.Get("k"); ok {
			t.Fatal("a value read before an eviction was inserted after it")
		}
	})
	t.Run("R-ARCH-018 a loader error caches nothing", func(t *testing.T) {
		r, _, _ := newRegistry(t)
		c := Register[string, string](r, "err", Options[string, string]{MaxEntries: 10, SafetyTTL: time.Minute})
		if _, err := c.Load(ctx, "k", func(context.Context) (string, error) { return "", context.DeadlineExceeded }); err == nil {
			t.Fatal("no error")
		}
		if c.Len() != 0 {
			t.Fatal("cached an error")
		}
	})
}

func TestChangesAndFlush(t *testing.T) {
	t.Run("R-ARCH-018 a cache without OnChange is flushed by every change of its area", func(t *testing.T) {
		r, _, reg := newRegistry(t)
		var flushed atomic.Value
		c := Register[string, int](r, "coarse", Options[string, int]{
			MaxEntries: 10, SafetyTTL: time.Minute, Areas: []string{"cache"},
			OnFlush: func(reason string) { flushed.Store(reason) },
		})
		other := Register[string, int](r, "other", Options[string, int]{MaxEntries: 10, SafetyTTL: time.Minute, Areas: []string{"auth"}})
		c.Set("a", 1)
		other.Set("a", 1)
		r.ApplyChange("cache", 1, json.RawMessage(`{"x":1}`))
		if c.Len() != 0 || flushed.Load() != ReasonChange {
			t.Fatalf("coarse cache: len=%d reason=%v", c.Len(), flushed.Load())
		}
		if other.Len() != 1 {
			t.Fatal("a change of another area flushed a cache")
		}
		if n := counter(t, reg, "trove_cache_flushes_total", "cache", "coarse", "reason", "change"); n != 1 {
			t.Fatalf("flush counter = %v", n)
		}
	})
	t.Run("R-ARCH-018 OnChange evicts selectively", func(t *testing.T) {
		r, _, _ := newRegistry(t)
		c := Register[string, int](r, "selective", Options[string, int]{
			MaxEntries: 10, SafetyTTL: time.Minute, Areas: []string{"auth"},
			OnChange: func(c *Cache[string, int], _ string, _ int, p json.RawMessage) {
				var v struct{ Token string }
				_ = json.Unmarshal(p, &v)
				c.Invalidate(v.Token)
			},
		})
		c.Set("t1", 1)
		c.Set("t2", 2)
		r.ApplyChange("auth", 1, json.RawMessage(`{"token":"t1"}`))
		if _, ok := c.Get("t1"); ok {
			t.Error("t1 stayed")
		}
		if _, ok := c.Get("t2"); !ok {
			t.Error("t2 was evicted")
		}
	})
	t.Run("R-ARCH-018 FlushArea reports its reason", func(t *testing.T) {
		r, _, reg := newRegistry(t)
		c := Register[string, int](r, "gap", Options[string, int]{MaxEntries: 10, SafetyTTL: time.Minute, Areas: []string{"cache"}})
		c.Set("a", 1)
		r.FlushArea("cache", "epoch_regressed")
		if c.Len() != 0 || counter(t, reg, "trove_cache_flushes_total", "cache", "gap", "reason", "epoch_regressed") != 1 {
			t.Fatal("not flushed with its reason")
		}
	})
	t.Run("R-ARCH-018 metrics follow the naming rules of R-OPS-007", func(t *testing.T) {
		r, _, reg := newRegistry(t)
		c := Register[string, int](r, "metrics", Options[string, int]{MaxEntries: 10, SafetyTTL: time.Minute})
		c.Get("x")
		c.Set("x", 1)
		c.Get("x")
		fams, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range observability.CheckFamilies(fams) {
			t.Error(e)
		}
		if counter(t, reg, "trove_cache_requests_total", "cache", "metrics", "result", "hit") != 1 {
			t.Error("hit not counted")
		}
	})
}

// counter reads one series of a counter family by label pairs.
func counter(t *testing.T, reg *prometheus.Registry, name string, kv ...string) float64 {
	t.Helper()
	fams, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			if matches(m, kv) {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func matches(m *dto.Metric, kv []string) bool {
	for i := 0; i < len(kv); i += 2 {
		found := false
		for _, l := range m.GetLabel() {
			if l.GetName() == kv[i] && l.GetValue() == kv[i+1] {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func TestConcurrentUse(t *testing.T) {
	r, _, _ := newRegistry(t)
	c := Register[int, int](r, "race", Options[int, int]{MaxEntries: 50, SafetyTTL: time.Minute, Areas: []string{"cache"}})
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 500 {
				switch (i + g) % 4 {
				case 0:
					c.Set(i%60, i)
				case 1:
					c.Get(i % 60)
				case 2:
					c.Load(context.Background(), i%60, func(context.Context) (int, error) { return i, nil })
				default:
					r.ApplyChange("cache", 1, nil)
				}
			}
		}()
	}
	wg.Wait()
	if c.Len() > 50 {
		t.Fatalf("Len = %d beyond the bound", c.Len())
	}
}
