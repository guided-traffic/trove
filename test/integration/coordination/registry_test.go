//go:build integration

package coordination

import (
	"context"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
)

func (e *env) list(rep *replica, sys config.System) map[string]cluster.ReplicaInfo {
	e.t.Helper()
	infos, err := cluster.ListReplicas(context.Background(), rep.db.Main(), sys)
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]cluster.ReplicaInfo{}
	for _, i := range infos {
		out[i.Name] = i
	}
	return out
}

func TestReplicaRegistry(t *testing.T) {
	e := newEnv(t)
	sys := defaultSystem() // heartbeat 500 ms, stale after 3 s: the same logic as 10 s and 30 s, faster
	web := e.newReplica("reg-web", replicaOpts{role: "web", sys: sys})
	w1 := e.newReplica("reg-worker-1", replicaOpts{role: "worker", sys: sys, noWorker: true})
	w2 := e.newReplica("reg-worker-2", replicaOpts{role: "worker", sys: sys, noWorker: true})
	w1.leases.Hold(itLease, func(ctx context.Context, h *cluster.Held) { <-ctx.Done() })

	t.Run("R-OPS-003 every web and worker replica registers with its role", func(t *testing.T) {
		got := e.list(web, sys)
		for name, role := range map[string]string{"reg-web": "web", "reg-worker-1": "worker", "reg-worker-2": "worker"} {
			r, ok := got[name]
			if !ok {
				t.Fatalf("%s is not listed: %v", name, got)
			}
			if len(r.Roles) != 1 || r.Roles[0] != role || r.Version != "0.1.0" || r.Lost || r.State != "ready" {
				t.Errorf("%s = %+v", name, r)
			}
			if len(r.SecretKeyIDs) != 1 || r.ActiveSecretKeyID != "k1" {
				t.Errorf("%s: key IDs %v / %s", name, r.SecretKeyIDs, r.ActiveSecretKeyID)
			}
		}
	})

	t.Run("R-OPS-003 the heartbeat refreshes the row on PostgreSQL's clock", func(t *testing.T) {
		first := e.list(web, sys)["reg-web"].HeartbeatAt
		waitFor(t, "a newer heartbeat", 5*time.Second, func() bool { return e.list(web, sys)["reg-web"].HeartbeatAt.After(first) })
	})

	t.Run("R-OPS-003 the leases a worker holds are listed", func(t *testing.T) {
		waitFor(t, "the lease in the registry", 5*time.Second, func() bool {
			r := e.list(web, sys)["reg-worker-1"]
			return len(r.Leases) == 1 && r.Leases[0].Name == "it_singleton" && !r.Leases[0].Since.IsZero()
		})
	})

	t.Run("R-OPS-003 a replica that stops heartbeating is listed as lost, and its replacement is listed", func(t *testing.T) {
		w2.registry.StopHeartbeat() // as a killed process: the row stays
		killed := time.Now()
		if e.list(web, sys)["reg-worker-2"].Lost {
			t.Fatal("lost at once")
		}
		e.newReplica("reg-worker-3", replicaOpts{role: "worker", sys: sys, noWorker: true})
		waitFor(t, "reg-worker-2 turning lost", 8*time.Second, func() bool { return e.list(web, sys)["reg-worker-2"].Lost })
		took := time.Since(killed)
		if took < sys.HeartbeatStaleAfter-sys.HeartbeatInterval-200*time.Millisecond {
			t.Errorf("lost after %s, before the stale bound %s could have passed", took, sys.HeartbeatStaleAfter)
		}
		got := e.list(web, sys)
		if r := got["reg-worker-3"]; r.Lost || r.State != "ready" {
			t.Errorf("the replacement: %+v", r)
		}
		if got["reg-worker-1"].Lost || got["reg-web"].Lost {
			t.Error("a live replica is listed as lost")
		}
		t.Logf("stopped replica listed as lost %s after its last heartbeat window (stale_after = %s)", took.Round(100*time.Millisecond), sys.HeartbeatStaleAfter)
	})

	t.Run("R-OPS-003 a draining replica shows draining and a clean shutdown deletes its row", func(t *testing.T) {
		ctx := context.Background()
		w1.registry.Drain(ctx)
		if st := e.list(web, sys)["reg-worker-1"].State; st != "draining" {
			t.Fatalf("state = %s", st)
		}
		time.Sleep(2 * sys.HeartbeatInterval)
		if st := e.list(web, sys)["reg-worker-1"].State; st != "draining" {
			t.Fatalf("a heartbeat after Drain reset the state to %s", st)
		}
		w1.stop()
		if _, ok := e.list(web, sys)["reg-worker-1"]; ok {
			t.Fatal("the row of a replica that shut down cleanly is still listed")
		}
	})

	t.Run("R-OPS-003 the prune deletes rows stale for 24 hours and a live replica re-registers", func(t *testing.T) {
		e.exec(`UPDATE replica SET heartbeat_at = now() - interval '25 hours' WHERE name = 'reg-worker-2'`)
		e.exec(`UPDATE replica SET heartbeat_at = now() - interval '23 hours' WHERE name = 'reg-worker-3'`)
		n, err := cluster.PruneReplicas(context.Background(), web.db.Main(), 24*time.Hour)
		if err != nil || n != 1 {
			t.Fatalf("PruneReplicas = %d, %v, want 1", n, err)
		}
		got := e.list(web, sys)
		if _, ok := got["reg-worker-2"]; ok {
			t.Error("the stale row survived")
		}
		// A row removed from under a live replica comes back with its next heartbeat.
		e.exec(`DELETE FROM replica WHERE name = 'reg-web'`)
		waitFor(t, "reg-web re-registering", 5*time.Second, func() bool { _, ok := e.list(web, sys)["reg-web"]; return ok })
	})
}

// TestReplicaLostWithDefaults is R-OPS-003's bound with the shipped values:
// heartbeat every 10 s, lost after 30 s without one.
func TestReplicaLostWithDefaults(t *testing.T) {
	e := newEnv(t)
	sys := config.System{HeartbeatInterval: 10 * time.Second, HeartbeatStaleAfter: 30 * time.Second}
	web := e.newReplica("def-web", replicaOpts{role: "web", sys: sys})
	victim := e.newReplica("def-worker-1", replicaOpts{role: "worker", sys: sys, noWorker: true})

	t.Run("R-OPS-003 a stopped replica is lost within 30 s and its replacement is listed", func(t *testing.T) {
		victim.registry.StopHeartbeat()
		start := time.Now()
		e.newReplica("def-worker-2", replicaOpts{role: "worker", sys: sys, noWorker: true})
		if e.list(web, sys)["def-worker-1"].Lost {
			t.Fatal("lost at once")
		}
		time.Sleep(20 * time.Second)
		if e.list(web, sys)["def-worker-1"].Lost {
			t.Fatal("lost after 20 s, before the 30 s window")
		}
		waitFor(t, "lost", 12*time.Second, func() bool { return e.list(web, sys)["def-worker-1"].Lost })
		if took := time.Since(start); took > 31*time.Second {
			t.Fatalf("marked lost after %s, want within 30 s of the last heartbeat", took)
		}
		if r := e.list(web, sys)["def-worker-2"]; r.Lost || len(r.Roles) != 1 || r.Roles[0] != "worker" {
			t.Fatalf("the replacement: %+v", r)
		}
	})
}
