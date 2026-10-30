package cluster

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

type negativeFixture struct {
	srv   *memServer
	v     *Valkey
	reg   *prometheus.Registry
	store NegativeStore
}

func newNegativeFixture(t *testing.T) *negativeFixture {
	t.Helper()
	srv := newMemServer(t)
	cfg := standaloneCfg(srv.addr())
	cfg.CommandTimeout = time.Second
	v, reg := newTestValkey(t, cfg)
	eventually(t, "availability", 5*time.Second, v.Available)
	return &negativeFixture{srv: srv, v: v, reg: reg, store: NewNegativeStore(v)}
}

func negKeyName(repoID int64, f FlightKey) string {
	return "trove:" + testKS + ":neg:v1:" + strconv.FormatInt(repoID, 10) + ":" + f.String()
}

// lookups reads trove_valkey_cache_lookups_total{cache="negative",result=result}.
func lookups(t *testing.T, reg *prometheus.Registry, result string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "trove_valkey_cache_lookups_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			if labelValue(m, "cache") == "negative" && labelValue(m, "result") == result {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func labelValue(m *dto.Metric, name string) string {
	for _, l := range m.GetLabel() {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}

var testNegFlight = NewFlightKey(7, "/does-not-exist", "full")

func TestNegativeStore(t *testing.T) {
	sent := time.UnixMilli(1_790_000_000_123)

	t.Run("R-ARCH-030 R-REPO-028 Record writes {s, c} under neg:v1:<repo id>:<f> with the repository's period as its expiry", func(t *testing.T) {
		fx := newNegativeFixture(t)
		if err := fx.store.Record(t.Context(), 7, testNegFlight, NegativeEntry{Status: 404, SentAt: sent}, 1800*time.Second); err != nil {
			t.Fatal(err)
		}
		val, found := fx.srv.value(negKeyName(7, testNegFlight))
		if !found || val != `{"s":404,"c":1790000000123}` {
			t.Fatalf("value = %q, %v", val, found)
		}
		var set []string
		for _, c := range fx.srv.commands("SET") {
			if c[1] == negKeyName(7, testNegFlight) {
				set = c
			}
		}
		if len(set) != 5 || !strings.EqualFold(set[3], "PX") || set[4] != "1800000" {
			t.Fatalf("SET = %v, want PX 1800000", set)
		}
	})
	t.Run("R-REPO-028 a recorded entry is found, with the status and the time its request was sent", func(t *testing.T) {
		fx := newNegativeFixture(t)
		for _, status := range []int{404, 410} {
			f := NewFlightKey(7, "/p", strconv.Itoa(status))
			if err := fx.store.Record(t.Context(), 7, f, NegativeEntry{Status: status, SentAt: sent}, time.Minute); err != nil {
				t.Fatal(err)
			}
			e, found, err := fx.store.Lookup(t.Context(), 7, f)
			if err != nil || !found || e.Status != status || !e.SentAt.Equal(sent) {
				t.Fatalf("Lookup(%d) = %+v, %v, %v", status, e, found, err)
			}
		}
		if got := lookups(t, fx.reg, "hit"); got != 2 {
			t.Fatalf("lookups{hit} = %v", got)
		}
	})
	t.Run("R-REPO-028 the entry belongs to its repository and its flight", func(t *testing.T) {
		fx := newNegativeFixture(t)
		_ = fx.store.Record(t.Context(), 7, testNegFlight, NegativeEntry{Status: 404, SentAt: sent}, time.Minute)
		if _, found, err := fx.store.Lookup(t.Context(), 8, testNegFlight); err != nil || found {
			t.Errorf("other repository: found=%v err=%v", found, err)
		}
		other := NewFlightKey(7, "/does-not-exist", "abbreviated")
		if _, found, err := fx.store.Lookup(t.Context(), 7, other); err != nil || found {
			t.Errorf("other flight: found=%v err=%v", found, err)
		}
		if got := lookups(t, fx.reg, "miss"); got != 2 {
			t.Fatalf("lookups{miss} = %v", got)
		}
	})
	t.Run("R-ARCH-030 every entry expires: it is gone when the period has passed", func(t *testing.T) {
		fx := newNegativeFixture(t)
		_ = fx.store.Record(t.Context(), 7, testNegFlight, NegativeEntry{Status: 404, SentAt: sent}, 150*time.Millisecond)
		time.Sleep(250 * time.Millisecond)
		if _, found, _ := fx.store.Lookup(t.Context(), 7, testNegFlight); found {
			t.Fatal("found after the period")
		}
	})
	t.Run("R-REPO-028 notFoundTtlSeconds 0 records nothing", func(t *testing.T) {
		fx := newNegativeFixture(t)
		for _, ttl := range []time.Duration{0, -time.Second} {
			if err := fx.store.Record(t.Context(), 7, testNegFlight, NegativeEntry{Status: 404, SentAt: sent}, ttl); err != nil {
				t.Fatal(err)
			}
		}
		if _, found := fx.srv.value(negKeyName(7, testNegFlight)); found {
			t.Fatal("an entry was stored")
		}
	})
	t.Run("R-ARCH-030 the expiry is capped at 24 h plus the clock allowance", func(t *testing.T) {
		fx := newNegativeFixture(t)
		e := NegativeEntry{Status: 404, SentAt: sent}
		if err := fx.store.Record(t.Context(), 7, testNegFlight, e, 24*time.Hour); err != nil {
			t.Fatalf("24 h: %v", err)
		}
		if err := fx.store.Record(t.Context(), 7, testNegFlight, e, MaxNegativeTTL+time.Second); err == nil || errors.Is(err, ErrUnavailable) {
			t.Fatalf("an expiry above the family's maximum: %v", err)
		}
	})
	t.Run("R-REPO-028 only a 404 or 410 with a send time is recorded", func(t *testing.T) {
		fx := newNegativeFixture(t)
		for _, e := range []NegativeEntry{{Status: 200, SentAt: sent}, {Status: 500, SentAt: sent}, {Status: 0, SentAt: sent}, {Status: 404}} {
			if err := fx.store.Record(t.Context(), 7, testNegFlight, e, time.Minute); err == nil || errors.Is(err, ErrUnavailable) {
				t.Errorf("Record(%+v) = %v, want a refusal", e, err)
			}
		}
		if _, found := fx.srv.value(negKeyName(7, testNegFlight)); found {
			t.Fatal("an entry was stored")
		}
	})
	t.Run("R-REPO-028 R-ARCH-030 a malformed entry is ignored and deleted", func(t *testing.T) {
		for _, raw := range []string{
			``, `junk`, `{}`, `null`, `[404,1]`, `{"s":404}`, `{"c":1790000000123}`, `{"s":200,"c":1790000000123}`,
			`{"s":500,"c":1790000000123}`, `{"s":404,"c":0}`, `{"s":404,"c":-5}`, `{"s":"404","c":1}`, `{"s":404.5,"c":1}`,
			`{"s":404,"c":1,"x":1}`, `{"s":404,"c":1}{"s":404,"c":1}`, `{"s":404,"c":1} x`,
		} {
			fx := newNegativeFixture(t)
			fx.srv.plant(negKeyName(7, testNegFlight), raw, time.Minute)
			e, found, err := fx.store.Lookup(t.Context(), 7, testNegFlight)
			if err != nil || found {
				t.Errorf("Lookup(%q) = %+v, %v, %v, want not found", raw, e, found, err)
			}
			if _, still := fx.srv.value(negKeyName(7, testNegFlight)); still {
				t.Errorf("the malformed entry %q was not deleted", raw)
			}
			if got := lookups(t, fx.reg, "rejected"); got != 1 {
				t.Errorf("%q: lookups{rejected} = %v", raw, got)
			}
		}
	})
	t.Run("R-ARCH-030 the deletion removes the value that was read and nothing else", func(t *testing.T) {
		fx := newNegativeFixture(t)
		fx.srv.plant(negKeyName(7, testNegFlight), "junk", time.Minute)
		_, _, _ = fx.store.Lookup(t.Context(), 7, testNegFlight)
		cmds := fx.srv.commands("DELIFEQ")
		if len(cmds) != 1 || cmds[0][1] != negKeyName(7, testNegFlight) || cmds[0][2] != "junk" {
			t.Fatalf("DELIFEQ = %v", cmds)
		}
	})
	t.Run("R-ARCH-030 the key carries ids and a hash, never a name", func(t *testing.T) {
		fx := newNegativeFixture(t)
		_ = fx.store.Record(t.Context(), 7, NewFlightKey(7, "/@scope/does-not-exist", "full"), NegativeEntry{Status: 404, SentAt: sent}, time.Minute)
		fx.srv.mu.Lock()
		defer fx.srv.mu.Unlock()
		for k := range fx.srv.data {
			if strings.Contains(k, "scope") || strings.Contains(k, "does-not-exist") || strings.Contains(k, "full") {
				t.Errorf("key %q names the path", k)
			}
		}
	})
}

func TestNegativeStoreUnavailable(t *testing.T) {
	t.Run("R-REPO-028 without a Valkey client every call is ErrUnavailable", func(t *testing.T) {
		s := NewNegativeStore(nil)
		if _, _, err := s.Lookup(t.Context(), 7, testNegFlight); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Lookup = %v", err)
		}
		if err := s.Record(t.Context(), 7, testNegFlight, NegativeEntry{Status: 404, SentAt: time.Now()}, time.Minute); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Record = %v", err)
		}
	})
	t.Run("R-REPO-028 R-ARCH-031 with the breaker open both calls return ErrUnavailable at once and count the fallback", func(t *testing.T) {
		fx := newNegativeFixture(t)
		for range 10 {
			fx.v.br.allow()
			fx.v.br.done(outcomeFailure)
		}
		start := time.Now()
		if _, _, err := fx.store.Lookup(t.Context(), 7, testNegFlight); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Lookup = %v", err)
		}
		if err := fx.store.Record(t.Context(), 7, testNegFlight, NegativeEntry{Status: 404, SentAt: time.Now()}, time.Minute); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Record = %v", err)
		}
		if took := time.Since(start); took > 50*time.Millisecond {
			t.Fatalf("the calls took %s", took)
		}
		if got := gaugeValue(t, fx.reg, "trove_valkey_degraded_total", MechanismNegativeCache); got != 2 {
			t.Fatalf("trove_valkey_degraded_total{negative_cache} = %v, want 2", got)
		}
		if got := lookups(t, fx.reg, "unavailable"); got != 1 {
			t.Fatalf("lookups{unavailable} = %v, want 1 (Record is not a lookup)", got)
		}
	})
	t.Run("R-ARCH-030 the zero flight key is refused", func(t *testing.T) {
		fx := newNegativeFixture(t)
		if _, _, err := fx.store.Lookup(t.Context(), 7, FlightKey{}); err == nil || errors.Is(err, ErrUnavailable) {
			t.Fatalf("Lookup(zero) = %v", err)
		}
		if err := fx.store.Record(t.Context(), 7, FlightKey{}, NegativeEntry{Status: 404, SentAt: time.Now()}, time.Minute); err == nil || errors.Is(err, ErrUnavailable) {
			t.Fatalf("Record(zero) = %v", err)
		}
	})
}
