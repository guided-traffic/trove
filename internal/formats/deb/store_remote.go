package deb

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
)

// RemoteStore is the part of the data port that the remote class reads and writes (R-DEB-019 to R-DEB-023): the stored release
// documents, the parsed index rows and the shadowing check's question. Built by the work package of the remote class. The rows are
// cache rows: a restore without remote caches deletes them (R-BAK-008).
type RemoteStore interface {
	// RecordRelease records a release document in the fill transaction (OnFill) and decides the anti-rollback rule (R-DEB-019,
	// Q-DEB-35): a document older than the directory's floor_date while keys are set is not stored (Decision.RolledBack). It also
	// records an 'absent' suite.
	RecordRelease(ctx context.Context, tx formats.Tx, in rows.ReleaseRecord) (rows.ReleaseDecision, error)
	// Release reads the stored release document of a directory; found is false when there is none.
	Release(ctx context.Context, repoID int64, dir string) (r rows.Release, found bool, err error)
	// EntryDigest reads the digest and size the newest parsed index lists for a path (R-DEB-022); found is false when no parsed
	// index lists it. The item lookup reads the same fact with the item (servefacts.DebRemote).
	EntryDigest(ctx context.Context, repoID int64, path string) (e rows.Entry, found bool, err error)
	// ServesName answers the shadowing check in a scope (R-DEB-023, Q-DEB-37, Q-DEB-41): served when a parsed row of an index in the
	// scope lists the name; known is false while a suite of the scope is neither settled by a stored release with parsed indexes nor
	// recorded absent.
	ServesName(ctx context.Context, q rows.NameScope) (served, known bool, err error)
	// EnqueueParse enqueues deb-remote-parse for an index path of a release directory in the transaction tx (coalesced per
	// repository, directory and index path, Q-DEB-36).
	EnqueueParse(ctx context.Context, tx formats.Tx, repoID int64, dir, indexPath string) error
	// EnsureParsed enqueues the release fetch and the parse of every index of a scope that is not settled, through the background
	// path (R-DEB-023, Q-DEB-41), and reports whether any was enqueued.
	EnsureParsed(ctx context.Context, q rows.NameScope) (enqueued bool, err error)
	// ClearVerifiedBy clears verified_by of the releases that the fingerprints verified (R-DEB-020, Q-DEB-42) and returns how many.
	ClearVerifiedBy(ctx context.Context, tx formats.Tx, repoID int64, fingerprints []string) (int, error)
}
