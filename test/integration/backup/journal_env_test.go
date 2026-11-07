//go:build integration

package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/blob/blobtest"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/journal/replay"
	"github.com/guided-traffic/trove/internal/journal/ship"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

// memStore is an in-memory journal.Store for the tests of the handlers, which need the replay and not the object store.
type memStore struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func newMemStore() *memStore { return &memStore{objs: map[string][]byte{}} }

func (m *memStore) Put(_ context.Context, key string, body []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[key] = append([]byte(nil), body...)
	return nil
}

func (m *memStore) List(_ context.Context, prefix string, fn func(string) error) error {
	m.mu.Lock()
	var keys []string
	for k := range m.objs {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			keys = append(keys, k)
		}
	}
	m.mu.Unlock()
	sortStrings(keys)
	for _, k := range keys {
		if err := fn(k); err != nil {
			return err
		}
	}
	return nil
}

func (m *memStore) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return b, nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// flaky wraps a store and makes Put fail while down is set: the object store stopped, for the shipper only. The shared
// SeaweedFS is never stopped.
type flaky struct {
	journal.Store
	down atomic.Bool
}

var errStoreDown = errors.New("the object store is unreachable")

func (f *flaky) Put(ctx context.Context, key string, body []byte) error {
	if f.down.Load() {
		return errStoreDown
	}
	return f.Store.Put(ctx, key, body)
}

// jenv is one installation with a database, the services that write journal entries, and a journal in the shared SeaweedFS
// below a prefix of its own.
type jenv struct {
	t       *testing.T
	stack   *testenv.Stack
	db      *postgres.DB
	rec     *audit.Writer
	changes *cluster.Changes
	tokens  *token.Service
	manager *principal.Manager
	prefix  string
	client  *s3.Client
	store   *flaky
	outbox  *ship.Outbox
	shipper *ship.Shipper
}

func newJEnv(t *testing.T) *jenv {
	t.Helper()
	stack := testenv.NewStack(t, testenv.StackOptions{Name: "journal"})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, stack.DatabaseConfig(), "journal-it", meta.RoleWeb, quietLog)
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	t.Cleanup(db.Close)
	if err := audit.NewStore(db).Prepare(ctx, 365, quietLog); err != nil {
		t.Fatalf("audit partitions: %v", err)
	}
	rec, err := audit.New(audit.Options{DB: db, Replica: "journal-it", Config: config.Audit{AsyncBuffer: 100}, Log: quietLog, AppLog: quietLog})
	if err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewRegistry()
	changes, err := cluster.NewChanges(cluster.ChangesOptions{
		DB: db.ControlPool(), Config: config.Cluster{EpochPollInterval: time.Second, EpochMaxStaleness: 20 * time.Second, ChangeRetention: time.Hour, SignalCoalesceWindow: 20 * time.Millisecond},
		WatchdogTimeout: 30 * time.Second, Log: quietLog, Metrics: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	caches, err := cache.NewRegistry(changes, reg)
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := principal.NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	gate := principal.Gate{Bound: 2 * time.Second, Metrics: metrics}
	tokCfg := config.AuthTokens{
		CacheTTL: 15 * time.Second, RetentionAfterEnd: 2160 * time.Hour, ExpiryNotice: 168 * time.Hour,
		PAT: config.AuthTokenPAT{DefaultTTL: 2160 * time.Hour, MaxTTL: 8760 * time.Hour}, SAT: config.AuthTokenSAT{DefaultTTL: 2160 * time.Hour, MaxTTL: 8760 * time.Hour},
		CIT: config.AuthTokenCIT{MaxTTL: 6 * time.Hour}, MaxActive: config.AuthTokenMax{PAT: 50, SAT: 20},
	}
	principals := principal.NewStore(principal.StoreOptions{DB: db, Cache: caches, Gate: gate, SafetyTTL: tokCfg.CacheTTL})
	settings := token.NewSettingsStore(token.SettingsOptions{DB: db, Notifier: changes, Recorder: rec, Cache: caches, Gate: gate, Tokens: tokCfg, Anonymous: config.AuthAnonymous{Allowed: true}})
	tokens := token.NewService(token.ServiceOptions{
		DB: db, Notifier: changes, Recorder: rec, Principals: principals, Settings: settings, Cache: caches, Gate: gate, Tokens: tokCfg, Metrics: metrics, Log: quietLog,
	})
	manager := principal.NewManager(principal.ManagerOptions{DB: db, Notifier: changes, Recorder: rec, BreakGlassName: "breakglass"})

	// The journal lives in the blob bucket under a prefix of this test.
	o := testenv.S3Config(t)
	cfg := blobtest.Storage(t, t.TempDir()).S3
	cfg.Endpoint, cfg.Region, cfg.Bucket, cfg.ForcePathStyle, cfg.CAFile = o.Endpoint, o.Region, o.BlobsBucket, true, testenv.CAFile(t)
	raw := make([]byte, 4)
	_, _ = rand.Read(raw)
	cfg.Prefix = "it/journal-" + hex.EncodeToString(raw)
	dir := t.TempDir()
	cfg.Credentials = config.StorageS3Credentials{
		Source: "static", AccessKeyIDFile: testenv.WriteFile(t, dir, "s3-id", o.AccessKey), SecretAccessKeyFile: testenv.WriteFile(t, dir, "s3-secret", o.SecretKey),
	}
	client, err := s3.NewFromFactory(ctx, cfg, outbound.New(config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true}}))
	if err != nil {
		t.Fatalf("object store client: %v", err)
	}
	store := &flaky{Store: s3.NewJournalStore(client, false)}
	outbox := ship.NewOutbox(db)
	return &jenv{
		t: t, stack: stack, db: db, rec: rec, changes: changes, tokens: tokens, manager: manager, prefix: cfg.Prefix, client: client, store: store,
		outbox: outbox, shipper: ship.New(ship.Options{Outbox: outbox, Store: store, BlobPrefix: cfg.Prefix}),
	}
}

