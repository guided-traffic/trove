//go:build integration

package valkey

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

const failoverRefresh = 2 * time.Second

// subscribeDenied creates, on every Sentinel, an ACL user that may ask for the
// primary but may not SUBSCRIBE, so the client never hears +switch-master and
// only the periodic refresh can move it. The user is removed when the test ends.
func subscribeDenied(t *testing.T, v testenv.Valkey) (userFile, passFile string) {
	t.Helper()
	ctx := context.Background()
	for _, s := range v.Sentinels {
		c := sentinelAdmin(t, v, s)
		err := c.Do(ctx, c.B().Arbitrary("ACL", "SETUSER", "nosub", "on", ">nosub-pw", "resetchannels",
			"+sentinel|get-master-addr-by-name", "+sentinel|sentinels", "+sentinel|replicas", "+ping", "+hello", "+client").Build()).Error()
		if err != nil {
			t.Fatalf("ACL SETUSER on Sentinel %s: %v", s, err)
		}
	}
	t.Cleanup(func() {
		for _, s := range v.Sentinels {
			c := sentinelAdmin(t, v, s)
			_ = c.Do(ctx, c.B().Arbitrary("ACL", "DELUSER", "nosub").Build()).Error()
		}
	})
	// the premise of the test, not an assumption: this user cannot subscribe
	nosub := direct(t, v.Sentinels[0], "nosub", "nosub-pw", stackTLS(t, v))
	if err := nosub.Do(ctx, nosub.B().Subscribe().Channel("+switch-master").Build()).Error(); err == nil || !strings.Contains(err.Error(), "NOPERM") {
		t.Fatalf("SUBSCRIBE as the restricted Sentinel user = %v, want NOPERM", err)
	}
	return writeFile(t, "sentinel-username", "nosub\n"), writeFile(t, "sentinel-password", "nosub-pw\n")
}

// sentinelSubscriptions sums the channels this client has subscribed to on the Sentinels.
func sentinelSubscriptions(ctx context.Context, t *testing.T, v testenv.Valkey, c *client) (subscribed int) {
	t.Helper()
	for _, s := range v.Sentinels {
		for _, m := range named(clientList(ctx, t, sentinelAdmin(t, v, s)), c.Instance) {
			if m["name"] == "trove:"+c.Instance+":sentinel" && m["sub"] != "0" && m["sub"] != "" {
				subscribed++
			}
		}
	}
	return subscribed
}

func TestFailover(t *testing.T) {
	v := testenv.ValkeyConfig(t)

	for _, blocked := range []bool{false, true} {
		name := "R-ARCH-031 the client moves to the new primary within refresh_interval + 1 s"
		if blocked {
			name += " without +switch-master (the poll is the bound)"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			cfg := stackConfig(t, failoverRefresh)
			if blocked {
				cfg.Sentinel.UsernameFile, cfg.Sentinel.PasswordFile = subscribeDenied(t, v)
			}
			t.Cleanup(func() { restoreLayout(t, v) })

			c := newClient(t, cfg)
			waitAvailable(t, c)
			if got := sentinelSubscriptions(ctx, t, v, c); blocked && got != 0 {
				t.Fatalf("%d Sentinel subscriptions although SUBSCRIBE is denied", got)
			} else if !blocked && got == 0 {
				t.Fatal("no Sentinel subscription: the early trigger is not in place in the unblocked run")
			}

			ch, _ := c.Channel(chanFamily, "failover")
			var in inbox
			stop, err := c.Subscribe(ctx, ch, in.handle)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			var resubscribed atomic.Bool
			c.OnSubscribed(func(reconnect bool) {
				if reconnect {
					resubscribed.Store(true)
				}
			})
			var change atomic.Pointer[cluster.PrimaryChange]
			c.OnPrimaryChange(func(pc cluster.PrimaryChange) { change.Store(&pc) })
			eventually(t, "the subscription", 10*time.Second, func() bool { return c.Status().Subscribed })

			bound := failoverRefresh + time.Second
			k, _ := c.Key(kvFamily, "failover", randHex(4))

			oldAddr, newAddr, announced := failover(ctx, t, v)
			t.Logf("primary %s -> %s (blocked switch-master: %v)", oldAddr, newAddr, blocked)

			// Writes from the announcement on, until one is on the new primary. A
			// write that succeeds earlier went to the old primary, which stays
			// writable until the Sentinels have reconfigured it.
			admin := adminAt(t, v, newAddr)
			var landed bool
			var lastErr error
			for i := 0; time.Since(announced) < bound+5*time.Second && !landed; i++ {
				val := "after-" + randHex(2)
				if lastErr = c.Set(ctx, k, val); lastErr == nil {
					got, err := admin.Do(ctx, admin.B().Get().Key(k.String()).Build()).ToString()
					landed = err == nil && got == val
				}
				if !landed {
					time.Sleep(25 * time.Millisecond)
				}
			}
			took := time.Since(announced)
			if !landed {
				t.Fatalf("no write reached the new primary %s after %s (last error %v)", newAddr, took, lastErr)
			}
			t.Logf("first write on the new primary %s after the Sentinels announced it (bound %s)", took.Round(time.Millisecond), bound)
			if took > bound {
				t.Errorf("the client needed %s to reach the new primary, bound is %s", took, bound)
			}
			eventually(t, "the status to name the new primary", 2*time.Second, func() bool { return c.Status().Primary == newAddr })

			// Pub/Sub: the subscription is on the new primary within the same bound
			eventually(t, "the Pub/Sub connection on the new primary", bound-took+time.Second, func() bool {
				return numSub(ctx, t, v, newAddr, ch.String()) == 1
			})
			if took := time.Since(announced); took > bound+time.Second {
				t.Errorf("the subscription moved after %s, bound is %s", took, bound)
			}
			named := named(clientList(ctx, t, admin), c.Instance)
			var pubsubConns int
			for _, m := range named {
				if m["name"] == "trove:"+c.Instance+":pubsub" {
					pubsubConns++
				}
			}
			if pubsubConns != 1 || len(named) > 6 {
				t.Errorf("on the new primary: %d pubsub connections, %d in all: %v", pubsubConns, len(named), named)
			}
			// a message published on the new primary arrives
			eventually(t, "a message after the failover", 5*time.Second, func() bool {
				_ = admin.Do(ctx, admin.B().Publish().Channel(ch.String()).Message("after-failover").Build()).Error()
				return in.has("after-failover")
			})
			eventually(t, "OnSubscribed(reconnect)", 5*time.Second, resubscribed.Load)
			if pc := change.Load(); pc == nil || pc.Old != oldAddr || pc.New != newAddr {
				t.Errorf("OnPrimaryChange = %+v, want %s -> %s", pc, oldAddr, newAddr)
			}
			if got := metric(t, c.Reg, "trove_valkey_primary_changes_total", ""); got != 1 {
				t.Errorf("trove_valkey_primary_changes_total = %v, want 1", got)
			}
			eventually(t, "the client to be available", 10*time.Second, c.Available)

			// and back: the client follows the restored layout too
			restoreLayout(t, v)
			eventually(t, "the client to follow the restored layout", 15*time.Second, func() bool { return c.Status().Primary == v.Nodes[0] })
			if err := c.Set(ctx, k, "restored"); err != nil {
				t.Fatalf("Set after the restore: %v", err)
			}
		})
	}
}
