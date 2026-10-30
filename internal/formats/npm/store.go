package npm

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
)

// Store is the data port of the npm format (Q-ARCH-193, D-1): everything the
// format needs of its own tables, behind an interface the format declares and
// internal/formatdata/npm implements. The format runs no SQL (R-ARCH-008); the
// composition root binds the implementation per format ID and refuses to start
// without it ([Format.Descriptor] names the type).
//
// The store persists what the format decided: the format holds the SemVer order, so
// it computes the dist-tags (the latest rule and the latest guard of R-NPM-016)
// and hands the store the complete map. The methods that take a transaction use it
// for every statement, and the package row of the write is locked by then: the writes
// of one package serialize on that lock (R-NPM-007 step 5, R-NPM-016).
//
// The rows are the values of package rows, a leaf the format and the
// implementation share. Reads that serve documents are added in the marked block
// by the work package that renders them.
type Store interface {
	// VersionFacts reads, outside a transaction, what the immutability and the
	// acknowledged-retry rules need of a version (R-NPM-007 step 4, R-NPM-008). It
	// returns the zero value for a version that was never published.
	VersionFacts(ctx context.Context, repoID int64, name, version string) (rows.VersionFacts, error)
	// RecordMismatch records npm.publish.manifest_mismatch, outside any
	// transaction: a publish that is refused for it leaves no other record (R-NPM-044).
	RecordMismatch(ctx context.Context, in rows.MismatchInput) error

	// Tags reads the dist-tags of a package under its row lock; a package without an
	// npm row holds none.
	Tags(ctx context.Context, tx formats.Tx, packageID int64) (map[string]string, error)
	// Publish stores the npm rows of a new version in the publish transaction
	// (R-NPM-007 step 5): the package's npm row (the unique name_fold refuses a
	// look-alike with 400 npm_invalid_name), the version, the dist-tags,
	// modified_at, and the npm.publish audit event. A tombstone of the number is 409
	// npm_version_unpublished.
	Publish(ctx context.Context, tx formats.Tx, in rows.PublishInput) error
	// RecordRetry writes the audit event of an acknowledged publish retry and
	// changes nothing else.
	RecordRetry(ctx context.Context, tx formats.Tx, in rows.RetryInput) error
	// SetTag and DeleteTag store the dist-tags a change of a tag produced and record
	// npm.dist_tag.set and npm.dist_tag.delete (R-NPM-016).
	SetTag(ctx context.Context, tx formats.Tx, in rows.SetTagInput) error
	DeleteTag(ctx context.Context, tx formats.Tx, in rows.DeleteTagInput) error
	// Tombstone writes the tombstone of a live version, stores the dist-tags the
	// package holds without it and records npm.unpublish with its journal entry
	// (R-NPM-008, R-NPM-016, R-NPM-017). It returns the integrity of the version.
	// The version row itself is removed by the core writer afterwards.
	Tombstone(ctx context.Context, tx formats.Tx, in rows.TombstoneInput) (integrity string, err error)

	// >>> M2 npm-read
	// The reads of package documents, dist-tags and tarball state (R-NPM-010,
	// R-NPM-015, R-NPM-016) are declared here.
	// <<< M2 npm-read

	// >>> M2 npm-remote
	// The fill, the removal marks and the tarball facts of remote repositories
	// (R-NPM-024 to R-NPM-026) are declared here.
	// <<< M2 npm-remote
}
