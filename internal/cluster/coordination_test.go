package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/observability"
)

func TestAreas(t *testing.T) {
	t.Run("R-ARCH-018 the first release's areas exist: jobs is a signal, cache a change area", func(t *testing.T) {
		j, ok := LookupArea("jobs")
		c, ok2 := LookupArea("cache")
		if !ok || !ok2 || j.Class != SignalArea || c.Class != ChangeArea || c.PayloadVersion != 1 {
			t.Fatalf("jobs=%+v cache=%+v", j, c)
		}
	})
	t.Run("R-ARCH-018 an area name matches ^[a-z][a-z0-9_]{1,31}$", func(t *testing.T) {
		for _, bad := range []string{"", "a", "Auth", "1auth", "has-dash", strings.Repeat("a", 33), "with space"} {
			expectPanic(t, "area "+bad, func() { RegisterArea(Area{Name: bad, Class: SignalArea}) })
		}
		expectPanic(t, "a duplicate", func() { RegisterArea(Area{Name: "jobs", Class: SignalArea}) })
		expectPanic(t, "a change area without a payload version", func() { RegisterArea(Area{Name: "unit_nover", Class: ChangeArea}) })
		expectPanic(t, "an unknown class", func() { RegisterArea(Area{Name: "unit_class", Class: "other"}) })
	})
}

func TestLeaseRegistry(t *testing.T) {
	t.Run("R-ARCH-017 the scheduler lease is number 1", func(t *testing.T) {
		if LeaseScheduler.Number() != 1 || LeaseScheduler.Name() != "scheduler" {
			t.Fatalf("%+v", LeaseScheduler)
		}
	})
	t.Run("R-ARCH-017 lease numbers and names are registered once", func(t *testing.T) {
		RegisterLease("unit_lease_a", 9001)
		expectPanic(t, "a duplicate number", func() { RegisterLease("unit_lease_b", 9001) })
		expectPanic(t, "a duplicate name", func() { RegisterLease("unit_lease_a", 9002) })
		expectPanic(t, "number 0", func() { RegisterLease("unit_lease_c", 0) })
	})
	t.Run("R-ARCH-017 the categories are the registered numbers", func(t *testing.T) {
		if LockLease != 1 || LockFetchRetired != 2 || LockMigration != 3 || LockConfig != 4 {
			t.Fatal("a category number changed; categories are never renumbered or reused")
		}
	})
	t.Run("R-ARCH-017 a web replica cannot construct the lease manager", func(t *testing.T) {
		if _, err := NewLeases(LeaseOptions{}); !errors.Is(err, ErrNoLeasePool) {
			t.Fatalf("err = %v", err)
		}
	})
}

