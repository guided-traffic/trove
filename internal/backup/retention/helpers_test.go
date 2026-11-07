package retention

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/listing/fixture"
	"github.com/guided-traffic/trove/internal/backup/target"
	"github.com/guided-traffic/trove/internal/backup/target/targettest"
)

var testNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

type fakeFacts struct{ f backup.SystemFacts }

func (f fakeFacts) SystemFacts(context.Context) (backup.SystemFacts, error) { return f.f, nil }

type fakeFence struct {
	mu     sync.Mutex
	err    error
	fences int
	ticks  int
}

func (f *fakeFence) Fence(context.Context, backup.State) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fences++
	return f.err
}

func (f *fakeFence) Tick() { f.mu.Lock(); f.ticks++; f.mu.Unlock() }

type fakeAudit struct {
	mu      sync.Mutex
	deleted []string
}

func (a *fakeAudit) BackupDeleted(_ context.Context, id format.ID, reason string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.deleted = append(a.deleted, string(id)+":"+reason)
	return nil
}

// recorder wraps a target and records the order of the delete requests.
type recorder struct {
	*targettest.Mem
	deletes [][]string
	// list, when set, replaces the order in which List reports the keys of a prefix.
	list func(prefix string, keys []string) []string
	// onList runs before every listing; an error fails it.
	onList func() error
}

func (r *recorder) Delete(ctx context.Context, keys []string) error {
	r.deletes = append(r.deletes, slices.Clone(keys))
	return r.Mem.Delete(ctx, keys)
}

func (r *recorder) List(ctx context.Context, prefix string, fn func(target.Object) error) error {
	if r.onList != nil {
		if err := r.onList(); err != nil {
			return err
		}
	}
	if r.list == nil {
		return r.Mem.List(ctx, prefix, fn)
	}
	var objs []target.Object
	if err := r.Mem.List(ctx, prefix, func(o target.Object) error { objs = append(objs, o); return nil }); err != nil {
		return err
	}
	keys := make([]string, len(objs))
	for i, o := range objs {
		keys[i] = o.Key
	}
	byKey := map[string]target.Object{}
	for _, o := range objs {
		byKey[o.Key] = o
	}
	for _, k := range r.list(prefix, keys) {
		if err := fn(byKey[k]); err != nil {
			return err
		}
	}
	return nil
}

type env struct {
	mem   *targettest.Mem
	rec   *recorder
	l     format.Layout
	audit *fakeAudit
	stage *Stage
}

func newEnv(t *testing.T, policy Policy) *env {
	t.Helper()
	mem := targettest.New()
	l := format.Layout{Prefix: "trove"}
	raw, err := format.NewMarker("inst-1", "inc-1", testNow).Encode()
	if err != nil {
		t.Fatal(err)
	}
	mem.Set(l.Marker(), raw)
	rec := &recorder{Mem: mem}
	a := &fakeAudit{}
	return &env{mem: mem, rec: rec, l: l, audit: a, stage: &Stage{
		Store: rec, Layout: l, Policy: policy, SweepInterval: 168 * time.Hour,
		Facts: fakeFacts{backup.SystemFacts{InstallationID: "inst-1", Incarnation: "inc-1"}}, Audit: a,
		Now: func() time.Time { return testNow },
	}}
}

func (e *env) put(t *testing.T, s fixture.Spec) fixture.Backup {
	t.Helper()
	return fixture.Put(t, e.mem, e.l, s)
}

func (e *env) has(key string) bool {
	_, ok := e.mem.Data(key)
	return ok
}

func (e *env) poolKeys() []string {
	var out []string
	for _, k := range e.mem.Keys() {
		if _, ok := e.l.ParsePool(k); ok {
			out = append(out, k)
		}
	}
	return out
}
