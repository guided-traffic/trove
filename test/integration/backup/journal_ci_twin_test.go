//go:build integration

package backup

import (
	"context"
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/journal/replay"
	"github.com/guided-traffic/trove/internal/journal/ship"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The twin databases of the replay tests of the exposure and CI entries (R-EP-020, R-CI-020): a migrated database at
// the point in time t, and its copy made at t with CREATE DATABASE … TEMPLATE, which stands for the database a restore
// brings back. A test changes the source after t through the services the API uses, so the audit writer fills
// revocation_outbox with the producers' real entries; the shipper of the journal writes them to an object store, and the
// replayer of the journal reads them from there and applies them to the copy, one transaction per entry, as
// `trove backup restore` and `trove admin post-restore` do (R-HA-054, R-BAK-024).

// twinPrefix is the blob prefix of the journal the tests ship to.
const twinPrefix = "trove"

// twin is the pair of databases.
type twin struct {
	t        *testing.T
	maint    *pgx.Conn
	base     config.Database
	src      string
	restored string
	// at is t, the moment of the copy.
	at time.Time
}

// newTwin creates and migrates the source database; the copy is made by snapshot.
func newTwin(t *testing.T) *twin {
	t.Helper()
	ctx := context.Background()
	maint, err := pgx.Connect(ctx, testenv.PgDSN(t, "postgres"))
	if err != nil {
		t.Fatalf("connect to PostgreSQL: %v", err)
	}
	tw := &twin{t: t, maint: maint, src: "trove_jr_" + testenv.RandHex(5)}
	tw.restored = tw.src + "_t"
	t.Cleanup(func() {
		for _, db := range []string{tw.restored, tw.src} {
			_, _ = maint.Exec(context.Background(), "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)")
		}
		_ = maint.Close(context.Background())
	})
	if _, err := maint.Exec(ctx, "CREATE DATABASE "+tw.src+" ENCODING 'UTF8' TEMPLATE template0"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	dir := t.TempDir()
	port, err := strconv.Atoi(testenv.Get(t, "TROVE_TEST_PG_PORT"))
	if err != nil {
		t.Fatal(err)
	}
	tw.base = config.Database{
		Host: testenv.Get(t, "TROVE_TEST_PG_HOST"), Port: port, Name: tw.src, Schema: "trove",
		UsernameFile: testenv.WriteFile(t, dir, "db-user", testenv.Get(t, "TROVE_TEST_PG_USER")+"\n"),
		PasswordFile: testenv.WriteFile(t, dir, "db-pass", testenv.Get(t, "TROVE_TEST_PG_PASSWORD")+"\n"),
		SSLMode:      "verify-full", SSLRootCertFile: testenv.CAFile(t), ConnectTimeout: 5 * time.Second, TargetSessionAttrs: "read-write",
		Pooler:        config.DatabasePooler{Mode: "none"},
		Pool:          config.DatabasePool{MaxConns: 4, MinConns: 1, MaxConnLifetime: 30 * time.Minute, MaxConnLifetimeJitter: time.Minute, MaxConnIdleTime: 5 * time.Minute, HealthCheckPeriod: 30 * time.Second},
		LeasePoolSize: 1, LeaseCheckInterval: time.Second,
		Session:         config.DatabaseSession{StatementTimeout: 30 * time.Second, LockTimeout: 5 * time.Second, IdleInTransactionSessionTimeout: 60 * time.Second},
		Retry:           config.DatabaseRetry{InitialBackoff: 50 * time.Millisecond, MaxBackoff: 200 * time.Millisecond, MaxElapsed: 2 * time.Second},
		Keepalive:       config.DatabaseKeepalive{Idle: 15 * time.Second, Interval: 5 * time.Second, Count: 3, UserTimeout: 30 * time.Second},
		ServerKeepalive: config.DatabaseServerKeepalive{Idle: 60 * time.Second, Interval: 10 * time.Second, Count: 6},
		Preflight:       config.DatabasePreflight{ConnectionBudget: "enforce", ReservedConnections: 10, AllowSuperuser: true},
	}
	mctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if _, err := postgres.Migrate(mctx, tw.base, postgres.MigrateOptions{
		Instance: "it-migrate", Version: "0.1.0", Log: testenv.Quiet(),
		Migrate: config.Migrate{AdvisoryLockWait: 20 * time.Second, DDLLockTimeout: 2 * time.Second, DDLLockRetries: 3},
	}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return tw
}

// twinSide is one database of the pair with what the services write through: the pool, the change log (never
// started: no poll and no Valkey, as in the restore Job) and the audit writer (transactional events only).
type twinSide struct {
	t       *testing.T
	name    string
	db      *postgres.DB
	admin   *pgx.Conn
	changes *cluster.Changes
	rec     *audit.Writer
}

// open opens a side; close it before snapshot, which needs the source without sessions.
func (tw *twin) open(name string) *twinSide {
	t := tw.t
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg := tw.base
	cfg.Name = name
	db, err := postgres.Open(ctx, cfg, "it-journal", meta.RoleAll, testenv.Quiet())
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	admin, err := pgx.Connect(ctx, testenv.PgDSN(t, name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "SET search_path TO trove"); err != nil {
		t.Fatal(err)
	}
	changes, err := cluster.NewChanges(cluster.ChangesOptions{
		DB: db.ControlPool(), Config: config.Cluster{EpochPollInterval: time.Second, EpochMaxStaleness: 20 * time.Second},
		WatchdogTimeout: 30 * time.Second, Log: testenv.Quiet(), Metrics: prometheus.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := audit.New(audit.Options{DB: db, Replica: "it-journal", Config: config.Audit{AsyncBuffer: 100}, Log: testenv.Quiet(), AppLog: testenv.Quiet()})
	if err != nil {
		t.Fatal(err)
	}
	s := &twinSide{t: t, name: name, db: db, admin: admin, changes: changes, rec: rec}
	t.Cleanup(s.close)
	return s
}

func (s *twinSide) close() {
	if s.admin != nil {
		_ = s.admin.Close(context.Background())
		s.admin = nil
	}
	if s.db != nil {
		s.db.Close()
		s.db = nil
	}
}

// snapshot copies the source as it is now: the database a restore to this moment brings back.
func (tw *twin) snapshot() {
	tw.t.Helper()
	tw.at = time.Now()
	if _, err := tw.maint.Exec(context.Background(), "CREATE DATABASE "+tw.restored+" TEMPLATE "+tw.src); err != nil {
		tw.t.Fatalf("copy the source at t: %v", err)
	}
}

func (s *twinSide) exec(sql string, args ...any) {
	s.t.Helper()
	if _, err := s.admin.Exec(context.Background(), sql, args...); err != nil {
		s.t.Fatalf("%s: %v", sql, err)
	}
}

func (s *twinSide) text(sql string, args ...any) string {
	s.t.Helper()
	var v string
	if err := s.admin.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		s.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func (s *twinSide) number(sql string, args ...any) int64 {
	s.t.Helper()
	var v int64
	if err := s.admin.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		s.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

// ship writes the side's outbox to the store with the journal's shipper and returns the number of objects.
func (s *twinSide) ship(store journal.Store) int {
	s.t.Helper()
	n, err := ship.New(ship.Options{Outbox: ship.NewOutbox(s.db), Store: store, BlobPrefix: twinPrefix, Log: testenv.Quiet()}).Ship(context.Background())
	if err != nil {
		s.t.Fatalf("ship: %v", err)
	}
	return n
}

// replay runs the journal's replayer over the store on this side, with the default replay margin and the side's change
// log as the notifier (nil leaves it out, as a restore without one), and returns the summary and the printed lines.
func (s *twinSide) replay(store journal.Store, restoredTo time.Time, notifier journal.Notifier) (*journal.Summary, string) {
	s.t.Helper()
	r := replay.New(replay.Options{DB: s.db, Reader: replay.NewReader(store, twinPrefix), Notifier: notifier, Log: testenv.Quiet()})
	var out strings.Builder
	sum, err := r.Replay(context.Background(), journal.ReplayOptions{RestoredTo: restoredTo, Margin: 5 * time.Minute, Out: &out})
	if err != nil {
		s.t.Fatalf("replay: %v", err)
	}
	return sum, out.String()
}

// handWritten writes a journal object as anybody with the runtime credential could and returns its ID.
func handWritten(t *testing.T, store journal.Store, event string, entry []byte) string {
	t.Helper()
	return handWrittenAt(t, store, event, entry, time.Now())
}

// handWrittenAt is handWritten with the time the writer puts in the object, which is the writer's to choose.
func handWrittenAt(t *testing.T, store journal.Store, event string, entry []byte, at time.Time) string {
	t.Helper()
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6], b[8] = b[6]&0x0f|0x70, b[8]&0x3f|0x80
	id := fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
	o := journal.Object{V: journal.Version, ID: id, At: at.UTC(), Event: event, Entry: entry}
	raw, err := o.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := store.Put(context.Background(), journal.Key(twinPrefix, o), raw); err != nil {
		t.Fatal(err)
	}
	return id
}

// outcomes maps "<event> <subject>" to the outcomes the summary reports for it, in order.
func outcomes(sum *journal.Summary) map[string][]journal.Outcome {
	out := map[string][]journal.Outcome{}
	for _, r := range sum.Results {
		k := r.Event + " " + r.Subject
		out[k] = append(out[k], r.Outcome)
	}
	return out
}

// epoch reads the epoch of a change area.
func (s *twinSide) epoch(area string) int64 {
	return s.number("SELECT epoch FROM cluster_epoch WHERE area = $1", area)
}
