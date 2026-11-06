package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/internal/observability/health"
)

// TestR_OPS_003_HeartbeatCarriesCheckDurations: the heartbeat stores how long each check's last run took (Q-OPS-175),
// and the replica list renders it as durationMs; a check without a finished run has none. The valkey check only reads
// the client's state, so its duration is the round trip of the client's last probe, and without a client it has none.
func TestR_OPS_003_HeartbeatCarriesCheckDurations(t *testing.T) {
	checks := (&App{}).replicaChecks([]health.CheckResult{
		{Name: "database", Status: health.Pass, Latency: 1234 * time.Microsecond},
		{Name: "valkey", Status: health.Degraded, Reason: "timeout", Latency: 2 * time.Second},
		{Name: health.CheckDraining, Status: health.Pass},
	})
	if d := checks["database"].DurationMs; d == nil || *d != 1.234 {
		t.Errorf("database: %v", checks["database"].DurationMs)
	}
	if d := checks["valkey"].DurationMs; d != nil || checks["valkey"].Reason != "timeout" {
		t.Errorf("valkey without a client takes the check's own duration: %+v", checks["valkey"])
	}
	if checks[health.CheckDraining].DurationMs != nil {
		t.Errorf("draining has a duration: %v", *checks[health.CheckDraining].DurationMs)
	}
	db := checkResultOf(checks["database"])
	if db.DurationMs == nil || *db.DurationMs != float32(1.234) || db.Reason != nil || db.ConfigError != nil {
		t.Errorf("rendered: %+v", db)
	}
	if got := checkResultOf(cluster.ReplicaCheck{Status: "pass"}); got.DurationMs != nil {
		t.Errorf("a check of a release before durations: %+v", got)
	}

	t.Run("the replica list of a shell without a registry carries them too, and no runtime", func(t *testing.T) {
		h := health.New(config.Health{CheckTimeout: time.Second, FailureThreshold: 1, SuccessThreshold: 1})
		h.AddReadiness("database", func(context.Context) error { time.Sleep(2 * time.Millisecond); return nil }, false)
		h.Refresh(t.Context())
		a := &App{health: h, id: Identity{Instance: "trove-api-1", UID: "u-1", Role: config.RoleAPI}, startedAt: time.Now()}
		reps, err := localReplicas{a}.Replicas(t.Context())
		if err != nil || len(reps) != 1 {
			t.Fatalf("%v %v", reps, err)
		}
		if d := reps[0].Checks["database"].DurationMs; d == nil || *d < 2 {
			t.Errorf("database: %v", reps[0].Checks["database"].DurationMs)
		}
		if d := reps[0].Checks[health.CheckDraining].DurationMs; d != nil {
			t.Errorf("draining: %v", *d)
		}
		if reps[0].Runtime != nil {
			t.Errorf("runtime without a heartbeat: %+v", reps[0].Runtime)
		}
	})
}

// TestR_OPS_003_RuntimeSnapshotReachesTheAPI: the runtime a heartbeat stores is the API's ReplicaRuntime member for
// member (the stored JSON decodes into the generated type unchanged), and a row without one renders no runtime.
func TestR_OPS_003_RuntimeSnapshotReachesTheAPI(t *testing.T) {
	p50, p95, p99 := 1.5, 12.25, 80.0
	stored := replicaRuntime(observability.RuntimeSample{
		SampledAt: time.Date(2026, 11, 6, 9, 0, 10, 0, time.FixedZone("CET", 3600)), Window: 10 * time.Second,
		WindowCounts: observability.WindowCounts{Requests: 420, Errors: 3, InFlight: 7, P50: &p50, P95: &p95, P99: &p99},
		CPUCores:     0.75, GOMAXPROCS: 2, MemoryBytes: 310 << 20, MemoryLimitBytes: 1 << 30, Goroutines: 211,
	})
	if stored.SampledAt.Location() != time.UTC || stored.WindowSeconds != 10 || stored.CPULimitCores == nil || *stored.CPULimitCores != 2 {
		t.Fatalf("stored: %+v", stored)
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	var viaJSON gen.ReplicaRuntime
	if err := json.Unmarshal(raw, &viaJSON); err != nil {
		t.Fatal(err)
	}
	if got := runtimeOf(stored); !reflect.DeepEqual(*got, viaJSON) {
		t.Errorf("rendered %+v\nthe stored JSON reads as %+v", *got, viaJSON)
	}
	apiRaw, _ := json.Marshal(runtimeOf(stored))
	if a, b := keys(t, raw), keys(t, apiRaw); !reflect.DeepEqual(a, b) {
		t.Errorf("stored keys %v, API keys %v", a, b)
	}
	if runtimeOf(nil) != nil || replicaOf(cluster.ReplicaInfo{}).Runtime != nil {
		t.Error("a row without a runtime renders one")
	}
	if r := replicaOf(cluster.ReplicaInfo{Runtime: stored}); r.Runtime == nil || r.Runtime.Requests != 420 || *r.Runtime.LatencyMs.P95 != 12.25 {
		t.Errorf("replicaOf: %+v", r.Runtime)
	}
}

// TestR_OPS_003_OnlyTheClientListenerCountsIntoTheWindow: the requests of every client endpoint's chain count into the
// runtime snapshot's window, the probes and scrapes of the ops listener do not.
func TestR_OPS_003_OnlyTheClientListenerCountsIntoTheWindow(t *testing.T) {
	cfg := &config.Config{}
	tele, err := newTelemetry(cfg, Identity{Instance: "trove-api-1", Role: config.RoleAPI}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	ok := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	_, client := tele.clientStages(cfg, config.DefaultEndpoint)
	_, partners := tele.clientStages(cfg, "partners")
	for _, h := range []http.Handler{client(ok), partners(ok), tele.opsStages(cfg)(ok)} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}
	if got := tele.runtime.Sample(); got.Requests != 2 || got.InFlight != 0 {
		t.Errorf("window: %+v, want the 2 client requests", got.WindowCounts)
	}
}

func keys(t *testing.T, raw []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
