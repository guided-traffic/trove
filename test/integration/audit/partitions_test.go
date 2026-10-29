//go:build integration

package audit

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

func partitions(t testing.TB, e *env) int {
	t.Helper()
	return scalar[int](t, e.admin, "SELECT count(*) FROM pg_inherits WHERE inhparent = 'trove.audit_event'::regclass")
}

func currentMonth(t testing.TB, e *env) string {
	t.Helper()
	return scalar[string](t, e.admin, `SELECT to_char(now() AT TIME ZONE 'UTC', 'YYYYMM')`)
}

func TestR_OPS_015_StartUpMaintainsThePartitionsThroughTheRuntimeRole(t *testing.T) {
	e := newEnv(t)
	// A replica that starts months after the last maintenance finds the window
	// gone: simulate it by dropping the partitions of this month and the next.
	mustExec(t, e.admin, "DROP TABLE trove.audit_event_"+currentMonth(t, e))
	mustExec(t, e.admin, `DO $$ BEGIN EXECUTE format('DROP TABLE trove.audit_event_%s',
		to_char(date_trunc('month', now() AT TIME ZONE 'UTC') + interval '1 month', 'YYYYMM')); END $$`)
	store := audit.NewStore(e.openWith(e.runtimeCfg()))
	ctx := context.Background()
	if ok, err := store.PartitionsReady(ctx); err != nil || ok {
		t.Fatalf("PartitionsReady = %v, %v; want false", ok, err)
	}
	if err := store.Prepare(ctx, 365, quiet); err != nil {
		t.Fatalf("Prepare as the runtime role: %v", err)
	}
	if ok, err := store.PartitionsReady(ctx); err != nil || !ok {
		t.Fatalf("PartitionsReady = %v, %v; want true", ok, err)
	}
	if n := partitions(t, e); n != 4 {
		t.Errorf("partitions = %d, want the current and the next three months", n)
	}
	// The recreated partition accepts the runtime role's inserts.
	w, _, _ := e.writer(e.openWith(e.runtimeCfg()), auditCfg(), time.Now)
	w.Record(ctx, hit(1))
	fctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := w.Flush(fctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, e, "audit_event", "action = 'probe.hit.logged'"); n != 1 {
		t.Errorf("rows = %d", n)
	}
}

func TestR_OPS_017_ARuntimeRoleCannotShortenTheRetentionAndAReplicaWarnsNamingMigrate(t *testing.T) {
	e := newEnv(t)
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	store := audit.NewStore(e.openWith(e.runtimeCfg()))
	if err := store.Prepare(context.Background(), 30, log); err != nil {
		t.Fatal(err)
	}
	if got := scalar[int](t, e.admin, "SELECT retention_days FROM trove.audit_policy"); got != 365 {
		t.Errorf("the runtime role changed the retention to %d", got)
	}
	out := logs.String()
	if !strings.Contains(out, "trove migrate") || !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, "below 90") {
		t.Errorf("warnings = %s", out)
	}
}

func TestR_OPS_015_SingleRoleModeWritesTheRetentionAtStartUp(t *testing.T) {
	e := newEnv(t)
	store := audit.NewStore(e.open()) // the owner is the runtime role (H-39)
	if err := store.Prepare(context.Background(), 120, quiet); err != nil {
		t.Fatal(err)
	}
	if got := scalar[int](t, e.admin, "SELECT retention_days FROM trove.audit_policy"); got != 120 {
		t.Errorf("retention = %d, want 120", got)
	}
}

func TestR_OPS_015_MigrateWritesTheRetentionOnEveryRunAndAppliesIt(t *testing.T) {
	e := newEnv(t)
	run := func(days int) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if _, err := postgres.Migrate(ctx, e.cfg, postgres.MigrateOptions{
			Instance: "it-migrate", Version: "0.1.0", Log: quiet, AuditRetentionDays: &days,
			Migrate: config.Migrate{AdvisoryLockWait: 20 * time.Second, DDLLockTimeout: 2 * time.Second, DDLLockRetries: 3},
		}); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}
	stored := func() int { return scalar[int](t, e.admin, "SELECT retention_days FROM trove.audit_policy") }

	run(0) // keep for ever
	mustExec(t, e.admin, "SELECT trove.audit_create_partitions('2025-01-15Z', '2025-04-02Z')")
	run(0)
	if stored() != 0 || partitions(t, e) != 8 {
		t.Fatalf("retention 0: stored %d, partitions %d (want 0 and 8)", stored(), partitions(t, e))
	}

	updated := scalar[time.Time](t, e.admin, "SELECT updated_at FROM trove.audit_policy")
	run(0)
	if again := scalar[time.Time](t, e.admin, "SELECT updated_at FROM trove.audit_policy"); !again.Equal(updated) {
		t.Error("an unchanged retention was written again")
	}

	run(30) // a changed value takes effect with the next migrate: the expired months go
	if stored() != 30 || partitions(t, e) != 4 {
		t.Fatalf("retention 30: stored %d, partitions %d (want 30 and 4)", stored(), partitions(t, e))
	}
}
