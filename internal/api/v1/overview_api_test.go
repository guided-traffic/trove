package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// overviewDB answers the five statements of queries/overview.sql by their name
// with fixed rows; the SQL itself is the integration test's
// (test/integration/postgres/overview_test.go).
type overviewDB struct {
	rows map[string][][]any
}

func (overviewDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("the overview writes nothing")
}

func (d overviewDB) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	rows, err := d.named(sql)
	if err != nil {
		return nil, err
	}
	return &fakeRows{rows: rows, at: -1}, nil
}

func (d overviewDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	rows, err := d.named(sql)
	if err != nil {
		return fakeRow{err: err}
	}
	if len(rows) != 1 {
		return fakeRow{err: fmt.Errorf("%d rows for a statement of one", len(rows))}
	}
	return fakeRow{values: rows[0]}
}

func (d overviewDB) named(sql string) ([][]any, error) {
	for name, rows := range d.rows {
		if strings.HasPrefix(sql, "-- name: "+name+" ") {
			return rows, nil
		}
	}
	return nil, fmt.Errorf("no answer for %.40q", sql)
}

type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return scanInto(r.values, dest)
}

func scanInto(values, dest []any) error {
	if len(values) != len(dest) {
		return fmt.Errorf("%d values for %d destinations", len(values), len(dest))
	}
	for i, v := range values {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(v))
	}
	return nil
}

type fakeRows struct {
	rows [][]any
	at   int
}

func (r *fakeRows) Close()                                       {}
func (r *fakeRows) Err() error                                   { return nil }
func (r *fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *fakeRows) Next() bool                                   { r.at++; return r.at < len(r.rows) }

func (r *fakeRows) Scan(dest ...any) error { return scanInto(r.rows[r.at], dest) }
func (r *fakeRows) Values() ([]any, error) { return r.rows[r.at], nil }
func (r *fakeRows) RawValues() [][]byte    { return nil }
func (r *fakeRows) Conn() *pgx.Conn        { return nil }
func (r *fakeRows) TypeMap() *pgtype.Map   { return nil }

// overviewStore counts the reads; fail makes the next reads fail with err.
type overviewStore struct {
	db    overviewDB
	reads atomic.Int32
	mu    sync.Mutex
	fail  error
}

func (*overviewStore) InTx(context.Context, func(meta.Tx) error) error {
	return errors.New("the overview runs no transaction")
}

func (s *overviewStore) Read(_ context.Context, fn func(q.DBTX) error) error {
	s.reads.Add(1)
	s.mu.Lock()
	err := s.fail
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return fn(s.db)
}

func (s *overviewStore) failWith(err error) { s.mu.Lock(); s.fail = err; s.mu.Unlock() }

func newOverviewStore() *overviewStore {
	return &overviewStore{db: overviewDB{rows: map[string][][]any{
		"OverviewRepositories": {{"local", int32(4)}, {"remote", int32(2)}},
		"OverviewPrincipals":   {{"bastion", int32(1)}, {"group", int32(3)}, {"service_account", int32(5)}, {"user", int32(12)}},
		"OverviewContent":      {{int64(140), int64(1830), int32(9), int32(2)}},
		"OverviewStorage":      {{int64(2210), int64(7_340_032_000), int64(17)}},
		"OverviewJobs":         {{int32(3), int32(1), int32(4)}},
	}}}
}

func overviewSetup(t *testing.T, store *overviewStore, opts func(*Options)) setup {
	t.Helper()
	return newSetup(t, func(o *Options) {
		if store != nil {
			o.API.Store = store
		}
		o.Endpoints.Endpoints = []config.ResolvedEndpoint{{Name: "default"}, {Name: "partners"}}
		if opts != nil {
			opts(o)
		}
	}, authed)
}

