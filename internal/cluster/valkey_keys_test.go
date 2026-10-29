package cluster

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
)

// bareValkey is a client that never connects: enough to form keys.
func bareValkey(prefix, ks string) *Valkey {
	v := &Valkey{cfg: config.Valkey{KeyPrefix: prefix}}
	if ks != "" {
		s, _ := normalizeKeyspace(ks)
		v.keyspace.Store(&s)
	}
	return v
}

func TestKeyRegistry(t *testing.T) {
	fam := RegisterKeyFamily("testkeys:unit", 1, 2*time.Minute)
	ch := RegisterChannelFamily("testkeys:chan", 1)

	t.Run("R-ARCH-030 key = prefix + keyspace + mechanism + version + parts", func(t *testing.T) {
		v := bareValkey("trove:", "0123456789ABCDEF0123")
		k, err := v.Key(fam, "abc", "d-e_f")
		if err != nil {
			t.Fatal(err)
		}
		if want := "trove:0123456789ab:testkeys:unit:v1:abc:d-e_f"; k.String() != want {
			t.Fatalf("key = %q, want %q", k, want)
		}
		c, err := v.Channel(ch)
		if err != nil || c.String() != "trove:0123456789ab:testkeys:chan:v1" {
			t.Fatalf("channel = %q, %v", c, err)
		}
	})
	t.Run("R-ARCH-030 another prefix separates installations", func(t *testing.T) {
		k, err := bareValkey("tenant-b:", "0123456789ab").Key(fam, "x")
		if err != nil || !strings.HasPrefix(k.String(), "tenant-b:0123456789ab:") {
			t.Fatalf("key = %q, %v", k, err)
		}
	})
	t.Run("R-ARCH-030 every key carries an expiry, capped by its family", func(t *testing.T) {
		v := bareValkey("trove:", "0123456789ab")
		k, _ := v.Key(fam, "x")
		if k.TTL() != 2*time.Minute {
			t.Fatalf("default ttl = %s", k.TTL())
		}
		k2, err := v.KeyTTL(fam, 30*time.Second, "x")
		if err != nil || k2.TTL() != 30*time.Second {
			t.Fatalf("KeyTTL = %v, %v", k2.TTL(), err)
		}
		for _, bad := range []time.Duration{0, -time.Second, 3 * time.Minute} {
			if _, err := v.KeyTTL(fam, bad, "x"); err == nil {
				t.Errorf("ttl %s accepted", bad)
			}
		}
	})
	t.Run("R-ARCH-030 a family without an expiry cannot be registered", func(t *testing.T) {
		mustPanic(t, func() { RegisterKeyFamily("testkeys:nottl", 1, 0) })
		mustPanic(t, func() { RegisterKeyFamily("testkeys:negttl", 1, -time.Second) })
	})
	t.Run("R-ARCH-030 the zero Key cannot be written", func(t *testing.T) {
		v := bareValkey("trove:", "0123456789ab")
		if err := v.Set(t.Context(), Key{}, "x"); err == nil || !strings.Contains(err.Error(), "invalid key") {
			t.Fatalf("Set(zero Key) = %v", err)
		}
		if _, err := v.Incr(t.Context(), Key{}); err == nil {
			t.Fatal("Incr(zero Key) accepted")
		}
		if _, err := v.SetNX(t.Context(), Key{name: "trove:x"}, "x"); err == nil {
			t.Fatal("SetNX(key without expiry) accepted")
		}
	})
	t.Run("R-ARCH-030 parts are identifiers or hashes, never raw names", func(t *testing.T) {
		v := bareValkey("trove:", "0123456789ab")
		for _, bad := range []string{"", "a:b", "left-pad/1.0", "a b", "*", "pkg@1", strings.Repeat("a", 129), "näme"} {
			if _, err := v.Key(fam, bad); err == nil {
				t.Errorf("part %q accepted", bad)
			}
		}
	})
	t.Run("R-ARCH-030 no key before the keyspace is known", func(t *testing.T) {
		if _, err := bareValkey("trove:", "").Key(fam, "x"); !errors.Is(err, ErrNoKeyspace) {
			t.Fatalf("err = %v, want ErrNoKeyspace", err)
		}
	})
	t.Run("R-ARCH-030 the keyspace is 12 hex digits and set once", func(t *testing.T) {
		v := bareValkey("trove:", "")
		if err := v.SetKeyspaceID("short"); err == nil {
			t.Fatal("short keyspace accepted")
		}
		if err := v.SetKeyspaceID("0123456789ab"); err != nil {
			t.Fatal(err)
		}
		if err := v.SetKeyspaceID("0123456789abffff"); err != nil {
			t.Fatalf("same first 12 digits must be accepted again: %v", err)
		}
		if err := v.SetKeyspaceID("ba9876543210"); err == nil {
			t.Fatal("a second, different keyspace accepted")
		}
	})
	t.Run("R-ARCH-030 mechanism names are checked and unique", func(t *testing.T) {
		mustPanic(t, func() { RegisterKeyFamily("Bad Name", 1, time.Second) })
		mustPanic(t, func() { RegisterKeyFamily("testkeys:v0", 0, time.Second) })
		mustPanic(t, func() { RegisterKeyFamily("testkeys:unit", 1, time.Second) })
		mustPanic(t, func() { RegisterChannelFamily("testkeys:unit", 1) })
		RegisterKeyFamily("testkeys:unit", 2, time.Second) // a new encoding version is a new family
	})
	t.Run("R-ARCH-030 the registry lists families with their expiry", func(t *testing.T) {
		var found bool
		for _, f := range Families() {
			if f.Mechanism == "probe" && f.Version == 1 && f.MaxTTL == 30*time.Second {
				found = true
			}
		}
		if !found {
			t.Fatal("the probe family (30 s) is not listed")
		}
	})
	t.Run("R-ARCH-030 values above 1 KiB are refused", func(t *testing.T) {
		v := bareValkey("trove:", "0123456789ab")
		k, _ := v.Key(fam, "x")
		big := strings.Repeat("x", MaxValueSize+1)
		if err := v.Set(t.Context(), k, big); err == nil || !strings.Contains(err.Error(), "1 KiB") {
			t.Fatalf("Set(big) = %v", err)
		}
		c, _ := v.Channel(ch)
		if err := v.Publish(t.Context(), c, big); err == nil {
			t.Fatal("Publish(big) accepted")
		}
	})
}

func mustPanic(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Error("expected a panic")
		}
	}()
	f()
}
