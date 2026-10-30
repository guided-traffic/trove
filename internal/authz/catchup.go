package authz

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
)

// Know makes a repository usable on this replica as soon as its creation has
// answered, wherever it was created (R-ARCH-011 step 2, R-API-052). It returns at
// once, reading memory only, for a key the grant index holds. For a key it does
// not hold, it looks the key up in the repository snapshot (which asks PostgreSQL
// and runs the change-log poll when the snapshot lacks it) and, if the repository
// exists, brings the index up to it ([Engine.CatchUp]). The result of the lookup
// is not used: the decision answers, and it answers a missing repository like one
// the caller may not read (R-AUTHZ-016, authorization before existence). Only a
// lookup or a catch-up that cannot finish in time returns an error, 503 with
// Retry-After. Decide and Begin call it for the keys of the request, and
// [Engine.Know] for a key a request learns later; no caller repeats it.
func (e *Engine) know(ctx context.Context, repoKey string) error {
	rd := e.o.Repositories
	if rd == nil || repoKey == "" {
		return nil
	}
	if idx := e.cur.Load(); idx != nil && idx.repo(repoKey) != nil {
		return nil
	}
	if _, err := rd.ByKey(ctx, repoKey); err != nil {
		if ee, ok := errs.As(err); ok && ee.Kind == errs.Unavailable {
			return err
		}
		return nil
	}
	return e.CatchUp(ctx, repoKey)
}

// CatchUp brings the grant index up to the database for a repository that the
// index does not hold. It returns at once, reading memory only, when the index
// holds the key, which is every request for a repository that has been known for
// more than a few milliseconds.
//
// [Engine.Know] calls it for a key that the repository snapshot knows and the
// index does not (R-ARCH-011 step 2): the repository was created, through this
// replica or another, and the decision must see it in the grant index before it
// answers, so that a repository is usable on every replica as soon as its creation
// has answered (R-API-052). It reads the epochs from the database with the
// coalesced read of R-AUTHZ-032, requests a build and waits, holding no database
// connection, until an index at least that new is installed. The wait is bounded
// like every wait for the index (R-AUTHZ-028): at its end the answer is 503
// authz_stale with Retry-After. A key that the index of the database's epochs
// does not hold either, such as a repository being deleted, is left to the
// decision, which refuses it.
func (e *Engine) CatchUp(ctx context.Context, repoKey string) error {
	if idx := e.cur.Load(); idx != nil && idx.repo(repoKey) != nil {
		return nil
	}
	stale := Decision{Outcome: DenyStale, Reason: ReasonStale}.Err()
	deadline := e.waitDeadline(ctx)
	rctx, cancel := context.WithDeadline(ctx, deadline)
	want, err := e.epochs.read(rctx, time.Now())
	cancel()
	if err != nil {
		return stale
	}
	for {
		if idx := e.cur.Load(); idx != nil && idx.label.AuthzEpoch >= want.AuthzEpoch && idx.label.RepoEpoch >= want.RepoEpoch {
			return nil
		}
		e.requestBuild(TriggerMinEpoch)
		if time.Now().After(deadline) || ctx.Err() != nil {
			return stale
		}
		select {
		case <-e.installedCh():
		case <-ctx.Done():
		case <-time.After(waitTick):
		}
	}
}
