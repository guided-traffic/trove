//go:build integration

package valkey

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

const (
	flightTTL  = 3 * time.Second // the minimum of cluster.flight_lock_ttl
	flightPoll = 500 * time.Millisecond
)

// flightReplica is a replica of the installation ks with its flight lock.
type flightReplica struct {
	*client
	lock cluster.FlightLock
}

func newFlightReplica(t *testing.T, ks string, poll time.Duration) *flightReplica {
	t.Helper()
	c := newClientIn(t, stackConfig(t, failoverRefresh), ks)
	waitAvailable(t, c)
	return &flightReplica{client: c, lock: cluster.NewFlightLock(c.Valkey, cluster.FlightLockOptions{
		TTL: flightTTL, PollInterval: poll, Registerer: c.Reg,
	})}
}

// newFlight is a flight key no other test uses.
func newFlight() cluster.FlightKey {
	return cluster.NewFlightKey(7, "/tarball/"+randHex(6)+".tgz", "")
}

// flightNames are the lock key and the outcome channel of f in the installation ks.
func flightNames(ks string, f cluster.FlightKey) (lockKey, channel string) {
	p := runPrefix + strings.ToLower(ks[:12]) + ":"
	return p + "lock:fetch:v1:" + f.String(), p + "fill:v1:" + f.String()
}

type waitResult struct {
	o   cluster.Outcome
	err error
	at  time.Time
}

func startWait(ctx context.Context, l cluster.FlightLock, f cluster.FlightKey) <-chan waitResult {
	out := make(chan waitResult, 1)
	go func() {
		o, err := l.Wait(ctx, f)
		out <- waitResult{o, err, time.Now()}
	}()
	return out
}

func awaitResult(t *testing.T, ch <-chan waitResult, within time.Duration, what string) waitResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(within):
		t.Fatalf("%s: Wait did not return within %s", what, within)
		return waitResult{}
	}
}

func noResult(t *testing.T, ch <-chan waitResult, within time.Duration, what string) {
	t.Helper()
	select {
	case r := <-ch:
		t.Fatalf("%s: Wait returned %+v", what, r)
	case <-time.After(within):
	}
}

// getKey reads a key as the administrator: found is false when it does not exist.
func getKey(ctx context.Context, t *testing.T, admin valkey.Client, key string) (string, bool) {
	t.Helper()
	s, err := admin.Do(ctx, admin.B().Get().Key(key).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return "", false
	}
	if err != nil {
		t.Fatalf("GET %s: %v", key, err)
	}
	return s, true
}

func pttl(ctx context.Context, t *testing.T, admin valkey.Client, key string) time.Duration {
	t.Helper()
	n, err := admin.Do(ctx, admin.B().Pttl().Key(key).Build()).AsInt64()
	if err != nil {
		t.Fatalf("PTTL %s: %v", key, err)
	}
	return time.Duration(n) * time.Millisecond
}

// loseKey deletes key without a message, as a failover that lost the write.
func loseKey(ctx context.Context, t *testing.T, admin valkey.Client, key string) {
	t.Helper()
	if err := admin.Do(ctx, admin.B().Del().Key(key).Build()).Error(); err != nil {
		t.Fatalf("DEL %s: %v", key, err)
	}
}

// listen collects the messages of channel as the administrator does.
func listen(ctx context.Context, t *testing.T, v testenv.Valkey, primary, channel string) *inbox {
	t.Helper()
	sub := adminAt(t, v, primary)
	ctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	in := &inbox{}
	go func() {
		_ = sub.Receive(ctx, sub.B().Subscribe().Channel(channel).Build(), func(m valkey.PubSubMessage) {
			in.handle(cluster.Message{Channel: m.Channel, Payload: m.Message})
		})
	}()
	eventually(t, "the listener", 10*time.Second, func() bool { return numSub(ctx, t, v, primary, channel) >= 1 })
	return in
}

