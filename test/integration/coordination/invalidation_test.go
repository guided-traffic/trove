//go:build integration

package coordination

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/proxy/invalidation"
	"github.com/guided-traffic/trove/internal/repository"
)

// invReplica is a replica with the invalidation set and service of the proxy.
type invReplica struct {
	*replica
	set *invalidation.Set
	svc *invalidation.Service
}

func (e *env) newInvReplica(name string, o replicaOpts) *invReplica {
	e.t.Helper()
	r := e.newReplica(name, o)
	set, err := invalidation.NewSet(invalidation.SetOptions{Runner: r.db, Changes: r.changes, Log: quiet})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := set.Start(context.Background()); err != nil {
		e.t.Fatalf("set.Start: %v", err)
	}
	e.t.Cleanup(func() { _ = set.Stop(context.Background()) })
	svc, err := invalidation.NewService(invalidation.ServiceOptions{Runner: r.db, Notifier: r.changes, Recorder: audit.Nop{}})
	if err != nil {
		e.t.Fatal(err)
	}
	return &invReplica{replica: r, set: set, svc: svc}
}

// TestInvalidationPropagation is R-OPS-021: an invalidation made on one replica is in
// force on the other within cluster.epoch_poll_interval plus 1 s with or without
// Valkey, and within milliseconds while Valkey is available.
func TestInvalidationPropagation(t *testing.T) {
	for _, mode := range []struct {
		name   string
		valkey vkMode
		poll   time.Duration
		bound  func(poll time.Duration) time.Duration
	}{
		// Without the fast path the poll alone carries the change.
		{"without Valkey", vkNone, time.Second, func(poll time.Duration) time.Duration { return poll + time.Second }},
		// The client is constructed degraded and keeps trying: the breaker path of an outage.
		{"with the Valkey client unavailable", vkDead, time.Second, func(poll time.Duration) time.Duration { return poll + time.Second }},
		// With Valkey the poll is set far above the bound, so only the message can explain
		// a change inside it.
		{"with Valkey", vkLive, 10 * time.Second, func(time.Duration) time.Duration { return time.Second }},
	} {
		t.Run("R-OPS-021 R-REPO-045 an invalidation is in force on the other replica "+mode.name, func(t *testing.T) {
			e := newEnv(t)
			var repoID int64
			if err := e.admin.QueryRow(context.Background(),
				`INSERT INTO repositories (key, format, class) VALUES ('npm-remote', 'npm', 'remote') RETURNING id`).Scan(&repoID); err != nil {
				t.Fatal(err)
			}
			repo := repository.Repository{ID: strconv.FormatInt(repoID, 10), Key: "npm-remote", Class: repository.Remote, Format: "npm", Lifecycle: repository.Active}
			cc := defaultCluster()
			cc.EpochPollInterval = mode.poll
			a := e.newInvReplica("inv-a", replicaOpts{role: "web", valkey: mode.valkey, clu: cc})
			b := e.newInvReplica("inv-b", replicaOpts{role: "web", valkey: mode.valkey, clu: cc})
			if mode.valkey == vkLive {
				waitValkey(t, a.replica)
				waitValkey(t, b.replica)
				time.Sleep(300 * time.Millisecond) // the subscriptions are up
			}
			bound := mode.bound(mode.poll)
			validated := time.Now().Add(-time.Hour)
			caller := repository.Caller{}

			var worst time.Duration
			for i := range 5 {
				prefix, path := fmt.Sprintf("pkg-%d", i), fmt.Sprintf("pkg-%d/doc", i)
				if !b.set.MetadataValid(repoID, path, validated) {
					t.Fatalf("round %d: the path is invalid before the call", i)
				}
				if _, err := a.svc.Invalidate(context.Background(), caller, repo, prefix, "all"); err != nil {
					t.Fatal(err)
				}
				returned := time.Now()
				if a.set.MetadataValid(repoID, path, validated) {
					t.Errorf("round %d: the writer does not apply its own invalidation at once", i)
				}
				waitFor(t, fmt.Sprintf("round %d: replica B to apply the invalidation", i), bound+time.Second, func() bool {
					return !b.set.MetadataValid(repoID, path, validated)
				})
				took := time.Since(returned)
				worst = max(worst, took)
				if took > bound {
					t.Errorf("round %d: the invalidation took %s on B; the bound is %s", i, took.Round(time.Millisecond), bound)
				}
				if b.set.NegativeValid(repoID, path, validated) {
					t.Errorf("round %d: scope all left the negative entries valid", i)
				}
				if !b.set.MetadataValid(repoID, "other/doc", validated) {
					t.Errorf("round %d: another prefix was invalidated", i)
				}
				time.Sleep(137 * time.Millisecond) // a different phase of the poll in every round
			}
			t.Logf("%s: the slowest of 5 invalidations took %s on B (bound %s)", mode.name, worst.Round(time.Millisecond), bound)
		})
	}
}
