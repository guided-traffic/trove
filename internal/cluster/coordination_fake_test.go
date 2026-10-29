package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/meta"
)

// fakeBus is a Bus that records what is published and delivers to subscribers.
type fakeBus struct {
	mu        sync.Mutex
	available bool
	published []string // "<channel> <message>"
	handlers  map[string][]Handler
	degraded  []string
	subbed    []func(bool)
}

func newFakeBus() *fakeBus { return &fakeBus{available: true, handlers: map[string][]Handler{}} }

func (b *fakeBus) Available() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.available }
func (b *fakeBus) setAvailable(v bool) {
	b.mu.Lock()
	b.available = v
	b.mu.Unlock()
}

func (b *fakeBus) Publish(_ context.Context, ch Channel, msg string) error {
	b.mu.Lock()
	b.published = append(b.published, ch.name+" "+msg)
	hs := append([]Handler(nil), b.handlers[ch.name]...)
	b.mu.Unlock()
	for _, h := range hs {
		h(Message{Channel: ch.name, Payload: msg})
	}
	return nil
}

func (b *fakeBus) Subscribe(_ context.Context, ch Channel, h Handler) (func(), error) {
	b.mu.Lock()
	b.handlers[ch.name] = append(b.handlers[ch.name], h)
	b.mu.Unlock()
	return func() {}, nil
}

func (b *fakeBus) Channel(f *ChannelFamily, parts ...string) (Channel, error) {
	return Channel{name: "trove:test:" + join("", f.mechanism, f.version, parts)}, nil
}

func (b *fakeBus) OnSubscribed(fn func(bool)) {
	b.mu.Lock()
	b.subbed = append(b.subbed, fn)
	b.mu.Unlock()
}

func (b *fakeBus) CountDegraded(m string) {
	b.mu.Lock()
	b.degraded = append(b.degraded, m)
	b.mu.Unlock()
}
func (b *fakeBus) count() int { b.mu.Lock(); defer b.mu.Unlock(); return len(b.published) }

// fakeTx is a meta.Tx that records statements and runs hooks like the runner.
type fakeTx struct {
	mu     sync.Mutex
	stmts  []string
	epochs map[string]int64
	before []func(context.Context, meta.Tx) error
	after  []func()
}

func newFakeTx(epochs map[string]int64) *fakeTx { return &fakeTx{epochs: epochs} }

func (t *fakeTx) Exec(_ context.Context, sql string, args ...any) (int64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stmts = append(t.stmts, strings.Fields(sql)[0]+" "+argString(args))
	return 1, nil
}

func (t *fakeTx) Query(context.Context, string, ...any) (meta.Rows, error) {
	return nil, errors.New("not used")
}

type fakeRow struct{ epoch int64 }

func (r fakeRow) Scan(dest ...any) error { *dest[0].(*int64) = r.epoch; return nil }

func (t *fakeTx) QueryRow(_ context.Context, sql string, args ...any) meta.Row {
	t.mu.Lock()
	defer t.mu.Unlock()
	area := args[0].(string)
	t.stmts = append(t.stmts, "BUMP "+area)
	t.epochs[area]++
	return fakeRow{t.epochs[area]}
}
func (t *fakeTx) Before(fn func(context.Context, meta.Tx) error) { t.before = append(t.before, fn) }
func (t *fakeTx) After(fn func())                                { t.after = append(t.after, fn) }

func argString(args []any) string {
	var parts []string
	for _, a := range args {
		switch v := a.(type) {
		case string:
			parts = append(parts, v)
		case int64:
			parts = append(parts, "e")
		}
	}
	return strings.Join(parts[:min(len(parts), 1)], " ")
}

// commit runs the hooks as the postgres runner does.
func (t *fakeTx) commit(ctx context.Context) error {
	for i := 0; i < len(t.before); i++ {
		if err := t.before[i](ctx, t); err != nil {
			return err
		}
	}
	for _, f := range t.after {
		f()
	}
	return nil
}

// recordingListener collects applied changes and flushes.
type recordingListener struct {
	mu      sync.Mutex
	changes []string
	flushes []string
}

func (l *recordingListener) ApplyChange(area string, version int, payload json.RawMessage) {
	l.mu.Lock()
	l.changes = append(l.changes, area+":"+string(payload))
	l.mu.Unlock()
}

func (l *recordingListener) FlushArea(area, reason string) {
	l.mu.Lock()
	l.flushes = append(l.flushes, area+":"+reason)
	l.mu.Unlock()
}

func (l *recordingListener) snapshot() (changes, flushes []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.changes...), append([]string(nil), l.flushes...)
}

func waitFor(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s did not happen within %s", what, within)
}

// nopDB is a q.DBTX for tests that never reach the database.
type nopDB struct{}

func (nopDB) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("no database in this test")
}

func (nopDB) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, errors.New("no database in this test")
}
func (nopDB) QueryRow(context.Context, string, ...interface{}) pgx.Row { return errRow{} }

type errRow struct{}

func (errRow) Scan(...any) error { return errors.New("no database in this test") }

// gather reads one series of a counter family by label pairs.
func gather(t *testing.T, reg *prometheus.Registry, name string, kv ...string) float64 {
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
			ok := true
			for i := 0; i < len(kv); i += 2 {
				found := false
				for _, l := range m.GetLabel() {
					if l.GetName() == kv[i] && l.GetValue() == kv[i+1] {
						found = true
					}
				}
				ok = ok && found
			}
			if ok {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func expectPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s did not panic", what)
		}
	}()
	fn()
}