func newTestSignals(t *testing.T, bus Bus, window time.Duration) (*Signals, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	s, err := NewSignals(SignalsOptions{Bus: bus, Config: config.Cluster{SignalCoalesceWindow: window}, Metrics: reg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, reg
}

func TestSignals(t *testing.T) {
	t.Run("R-ARCH-018 a rollback publishes nothing and a commit publishes once", func(t *testing.T) {
		bus := newFakeBus()
		s, _ := newTestSignals(t, bus, 10*time.Millisecond)
		rolledBack := newFakeTx(nil)
		s.Notify(rolledBack, "jobs", "default")
		// no commit: the After hooks never run
		time.Sleep(30 * time.Millisecond)
		if bus.count() != 0 {
			t.Fatal("published before the commit")
		}
		committed := newFakeTx(nil)
		s.Notify(committed, "jobs", "default")
		_ = committed.commit(context.Background())
		waitFor(t, "the signal", time.Second, func() bool { return bus.count() == 1 })
	})
	t.Run("R-ARCH-018 at most one message per area per window, payloads batched", func(t *testing.T) {
		bus := newFakeBus()
		s, _ := newTestSignals(t, bus, 100*time.Millisecond)
		s.Publish("jobs", "default") // leaves at once
		waitFor(t, "the leading signal", time.Second, func() bool { return bus.count() == 1 })
		for i := range 20 {
			s.Publish("jobs", fmt.Sprintf("q%d", i%3))
		}
		time.Sleep(30 * time.Millisecond)
		if bus.count() != 1 {
			t.Fatalf("%d messages inside one window", bus.count())
		}
		waitFor(t, "the trailing batch", time.Second, func() bool { return bus.count() == 2 })
		time.Sleep(150 * time.Millisecond)
		if bus.count() != 2 {
			t.Fatalf("%d messages for 21 signals", bus.count())
		}
		bus.mu.Lock()
		last := bus.published[1]
		bus.mu.Unlock()
		for _, q := range []string{"q0", "q1", "q2"} {
			if strings.Count(last, `"`+q+`"`) != 1 {
				t.Errorf("batch %s: %s is missing or repeated", last, q)
			}
		}
	})
	t.Run("R-ARCH-018 with Valkey unavailable a signal is dropped and counted", func(t *testing.T) {
		bus := newFakeBus()
		bus.setAvailable(false)
		s, reg := newTestSignals(t, bus, time.Millisecond)
		s.Publish("jobs", "default")
		waitFor(t, "the drop", time.Second, func() bool { return gather(t, reg, "trove_cluster_signals_dropped_total", "area", "jobs") == 1 })
		if bus.count() != 0 {
			t.Fatal("published while unavailable")
		}
		if len(bus.degraded) == 0 {
			t.Error("the fallback was not counted as degraded")
		}
	})
	t.Run("R-ARCH-018 a replica without a bus still wakes its own subscribers", func(t *testing.T) {
		s, _ := newTestSignals(t, nil, time.Millisecond)
		got := make(chan []string, 1)
		cancel, err := s.Subscribe(context.Background(), "jobs", func(p []string, _ bool) { got <- p })
		if err != nil {
			t.Fatal(err)
		}
		defer cancel()
		s.Publish("jobs", "index")
		select {
		case p := <-got:
			if len(p) != 1 || p[0] != "index" {
				t.Fatalf("payloads = %v", p)
			}
		case <-time.After(time.Second):
			t.Fatal("no local delivery")
		}
	})
	t.Run("R-ARCH-018 a message from the bus reaches the subscribers; garbage is ignored", func(t *testing.T) {
		bus := newFakeBus()
		s, _ := newTestSignals(t, bus, time.Millisecond)
		got := make(chan []string, 2)
		if _, err := s.Subscribe(context.Background(), "jobs", func(p []string, _ bool) { got <- p }); err != nil {
			t.Fatal(err)
		}
		ch, _ := bus.channel(sigChannels, "jobs")
		for _, h := range bus.handlers[ch.name] {
			h(Message{Payload: "not json"})
			h(Message{Payload: `{"p":["maintenance"]}`})
		}
		select {
		case p := <-got:
			if len(p) != 1 || p[0] != "maintenance" {
				t.Fatalf("payloads = %v", p)
			}
		case <-time.After(time.Second):
			t.Fatal("no delivery")
		}
	})
	t.Run("R-ARCH-018 a batch above 1 KiB becomes a coarse signal", func(t *testing.T) {
		var big []string
		for i := range 100 {
			big = append(big, fmt.Sprintf("a-rather-long-queue-payload-%03d", i))
		}
		body, err := encodeSignal(big, false)
		if err != nil || len(body) > MaxValueSize || !strings.Contains(body, `"c":true`) {
			t.Fatalf("body = %q, %v", body, err)
		}
	})
	t.Run("R-ARCH-018 only registered signal areas are accepted", func(t *testing.T) {
		s, _ := newTestSignals(t, nil, time.Millisecond)
		if _, err := s.Subscribe(context.Background(), "cache", func([]string, bool) {}); err == nil {
			t.Error("a change area was subscribed as a signal area")
		}
		if _, err := s.Subscribe(context.Background(), "nonexistent", func([]string, bool) {}); err == nil {
			t.Error("an unknown area was subscribed")
		}
	})
}

func TestNotify(t *testing.T) {
	RegisterArea(Area{Name: "unit_a", Owner: "test", Class: ChangeArea, PayloadVersion: 2})
	RegisterArea(Area{Name: "unit_b", Owner: "test", Class: ChangeArea, PayloadVersion: 1})
	newChanges := func(t *testing.T, bus Bus) (*Changes, *recordingListener, *prometheus.Registry) {
		t.Helper()
		reg := prometheus.NewRegistry()
		c, err := NewChanges(ChangesOptions{DB: nopDB{}, Bus: bus, Config: config.Cluster{EpochPollInterval: time.Second, EpochMaxStaleness: 20 * time.Second}, Metrics: reg})
		if err != nil {
			t.Fatal(err)
		}
		c.sql = fakeChangeSQL{}
		l := &recordingListener{}
		c.AddListener(l)
		return c, l, reg
	}
	run := func(c *Changes) (stop func()) {
		ctx, cancel := context.WithCancel(context.Background())
		c.wg.Add(1)
		go c.publishLoop(ctx)
		return func() { cancel(); c.wg.Wait() }
	}

	t.Run("R-ARCH-018 areas are written in name order whatever the call order", func(t *testing.T) {
		c, _, _ := newChanges(t, nil)
		tx := newFakeTx(map[string]int64{})
		for _, a := range []string{"unit_b", "unit_a", "unit_b"} {
			if err := c.Notify(tx, a, 1, map[string]string{"x": a}); err != nil {
				t.Fatal(err)
			}
		}
		if len(tx.before) != 1 || len(tx.after) != 1 {
			t.Fatalf("hooks: %d before, %d after; one each per transaction", len(tx.before), len(tx.after))
		}
		if err := tx.commit(context.Background()); err != nil {
			t.Fatal(err)
		}
		var bumps []string
		for _, s := range tx.stmts {
			if strings.HasPrefix(s, "BUMP") {
				bumps = append(bumps, s)
			}
		}
		if got := strings.Join(bumps, ","); got != "BUMP unit_a,BUMP unit_b,BUMP unit_b" {
			t.Fatalf("epoch updates = %s", got)
		}
	})
	t.Run("R-ARCH-018 the writer applies its own invalidation after commit, and a rollback applies nothing", func(t *testing.T) {
		c, l, _ := newChanges(t, nil)
		rolled := newFakeTx(map[string]int64{})
		_ = c.Notify(rolled, "unit_a", 1, map[string]int{"id": 1})
		// never committed
		if ch, _ := l.snapshot(); len(ch) != 0 {
			t.Fatal("applied before commit")
		}
		tx := newFakeTx(map[string]int64{})
		_ = c.Notify(tx, "unit_a", 1, map[string]int{"id": 2})
		_ = tx.commit(context.Background())
		ch, _ := l.snapshot()
		if len(ch) != 1 || ch[0] != `unit_a:{"id":2}` {
			t.Fatalf("applied = %v", ch)
		}
		if c.Applied("unit_a") != 0 {
			t.Fatal("a local application advanced the applied epoch; only the poll may")
		}
	})
	t.Run("R-ARCH-018 the change message carries area, epoch, version and payload", func(t *testing.T) {
		bus := newFakeBus()
		c, _, _ := newChanges(t, bus)
		defer run(c)()
		tx := newFakeTx(map[string]int64{"unit_a": 6})
		_ = c.Notify(tx, "unit_a", 2, map[string]string{"k": "v"})
		_ = tx.commit(context.Background())
		waitFor(t, "the message", time.Second, func() bool { return bus.count() == 1 })
		bus.mu.Lock()
		msg := bus.published[0]
		bus.mu.Unlock()
		if !strings.HasPrefix(msg, "trove:test:inv:v1 ") {
			t.Fatalf("channel: %s", msg)
		}
		var w wireChange
		if err := json.Unmarshal([]byte(strings.TrimPrefix(msg, "trove:test:inv:v1 ")), &w); err != nil {
			t.Fatal(err)
		}
		if w.A != "unit_a" || w.E != 7 || w.V != 2 || string(w.P) != `{"k":"v"}` {
			t.Fatalf("message = %+v", w)
		}
	})
	t.Run("R-ARCH-018 with Valkey unavailable the message is dropped and counted", func(t *testing.T) {
		bus := newFakeBus()
		bus.setAvailable(false)
		c, _, reg := newChanges(t, bus)
		defer run(c)()
		tx := newFakeTx(map[string]int64{})
		_ = c.Notify(tx, "unit_a", 1, 1)
		_ = tx.commit(context.Background())
		waitFor(t, "the drop", time.Second, func() bool { return gather(t, reg, "trove_cluster_changes_dropped_total", "area", "unit_a") == 1 })
	})
	t.Run("R-ARCH-018 a payload above 1 KiB, a bad version and an unknown area are refused", func(t *testing.T) {
		c, _, _ := newChanges(t, nil)
		tx := newFakeTx(map[string]int64{})
		if err := c.Notify(tx, "unit_a", 1, strings.Repeat("x", 1100)); !errors.Is(err, ErrPayloadTooLarge) {
			t.Errorf("large payload: %v", err)
		}
		if err := c.Notify(tx, "unit_b", 2, 1); err == nil {
			t.Error("version above the area's was accepted")
		}
		if err := c.Notify(tx, "unit_a", 0, 1); err == nil {
			t.Error("version 0 was accepted")
		}
		if err := c.Notify(tx, "jobs", 1, 1); err == nil {
			t.Error("a signal area was accepted as a change area")
		}
		if err := c.Notify(tx, "nonexistent", 1, 1); err == nil {
			t.Error("an unknown area was accepted")
		}
	})
}

func TestMessages(t *testing.T) {
	RegisterArea(Area{Name: "unit_msg", Owner: "test", Class: ChangeArea, PayloadVersion: 1})
	newChanges := func(t *testing.T) (*Changes, *recordingListener) {
		c, err := NewChanges(ChangesOptions{DB: nopDB{}, Config: config.Cluster{EpochPollInterval: time.Second, EpochMaxStaleness: 20 * time.Second}})
		if err != nil {
			t.Fatal(err)
		}
		l := &recordingListener{}
		c.AddListener(l)
		return c, l
	}
	t.Run("R-ARCH-018 a message evicts early and never advances the applied epoch", func(t *testing.T) {
		c, l := newChanges(t)
		c.handleMessage(Message{Payload: `{"a":"unit_msg","e":9,"v":1,"p":{"id":3}}`})
		ch, _ := l.snapshot()
		if len(ch) != 1 || ch[0] != `unit_msg:{"id":3}` {
			t.Fatalf("applied = %v", ch)
		}
		if c.Applied("unit_msg") != 0 {
			t.Fatal("a message advanced the applied epoch")
		}
	})
	t.Run("R-ARCH-018 a message of an unknown payload version flushes the area", func(t *testing.T) {
		c, l := newChanges(t)
		c.handleMessage(Message{Payload: `{"a":"unit_msg","e":9,"v":2,"p":{}}`})
		_, fl := l.snapshot()
		if len(fl) != 1 || fl[0] != "unit_msg:unknown_payload" {
			t.Fatalf("flushes = %v", fl)
		}
	})
	t.Run("R-ARCH-018 a forged or foreign message can only be ignored", func(t *testing.T) {
		c, l := newChanges(t)
		for _, p := range []string{"", "{", `{"a":"nonexistent","e":1,"v":1}`, `{"a":"jobs","e":1,"v":1}`, `[]`} {
			c.handleMessage(Message{Payload: p})
		}
		ch, fl := l.snapshot()
		if len(ch)+len(fl) != 0 {
			t.Fatalf("changes %v flushes %v", ch, fl)
		}
	})
	t.Run("R-ARCH-018 a listener that panics does not stop the others", func(t *testing.T) {
		c, l := newChanges(t)
		c.listeners = append([]Listener{panicListener{}}, c.listeners...)
		c.handleMessage(Message{Payload: `{"a":"unit_msg","e":1,"v":1,"p":{}}`})
		if ch, _ := l.snapshot(); len(ch) != 1 {
			t.Fatal("the listener after the panicking one did not run")
		}
	})
	t.Run("R-ARCH-018 freshness: no poll, no readiness; a recent poll passes the staleness bound", func(t *testing.T) {
		c, _ := newChanges(t)
		if c.Fresh() || c.FreshWithin(time.Hour) || c.Check(context.Background()) == nil {
			t.Fatal("fresh before any poll")
		}
		c.fmu.Lock()
		c.lastStart, c.lastEnd = time.Now().Add(-5*time.Second), time.Now().Add(-4*time.Second)
		c.fmu.Unlock()
		if !c.Fresh() || c.Check(context.Background()) != nil {
			t.Error("a poll 4 s ago is not fresh under a 20 s bound")
		}
		if !c.FreshWithin(10*time.Second) || c.FreshWithin(2*time.Second) {
			t.Error("FreshWithin does not follow the poll's start")
		}
	})
}

type panicListener struct{}

func (panicListener) ApplyChange(string, int, json.RawMessage) { panic("boom") }
func (panicListener) FlushArea(string, string)                 { panic("boom") }

func TestMetricNames(t *testing.T) {
	t.Run("R-OPS-007 the coordination families follow the naming and label rules", func(t *testing.T) {
		reg := prometheus.NewRegistry()
		if _, err := NewChanges(ChangesOptions{DB: nopDB{}, Config: config.Cluster{EpochPollInterval: time.Second}, Metrics: reg}); err != nil {
			t.Fatal(err)
		}
		if _, err := NewSignals(SignalsOptions{Config: config.Cluster{}, Metrics: reg}); err != nil {
			t.Fatal(err)
		}
		// touch every labelled family so that it is exposed
		c, _ := NewChanges(ChangesOptions{DB: nopDB{}, Config: config.Cluster{}, Metrics: prometheus.NewRegistry()})
		_ = c
		fams, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range observability.CheckFamilies(fams) {
			t.Error(e)
		}
	})
	t.Run("R-OPS-007 two instances on one registerer are a start-up error", func(t *testing.T) {
		reg := prometheus.NewRegistry()
		if _, err := NewSignals(SignalsOptions{Metrics: reg}); err != nil {
			t.Fatal(err)
		}
		if _, err := NewSignals(SignalsOptions{Metrics: reg}); err == nil {
			t.Fatal("registered twice")
		}
	})
}