func TestFlightLock(t *testing.T) {
	v := testenv.ValkeyConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	t.Run("R-ARCH-033 acquire, extend while the fetch runs, release with DELIFEQ and publish the outcome", func(t *testing.T) {
		ks := randHex(8)
		a, b := newFlightReplica(t, ks, flightPoll), newFlightReplica(t, ks, flightPoll)
		admin := adminAt(t, v, a.Status().Primary)
		f := newFlight()
		lockKey, channel := flightNames(ks, f)
		in := listen(ctx, t, v, a.Status().Primary, channel)

		h, ok, err := a.lock.Acquire(ctx, f)
		if err != nil || !ok {
			t.Fatalf("Acquire = %v, %v", ok, err)
		}
		token, found := getKey(ctx, t, admin, lockKey)
		if !found || !strings.HasPrefix(token, a.Instance+".") || len(token) != len(a.Instance)+1+22 {
			t.Fatalf("lock %s = %q, %v, want <instance uid>.<16 random bytes, base64url>", lockKey, token, found)
		}
		if d := pttl(ctx, t, admin, lockKey); d <= 0 || d > flightTTL {
			t.Fatalf("PTTL after Acquire = %s, want within the TTL of %s", d, flightTTL)
		}
		if _, ok, err := b.lock.Acquire(ctx, f); err != nil || ok {
			t.Fatalf("Acquire of a held flight = %v, %v", ok, err)
		}

		// longer than the TTL: only the extension keeps the key
		time.Sleep(flightTTL + time.Second)
		if got, found := getKey(ctx, t, admin, lockKey); !found || got != token {
			t.Fatalf("lock after %s = %q, %v: the hold did not extend it", flightTTL+time.Second, got, found)
		}
		if d := pttl(ctx, t, admin, lockKey); d < flightTTL/2 {
			t.Fatalf("PTTL %s after an extension, want a renewed expiry close to %s", d, flightTTL)
		}
		if h.Lost() {
			t.Fatal("Lost() although nothing took the lock")
		}

		const tp = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
		if err := h.Release(ctx, cluster.OutcomeStored, tp); err != nil {
			t.Fatal(err)
		}
		if _, found := getKey(ctx, t, admin, lockKey); found {
			t.Fatal("the lock is still there after Release")
		}
		eventually(t, "the outcome message", 5*time.Second, func() bool { return in.has("stored " + tp) })

		h2, ok, err := b.lock.Acquire(ctx, f)
		if err != nil || !ok {
			t.Fatalf("Acquire after Release = %v, %v", ok, err)
		}
		if token2, _ := getKey(ctx, t, admin, lockKey); token2 == token || !strings.HasPrefix(token2, b.Instance+".") {
			t.Fatalf("the second token %q repeats the first or lacks its instance", token2)
		}
		_ = h2.Release(ctx, cluster.OutcomeFailed, "")
	})

	t.Run("R-ARCH-033 R-REPO-026 waiters of two replicas are woken by the outcome, with one subscription per replica, and a failed outcome lets one acquire", func(t *testing.T) {
		ks := randHex(8)
		holder := newFlightReplica(t, ks, flightPoll)
		r1, r2 := newFlightReplica(t, ks, time.Hour), newFlightReplica(t, ks, time.Hour) // the poll cannot be what wakes them
		primary := holder.Status().Primary
		f := newFlight()
		_, channel := flightNames(ks, f)

		h, ok, _ := holder.lock.Acquire(ctx, f)
		if !ok {
			t.Fatal("Acquire refused")
		}
		const perReplica = 25
		var results []<-chan waitResult
		for _, r := range []*flightReplica{r1, r2} {
			for range perReplica {
				results = append(results, startWait(ctx, r.lock, f))
			}
		}
		eventually(t, "one subscription per replica", 10*time.Second, func() bool { return numSub(ctx, t, v, primary, channel) == 2 })
		time.Sleep(300 * time.Millisecond)
		if n := numSub(ctx, t, v, primary, channel); n != 2 {
			t.Fatalf("PUBSUB NUMSUB = %d for 2 replicas with %d waiters each, want 2", n, perReplica)
		}
		for i, ch := range results {
			select {
			case r := <-ch:
				t.Fatalf("waiter %d returned %+v while the flight is held", i, r)
			default:
			}
		}

		start := time.Now()
		if err := h.Release(ctx, cluster.OutcomeFailed, ""); err != nil {
			t.Fatal(err)
		}
		for i, ch := range results {
			r := awaitResult(t, ch, 3*time.Second, "after Release")
			if r.err != nil || r.o != cluster.OutcomeFailed {
				t.Fatalf("waiter %d: %+v, want failed", i, r)
			}
			if took := r.at.Sub(start); took > 2*time.Second {
				t.Errorf("waiter %d needed %s", i, took)
			}
		}
		// the holder released with failed: a waiter acquires at once
		h2, ok, err := r1.lock.Acquire(ctx, f)
		if err != nil || !ok {
			t.Fatalf("Acquire after a failed release = %v, %v", ok, err)
		}
		_ = h2.Release(ctx, cluster.OutcomeStored, "")
		eventually(t, "the last waiter to unsubscribe", 10*time.Second, func() bool { return numSub(ctx, t, v, primary, channel) == 0 })
	})

	t.Run("R-ARCH-033 a dropped Pub/Sub message: the poll completes the wait", func(t *testing.T) {
		ks := randHex(8)
		holder := newFlightReplica(t, ks, flightPoll)
		w := newFlightReplica(t, ks, flightPoll)
		admin := adminAt(t, v, holder.Status().Primary)
		f := newFlight()
		lockKey, channel := flightNames(ks, f)
		if _, ok, _ := holder.lock.Acquire(ctx, f); !ok {
			t.Fatal("Acquire refused")
		}
		res := startWait(ctx, w.lock, f)
		eventually(t, "the subscription", 10*time.Second, func() bool { return numSub(ctx, t, v, holder.Status().Primary, channel) == 1 })
		time.Sleep(200 * time.Millisecond)

		start := time.Now()
		loseKey(ctx, t, admin, lockKey) // no PUBLISH: the message is dropped
		r := awaitResult(t, res, 3*time.Second, "poll")
		if r.err != nil || r.o != cluster.OutcomeGone {
			t.Fatalf("Wait = %+v, want gone", r)
		}
		if took := r.at.Sub(start); took > flightPoll+500*time.Millisecond {
			t.Fatalf("the poll needed %s, interval %s", took, flightPoll)
		}
	})

	t.Run("R-ARCH-033 a holder killed mid-transfer frees the flight within the TTL plus the poll interval", func(t *testing.T) {
		ks := randHex(8)
		holder := newFlightReplica(t, ks, flightPoll)
		w := newFlightReplica(t, ks, flightPoll)
		f := newFlight()
		if _, ok, _ := holder.lock.Acquire(ctx, f); !ok {
			t.Fatal("Acquire refused")
		}
		time.Sleep(1500 * time.Millisecond) // extended at least once
		holder.Close()                      // the process is gone: no extension, no release, no message

		start := time.Now()
		res := startWait(ctx, w.lock, f)
		r := awaitResult(t, res, flightTTL+flightPoll+3*time.Second, "takeover")
		if r.err != nil || r.o != cluster.OutcomeGone {
			t.Fatalf("Wait = %+v, want gone when the key expires", r)
		}
		took := r.at.Sub(start)
		t.Logf("the waiter saw the flight free after %s (ttl %s, poll %s)", took.Round(time.Millisecond), flightTTL, flightPoll)
		if took > flightTTL+flightPoll+500*time.Millisecond {
			t.Errorf("the waiter needed %s, want at most ttl + poll = %s", took, flightTTL+flightPoll)
		}
		h, ok, err := w.lock.Acquire(ctx, f)
		if err != nil || !ok {
			t.Fatalf("takeover Acquire = %v, %v", ok, err)
		}
		_ = h.Release(ctx, cluster.OutcomeStored, "")
	})

	t.Run("R-ARCH-033 R-ARCH-031 with the breaker open Acquire and Wait return ErrUnavailable at once", func(t *testing.T) {
		c := newClientIn(t, stackConfig(t, failoverRefresh), randHex(8))
		waitAvailable(t, c)
		l := cluster.NewFlightLock(c.Valkey, cluster.FlightLockOptions{TTL: flightTTL, PollInterval: flightPoll})
		f := newFlight()
		openBreaker(ctx, t, v, c, func() { _, _, _ = l.Acquire(ctx, f) })
		start := time.Now()
		h, ok, err := l.Acquire(ctx, f)
		if h != nil || ok || !errors.Is(err, cluster.ErrUnavailable) {
			t.Fatalf("Acquire = %v, %v, %v, want ErrUnavailable", h, ok, err)
		}
		if _, err := l.Wait(ctx, f); !errors.Is(err, cluster.ErrUnavailable) {
			t.Fatalf("Wait = %v, want ErrUnavailable", err)
		}
		if took := time.Since(start); took > 50*time.Millisecond {
			t.Fatalf("the calls took %s with the breaker open", took)
		}
		if got := metric(t, c.Reg, "trove_valkey_degraded_total", cluster.MechanismFlightLock); got < 2 {
			t.Fatalf("trove_valkey_degraded_total{mechanism=flight_lock} = %v, want at least 2", got)
		}
	})
}

