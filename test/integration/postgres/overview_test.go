//go:build integration

package postgres

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// overviewCounts is every number the statements of queries/overview.sql return, flat, so two readings subtract.
type overviewCounts struct {
	local, remote, virtual, users, groups, serviceAccounts, bastions int32
	packages, versions                                               int64
	activeTokens, projects                                           int32
	blobs, bytes, pending                                            int64
	queued, running, failed                                          int32
}

func readOverview(t *testing.T, qs *q.Queries) overviewCounts {
	t.Helper()
	ctx := context.Background()
	var c overviewCounts
	repos, err := qs.OverviewRepositories(ctx)
	if err != nil {
		t.Fatalf("OverviewRepositories: %v", err)
	}
	for _, r := range repos {
		switch r.Class {
		case "local":
			c.local = r.Repositories
		case "remote":
			c.remote = r.Repositories
		case "virtual":
			c.virtual = r.Repositories
		}
	}
	principals, err := qs.OverviewPrincipals(ctx)
	if err != nil {
		t.Fatalf("OverviewPrincipals: %v", err)
	}
	for _, p := range principals {
		switch p.Kind {
		case "user":
			c.users = p.Principals
		case "group":
			c.groups = p.Principals
		case "service_account":
			c.serviceAccounts = p.Principals
		case "bastion":
			c.bastions = p.Principals
		default:
			t.Errorf("a principal of the kind %q is counted", p.Kind)
		}
	}
	content, err := qs.OverviewContent(ctx)
	if err != nil {
		t.Fatalf("OverviewContent: %v", err)
	}
	c.packages, c.versions, c.activeTokens, c.projects = content.Packages, content.PackageVersions, content.ActiveTokens, content.Projects
	st, err := qs.OverviewStorage(ctx)
	if err != nil {
		t.Fatalf("OverviewStorage: %v", err)
	}
	c.blobs, c.bytes, c.pending = st.Blobs, st.Bytes, st.PendingDeletion
	jobs, err := qs.OverviewJobs(ctx)
	if err != nil {
		t.Fatalf("OverviewJobs: %v", err)
	}
	c.queued, c.running, c.failed = jobs.Queued, jobs.Running, jobs.FailedLast24h
	return c
}

// TestR_OPS_004_OverviewStatements: the statements of GET /api/v1/system/overview against the migrated schema count
// what Q-OPS-175 names: active repositories per class, the four principal kinds with disabled ones, live versions,
// tokens neither revoked nor expired, the storage totals the fold maintains, the GC queue, and the job queue with the
// failures of the last 24 hours. The test compares two readings, so the rows the migrations seed do not matter.
func TestR_OPS_004_OverviewStatements(t *testing.T) {
	st := getStack(t)
	_, _, admin := migrated(t, st)
	mustExec(t, admin, "SET search_path = trove, public")
	qs := q.New(admin)
	before := readOverview(t, qs)

	for _, sql := range []string{
		"INSERT INTO trove.repositories (key, format, class) VALUES ('ov-local', 'npm', 'local'), ('ov-remote', 'npm', 'remote'), ('ov-virtual', 'npm', 'virtual')",
		"UPDATE trove.repositories SET lifecycle = 'deleting' WHERE key = 'ov-virtual'",
		"INSERT INTO trove.packages (repo_id, name, display_name) SELECT id, 'left-pad', 'left-pad' FROM trove.repositories WHERE key = 'ov-local'",
		"INSERT INTO trove.package_versions (package_id, version, display_version) SELECT id, '1.0.0', '1.0.0' FROM trove.packages WHERE name = 'left-pad'",
		"INSERT INTO trove.package_versions (package_id, version, display_version, trashed_at) SELECT id, '0.9.0', '0.9.0', now() FROM trove.packages WHERE name = 'left-pad'",
		"INSERT INTO trove.principal (id, kind, name) VALUES ('0c000000-0000-0000-0000-000000000001', 'user', 'ov-alice')",
		"INSERT INTO trove.principal (id, kind, name, status) VALUES ('0c000000-0000-0000-0000-000000000002', 'user', 'ov-bob', 'disabled')",
		"INSERT INTO trove.principal (id, kind, name) VALUES ('0c000000-0000-0000-0000-000000000003', 'group', 'ov-group')",
		"INSERT INTO trove.principal (id, kind, name) VALUES ('0c000000-0000-0000-0000-000000000004', 'service_account', 'ov-robot')",
		"INSERT INTO trove.principal (id, kind, name) VALUES ('0c000000-0000-0000-0000-000000000005', 'bastion', 'ov-bastion')",
		"INSERT INTO trove.api_token (id, type, secret_sha256, hint, principal_id, description, scope, created_via, expires_at) VALUES " +
			"('0c000000-0000-0000-0000-0000000000b1', 'pat', decode(repeat('a1', 32), 'hex'), 'trv_pat_…a1', '0c000000-0000-0000-0000-000000000001', 'live', '{}', 'api', now() + interval '1 day'), " +
			"('0c000000-0000-0000-0000-0000000000b2', 'pat', decode(repeat('a2', 32), 'hex'), 'trv_pat_…a2', '0c000000-0000-0000-0000-000000000001', 'expired', '{}', 'api', now() - interval '1 hour')",
		"INSERT INTO trove.api_token (id, type, secret_sha256, hint, principal_id, description, scope, created_via, expires_at, revoked_at, revocation_reason) VALUES " +
			"('0c000000-0000-0000-0000-0000000000b3', 'pat', decode(repeat('a3', 32), 'hex'), 'trv_pat_…a3', '0c000000-0000-0000-0000-000000000001', 'revoked', '{}', 'api', now() + interval '1 day', now(), 'owner')",
		"INSERT INTO trove.project (id, key, display_name) VALUES ('0c000000-0000-0000-0000-0000000000c1', 'ov-project', 'Overview')",
		"UPDATE trove.storage_usage_totals SET blobs = blobs + 5, physical_bytes = physical_bytes + 1000 WHERE id = 1",
		"INSERT INTO trove.blob_gc_queue (sha256, enqueued_at) VALUES (decode(repeat('ef', 32), 'hex'), now())",
		"INSERT INTO trove.job (id, kind, state) VALUES ('0c000000-0000-0000-0000-0000000000d1', 'ov', 'queued'), " +
			"('0c000000-0000-0000-0000-0000000000d2', 'ov', 'running'), ('0c000000-0000-0000-0000-0000000000d3', 'ov', 'cancelling')",
		"INSERT INTO trove.job (id, kind, state, finished_at) VALUES ('0c000000-0000-0000-0000-0000000000d4', 'ov', 'failed', now() - interval '1 hour'), " +
			"('0c000000-0000-0000-0000-0000000000d5', 'ov', 'failed', now() - interval '2 days'), ('0c000000-0000-0000-0000-0000000000d6', 'ov', 'succeeded', now())",
	} {
		mustExec(t, admin, sql)
	}

	after := readOverview(t, qs)
	got := overviewCounts{
		local: after.local - before.local, remote: after.remote - before.remote, virtual: after.virtual - before.virtual,
		users: after.users - before.users, groups: after.groups - before.groups,
		serviceAccounts: after.serviceAccounts - before.serviceAccounts, bastions: after.bastions - before.bastions,
		packages: after.packages - before.packages, versions: after.versions - before.versions,
		activeTokens: after.activeTokens - before.activeTokens, projects: after.projects - before.projects,
		blobs: after.blobs - before.blobs, bytes: after.bytes - before.bytes, pending: after.pending - before.pending,
		queued: after.queued - before.queued, running: after.running - before.running, failed: after.failed - before.failed,
	}
	want := overviewCounts{
		local: 1, remote: 1, virtual: 0, users: 2, groups: 1, serviceAccounts: 1, bastions: 1,
		packages: 1, versions: 1, activeTokens: 1, projects: 1, blobs: 5, bytes: 1000, pending: 1,
		queued: 1, running: 2, failed: 1,
	}
	if got != want {
		t.Errorf("the seeded rows count as %+v, want %+v", got, want)
	}
}

