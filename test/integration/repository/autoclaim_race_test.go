//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// heldAutoClaim runs the auto claim of a first publish in a transaction that stays open after the claim was decided:
// applied is closed when the call returned, and the transaction commits when release is closed. It stands for a publish
// whose commit has not finished while a second publish makes its decision.
type heldAutoClaim struct {
	applied chan struct{}
	done    chan struct{}
	res     repository.AutoClaimResult
	err     error
}

func (r *replica) holdAutoClaim(repoKey, name string, release <-chan struct{}) *heldAutoClaim {
	r.t.Helper()
	h := &heldAutoClaim{applied: make(chan struct{}), done: make(chan struct{})}
	ctx := context.Background()
	repo, err := r.reader.ByName(ctx, qr(repoKey))
	if err != nil {
		r.t.Fatal(err)
	}
	go func() {
		defer close(h.done)
		var once sync.Once
		h.err = r.db.InTx(ctx, func(tx meta.Tx) error {
			var err error
			h.res, err = r.routing.AutoClaim(ctx, tx, repo, name, caller)
			once.Do(func() { close(h.applied) })
			if err != nil {
				return err
			}
			<-release
			return nil
		})
		once.Do(func() { close(h.applied) })
	}()
	return h
}

// overlapAutoClaims runs two first publishes so that their decisions overlap: the first decides and holds its transaction
// open, the second starts, and the first commits once the second has either finished or is waiting for a lock. Without a
// lock between the two, the second finishes while the first is still open and decides on committed claims alone.
func (e *env) overlapAutoClaims(r *replica, repoA, nameA, repoB, nameB string) (first, second repository.AutoClaimResult) {
	e.t.Helper()
	release := make(chan struct{})
	h1 := r.holdAutoClaim(repoA, nameA, release)
	<-h1.applied
	if h1.err != nil {
		e.t.Fatal(h1.err)
	}
	h2 := r.holdAutoClaim(repoB, nameB, release)
	waitFor(e.t, "the second publish to finish or to wait for a lock", 20*time.Second, func() bool {
		select {
		case <-h2.applied:
			return true
		default:
		}
		return e.scalar(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`) > 0
	})
	close(release)
	<-h1.done
	<-h2.done
	if h1.err != nil || h2.err != nil {
		e.t.Fatalf("the publishes failed: %v, %v", h1.err, h2.err)
	}
	return h1.res, h2.res
}

func TestAutoClaimConcurrentPublishes(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("auto-race", replicaOpts{poll: 300 * time.Millisecond, catalog: pkgCatalog()})

	t.Run("R-REPO-023 two first publishes of one name into two repositories of one virtual repository create one claim", func(t *testing.T) {
		a.mustCreate("shared-a", "local", "pkgfmt", "")
		a.mustCreate("shared-b", "local", "pkgfmt", "")
		a.mustCreate("shared-v", "virtual", "pkgfmt", `"virtual":{"members":["shared-a","shared-b"]}`)
		first, second := e.overlapAutoClaims(a, "shared-a", "contested-name", "shared-b", "contested-name")
		if !first.Created || second.Created || second.Reason != repository.AutoClaimSkipped {
			t.Fatalf("results %+v, %+v: want the first created and the second skipped", first, second)
		}
		if n := e.scalar(`SELECT count(*) FROM namespace_claims WHERE pattern = 'contested-name'`); n != 1 {
			t.Fatalf("%d claims of the name, want 1", n)
		}
	})

	t.Run("R-REPO-023 two first publishes of one name into repositories that share no virtual repository both claim it", func(t *testing.T) {
		a.mustCreate("apart-a", "local", "pkgfmt", "")
		a.mustCreate("apart-b", "local", "pkgfmt", "")
		first, second := e.overlapAutoClaims(a, "apart-a", "apart-name", "apart-b", "apart-name")
		if !first.Created || !second.Created {
			t.Fatalf("results %+v, %+v: want both created", first, second)
		}
	})

	t.Run("R-REPO-023 two first publishes of different names into a repository one claim below the limit create one claim", func(t *testing.T) {
		a.mustCreate("nearly-full", "local", "pkgfmt", "")
		e.exec(`INSERT INTO namespace_claims (repo_id, pattern, source, scope)
		        SELECT r.id, 'n' || i, 'auto', 'members' FROM repositories r, generate_series(1, $1::int) AS i WHERE r.key = 'nearly-full'`, repository.MaxClaims-1)
		first, second := e.overlapAutoClaims(a, "nearly-full", "last-slot-a", "nearly-full", "last-slot-b")
		if !first.Created || second.Created || second.Reason != repository.AutoClaimLimit {
			t.Fatalf("results %+v, %+v: want the first created and the second at the limit", first, second)
		}
		if n := e.scalar(`SELECT count(*) FROM namespace_claims c JOIN repositories r ON r.id = c.repo_id WHERE r.key = 'nearly-full'`); n != repository.MaxClaims {
			t.Fatalf("%d claims, want %d", n, repository.MaxClaims)
		}
	})

	t.Run("R-REPO-023 a publish that rolls back leaves no claim and no lock", func(t *testing.T) {
		a.mustCreate("rolled-a", "local", "pkgfmt", "")
		a.mustCreate("rolled-b", "local", "pkgfmt", "")
		a.mustCreate("rolled-v", "virtual", "pkgfmt", `"virtual":{"members":["rolled-a","rolled-b"]}`)
		ctx := context.Background()
		repo, err := a.reader.ByName(ctx, qr("rolled-a"))
		if err != nil {
			t.Fatal(err)
		}
		errAbort := errs.E(errs.Conflict, errs.CodeConflict, "the publish failed after the claim")
		err = a.db.InTx(ctx, func(tx meta.Tx) error {
			if res, err := a.routing.AutoClaim(ctx, tx, repo, "never-published", caller); err != nil || !res.Created {
				t.Fatalf("result = %+v, %v", res, err)
			}
			return errAbort
		})
		if err == nil {
			t.Fatal("the transaction committed")
		}
		if n := e.scalar(`SELECT count(*) FROM namespace_claims WHERE pattern = 'never-published'`); n != 0 {
			t.Fatalf("%d claims after the rollback", n)
		}
		if res, err := a.autoClaim("rolled-b", "never-published"); err != nil || !res.Created {
			t.Fatalf("result = %+v, %v", res, err)
		}
	})
}