// openBreaker holds every write of the Valkey primary until the breaker of c
// has opened (the way breaker_test.go does it), calling drive for each attempt,
// and lets the writes go again when the test ends.
func openBreaker(ctx context.Context, t *testing.T, v testenv.Valkey, c *client, drive func()) {
	t.Helper()
	admin := adminAt(t, v, c.Status().Primary)
	if err := admin.Do(ctx, admin.B().Arbitrary("CLIENT", "PAUSE", "3500", "WRITE").Build()).Error(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Do(context.Background(), admin.B().Arbitrary("CLIENT", "UNPAUSE").Build()).Error() })
	for i := 0; i < 10 && !c.Status().CircuitOpen; i++ {
		drive()
	}
	if !c.Status().CircuitOpen {
		t.Fatalf("the breaker did not open: %+v", c.Status())
	}
}

// TestFlightLockFailover forces Sentinel failovers: it belongs to the owner of
// the failover slot of the stack.
func TestFlightLockFailover(t *testing.T) {
	v := testenv.ValkeyConfig(t)

	t.Run("R-ARCH-033 a lock lost in a failover costs one duplicate holder, never more, and the first holder never deletes the second's key", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		t.Cleanup(func() { restoreLayout(t, v) })
		ks := randHex(8)
		a, b, c := newFlightReplica(t, ks, flightPoll), newFlightReplica(t, ks, flightPoll), newFlightReplica(t, ks, flightPoll)
		f := newFlight()
		lockKey, _ := flightNames(ks, f)

		holders := 0
		hA, ok, err := a.lock.Acquire(ctx, f)
		if err != nil || !ok {
			t.Fatalf("Acquire = %v, %v", ok, err)
		}
		holders++

		oldAddr, newAddr, _ := failover(ctx, t, v)
		t.Logf("primary %s -> %s", oldAddr, newAddr)
		for _, r := range []*flightReplica{a, b, c} {
			eventually(t, "a replica to follow the new primary", 15*time.Second, func() bool { return r.Status().Primary == newAddr })
		}

		// the write that created the lock did not reach the new primary
		admin := adminAt(t, v, newAddr)
		loseKey(ctx, t, admin, lockKey)

		hB, ok, err := b.lock.Acquire(ctx, f)
		if err != nil {
			t.Fatalf("second Acquire: %v", err)
		}
		if !ok {
			t.Fatal("the second replica could not acquire the flight whose lock was lost")
		}
		holders++
		tokenB, found := getKey(ctx, t, admin, lockKey)
		if !found || !strings.HasPrefix(tokenB, b.Instance+".") {
			t.Fatalf("lock = %q, %v, want the second holder's token", tokenB, found)
		}

		// from now on the second holder extends: nobody else gets in
		eventually(t, "the first hold to notice the loss", 3*flightTTL, hA.Lost)
		if got := metric(t, a.Reg, "trove_flight_lock_lost_total", ""); got != 1 {
			t.Fatalf("trove_flight_lock_lost_total on the first holder = %v, want 1", got)
		}
		for end := time.Now().Add(flightTTL + time.Second); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
			if h, ok, err := c.lock.Acquire(ctx, f); err == nil && ok {
				holders++
				_ = h.Release(ctx, cluster.OutcomeFailed, "")
			}
		}
		if holders != 2 {
			t.Fatalf("%d holders of one flight, want at most 2 (one duplicate fetch)", holders)
		}
		if hB.Lost() {
			t.Fatal("the second hold lost its lock")
		}

		// the first holder finishes: its release leaves the second holder's key alone
		if err := hA.Release(ctx, cluster.OutcomeStored, ""); err != nil {
			t.Fatal(err)
		}
		if got, found := getKey(ctx, t, admin, lockKey); !found || got != tokenB {
			t.Fatalf("lock after the first holder's Release = %q, %v, want the second holder's %q", got, found, tokenB)
		}
		_ = hB.Release(ctx, cluster.OutcomeStored, "")
		if _, found := getKey(ctx, t, admin, lockKey); found {
			t.Fatal("the second holder's Release left the lock")
		}
	})

	t.Run("R-ARCH-031 R-ARCH-033 the re-subscription after a primary change wakes a waiter, which then finds its lock gone", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		t.Cleanup(func() { restoreLayout(t, v) })
		ks := randHex(8)
		holder := newFlightReplica(t, ks, flightPoll)
		w := newFlightReplica(t, ks, time.Hour) // only a message or the wake-up can end this wait
		f := newFlight()
		lockKey, channel := flightNames(ks, f)
		primary := holder.Status().Primary
		admin := adminAt(t, v, primary)

		if _, ok, _ := holder.lock.Acquire(ctx, f); !ok {
			t.Fatal("Acquire refused")
		}
		res := startWait(ctx, w.lock, f)
		eventually(t, "the subscription", 10*time.Second, func() bool { return numSub(ctx, t, v, primary, channel) == 1 })
		time.Sleep(300 * time.Millisecond)

		// the lock is gone and nobody said so
		loseKey(ctx, t, admin, lockKey)
		noResult(t, res, 1500*time.Millisecond, "after the key was lost without a message")

		oldAddr, newAddr, announced := failover(ctx, t, v)
		t.Logf("primary %s -> %s", oldAddr, newAddr)
		bound := failoverRefresh + 3*time.Second
		r := awaitResult(t, res, bound+10*time.Second, "after the failover")
		if r.err != nil || r.o != cluster.OutcomeGone {
			t.Fatalf("Wait = %+v, want gone", r)
		}
		t.Logf("the waiter returned %s after the Sentinels announced the new primary (bound %s)", r.at.Sub(announced).Round(time.Millisecond), bound)
		if took := r.at.Sub(announced); took > bound {
			t.Errorf("the waiter needed %s after the announcement, bound is %s", took, bound)
		}
	})
}

