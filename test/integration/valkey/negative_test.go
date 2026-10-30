//go:build integration

package valkey

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func negativeKeyName(ks string, repoID int64, f cluster.FlightKey) string {
	return runPrefix + strings.ToLower(ks[:12]) + ":neg:v1:" + strconv.FormatInt(repoID, 10) + ":" + f.String()
}

// negativeLookups reads trove_valkey_cache_lookups_total{cache="negative",result}.
func negativeLookups(t *testing.T, reg *prometheus.Registry, result string) float64 {
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
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["cache"] == "negative" && labels["result"] == result {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func TestNegativeStore(t *testing.T) {
	v := testenv.ValkeyConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sent := time.Now().Truncate(time.Millisecond)

	t.Run("R-REPO-028 R-ARCH-030 an entry written on one replica is found on another, as {s, c} with the repository's period as its expiry", func(t *testing.T) {
		ks := randHex(8)
		w, r := newClientIn(t, stackConfig(t, 2*time.Second), ks), newClientIn(t, stackConfig(t, 2*time.Second), ks)
		waitAvailable(t, w)
		waitAvailable(t, r)
		admin := adminAt(t, v, w.Status().Primary)
		f := cluster.NewFlightKey(7, "/does-not-exist-4711", "full")
		key := negativeKeyName(ks, 7, f)

		if err := cluster.NewNegativeStore(w.Valkey).Record(ctx, 7, f, cluster.NegativeEntry{Status: 404, SentAt: sent}, 1800*time.Second); err != nil {
			t.Fatal(err)
		}
		val, found := getKey(ctx, t, admin, key)
		if want := `{"s":404,"c":` + strconv.FormatInt(sent.UnixMilli(), 10) + `}`; !found || val != want {
			t.Fatalf("%s = %q, %v, want %s", key, val, found, want)
		}
		if d := pttl(ctx, t, admin, key); d <= 1790*time.Second || d > 1800*time.Second {
			t.Fatalf("PTTL = %s, want the 1800 s of notFoundTtlSeconds", d)
		}
		e, found, err := cluster.NewNegativeStore(r.Valkey).Lookup(ctx, 7, f)
		if err != nil || !found || e.Status != 404 || !e.SentAt.Equal(sent) {
			t.Fatalf("Lookup on the other replica = %+v, %v, %v", e, found, err)
		}
		if got := negativeLookups(t, r.Reg, "hit"); got != 1 {
			t.Fatalf("trove_valkey_cache_lookups_total{cache=negative,result=hit} = %v, want 1", got)
		}
		if _, found, err := cluster.NewNegativeStore(r.Valkey).Lookup(ctx, 8, f); err != nil || found {
			t.Fatalf("another repository found %v (%v)", found, err)
		}
		if got := negativeLookups(t, r.Reg, "miss"); got != 1 {
			t.Fatalf("result=miss = %v, want 1", got)
		}
	})

	t.Run("R-ARCH-030 R-REPO-028 an entry expires with its period and a repository with notFoundTtlSeconds 0 stores none", func(t *testing.T) {
		ks := randHex(8)
		c := newClientIn(t, stackConfig(t, 2*time.Second), ks)
		waitAvailable(t, c)
		store := cluster.NewNegativeStore(c.Valkey)
		admin := adminAt(t, v, c.Status().Primary)
		f := cluster.NewFlightKey(7, "/short-lived", "")
		if err := store.Record(ctx, 7, f, cluster.NegativeEntry{Status: 410, SentAt: sent}, time.Second); err != nil {
			t.Fatal(err)
		}
		if _, found, _ := store.Lookup(ctx, 7, f); !found {
			t.Fatal("not found right after Record")
		}
		eventually(t, "the entry to expire", 5*time.Second, func() bool {
			_, found, err := store.Lookup(ctx, 7, f)
			return err == nil && !found
		})

		off := cluster.NewFlightKey(7, "/off", "")
		if err := store.Record(ctx, 7, off, cluster.NegativeEntry{Status: 404, SentAt: sent}, 0); err != nil {
			t.Fatal(err)
		}
		if _, found := getKey(ctx, t, admin, negativeKeyName(ks, 7, off)); found {
			t.Fatal("an entry was stored for a period of 0")
		}
	})

	t.Run("R-REPO-028 R-ARCH-030 a malformed entry is ignored and deleted by the least-privilege user", func(t *testing.T) {
		ks := randHex(8)
		c := newClientIn(t, stackConfig(t, 2*time.Second), ks)
		waitAvailable(t, c)
		store := cluster.NewNegativeStore(c.Valkey)
		admin := adminAt(t, v, c.Status().Primary)
		for i, raw := range []string{
			`junk`, `{}`, `{"s":404}`, `{"s":500,"c":1790000000123}`, `{"s":404,"c":-1}`,
			`{"s":404,"c":1790000000123,"x":true}`, `{"s":404,"c":1790000000123}{}`, `[404,1790000000123]`,
		} {
			f := cluster.NewFlightKey(7, "/malformed", strconv.Itoa(i))
			key := negativeKeyName(ks, 7, f)
			if err := admin.Do(ctx, admin.B().Set().Key(key).Value(raw).ExSeconds(600).Build()).Error(); err != nil {
				t.Fatal(err)
			}
			e, found, err := store.Lookup(ctx, 7, f)
			if err != nil || found {
				t.Errorf("Lookup(%q) = %+v, %v, %v, want not found", raw, e, found, err)
			}
			if _, still := getKey(ctx, t, admin, key); still {
				t.Errorf("the malformed entry %q was not deleted", raw)
			}
		}
		if got := negativeLookups(t, c.Reg, "rejected"); got != 8 {
			t.Fatalf("result=rejected = %v, want 8", got)
		}
	})

	t.Run("R-REPO-028 R-ARCH-030 an entry the installation of another keyspace wrote is not read", func(t *testing.T) {
		mine, theirs := newClientIn(t, stackConfig(t, 2*time.Second), randHex(8)), newClientIn(t, stackConfig(t, 2*time.Second), randHex(8))
		waitAvailable(t, mine)
		waitAvailable(t, theirs)
		f := cluster.NewFlightKey(7, "/shared-name", "")
		if err := cluster.NewNegativeStore(theirs.Valkey).Record(ctx, 7, f, cluster.NegativeEntry{Status: 404, SentAt: sent}, time.Minute); err != nil {
			t.Fatal(err)
		}
		if _, found, err := cluster.NewNegativeStore(mine.Valkey).Lookup(ctx, 7, f); err != nil || found {
			t.Fatalf("Lookup in another keyspace = %v, %v", found, err)
		}
	})

	t.Run("R-REPO-028 R-ARCH-031 with the breaker open Lookup and Record return ErrUnavailable at once and count the fallback", func(t *testing.T) {
		c := newClientIn(t, stackConfig(t, 2*time.Second), randHex(8))
		waitAvailable(t, c)
		store := cluster.NewNegativeStore(c.Valkey)
		f := cluster.NewFlightKey(7, "/down", "")
		openBreaker(ctx, t, v, c, func() {
			_ = store.Record(ctx, 7, f, cluster.NegativeEntry{Status: 404, SentAt: sent}, time.Minute)
		})
		start := time.Now()
		if _, _, err := store.Lookup(ctx, 7, f); !errors.Is(err, cluster.ErrUnavailable) {
			t.Fatalf("Lookup = %v", err)
		}
		if err := store.Record(ctx, 7, f, cluster.NegativeEntry{Status: 404, SentAt: sent}, time.Minute); !errors.Is(err, cluster.ErrUnavailable) {
			t.Fatalf("Record = %v", err)
		}
		if took := time.Since(start); took > 50*time.Millisecond {
			t.Fatalf("the calls took %s with the breaker open", took)
		}
		if got := metric(t, c.Reg, "trove_valkey_degraded_total", cluster.MechanismNegativeCache); got < 2 {
			t.Fatalf("trove_valkey_degraded_total{mechanism=negative_cache} = %v", got)
		}
		if got := negativeLookups(t, c.Reg, "unavailable"); got < 1 {
			t.Fatalf("result=unavailable = %v", got)
		}
	})
}
