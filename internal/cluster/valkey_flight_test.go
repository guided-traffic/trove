package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	testKS      = "0123456789ab" // the first 12 digits of testKeyspace
	fixtureTTL  = 600 * time.Millisecond
	fixturePoll = 100 * time.Millisecond
)

type flightFixture struct {
	srv  *memServer
	v    *Valkey
	reg  *prometheus.Registry
	lock FlightLock
}

func newFlightFixture(t *testing.T, ttl, poll time.Duration) *flightFixture {
	t.Helper()
	srv := newMemServer(t)
	cfg := standaloneCfg(srv.addr())
	cfg.CommandTimeout = time.Second
	v, _ := newTestValkey(t, cfg)
	eventually(t, "availability", 5*time.Second, v.Available)
	eventually(t, "the Pub/Sub connection", 5*time.Second, func() bool { return v.Status().Subscribed })
	reg := prometheus.NewRegistry()
	return &flightFixture{srv: srv, v: v, reg: reg, lock: NewFlightLock(v, FlightLockOptions{TTL: ttl, PollInterval: poll, Registerer: reg})}
}

func lockKeyName(f FlightKey) string { return "trove:" + testKS + ":lock:fetch:v1:" + f.String() }
func fillChannel(f FlightKey) string { return "trove:" + testKS + ":fill:v1:" + f.String() }

var testFlight = NewFlightKey(7, "/left-pad/-/left-pad-1.3.0.tgz", "")

func TestFlightKey(t *testing.T) {
	t.Run("R-REPO-026 the flight key is the SHA-256 of repository id, path and variant, as 64 lowercase hex digits", func(t *testing.T) {
		f := NewFlightKey(7, "/a/b", "full")
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(f.String()) {
			t.Fatalf("flight key = %q", f)
		}
		sum := sha256.Sum256([]byte("7\x00/a/b\x00full"))
		if want := hex.EncodeToString(sum[:]); f.String() != want {
			t.Fatalf("flight key = %s, want sha256(7 0x00 /a/b 0x00 full) = %s", f, want)
		}
		if NewFlightKey(7, "/a/b", "full") != f {
			t.Fatal("not deterministic")
		}
	})
	t.Run("R-REPO-026 every part of the key matters and the separators keep parts apart", func(t *testing.T) {
		seen := map[FlightKey]string{}
		for name, f := range map[string]FlightKey{
			"base":         NewFlightKey(1, "/p", "v"),
			"other repo":   NewFlightKey(2, "/p", "v"),
			"other path":   NewFlightKey(1, "/q", "v"),
			"other var":    NewFlightKey(1, "/p", "w"),
			"no variant":   NewFlightKey(1, "/p", ""),
			"moved":        NewFlightKey(1, "/", "pv"),
			"repo 12":      NewFlightKey(12, "/p", "v"),
			"repo 1 + '2'": NewFlightKey(1, "2/p", "v"),
		} {
			if prev, dup := seen[f]; dup {
				t.Errorf("%s and %s share the flight key", name, prev)
			}
			seen[f] = name
		}
	})
	t.Run("R-ARCH-033 the zero flight key is refused", func(t *testing.T) {
		fx := newFlightFixture(t, fixtureTTL, fixturePoll)
		if _, _, err := fx.lock.Acquire(t.Context(), FlightKey{}); err == nil || errors.Is(err, ErrUnavailable) {
			t.Fatalf("Acquire(zero) = %v", err)
		}
		if _, err := fx.lock.Wait(t.Context(), FlightKey{}); err == nil || errors.Is(err, ErrUnavailable) {
			t.Fatalf("Wait(zero) = %v", err)
		}
	})
}

