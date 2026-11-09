package deb

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
)

// BuildStore is the part of the data port that the index builder reads and writes (R-DEB-010 to R-DEB-014): the suites of a
// repository, the inputs of a build in one snapshot and the record of a generation. Built by the work package of the index builder.
type BuildStore interface {
	// Suites lists the suites of a repository with their state: the index state read of R-DEB-014.
	Suites(ctx context.Context, repoID int64) ([]rows.Suite, error)
	// WaitPublished returns the suite once its built_seq has reached seq, or after max with the state it has reached
	// (deb.index_wait_max): the waitFor of R-DEB-014. found is false for a suite the repository does not have.
	WaitPublished(ctx context.Context, repoID int64, suite string, seq int64, max time.Duration) (s rows.Suite, found bool, err error)
	// GenerationInputs reads, in the build's REPEATABLE READ snapshot tx, the suites whose change_seq exceeds their built_seq
	// (every suite when full) with the paragraphs of their coordinates (R-DEB-010). A build never reads a package file.
	GenerationInputs(ctx context.Context, tx formats.Tx, repoID int64, full bool) (rows.BuildInputs, error)
	// RecordGeneration writes a build's own rows in the publication's transaction: the deb_generations row, the previous generation
	// marked superseded, the generations retention gave up deleted, and built_seq, generation, release_date, published_at and state
	// of the suite, each only while the suite's built_seq is still below the change_seq the build read
	// (rows.GenerationRecord.ReadSeq). applied is false when a newer build has published already; the caller then returns
	// items.ErrSuperseded from its commit function.
	RecordGeneration(ctx context.Context, tx formats.Tx, in rows.GenerationRecord) (applied bool, err error)
	// FailBuild records that a build published nothing for a suite: state failed with the reason, the generation kept (R-DEB-010).
	FailBuild(ctx context.Context, in rows.BuildFailure) error
}