// TestR_OPS_003_ReplicaRuntimeColumn: migration 00044 gives the registry the runtime column (Q-OPS-175); a row
// written without it, as a release before it writes, reads as no runtime, and a heartbeat's runtime and check
// durations read back as written.
func TestR_OPS_003_ReplicaRuntimeColumn(t *testing.T) {
	st := getStack(t)
	_, _, admin := migrated(t, st)
	mustExec(t, admin, "SET search_path = trove, public")
	ctx := context.Background()
	sys := config.System{HeartbeatInterval: time.Hour, HeartbeatStaleAfter: 3 * time.Hour}

	mustExec(t, admin, "INSERT INTO trove.replica (instance_uid, name, version, revision, roles, state, secret_key_ids, active_secret_key_id, started_at, heartbeat_at) "+
		"VALUES ('0c000000-0000-0000-0000-0000000000e1', 'ov-old', '0.19.4', 'old', '{api}', 'ready', '{k1}', 'k1', now(), now())")
	if raw := scalar[string](t, admin, "SELECT runtime::text FROM trove.replica WHERE name = 'ov-old'"); raw != "{}" {
		t.Errorf("the default of a row without the column: %s", raw)
	}

	p50, cores := 1.5, 2.0
	rt := &cluster.ReplicaRuntime{
		SampledAt: time.Now().UTC().Truncate(time.Microsecond), WindowSeconds: 10, Requests: 42, Errors: 1, InFlight: 3,
		LatencyMs: cluster.ReplicaLatency{P50: &p50}, CPUCores: 0.25, CPULimitCores: &cores,
		MemoryBytes: 300 << 20, MemoryLimitBytes: 1 << 30, Goroutines: 120,
	}
	dur := 1.234
	reg, err := cluster.NewRegistry(cluster.RegistryOptions{
		DB: admin,
		Identity: cluster.ReplicaIdentity{
			UID: "0c000000-0000-0000-0000-0000000000e2", Name: "ov-new", Version: "0.20.0", Revision: "new", Role: "api",
			StartedAt: time.Now(), SecretKeyIDs: []string{"k1"}, ActiveSecretKeyID: "k1",
		},
		Snapshot: func() cluster.ReplicaSnapshot {
			return cluster.ReplicaSnapshot{
				State: cluster.ReplicaReady, Runtime: rt,
				Checks: map[string]cluster.ReplicaCheck{"database": {Status: "pass", DurationMs: &dur}},
			}
		},
		Config: sys, WatchdogTimeout: 30 * time.Second, Log: quiet,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Start(ctxT(t, 30*time.Second)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.StopHeartbeat)

	infos, err := cluster.ListReplicas(ctx, admin, sys)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]cluster.ReplicaInfo{}
	for _, i := range infos {
		byName[i.Name] = i
	}
	if old := byName["ov-old"]; old.Runtime != nil {
		t.Errorf("the row of a release before the column lists a runtime: %+v", old.Runtime)
	}
	got := byName["ov-new"]
	if !reflect.DeepEqual(got.Runtime, rt) {
		t.Errorf("runtime %+v, want %+v", got.Runtime, rt)
	}
	if d := got.Checks["database"].DurationMs; d == nil || *d != dur {
		t.Errorf("database check: %+v", got.Checks["database"])
	}
}
