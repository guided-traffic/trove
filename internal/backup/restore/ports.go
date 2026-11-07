package restore

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Session is the restore's one database connection, which holds the migration lock (R-BAK-021 step 1).
// *postgres.RestoreSession implements it.
type Session interface {
	meta.Runner
	Read(ctx context.Context, fn func(q.DBTX) error) error
	DBTX() q.DBTX

	Lock(ctx context.Context, wait time.Duration) error
	Close(ctx context.Context)
	Schema() string
	Inspect(ctx context.Context) (postgres.RestoreInspection, error)
	ServerCheck(ctx context.Context) error
	Steps() ([]postgres.RestoreStep, error)
	SetAnchor(ctx context.Context, backupID string) error
	MigrateTo(ctx context.Context, m postgres.RestoreMigration) (postgres.Result, error)
	Load(ctx context.Context, plan postgres.LoadPlan, src postgres.LoadSource) (postgres.LoadResult, error)
	Fix(ctx context.Context, fn func(meta.Tx) error) error
	Finish(ctx context.Context, fn func(meta.Tx) error) error
	EnsureAuditPartitions(ctx context.Context) error
	PendingSteps(ctx context.Context) ([]postgres.StepInfo, error)
	LiveBlobs(ctx context.Context, fn func(digest []byte) error) error
	SecretCanaries(ctx context.Context) (map[string]string, error)
}

var _ Session = (*postgres.RestoreSession)(nil)

// BlobStore is the new installation's object store as the restore uses it: the sink of step 3 plus the listing of check 5
// and the probe of check 7. internal/blob/s3.BlobSink implements it.
type BlobStore interface {
	backup.BlobSink
	// FirstForeign returns the first key below the blob prefix that is not a probe object, and with allowBlobs not below
	// sha256/ either; empty when there is none (R-BAK-020 check 5).
	FirstForeign(ctx context.Context, allowBlobs bool) (string, error)
	// Probe writes and deletes <prefix>/_probe/<name> (R-BAK-020 check 7).
	Probe(ctx context.Context, name string) error
}

// JournalEnv is what the journal needs to be built for one restore.
type JournalEnv struct {
	// Runner is the restore's session: the replay handlers apply in transactions on it, as the owner.
	Runner meta.Runner
	// SourcePrefix is manifest.source.storage.prefix, below which the journal lives; Bucket and Endpoint name the source.
	SourcePrefix, SourceBucket, SourceEndpoint string
	// CredentialsFile is --journal-credentials-file, empty to use the blob-store credentials.
	CredentialsFile string
	// Recorder writes the audit events of the replay, in transactions of the restore's session.
	Recorder audit.Recorder
}

// Journal is the revocation journal of the source as the restore needs it.
type Journal interface {
	// Reachable lists the journal prefix with the credentials of the restore (R-BAK-020 check 8).
	Reachable(ctx context.Context) error
	journal.Replayer
}