func TestFlightLockAcquire(t *testing.T) {
	t.Run("R-ARCH-033 Acquire is SET key token NX PX ttl on the lock key with the full hash", func(t *testing.T) {
		fx := newFlightFixture(t, fixtureTTL, fixturePoll)
		h, ok, err := fx.lock.Acquire(t.Context(), testFlight)
		if err != nil || !ok {
			t.Fatalf("Acquire = %v, %v", ok, err)
		}
		defer func() { _ = h.Release(t.Context(), OutcomeFailed, "") }()
		token, found := fx.srv.value(lockKeyName(testFlight))
		if !found {
			t.Fatalf("no key %s", lockKeyName(testFlight))
		}
		if !regexp.MustCompile(`^test-pod\.[A-Za-z0-9_-]{22}$`).MatchString(token) {
			t.Fatalf("token = %q, want <instance uid>.<16 random bytes, base64url>", token)
		}
		var set []string
		for _, c := range fx.srv.commands("SET") {
			if c[1] == lockKeyName(testFlight) {
				set = c
				break
			}
		}
		if len(set) != 6 || set[3] != "NX" || !strings.EqualFold(set[4], "PX") || set[5] != "600" {
			t.Fatalf("SET = %v, want SET key token NX PX 600", set)
		}
	})
	t.Run("R-ARCH-033 a second Acquire of a held flight is refused and does not disturb the holder", func(t *testing.T) {
		fx := newFlightFixture(t, fixtureTTL, fixturePoll)
		h, ok, _ := fx.lock.Acquire(t.Context(), testFlight)
		if !ok {
			t.Fatal("first Acquire refused")
		}
		defer func() { _ = h.Release(t.Context(), OutcomeFailed, "") }()
		before, _ := fx.srv.value(lockKeyName(testFlight))
		h2, ok, err := fx.lock.Acquire(t.Context(), testFlight)
		if err != nil || ok || h2 != nil {
			t.Fatalf("second Acquire = %v, %v, %v", h2, ok, err)
		}
		if after, _ := fx.srv.value(lockKeyName(testFlight)); after != before {
			t.Fatalf("the holder's token changed from %q to %q", before, after)
		}
	})
	t.Run("R-ARCH-033 the token differs for every acquisition", func(t *testing.T) {
		fx := newFlightFixture(t, fixtureTTL, fixturePoll)
		tokens := map[string]bool{}
		for range 3 {
			h, ok, _ := fx.lock.Acquire(t.Context(), testFlight)
			if !ok {
				t.Fatal("Acquire refused")
			}
			tok, _ := fx.srv.value(lockKeyName(testFlight))
			if tokens[tok] {
				t.Fatalf("token %q used twice", tok)
			}
			tokens[tok] = true
			if err := h.Release(t.Context(), OutcomeStored, ""); err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Run("R-ARCH-033 the hold extends the lock every third of the TTL while the fetch runs", func(t *testing.T) {
		fx := newFlightFixture(t, fixtureTTL, fixturePoll)
		h, _, _ := fx.lock.Acquire(t.Context(), testFlight)
		token, _ := fx.srv.value(lockKeyName(testFlight))
		time.Sleep(2*fixtureTTL + 100*time.Millisecond) // two lifetimes
		if got, found := fx.srv.value(lockKeyName(testFlight)); !found || got != token {
			t.Fatalf("lock after two TTLs = %q, %v: the hold did not extend it", got, found)
		}
		var ext int
		for _, c := range fx.srv.commands("SET") {
			if len(c) == 7 && c[1] == lockKeyName(testFlight) && c[2] == token && strings.EqualFold(c[3], "IFEQ") && c[4] == token && strings.EqualFold(c[5], "PX") && c[6] == "600" {
				ext++
			}
		}
		if ext < 5 {
			t.Fatalf("%d extensions SET key token IFEQ token PX 600 in %s, want about 6", ext, 2*fixtureTTL)
		}
		if h.Lost() {
			t.Fatal("Lost() although nothing took the lock")
		}
		_ = h.Release(t.Context(), OutcomeStored, "")
	})
}

func TestFlightLockLost(t *testing.T) {
	t.Run("R-ARCH-033 an extension that finds the key gone counts the loss and stops, and Release never deletes the next holder's key", func(t *testing.T) {
		fx := newFlightFixture(t, fixtureTTL, fixturePoll)
		a, _, _ := fx.lock.Acquire(t.Context(), testFlight)
		fx.srv.lose(lockKeyName(testFlight)) // a failover that lost the write
		b, ok, err := fx.lock.Acquire(t.Context(), testFlight)
		if err != nil || !ok {
			t.Fatalf("Acquire after the loss = %v, %v", ok, err)
		}
		bToken, _ := fx.srv.value(lockKeyName(testFlight))
		eventually(t, "the first hold to notice", 2*fixtureTTL, a.Lost)
		if got := gaugeValue(t, fx.reg, "trove_flight_lock_lost_total", ""); got != 1 {
			t.Fatalf("trove_flight_lock_lost_total = %v, want 1", got)
		}
		// the first hold finishes its fetch and releases: the second holder keeps its lock
		if err := a.Release(t.Context(), OutcomeStored, ""); err != nil {
			t.Fatal(err)
		}
		if got, found := fx.srv.value(lockKeyName(testFlight)); !found || got != bToken {
			t.Fatalf("key after the first hold's Release = %q, %v, want the second holder's token", got, found)
		}
		if b.Lost() {
			t.Fatal("the second hold is not lost")
		}
		if _, ok, _ := fx.lock.Acquire(t.Context(), testFlight); ok {
			t.Fatal("a third holder acquired: at most two holders of a lost lock")
		}
		_ = b.Release(t.Context(), OutcomeStored, "")
	})
	t.Run("R-ARCH-033 a hold stops extending after the loss", func(t *testing.T) {
		fx := newFlightFixture(t, fixtureTTL, fixturePoll)
		a, _, _ := fx.lock.Acquire(t.Context(), testFlight)
		fx.srv.lose(lockKeyName(testFlight))
		eventually(t, "the loss", 2*fixtureTTL, a.Lost)
		setsOfLock := func() (n int) {
			for _, c := range fx.srv.commands("SET") {
				if c[1] == lockKeyName(testFlight) {
					n++
				}
			}
			return n
		}
		n := setsOfLock()
		time.Sleep(fixtureTTL)
		if got := setsOfLock(); got != n {
			t.Fatalf("%d SET commands on the lock after the loss", got-n)
		}
		_ = a.Release(t.Context(), OutcomeFailed, "")
		if got := gaugeValue(t, fx.reg, "trove_flight_lock_lost_total", ""); got != 1 {
			t.Fatalf("trove_flight_lock_lost_total = %v, want 1 for one lost hold", got)
		}
	})
}

func TestFlightLockRelease(t *testing.T) {
	t.Run("R-ARCH-033 Release deletes the lock if it is ours, then publishes the outcome", func(t *testing.T) {
		fx := newFlightFixture(t, fixtureTTL, fixturePoll)
		h, _, _ := fx.lock.Acquire(t.Context(), testFlight)
		if err := h.Release(t.Context(), OutcomeStored, ""); err != nil {
			t.Fatal(err)
		}
		if _, found := fx.srv.value(lockKeyName(testFlight)); found {
			t.Fatal("the lock is still there after Release")
		}
		var order []string
		fx.srv.mu.Lock()
		for _, c := range fx.srv.cmds {
			switch strings.ToUpper(c[0]) {
			case "DELIFEQ", "PUBLISH":
				order = append(order, strings.ToUpper(c[0])+" "+c[1])
				if strings.ToUpper(c[0]) == "PUBLISH" && c[2] != "stored" {
					t.Errorf("PUBLISH payload = %q, want stored", c[2])
				}
			}
		}
		fx.srv.mu.Unlock()
		want := []string{"DELIFEQ " + lockKeyName(testFlight), "PUBLISH " + fillChannel(testFlight)}
		if strings.Join(order, "|") != strings.Join(want, "|") {
			t.Fatalf("commands = %v, want %v", order, want)
		}
	})
	t.Run("R-ARCH-033 the outcome carries the leader's traceparent when it is a short printable token", func(t *testing.T) {
		const tp = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
		for tc, want := range map[string]string{
			tp:                              "failed " + tp,
			"":                              "failed",
			"has space":                     "failed",
			"bell\a":                        "failed",
			strings.Repeat("a", 257):        "failed",
			"00-" + strings.Repeat("a", 20): "failed 00-" + strings.Repeat("a", 20),
		} {
			if got := outcomeMessage(OutcomeFailed, tc); got != want {
				t.Errorf("outcomeMessage(%q) = %q, want %q", tc, got, want)
			}
		}
		if len(outcomeMessage(OutcomeStored, strings.Repeat("a", 256))) >= MaxValueSize {
			t.Error("a full-length traceparent exceeds the 1 KiB of R-ARCH-030")
		}
	})
	t.Run("R-ARCH-033 Release runs although the initiating client's context is cancelled", func(t *testing.T) {
		fx := newFlightFixture(t, fixtureTTL, fixturePoll)
		h, _, _ := fx.lock.Acquire(t.Context(), testFlight)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := h.Release(ctx, OutcomeFailed, ""); err != nil {
			t.Fatalf("Release with a cancelled context: %v", err)
		}
		if _, found := fx.srv.value(lockKeyName(testFlight)); found {
			t.Fatal("the lock is still there")
		}
		if fx.srv.count("PUBLISH") != 1 {
			t.Fatal("the outcome was not published")
		}
	})
	t.Run("R-ARCH-033 a second Release does nothing", func(t *testing.T) {
		fx := newFlightFixture(t, fixtureTTL, fixturePoll)
		h, _, _ := fx.lock.Acquire(t.Context(), testFlight)
		_ = h.Release(t.Context(), OutcomeStored, "")
		_ = h.Release(t.Context(), OutcomeFailed, "")
		if fx.srv.count("PUBLISH") != 1 || fx.srv.count("DELIFEQ") != 1 {
			t.Fatalf("PUBLISH %d, DELIFEQ %d after two Releases", fx.srv.count("PUBLISH"), fx.srv.count("DELIFEQ"))
		}
	})
}

// waitResult is what a goroutine's Wait returned.
type waitResult struct {
	o   Outcome
	err error
	at  time.Time
}

func startWait(ctx context.Context, l FlightLock, f FlightKey) <-chan waitResult {
	out := make(chan waitResult, 1)
	go func() {
		o, err := l.Wait(ctx, f)
		out <- waitResult{o, err, time.Now()}
	}()
	return out
}

func expect(t *testing.T, ch <-chan waitResult, within time.Duration, what string) waitResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(within):
		t.Fatalf("%s: Wait did not return within %s", what, within)
		return waitResult{}
	}
}

func TestFlightLockWait(t *testing.T) {
	t.Run("R-ARCH-033 a waiter returns the outcome the holder publishes, long before its poll", func(t *testing.T) {
		fx := newFlightFixture(t, fixtureTTL, time.Hour)
		h, _, _ := fx.lock.Acquire(t.Context(), testFlight)
		res := startWait(t.Context(), fx.lock, testFlight)
		eventually(t, "the subscription", 5*time.Second, func() bool { return fx.srv.subscribers(fillChannel(testFlight)) == 1 })
		select {
		case r := <-res:
			t.Fatalf("Wait returned %+v while the lock is held", r)
		case <-time.After(150 * time.Millisecond):
		}
		_ = h.Release(t.Context(), OutcomeStored, "")
		if r := expect(t, res, 2*time.Second, "after Release"); r.err != nil || r.o != OutcomeStored {
			t.Fatalf("Wait = %+v, want stored", r)
		}
	})
	t.Run("R-ARCH-033 a failed outcome is returned as such", func(t *testing.T) {
		fx := newFlightFixture(t, fixtureTTL, time.Hour)
		h, _, _ := fx.lock.Acquire(t.Context(), testFlight)
		res := startWait(t.Context(), fx.lock, testFlight)
		eventually(t, "the subscription", 5*time.Second, func() bool { return fx.srv.subscribers(fillChannel(testFlight)) == 1 })
		_ = h.Release(t.Context(), OutcomeFailed, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
		if r := expect(t, res, 2*time.Second, "after Release"); r.err != nil || r.o != OutcomeFailed {
			t.Fatalf("Wait = %+v, want failed", r)
		}
	})
	t.Run("R-ARCH-033 Wait returns at once when the lock is gone", func(t *testing.T) {
		fx := newFlightFixture(t, fixtureTTL, time.Hour)
		if r := expect(t, startWait(t.Context(), fx.lock, testFlight), time.Second, "no lock"); r.err != nil || r.o != OutcomeGone {
			t.Fatalf("Wait = %+v, want gone", r)
		}
	})
	t.Run("R-ARCH-033 with every message dropped the poll completes the wait", func(t *testing.T) {
		fx := newFlightFixture(t, fixtureTTL, fixturePoll)
		h, _, _ := fx.lock.Acquire(t.Context(), testFlight)
		res := startWait(t.Context(), fx.lock, testFlight)
		eventually(t, "the subscription", 5*time.Second, func() bool { return fx.srv.subscribers(fillChannel(testFlight)) == 1 })
		fx.srv.dropPublish.Store(true)
		start := time.Now()
		_ = h.Release(t.Context(), OutcomeStored, "")
		r := expect(t, res, 2*time.Second, "poll")
		if r.err != nil || r.o != OutcomeGone {
			t.Fatalf("Wait = %+v, want gone from the poll", r)
		}
		if took := r.at.Sub(start); took > 3*fixturePoll {
			t.Fatalf("the poll took %s, want about %s", took, fixturePoll)
		}
	})
	t.Run("R-ARCH-033 a context that ends ends the wait with its own error", func(t *testing.T) {
		fx := newFlightFixture(t, fixtureTTL, fixturePoll)
		h, _, _ := fx.lock.Acquire(t.Context(), testFlight)
		defer func() { _ = h.Release(t.Context(), OutcomeFailed, "") }()
		ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
		defer cancel()
		r := expect(t, startWait(ctx, fx.lock, testFlight), 2*time.Second, "deadline")
		if !errors.Is(r.err, context.DeadlineExceeded) || errors.Is(r.err, ErrUnavailable) {
			t.Fatalf("Wait = %+v, want the context's deadline error", r)
		}
	})
	t.Run("R-ARCH-033 a killed holder frees the flight within the TTL plus the poll interval", func(t *testing.T) {
		fx := newFlightFixture(t, fixtureTTL, fixturePoll)
		h, _, _ := fx.lock.Acquire(t.Context(), testFlight)
		h.stop() // the process dies: no extension, no release
		<-h.done
		start := time.Now()
		r := expect(t, startWait(t.Context(), fx.lock, testFlight), fixtureTTL+fixturePoll+500*time.Millisecond, "takeover")
		if r.err != nil || r.o != OutcomeGone {
			t.Fatalf("Wait = %+v, want gone", r)
		}
		if took := r.at.Sub(start); took > fixtureTTL+fixturePoll+150*time.Millisecond {
			t.Fatalf("the waiter needed %s, want at most ttl+poll = %s (and scheduling)", took, fixtureTTL+fixturePoll)
		}
		h2, ok, err := fx.lock.Acquire(t.Context(), testFlight)
		if err != nil || !ok {
			t.Fatalf("takeover Acquire = %v, %v", ok, err)
		}
		_ = h2.Release(t.Context(), OutcomeStored, "")
	})
}

func TestFlightLockSubscriptions(t *testing.T) {
	t.Run("R-ARCH-033 fifty waiters of one flight cost one SUBSCRIBE and all hear the outcome", func(t *testing.T) {
		fx := newFlightFixture(t, fixtureTTL, time.Hour)
		h, _, _ := fx.lock.Acquire(t.Context(), testFlight)
		const n = 50
		results := make([]<-chan waitResult, n)
		for i := range results {
			results[i] = startWait(t.Context(), fx.lock, testFlight)
		}
		eventually(t, "the subscription", 5*time.Second, func() bool { return fx.srv.subscribers(fillChannel(testFlight)) == 1 })
		time.Sleep(100 * time.Millisecond)
		var subscribes int
		for _, c := range fx.srv.commands("SUBSCRIBE") {
			if c[1] == fillChannel(testFlight) {
				subscribes++
			}
		}
		if subscribes != 1 {
			t.Fatalf("%d SUBSCRIBE of the flight channel for %d waiters, want 1", subscribes, n)
		}
		_ = h.Release(t.Context(), OutcomeStored, "")
		for i, ch := range results {
			if r := expect(t, ch, 2*time.Second, "waiter"); r.err != nil || r.o != OutcomeStored {
				t.Fatalf("waiter %d: %+v", i, r)
			}
		}
		eventually(t, "the last waiter to unsubscribe", 5*time.Second, func() bool { return fx.srv.subscribers(fillChannel(testFlight)) == 0 })
	})
	t.Run("R-ARCH-033 two flights have two channels", func(t *testing.T) {
		fx := newFlightFixture(t, fixtureTTL, time.Hour)
		other := NewFlightKey(7, "/other", "")
		ha, _, _ := fx.lock.Acquire(t.Context(), testFlight)
		hb, _, _ := fx.lock.Acquire(t.Context(), other)
		ra := startWait(t.Context(), fx.lock, testFlight)
		rb := startWait(t.Context(), fx.lock, other)
		eventually(t, "both subscriptions", 5*time.Second, func() bool {
			return fx.srv.subscribers(fillChannel(testFlight)) == 1 && fx.srv.subscribers(fillChannel(other)) == 1
		})
		_ = ha.Release(t.Context(), OutcomeStored, "")
		if r := expect(t, ra, 2*time.Second, "flight a"); r.o != OutcomeStored {
			t.Fatalf("a: %+v", r)
		}
		select {
		case r := <-rb:
			t.Fatalf("the waiter of the other flight returned %+v", r)
		case <-time.After(150 * time.Millisecond):
		}
		_ = hb.Release(t.Context(), OutcomeFailed, "")
		if r := expect(t, rb, 2*time.Second, "flight b"); r.o != OutcomeFailed {
			t.Fatalf("b: %+v", r)
		}
	})
	t.Run("R-ARCH-031 a re-subscription after a lost Pub/Sub connection wakes the waiters to check their key", func(t *testing.T) {
		fx := newFlightFixture(t, fixtureTTL, time.Hour) // no poll within the test
		h, _, _ := fx.lock.Acquire(t.Context(), testFlight)
		defer func() { _ = h.Release(t.Context(), OutcomeFailed, "") }()
		res := startWait(t.Context(), fx.lock, testFlight)
		eventually(t, "the subscription", 5*time.Second, func() bool { return fx.srv.subscribers(fillChannel(testFlight)) == 1 })
		time.Sleep(100 * time.Millisecond)
		fx.srv.lose(lockKeyName(testFlight)) // gone without a message
		select {
		case r := <-res:
			t.Fatalf("Wait returned %+v before the connection was lost: nothing should have woken it", r)
		case <-time.After(200 * time.Millisecond):
		}
		fx.srv.cutSubscribers()
		if r := expect(t, res, 5*time.Second, "reconnect"); r.err != nil || r.o != OutcomeGone {
			t.Fatalf("Wait = %+v, want gone after the re-subscription", r)
		}
	})
}

func TestFlightLockUnavailable(t *testing.T) {
	t.Run("R-ARCH-033 without a Valkey client every call is ErrUnavailable", func(t *testing.T) {
		l := NewFlightLock(nil, FlightLockOptions{})
		if _, ok, err := l.Acquire(t.Context(), testFlight); ok || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Acquire = %v, %v", ok, err)
		}
		if _, err := l.Wait(t.Context(), testFlight); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Wait = %v", err)
		}
	})
	t.Run("R-ARCH-033 R-ARCH-031 with the breaker open Acquire and Wait return ErrUnavailable at once, without a call, and count the fallback", func(t *testing.T) {
		fx := newFlightFixture(t, fixtureTTL, fixturePoll)
		for range 10 {
			fx.v.br.allow()
			fx.v.br.done(outcomeFailure)
		}
		if !fx.v.br.isOpen() {
			t.Fatal("the breaker did not open")
		}
		touching := func() (n int) {
			for _, name := range []string{"SET", "GET"} {
				for _, c := range fx.srv.commands(name) {
					if c[1] == lockKeyName(testFlight) {
						n++
					}
				}
			}
			return n
		}
		before := touching()
		start := time.Now()
		_, ok, err := fx.lock.Acquire(t.Context(), testFlight)
		if ok || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Acquire = %v, %v", ok, err)
		}
		if _, err := fx.lock.Wait(t.Context(), testFlight); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Wait = %v", err)
		}
		if took := time.Since(start); took > 50*time.Millisecond {
			t.Fatalf("the calls took %s with the breaker open", took)
		}
		if after := touching(); after != before {
			t.Fatal("a call reached the server while the breaker was open")
		}
	})
	t.Run("R-ARCH-033 a failing call is ErrUnavailable too, and counts trove_valkey_degraded_total{flight_lock}", func(t *testing.T) {
		srv := newFakeServer(t, func(args []string) string {
			switch strings.ToUpper(args[0]) {
			case "HELLO":
				return helloReplyFor("valkey", "9.1.3")
			case "PING":
				return "+PONG\r\n"
			case "SET", "GET":
				return "-OOM command not allowed when used memory > 'maxmemory'.\r\n"
			}
			return "+OK\r\n"
		})
		cfg := standaloneCfg(srv.addr())
		cfg.HealthCheckInterval = time.Hour
		reg := prometheus.NewRegistry()
		v, err := NewValkey(t.Context(), cfg, "test-pod", "web", nil, reg, WithKeyspaceID(testKeyspace))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(v.Close)
		eventually(t, "connection", 5*time.Second, func() bool { return v.Status().Version != "" })
		l := NewFlightLock(v, FlightLockOptions{})
		if _, ok, err := l.Acquire(t.Context(), testFlight); ok || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Acquire = %v, %v, want ErrUnavailable", ok, err)
		}
		if got := gaugeValue(t, reg, "trove_valkey_degraded_total", MechanismFlightLock); got != 1 {
			t.Fatalf("trove_valkey_degraded_total{flight_lock} = %v", got)
		}
	})
}