// TestMechanismKeys checks what the lock and the negative store leave in Valkey.
func TestMechanismKeys(t *testing.T) {
	v := testenv.ValkeyConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ks := randHex(8)
	r := newFlightReplica(t, ks, flightPoll)
	store := cluster.NewNegativeStore(r.Valkey)
	admin := adminAt(t, v, r.Status().Primary)

	secret := "/@private-scope/left-pad/-/left-pad-1.3.0.tgz"
	f := cluster.NewFlightKey(7, secret, "full")
	h, ok, err := r.lock.Acquire(ctx, f)
	if err != nil || !ok {
		t.Fatalf("Acquire = %v, %v", ok, err)
	}
	defer func() { _ = h.Release(ctx, cluster.OutcomeStored, "") }()
	if err := store.Record(ctx, 7, f, cluster.NegativeEntry{Status: 404, SentAt: time.Now()}, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second) // a health probe

	t.Run("R-ARCH-030 every key under the installation's prefix expires and carries ids and hashes, never a name", func(t *testing.T) {
		prefix := runPrefix + strings.ToLower(ks[:12]) + ":"
		allowed := regexp.MustCompile("^" + regexp.QuoteMeta(prefix) + `(lock:fetch:v1:[0-9a-f]{64}|neg:v1:7:[0-9a-f]{64}|probe:v1:[A-Za-z0-9_-]+)$`)
		keys, err := admin.Do(ctx, admin.B().Keys().Pattern(prefix+"*").Build()).AsStrSlice()
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, k := range keys {
			if !allowed.MatchString(k) {
				t.Errorf("key %q is not <prefix><keyspace>:<mechanism>:v1:<ids or hashes>", k)
			}
			if d := pttl(ctx, t, admin, k); d <= 0 {
				t.Errorf("key %q has no expiry (PTTL %s)", k, d)
			}
			seen[strings.Split(strings.TrimPrefix(k, prefix), ":")[0]] = true
		}
		for _, m := range []string{"lock", "neg", "probe"} {
			if !seen[m] {
				t.Errorf("no %s key among %v", m, keys)
			}
		}
	})
	t.Run("R-ARCH-030 the flight lock key and the channel are the documented names", func(t *testing.T) {
		lockKey, channel := flightNames(ks, f)
		if _, found := getKey(ctx, t, admin, lockKey); !found {
			t.Fatalf("no lock at %s", lockKey)
		}
		if !strings.HasPrefix(channel, runPrefix) || !strings.Contains(channel, ":fill:v1:") {
			t.Fatalf("channel %s", channel)
		}
	})
}
