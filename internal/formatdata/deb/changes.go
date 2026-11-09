package deb

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// DefaultIndexDelay is deb.index_delay's default, used when Options.IndexDelay is zero.
const DefaultIndexDelay = 2 * time.Second

// indexSpec is the registration of the deb-index kind, without its handler (jobs.go registers the same).
func indexSpec() jobs.KindSpec {
	return jobs.KindSpec{Queue: "index", DedupMode: jobs.Coalesce, MaxAttempts: 5, Timeout: indexTimeout}
}

// enqueue enqueues an automatic build in the caller's transaction (Q-ARCH-384): a waiting build is moved to the earlier time. Without
// a queue nothing is enqueued (the restore's replay, whose fix-up enqueues the full builds).
func (g *group) enqueue(ctx context.Context, tx meta.Tx, repoID int64, delay time.Duration) error {
	if g.o.Jobs == nil {
		return nil
	}
	_, err := g.o.Jobs.Enqueue(ctx, tx, buildSpec(repoID, delay, IndexJobArgs{}, nil, ""))
	return err
}

// enqueueDefault enqueues the build of a change after deb.index_delay (R-DEB-010).
func (g *group) enqueueDefault(ctx context.Context, tx meta.Tx, repoID int64) error {
	delay := g.o.IndexDelay
	if delay <= 0 {
		delay = DefaultIndexDelay
	}
	return g.enqueue(ctx, tx, repoID, delay)
}

// raise raises change_seq of the suites and enqueues the build that publishes the change after deb.index_delay (R-DEB-010). changed
// says that a change happened that a build must publish: a suite created by it starts with change_seq 1 and is not raised, but it
// needs its build as much as a raised one.
func (g *group) raise(ctx context.Context, tx meta.Tx, qs *q.Queries, repoID int64, suites []string, changed bool) error {
	if err := raise(ctx, qs, repoID, suites); err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return g.enqueueDefault(ctx, tx, repoID)
}

// EnqueueFullBuilds enqueues a full build of every local and virtual Debian repository, in tx: the fix-up of a restore (R-DEB-032).
// It needs no queue: the restore holds none, and the workers find the rows by polling.
func EnqueueFullBuilds(ctx context.Context, tx meta.Tx) error {
	qs, err := txQueries(tx)
	if err != nil {
		return err
	}
	ids, err := qs.DebIndexedRepositories(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := jobs.EnqueueTx(ctx, tx, indexSpec(), buildSpec(id, 0, IndexJobArgs{Full: true}, nil, "")); err != nil {
			return err
		}
	}
	return nil
}

// Queries gives the statements of the store a transaction of the composition root runs (the lookup of a repository for a build).
func (s *Store) Queries(tx meta.Tx) (*q.Queries, error) { return txQueries(tx) }
