//go:build integration

package valkey

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

var (
	kvFamily   = cluster.RegisterKeyFamily("itest:kv", 1, 60*time.Second)
	ctrFamily  = cluster.RegisterKeyFamily("itest:ctr", 1, 90*time.Second)
	chanFamily = cluster.RegisterChannelFamily("itest:chan", 1)
)

// exerciseAPI runs every write method of the client once, as the least-privilege
// user. It is shared by the stack run and the throwaway-container runs.
func exerciseAPI(ctx context.Context, t *testing.T, c *client) {
	t.Helper()
	k, err := c.Key(kvFamily, "api", randHex(4))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Set(ctx, k, "v1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, found, err := c.Get(ctx, k); err != nil || !found || got != "v1" {
		t.Fatalf("Get = %q, %v, %v", got, found, err)
	}
	if ok, err := c.SetNX(ctx, k, "other"); err != nil || ok {
		t.Fatalf("SetNX on an existing key = %v, %v", ok, err)
	}
	if ok, err := c.SetIfEq(ctx, k, "wrong", "v2"); err != nil || ok {
		t.Fatalf("SetIfEq with a wrong old value = %v, %v", ok, err)
	}
	if ok, err := c.SetIfEq(ctx, k, "v1", "v2"); err != nil || !ok {
		t.Fatalf("SetIfEq = %v, %v", ok, err)
	}
	if ok, err := c.DelIfEq(ctx, k, "v1"); err != nil || ok {
		t.Fatalf("DelIfEq with a stale value = %v, %v", ok, err)
	}
	if ok, err := c.DelIfEq(ctx, k, "v2"); err != nil || !ok {
		t.Fatalf("DelIfEq = %v, %v", ok, err)
	}
	if _, found, err := c.Get(ctx, k); err != nil || found {
		t.Fatalf("Get after DelIfEq: found=%v err=%v", found, err)
	}
	if ok, err := c.SetNX(ctx, k, "fresh"); err != nil || !ok {
		t.Fatalf("SetNX on a missing key = %v, %v", ok, err)
	}
	ctr, _ := c.Key(ctrFamily, "api", randHex(4))
	for want := int64(1); want <= 3; want++ {
		if n, err := c.Incr(ctx, ctr); err != nil || n != want {
			t.Fatalf("Incr = %d, %v, want %d", n, err, want)
		}
	}
	ch, _ := c.Channel(chanFamily, "api")
	if err := c.Publish(ctx, ch, "hello"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestKeys(t *testing.T) {
	v := testenv.ValkeyConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := newClient(t, stackConfig(t, 2*time.Second))
	waitAvailable(t, c)
	admin := adminAt(t, v, c.Status().Primary)

	pttl := func(k cluster.Key) time.Duration {
		n, err := admin.Do(ctx, admin.B().Pttl().Key(k.String()).Build()).AsInt64()
		if err != nil {
			t.Fatal(err)
		}
		return time.Duration(n) * time.Millisecond
	}

	t.Run("R-ARCH-030 Set, SetNX and SetIfEq leave a key with the expiry of its family", func(t *testing.T) {
		k, _ := c.Key(kvFamily, "ttl", randHex(4))
		if err := c.Set(ctx, k, "a"); err != nil {
			t.Fatal(err)
		}
		if d := pttl(k); d <= 0 || d > 60*time.Second {
			t.Fatalf("PTTL after Set = %s", d)
		}
		k2, _ := c.KeyTTL(kvFamily, 20*time.Second, "ttl", randHex(4))
		if ok, err := c.SetNX(ctx, k2, "a"); err != nil || !ok {
			t.Fatal(err)
		}
		if d := pttl(k2); d <= 0 || d > 20*time.Second {
			t.Fatalf("PTTL after SetNX with a 20 s expiry = %s", d)
		}
		time.Sleep(1500 * time.Millisecond)
		before := pttl(k2)
		if ok, err := c.SetIfEq(ctx, k2, "a", "b"); err != nil || !ok {
			t.Fatal(err)
		}
		if after := pttl(k2); after <= before {
			t.Fatalf("SetIfEq did not renew the expiry: %s then %s", before, after)
		}
	})
	t.Run("R-ARCH-034 Incr sets the expiry once and traffic does not extend it", func(t *testing.T) {
		k, _ := c.KeyTTL(ctrFamily, 30*time.Second, "win", randHex(4))
		if _, err := c.Incr(ctx, k); err != nil {
			t.Fatal(err)
		}
		first := pttl(k)
		if first <= 0 || first > 30*time.Second {
			t.Fatalf("PTTL after the first Incr = %s", first)
		}
		time.Sleep(1500 * time.Millisecond)
		if _, err := c.Incr(ctx, k); err != nil {
			t.Fatal(err)
		}
		if second := pttl(k); second >= first {
			t.Fatalf("a later Incr extended the expiry: %s then %s", first, second)
		}
	})
	t.Run("R-ARCH-030 the health probe key has the prefix, the keyspace and a TTL of at most 30 s", func(t *testing.T) {
		pattern := runPrefix + "*:probe:v1:" + c.Instance
		eventually(t, "the probe key", 10*time.Second, func() bool {
			keys, err := admin.Do(ctx, admin.B().Keys().Pattern(pattern).Build()).AsStrSlice()
			return err == nil && len(keys) == 1
		})
		keys, _ := admin.Do(ctx, admin.B().Keys().Pattern(pattern).Build()).AsStrSlice()
		n, err := admin.Do(ctx, admin.B().Pttl().Key(keys[0]).Build()).AsInt64()
		if err != nil || n <= 0 || n > 30000 {
			t.Fatalf("PTTL of %s = %d, %v", keys[0], n, err)
		}
		if !strings.HasPrefix(keys[0], runPrefix) {
			t.Fatalf("%s does not start with %s", keys[0], runPrefix)
		}
	})
	t.Run("R-ARCH-030 keys of two keyspaces do not meet", func(t *testing.T) {
		other := newClient(t, stackConfig(t, 2*time.Second))
		waitAvailable(t, other)
		k1, _ := c.Key(kvFamily, "shared")
		k2, _ := other.Key(kvFamily, "shared")
		if k1.String() == k2.String() {
			t.Fatal("two keyspaces formed the same key")
		}
		if err := c.Set(ctx, k1, "mine"); err != nil {
			t.Fatal(err)
		}
		if _, found, err := other.Get(ctx, k2); err != nil || found {
			t.Fatalf("a key of another keyspace is visible: found=%v err=%v", found, err)
		}
	})
}