func TestFlightLockOptions(t *testing.T) {
	t.Run("R-ARCH-033 the TTL defaults to 30 s and is capped by the key family", func(t *testing.T) {
		fx := newFlightFixture(t, 0, 0)
		if l := fx.lock.(*flightLock); l.ttl != 30*time.Second || l.poll != time.Second {
			t.Fatalf("defaults = %s, %s", l.ttl, l.poll)
		}
		if l := NewFlightLock(fx.v, FlightLockOptions{TTL: time.Hour}).(*flightLock); l.ttl != MaxFlightLockTTL {
			t.Fatalf("ttl = %s", l.ttl)
		}
	})
	t.Run("R-ARCH-033 a second lock on one registry shares the lost counter", func(t *testing.T) {
		fx := newFlightFixture(t, fixtureTTL, fixturePoll)
		a, b := fx.lock.(*flightLock), NewFlightLock(fx.v, FlightLockOptions{Registerer: fx.reg}).(*flightLock)
		if a.lost != b.lost {
			t.Fatal("the second lock counts into a counter of its own")
		}
	})
	t.Run("R-ARCH-030 the lock key and the channel are registered with their families", func(t *testing.T) {
		var lock, fill bool
		for _, f := range registeredFamilies() {
			switch {
			case f.Mechanism == "lock:fetch" && f.Version == 1:
				lock = f.MaxTTL == 10*time.Minute
			case f.Mechanism == "fill" && f.Version == 1:
				fill = f.MaxTTL == 0
			}
		}
		if !lock || !fill {
			t.Fatalf("lock:fetch (10 min) %v, fill channel %v", lock, fill)
		}
	})
}

// A hold's extension and a Wait in the same goroutines as many flights: run
// under -race to see the shared subscription table.
func TestFlightLockConcurrentFlights(t *testing.T) {
	t.Run("R-ARCH-033 many flights, holders and waiters at once", func(t *testing.T) {
		fx := newFlightFixture(t, fixtureTTL, fixturePoll)
		var wg sync.WaitGroup
		for i := range 20 {
			f := NewFlightKey(1, "/p/"+string(rune('a'+i)), "")
			h, ok, err := fx.lock.Acquire(t.Context(), f)
			if err != nil || !ok {
				t.Fatalf("Acquire %d = %v, %v", i, ok, err)
			}
			for range 3 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					// a waiter that joins after the release finds the lock gone
					if o, err := fx.lock.Wait(t.Context(), f); err != nil || (o != OutcomeStored && o != OutcomeGone) {
						t.Errorf("Wait = %q, %v", o, err)
					}
				}()
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				time.Sleep(300 * time.Millisecond)
				_ = h.Release(t.Context(), OutcomeStored, "")
			}()
		}
		wg.Wait()
	})
}