// TestR_OPS_004_SystemOverview: GET /api/v1/system/overview reports the counts of PostgreSQL, the configured endpoints
// and its cache time, counts once per cache period, and answers 503 without a database (Q-OPS-175).
func TestR_OPS_004_SystemOverview(t *testing.T) {
	t.Run("the counts, the storage and the job queue as the statements return them", func(t *testing.T) {
		s := overviewSetup(t, newOverviewStore(), nil)
		w := s.get("/api/v1/system/overview")
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		var got gen.SystemOverview
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		c := got.Counts
		if c.Repositories.Total != 6 || !reflect.DeepEqual(c.Repositories.ByClass, map[string]int{"local": 4, "remote": 2, "virtual": 0}) {
			t.Errorf("repositories = %+v (a class without a repository is 0)", c.Repositories)
		}
		if c.Users != 12 || c.Groups != 3 || c.ServiceAccounts != 5 || c.Bastions != 1 {
			t.Errorf("principals = users %d groups %d service accounts %d bastions %d", c.Users, c.Groups, c.ServiceAccounts, c.Bastions)
		}
		if c.Packages != 140 || c.PackageVersions != 1830 || c.ActiveTokens != 9 || c.Projects != 2 || c.Endpoints != 2 {
			t.Errorf("counts = %+v", c)
		}
		if got.Storage.Blobs != 2210 || got.Storage.Bytes != 7_340_032_000 || got.Storage.PendingDeletion != 17 {
			t.Errorf("storage = %+v", got.Storage)
		}
		if got.Jobs.Queued != 3 || got.Jobs.Running != 1 || got.Jobs.FailedLast24h != 4 {
			t.Errorf("jobs = %+v", got.Jobs)
		}
		if got.CacheSeconds != overviewCacheSeconds || got.ComputedAt.IsZero() || time.Since(got.ComputedAt) > time.Minute {
			t.Errorf("cacheSeconds %d computedAt %s", got.CacheSeconds, got.ComputedAt)
		}
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(w.Body.Bytes(), &raw)
		if ts := strings.Trim(string(raw["computedAt"]), `"`); !strings.HasSuffix(ts, "Z") {
			t.Errorf("computedAt = %s", ts)
		}
	})

	t.Run("the answers match the document", func(t *testing.T) {
		val := apitest.New(t, EmbeddedYAML())
		for _, store := range []*overviewStore{newOverviewStore(), nil} {
			s := overviewSetup(t, store, nil)
			h := val.Handler(t, s.handler)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/system/overview", nil))
			if w.Code != 200 && w.Code != 503 {
				t.Errorf("%d %s", w.Code, w.Body.String())
			}
		}
	})

	t.Run("a second request within the cache time is answered without counting again", func(t *testing.T) {
		store := newOverviewStore()
		s := overviewSetup(t, store, nil)
		first, second := s.get("/api/v1/system/overview"), s.get("/api/v1/system/overview")
		if first.Code != 200 || second.Code != 200 || first.Body.String() != second.Body.String() {
			t.Fatalf("%d %d\n%s\n%s", first.Code, second.Code, first.Body, second.Body)
		}
		if n := store.reads.Load(); n != 1 {
			t.Errorf("%d reads, want 1", n)
		}
	})

	t.Run("an unavailable database answers 503 and is not cached", func(t *testing.T) {
		store := newOverviewStore()
		store.failWith(errs.E(errs.Unavailable, errs.CodeDatabaseUnavailable, "the database is not available"))
		s := overviewSetup(t, store, nil)
		if w := s.get("/api/v1/system/overview"); w.Code != 503 || !strings.Contains(w.Body.String(), "database_unavailable") {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		store.failWith(nil)
		if w := s.get("/api/v1/system/overview"); w.Code != 200 {
			t.Fatalf("after the database came back: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("a replica without the store answers 503", func(t *testing.T) {
		s := overviewSetup(t, nil, nil)
		if w := s.get("/api/v1/system/overview"); w.Code != 503 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	})

	t.Run("the overview requires settings:read", func(t *testing.T) {
		if got := requirementOf(t, "getSystemOverview"); got.Permission != PermissionSystemRead || got.ProjectAdmin != "" {
			t.Errorf("declaration = %s, want permission %s", got, PermissionSystemRead)
		}
		store := newOverviewStore()
		s := newSetup(t, func(o *Options) { o.Access = denyAnonymous{}; o.API.Store = store }, nil)
		if w := s.get("/api/v1/system/overview"); w.Code != 401 {
			t.Errorf("anonymous: %d", w.Code)
		}
		if n := store.reads.Load(); n != 0 {
			t.Errorf("a refused request counted (%d reads)", n)
		}
	})
}

// TestR_OPS_003_ReplicaRuntimeMatchesTheDocument: a replica with its runtime and the durations of its checks (Q-OPS-175),
// and a replica of a release before them, are valid members of GET /api/v1/system/replicas.
func TestR_OPS_003_ReplicaRuntimeMatchesTheDocument(t *testing.T) {
	p50, p95, p99, limit, dur := float32(1.5), float32(12.25), float32(80), float32(2), float32(1.234)
	withRuntime := replica("trove-api-1", "web", "n1", gen.Ready, false)
	withRuntime.Checks = map[string]gen.CheckResult{"database": {Status: gen.CheckResultStatusPass, DurationMs: &dur}, "draining": {Status: gen.CheckResultStatusPass}}
	withRuntime.Runtime = &gen.ReplicaRuntime{
		SampledAt: time.Date(2026, 11, 6, 9, 0, 10, 0, time.UTC), WindowSeconds: 10, Requests: 420, Errors: 3, InFlight: 7,
		CpuCores: 0.75, CpuLimitCores: &limit, MemoryBytes: 310 << 20, MemoryLimitBytes: 1 << 30, Goroutines: 211,
	}
	withRuntime.Runtime.LatencyMs.P50, withRuntime.Runtime.LatencyMs.P95, withRuntime.Runtime.LatencyMs.P99 = &p50, &p95, &p99
	idle := replica("trove-api-2", "web", "n1", gen.Ready, false)
	idle.Runtime = &gen.ReplicaRuntime{SampledAt: time.Date(2026, 11, 6, 9, 0, 10, 0, time.UTC), WindowSeconds: 10, CpuLimitCores: &limit}
	old := replica("trove-api-3", "web", "n2", gen.Ready, false)
	s := newSetup(t, func(o *Options) { o.Replicas = fakeReplicas{withRuntime, idle, old} }, authed)
	h := apitest.New(t, EmbeddedYAML()).Handler(t, s.handler)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/system/replicas", nil))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var list struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Items) != 3 {
		t.Fatalf("%v %s", err, w.Body.String())
	}
	if !strings.Contains(string(list.Items[1]["runtime"]), `"latencyMs":{"p50":null,"p95":null,"p99":null}`) {
		t.Errorf("a window without requests: %s", list.Items[1]["runtime"])
	}
	if _, has := list.Items[2]["runtime"]; has {
		t.Errorf("a replica without a runtime lists one: %s", list.Items[2]["runtime"])
	}
}

// requirementOf is the authorization step's form of an operation's x-trove-permission.
func requirementOf(t *testing.T, operationID string) Requirement {
	t.Helper()
	doc, err := LoadDocument(EmbeddedYAML())
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range doc.Operations() {
		if op.ID == operationID {
			return op.Requirement
		}
	}
	t.Fatalf("no operation %s", operationID)
	return Requirement{}
}

// TestR_OPS_004_OverviewCache: the replica's cache of the overview counts once per period, makes concurrent requests
// wait for the running count, never keeps a failed count, and counts again once the period ends.
func TestR_OPS_004_OverviewCache(t *testing.T) {
	now := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	advance := func(d time.Duration) { clockMu.Lock(); now = now.Add(d); clockMu.Unlock() }
	ttl := 30 * time.Second

	t.Run("concurrent requests share one count", func(t *testing.T) {
		var c overviewCache
		var counts atomic.Int32
		release := make(chan struct{})
		count := func(context.Context) (gen.SystemOverview, error) {
			counts.Add(1)
			<-release
			return gen.SystemOverview{CacheSeconds: 30}, nil
		}
		var wg sync.WaitGroup
		results := make(chan error, 8)
		for range 8 {
			wg.Go(func() {
				v, err := c.get(t.Context(), clock, ttl, count)
				if err == nil && v.CacheSeconds != 30 {
					err = fmt.Errorf("value %+v", v)
				}
				results <- err
			})
		}
		waitUntil(t, func() bool { return counts.Load() == 1 })
		time.Sleep(20 * time.Millisecond)
		if len(results) != 0 {
			t.Error("a request returned while the count it waits for still ran")
		}
		close(release)
		wg.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Error(err)
			}
		}
		if n := counts.Load(); n != 1 {
			t.Errorf("%d counts, want 1", n)
		}
	})

	t.Run("a failed count is not kept, and the period ends with the ttl", func(t *testing.T) {
		var c overviewCache
		var counts atomic.Int32
		fail := true
		count := func(context.Context) (gen.SystemOverview, error) {
			counts.Add(1)
			if fail {
				return gen.SystemOverview{}, errors.New("down")
			}
			return gen.SystemOverview{}, nil
		}
		if _, err := c.get(t.Context(), clock, ttl, count); err == nil {
			t.Fatal("the failure was not returned")
		}
		fail = false
		for range 3 {
			if _, err := c.get(t.Context(), clock, ttl, count); err != nil {
				t.Fatal(err)
			}
		}
		if n := counts.Load(); n != 2 {
			t.Fatalf("%d counts, want 2 (the failure, then one count for three requests)", n)
		}
		advance(ttl - time.Second)
		_, _ = c.get(t.Context(), clock, ttl, count)
		if n := counts.Load(); n != 2 {
			t.Fatalf("counted again within the period (%d)", n)
		}
		advance(time.Second)
		_, _ = c.get(t.Context(), clock, ttl, count)
		if n := counts.Load(); n != 3 {
			t.Fatalf("%d counts after the period, want 3", n)
		}
	})

	t.Run("a count that panics releases the requests that wait for it", func(t *testing.T) {
		var c overviewCache
		func() {
			defer func() { _ = recover() }()
			_, _ = c.get(t.Context(), clock, ttl, func(context.Context) (gen.SystemOverview, error) { panic("count") })
		}()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if _, err := c.get(ctx, clock, ttl, func(context.Context) (gen.SystemOverview, error) { return gen.SystemOverview{}, nil }); err != nil {
			t.Fatalf("after a panic: %v", err)
		}
	})
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in 5 s")
		}
		time.Sleep(time.Millisecond)
	}
}