// freshTokens is a token service with empty caches over the same database: the replica that starts after a restore.
func (e *jenv) freshTokens() *token.Service {
	e.t.Helper()
	reg := prometheus.NewRegistry()
	caches, err := cache.NewRegistry(e.changes, reg)
	if err != nil {
		e.t.Fatal(err)
	}
	metrics, err := principal.NewMetrics(reg)
	if err != nil {
		e.t.Fatal(err)
	}
	gate := principal.Gate{Bound: 2 * time.Second, Metrics: metrics}
	cfg := config.AuthTokens{CacheTTL: 15 * time.Second, PAT: config.AuthTokenPAT{DefaultTTL: time.Hour, MaxTTL: time.Hour}, SAT: config.AuthTokenSAT{DefaultTTL: time.Hour, MaxTTL: time.Hour}, MaxActive: config.AuthTokenMax{PAT: 50, SAT: 20}}
	principals := principal.NewStore(principal.StoreOptions{DB: e.db, Cache: caches, Gate: gate, SafetyTTL: cfg.CacheTTL})
	settings := token.NewSettingsStore(token.SettingsOptions{DB: e.db, Notifier: e.changes, Recorder: e.rec, Cache: caches, Gate: gate, Tokens: cfg, Anonymous: config.AuthAnonymous{Allowed: true}})
	return token.NewService(token.ServiceOptions{DB: e.db, Notifier: e.changes, Recorder: e.rec, Principals: principals, Settings: settings, Cache: caches, Gate: gate, Tokens: cfg, Metrics: metrics, Log: quietLog})
}

// replayer builds a replayer over the given store.
func (e *jenv) replayer(store journal.Store, rec audit.Recorder) *replay.Replayer {
	return replay.New(replay.Options{
		DB: e.db, Reader: replay.NewReader(store, e.prefix), Recorder: rec, AdminBinding: authz.HasAdminBinding, Log: quietLog,
	})
}

// replayAt runs a replay and returns its lines and summary.
func (e *jenv) replayAt(r *replay.Replayer, restoredTo time.Time) (*journal.Summary, string) {
	e.t.Helper()
	var out stringsBuilder
	sum, err := r.Replay(context.Background(), journal.ReplayOptions{RestoredTo: restoredTo, Margin: time.Minute, Out: &out})
	if err != nil {
		e.t.Fatalf("replay: %v", err)
	}
	return sum, out.String()
}

type stringsBuilder struct{ b []byte }

func (s *stringsBuilder) Write(p []byte) (int, error) { s.b = append(s.b, p...); return len(p), nil }
func (s *stringsBuilder) String() string              { return string(s.b) }

// put writes a hand-built journal object, as anybody with the runtime credential could.
func (e *jenv) put(store journal.Store, at time.Time, event, entry string) string {
	e.t.Helper()
	var id [16]byte
	_, _ = rand.Read(id[:])
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	uuid := fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:])
	o := journal.Object{V: journal.Version, ID: uuid, At: at.UTC(), Event: event, Entry: []byte(entry)}
	body, err := o.Encode()
	if err != nil {
		e.t.Fatalf("encode: %v", err)
	}
	if err := store.Put(context.Background(), journal.Key(e.prefix, o), body); err != nil {
		e.t.Fatalf("put: %v", err)
	}
	return uuid
}

// outcomeOf returns the outcome the summary reports for the entry.
func outcomeOf(t *testing.T, sum *journal.Summary, id string) journal.Outcome {
	t.Helper()
	for _, r := range sum.Results {
		if r.ID == id {
			return r.Outcome
		}
	}
	t.Fatalf("no result for entry %s in %v", id, sum.Results)
	return ""
}
