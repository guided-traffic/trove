package cluster

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// rowOf is the registry row a heartbeat's parameters become, as ListReplicas reads it back: PostgreSQL stores the
// JSON columns as given.
func rowOf(t *testing.T, p q.UpsertReplicaParams) q.ListReplicasRow {
	t.Helper()
	var uid pgtype.UUID
	if err := uid.Scan(p.InstanceUid); err != nil {
		t.Fatal(err)
	}
	return q.ListReplicasRow{
		InstanceUid: uid, Name: p.Name, Version: p.Version, Revision: p.Revision, Roles: p.Roles, State: p.State,
		Checks: []byte(p.Checks), Leases: []byte(p.Leases), SecretKeyIds: p.SecretKeyIds, ActiveSecretKeyID: p.ActiveSecretKeyID,
		Valkey: []byte(p.Valkey), Runtime: []byte(p.Runtime), StartedAt: p.StartedAt,
		HeartbeatAt: pgtype.Timestamptz{Time: p.StartedAt.Time.Add(10 * time.Second), Valid: true},
	}
}

func testIdentity() ReplicaIdentity {
	return ReplicaIdentity{
		UID: "0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a10", Name: "trove-api-1", Version: "0.20.0", Revision: "abc", Role: "api",
		StartedAt: time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC), SecretKeyIDs: []string{"k1"}, ActiveSecretKeyID: "k1",
	}
}

func f64(v float64) *float64 { return &v }

// TestR_OPS_003_RuntimeAndCheckDurationsRoundTrip: what a heartbeat writes into the row (the runtime of Q-OPS-175 and
// the duration of every check's last run) is what the registry list reads back.
func TestR_OPS_003_RuntimeAndCheckDurationsRoundTrip(t *testing.T) {
	rt := &ReplicaRuntime{
		SampledAt: time.Date(2026, 11, 6, 9, 0, 10, 0, time.UTC), WindowSeconds: 10.002,
		Requests: 420, Errors: 3, InFlight: 7, LatencyMs: ReplicaLatency{P50: f64(1.5), P95: f64(12.25), P99: f64(80)},
		CPUCores: 0.75, CPULimitCores: f64(2), MemoryBytes: 310 << 20, MemoryLimitBytes: 1 << 30, Goroutines: 211,
	}
	snap := ReplicaSnapshot{
		State: ReplicaReady,
		Checks: map[string]ReplicaCheck{
			"database": {Status: "pass", DurationMs: f64(1.234)},
			"valkey":   {Status: "degraded", Reason: "timeout", DurationMs: f64(2000)},
			"draining": {Status: "pass"},
		},
		Runtime: rt,
	}
	p, err := upsertParams(testIdentity(), snap, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Checks, `"draining":{"status":"pass"}`) || !strings.Contains(p.Checks, `"durationMs":1.234`) {
		t.Errorf("checks %s: a duration where a run finished, none where none did", p.Checks)
	}
	got, err := replicaInfoOf(rowOf(t, p))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Runtime, rt) {
		t.Errorf("runtime %+v, want %+v", got.Runtime, rt)
	}
	if !reflect.DeepEqual(got.Checks, snap.Checks) {
		t.Errorf("checks %+v, want %+v", got.Checks, snap.Checks)
	}
	if !reflect.DeepEqual(got.Degraded, []string{"valkey"}) {
		t.Errorf("degraded %v", got.Degraded)
	}
}

// TestR_OPS_003_RowsWithoutRuntime: a heartbeat without a runtime writes '{}', the column's default, which a release
// before Q-OPS-175 leaves in its rows; such a row, and one whose runtime this release cannot read, lists no runtime
// and no check durations, and the list does not fail for it.
func TestR_OPS_003_RowsWithoutRuntime(t *testing.T) {
	p, err := upsertParams(testIdentity(), ReplicaSnapshot{State: ReplicaReady}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Runtime != "{}" || p.Checks != "{}" || p.Valkey != "{}" || p.Leases != "[]" {
		t.Fatalf("params %+v", p)
	}
	for name, raw := range map[string]string{
		"the default":                   "{}",
		"an empty value":                "",
		"a value of another shape":      `{"sampledAt": 5}`,
		"a value without a sample time": `{"requests": 4}`,
	} {
		row := rowOf(t, p)
		row.Runtime = []byte(raw)
		row.Checks = []byte(`{"database":{"status":"pass"}}`) // a release before durations
		got, err := replicaInfoOf(row)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Runtime != nil {
			t.Errorf("%s: runtime %+v", name, got.Runtime)
		}
		if got.Checks["database"].DurationMs != nil {
			t.Errorf("%s: a duration appeared", name)
		}
	}
}

// TestR_OPS_003_RuntimeColumnNames: the stored names are those of the API's ReplicaRuntime (Q-OPS-175); rows are read
// by replicas of other releases, so a rename would lose the value for them.
func TestR_OPS_003_RuntimeColumnNames(t *testing.T) {
	raw, err := json.Marshal(ReplicaRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	want := []string{
		"sampledAt", "windowSeconds", "requests", "errors", "inFlight", "latencyMs", "cpuCores", "cpuLimitCores",
		"memoryBytes", "memoryLimitBytes", "goroutines",
	}
	if len(m) != len(want) {
		t.Errorf("stored keys %v", m)
	}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Errorf("missing %q in %s", k, raw)
		}
	}
	if string(m["latencyMs"]) != `{"p50":null,"p95":null,"p99":null}` {
		t.Errorf("latencyMs = %s", m["latencyMs"])
	}
}
