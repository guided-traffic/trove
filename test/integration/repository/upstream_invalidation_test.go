//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/proxy/invalidation"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestUpstreamChangeInvalidatesTheCache binds the hook the way the composition
// root does (wire_proxy.go) and shows that a change of the upstream records the
// scope-all invalidation in the transaction of the update (R-REPO-011, R-REPO-045).
func TestUpstreamChangeInvalidatesTheCache(t *testing.T) {
	e := newEnv(t)
	stub := newStubUpstream(t)
	r := e.newRemoteReplica("inv-hook", writeKeyring(t, "2026-10", "2026-10"), stub.srv)
	ctx := context.Background()
	svc, err := invalidation.NewService(invalidation.ServiceOptions{Runner: r.db, Notifier: r.changes, Recorder: r.rec})
	if err != nil {
		t.Fatal(err)
	}
	r.svc.Bind(repository.RemoteDeps{
		Ring: r.ring,
		OnUpstreamChanged: func(ctx context.Context, tx meta.Tx, c repository.Caller, repo repository.Repository) error {
			_, err := svc.InvalidateTx(ctx, tx, c, repo, "", invalidation.ScopeAll)
			return err
		},
	})
	r.create(t, "npm-inv", fmt.Sprintf(`{"url":%q}`, stub.srv.URL))
	patch := func(p string) error {
		_, err := r.svc.Patch(ctx, caller, "npm-inv", repository.MatchAny(), mergePatch(p))
		return err
	}

	t.Run("R-REPO-011 R-REPO-045 a change that is not the upstream's identity records no invalidation", func(t *testing.T) {
		if err := patch(`{"description":"x"}`); err != nil {
			t.Fatal(err)
		}
		if n := e.invalidationRows(); n != 0 {
			t.Fatalf("%d invalidation rows", n)
		}
	})

	t.Run("R-REPO-011 R-REPO-045 a new url records one scope-all invalidation of the whole repository, with its change and its event", func(t *testing.T) {
		changes := e.scalar(`SELECT count(*) FROM cluster_change WHERE area = 'cache_invalidation'`)
		events := len(r.rec.types())
		if err := patch(`{"remote":{"url":"https://other.example.com/"}}`); err != nil {
			t.Fatal(err)
		}
		if n := e.scalar(`SELECT count(*) FROM remote_cache_invalidations WHERE scope = 'all' AND prefix = ''`); n != 1 {
			t.Fatalf("%d scope-all rows", n)
		}
		if got := e.scalar(`SELECT count(*) FROM cluster_change WHERE area = 'cache_invalidation'`); got != changes+1 {
			t.Fatalf("change rows %d -> %d", changes, got)
		}
		// The update's own event and the invalidation's.
		if got := len(r.rec.types()); got != events+2 {
			t.Fatalf("events %d -> %d: %v", events, got, r.rec.types())
		}
	})

	t.Run("R-REPO-045 an invalidation that cannot be recorded rolls the update back", func(t *testing.T) {
		rows, before := e.invalidationRows(), e.scalar(`SELECT config_version FROM repositories WHERE key = 'npm-inv'`)
		r.rec.setFail(true)
		defer r.rec.setFail(false)
		if err := patch(`{"remote":{"url":"https://third.example.com/"}}`); err == nil {
			t.Fatal("the update succeeded although its invalidation could not be recorded")
		}
		if e.invalidationRows() != rows || e.scalar(`SELECT config_version FROM repositories WHERE key = 'npm-inv'`) != before {
			t.Fatal("a rolled-back update left a row or a version behind")
		}
	})
}
