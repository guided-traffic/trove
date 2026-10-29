//go:build integration

package valkey

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// inbox collects the messages of a subscription.
type inbox struct {
	mu   sync.Mutex
	msgs []cluster.Message
}

func (b *inbox) handle(m cluster.Message) {
	b.mu.Lock()
	b.msgs = append(b.msgs, m)
	b.mu.Unlock()
}

func (b *inbox) has(payload string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, m := range b.msgs {
		if m.Payload == payload {
			return true
		}
	}
	return false
}

func (b *inbox) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.msgs)
}

// numSub is PUBSUB NUMSUB of a channel on the current primary.
func numSub(ctx context.Context, t *testing.T, v testenv.Valkey, addr, channel string) int64 {
	t.Helper()
	admin := adminAt(t, v, addr)
	arr, err := admin.Do(ctx, admin.B().Arbitrary("PUBSUB", "NUMSUB", channel).Build()).ToArray()
	if err != nil || len(arr) != 2 {
		t.Fatalf("PUBSUB NUMSUB: %v", err)
	}
	n, _ := arr[1].AsInt64()
	return n
}

func TestPubSub(t *testing.T) {
	v := testenv.ValkeyConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ks := randHex(8)

	t.Run("R-ARCH-031 a published message reaches a subscriber on another replica", func(t *testing.T) {
		pub := newClientIn(t, stackConfig(t, 2*time.Second), ks)
		sub := newClientIn(t, stackConfig(t, 2*time.Second), ks)
		waitAvailable(t, pub)
		waitAvailable(t, sub)
		ch, err := sub.Channel(chanFamily, "one")
		if err != nil {
			t.Fatal(err)
		}
		var in inbox
		stop, err := sub.Subscribe(ctx, ch, in.handle)
		if err != nil {
			t.Fatal(err)
		}
		defer stop()
		eventually(t, "the subscription", 10*time.Second, func() bool { return sub.Status().Subscribed })
		if got := metric(t, sub.Reg, "trove_valkey_pubsub_subscribed", ""); got != 1 {
			t.Fatalf("trove_valkey_pubsub_subscribed = %v", got)
		}
		if err := pub.Publish(ctx, ch, "m1"); err != nil {
			t.Fatal(err)
		}
		eventually(t, "delivery", 5*time.Second, func() bool { return in.has("m1") })
	})

	t.Run("R-ARCH-031 the Pub/Sub connection is named trove:<instance>:pubsub and subscribes on the primary", func(t *testing.T) {
		c := newClientIn(t, stackConfig(t, 2*time.Second), ks)
		waitAvailable(t, c)
		ch, _ := c.Channel(chanFamily, "named")
		stop, err := c.Subscribe(ctx, ch, func(cluster.Message) {})
		if err != nil {
			t.Fatal(err)
		}
		defer stop()
		eventually(t, "the subscription", 10*time.Second, func() bool { return numSub(ctx, t, v, c.Status().Primary, ch.String()) == 1 })
		admin := adminAt(t, v, c.Status().Primary)
		var found bool
		for _, m := range named(clientList(ctx, t, admin), c.Instance) {
			if m["name"] == "trove:"+c.Instance+":pubsub" && m["sub"] != "0" {
				found = true
			}
		}
		if !found {
			t.Fatal("no subscribed connection named trove:" + c.Instance + ":pubsub on the primary")
		}
	})

	t.Run("R-ARCH-031 handlers are independent and the last cancel unsubscribes", func(t *testing.T) {
		c := newClientIn(t, stackConfig(t, 2*time.Second), ks)
		waitAvailable(t, c)
		ch, _ := c.Channel(chanFamily, "cancel")
		var a, b inbox
		stopA, _ := c.Subscribe(ctx, ch, a.handle)
		stopB, _ := c.Subscribe(ctx, ch, b.handle)
		eventually(t, "the subscription", 10*time.Second, func() bool { return numSub(ctx, t, v, c.Status().Primary, ch.String()) == 1 })
		_ = c.Publish(ctx, ch, "both")
		eventually(t, "delivery to both", 5*time.Second, func() bool { return a.has("both") && b.has("both") })
		stopA()
		_ = c.Publish(ctx, ch, "only-b")
		eventually(t, "delivery to b", 5*time.Second, func() bool { return b.has("only-b") })
		if a.has("only-b") {
			t.Fatal("a cancelled handler still receives")
		}
		stopB()
		eventually(t, "the unsubscribe", 5*time.Second, func() bool { return numSub(ctx, t, v, c.Status().Primary, ch.String()) == 0 })
	})

	t.Run("R-ARCH-030 a replica of another keyspace does not hear the message", func(t *testing.T) {
		mine := newClientIn(t, stackConfig(t, 2*time.Second), ks)
		theirs := newClientIn(t, stackConfig(t, 2*time.Second), randHex(8))
		waitAvailable(t, mine)
		waitAvailable(t, theirs)
		chMine, _ := mine.Channel(chanFamily, "iso")
		chTheirs, _ := theirs.Channel(chanFamily, "iso")
		var in inbox
		stop, _ := theirs.Subscribe(ctx, chTheirs, in.handle)
		defer stop()
		eventually(t, "the subscription", 10*time.Second, func() bool { return theirs.Status().Subscribed })
		if err := mine.Publish(ctx, chMine, "secret"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(500 * time.Millisecond)
		if in.count() != 0 {
			t.Fatalf("a replica of another installation received %d messages", in.count())
		}
	})

	t.Run("R-ARCH-031 OnSubscribed runs when the connection has subscribed, not as a reconnect the first time", func(t *testing.T) {
		c := newClientIn(t, stackConfig(t, 2*time.Second), ks)
		got := make(chan bool, 4)
		c.OnSubscribed(func(reconnect bool) { got <- reconnect })
		waitAvailable(t, c)
		select {
		case reconnect := <-got:
			if reconnect {
				t.Fatal("the first subscription was reported as a reconnect")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("OnSubscribed did not run")
		}
	})
}
