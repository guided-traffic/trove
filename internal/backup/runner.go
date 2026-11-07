package backup

import (
	"context"
	"encoding/json"
	"time"

	"github.com/guided-traffic/trove/internal/backup/format"
)

// The operation interfaces of the backup commands. cmd/trove builds one implementation of each and calls it; the work
// packages of the lane replace the implementation behind the interface, never the command wiring (docs/developer/backup.md).
// Every method returns an error that [ExitCode] maps to the exit code of R-BAK-013, and a result for the log and --json.

// CreateOptions are the flags of `trove backup create` (R-BAK-009).
type CreateOptions struct {
	Trigger             Trigger
	ExcludeRemoteCaches bool
	NoPrune             bool
	AdoptTarget         bool
}

// Creator runs one backup. A returned result with a nil error is a completed run or one with warnings.
type Creator interface {
	Create(ctx context.Context, opts CreateOptions) (*Result, error)
}

// ListOptions are the flags of `trove backup list` (R-BAK-018).
type ListOptions struct{ All bool }

// Listing is one line of `trove backup list` (R-BAK-018): a backup of the target, complete or not. The fields from
// TroveVersion on come from the manifest and are empty for an incomplete directory.
type Listing struct {
	BackupID      format.ID          `json:"backupId"`
	Complete      bool               `json:"complete"`
	SnapshotAt    time.Time          `json:"snapshotAt,omitzero"`
	TroveVersion  string             `json:"troveVersion,omitempty"`
	SchemaStep    int                `json:"schemaStep,omitempty"`
	DatabaseBytes int64              `json:"databaseBytes,omitempty"`
	BlobCount     int64              `json:"blobCount,omitempty"`
	BlobBytes     int64              `json:"blobBytes,omitempty"`
	BlobsCopied   int64              `json:"blobsCopied,omitempty"`
	Options       *format.Options    `json:"options,omitempty"`
	KeyIDs        []string           `json:"keyIds,omitempty"`
	Incarnation   string             `json:"incarnation,omitempty"`
	Damaged       bool               `json:"damaged,omitempty"`
	Encryption    *format.Encryption `json:"encryption,omitempty"`
}

// Lister reads the target.
type Lister interface {
	List(ctx context.Context, opts ListOptions) ([]Listing, error)
}

// VerifyOptions are the flags of `trove backup verify` (R-BAK-019). Exactly one of ID, Latest and All is set.
type VerifyOptions struct {
	ID             format.ID
	Latest, All    bool
	Deep           bool
	BytesPerSecond int64
}

// FindingKind is what verify found wrong (R-BAK-019).
type FindingKind string

// The finding kinds.
const (
	FindingMissing              FindingKind = "missing"
	FindingSizeMismatch         FindingKind = "size_mismatch"
	FindingDigestMismatch       FindingKind = "digest_mismatch"
	FindingFileChecksumMismatch FindingKind = "file_checksum_mismatch"
)

// Finding is one line of verify's output.
type Finding struct {
	Kind     FindingKind `json:"kind"`
	BackupID string      `json:"backupId"`
	// Object is the digest or the target key.
	Object string `json:"object"`
	// Detail says what is wrong in words, for the log and --json; it is empty where Kind says it all.
	Detail string `json:"detail,omitempty"`
}

// VerifyReport is the outcome of verify. A report with findings comes with [ErrFindings].
type VerifyReport struct {
	Verified []format.ID `json:"verified"`
	Findings []Finding   `json:"findings"`
}

// Verifier checks backups without changing them.
type Verifier interface {
	Verify(ctx context.Context, opts VerifyOptions) (*VerifyReport, error)
}

// DeleteOptions are the flags of `trove backup delete` (R-BAK-016).
type DeleteOptions struct {
	ID        format.ID
	Yes       bool
	AllowLast bool
}

// Deleter deletes one backup regardless of the retention policy, then sweeps the pool.
type Deleter interface {
	Delete(ctx context.Context, opts DeleteOptions) (*Result, error)
}

// Pruner applies the retention policy and sweeps the pool (`trove backup prune`, the pruning phase of create).
type Pruner interface {
	Prune(ctx context.Context) (*Result, error)
}

// PruneStage is the pruning phase as the create pipeline calls it inside its own run: same run row, same lease handle, no
// slot of its own (R-BAK-009 state pruning). The retention work package implements it; the same implementation serves
// `trove backup prune` and `delete` through their own runs.
type PruneStage interface {
	Prune(ctx context.Context, h *Handle, into *Result) error
}

// RestoreOptions are the flags of `trove backup restore` (R-BAK-020).
type RestoreOptions struct {
	ID                     format.ID
	Check                  bool
	SkipRevocationJournal  bool
	JournalCredentialsFile string
	// IdentityFile is the age identity of client-side encrypted backups (R-BAK-027, P1).
	IdentityFile string
}

// RestoreReport is the summary a finished restore prints (R-BAK-021 step 9).
type RestoreReport struct {
	BackupID   format.ID `json:"backupId"`
	SnapshotAt time.Time `json:"snapshotAt"`
	// Tables lists the loaded row count per table.
	Tables map[string]int64 `json:"tables,omitempty"`
	// Journal is "replayed" or "skipped".
	Journal string `json:"journal"`
	// Replay holds the outcome counts of the journal replay, and the versions it reported revived or suspended.
	Replay json.RawMessage `json:"replay,omitempty"`
	// Phases holds the duration of each restore phase in seconds, for the drill (R-BAK-029 scenario 9).
	Phases map[string]float64 `json:"phasesSeconds,omitempty"`
	// Checked is true for --check, which changed nothing.
	Checked bool `json:"checked,omitempty"`
	// AlreadyRestored is true when the database holds the finished restore of this very backup: an earlier pod committed step 9
	// and was killed before it exited, so the Job's next pod finds nothing to do (R-BAK-021).
	AlreadyRestored bool `json:"alreadyRestored,omitempty"`
}

// Restorer restores a backup into an empty installation, or resumes an interrupted restore of the same backup.
type Restorer interface {
	Restore(ctx context.Context, opts RestoreOptions) (*RestoreReport, error)
}

// PostRestoreOptions are the flags of `trove admin post-restore` (R-HA-025 step 4).
type PostRestoreOptions struct {
	// RestoredTo is t, the point in time the platform restore went to.
	RestoredTo time.Time
	// CredentialsFile is the journal reader credential, when the runtime credential cannot list object versions.
	CredentialsFile string
	// SkipJournal skips the replay, for the disaster in which the source bucket is gone.
	SkipJournal bool
}

// PostRestoreReport is what post-restore prints.
type PostRestoreReport struct {
	// Steps lists what it did, one line each.
	Steps []string `json:"steps"`
	// Replay holds the journal summary.
	Replay json.RawMessage `json:"replay,omitempty"`
	// MissingBlobs is the number of referenced blobs whose objects are missing (admin storage verify).
	MissingBlobs int64 `json:"missingBlobs"`
}

// PostRestorer is the clean-up of a platform restore (PITR): `trove admin post-restore`. It runs the fix-ups of R-BAK-022
// that a PITR needs, sets the deletion pause, replays the journal and writes system.restored with source "pitr".
type PostRestorer interface {
	PostRestore(ctx context.Context, opts PostRestoreOptions) (*PostRestoreReport, error)
}

// Result is the outcome of a create, delete or prune run: the object stored in backup_run.result, printed by --json and
// summarised by the audit event (R-BAK-009, R-BAK-014, R-BAK-016).
type Result struct {
	BackupID string `json:"backupId,omitempty"`
	// Outcome is "completed" or "completed_with_warnings".
	Outcome string `json:"outcome,omitempty"`

	DatabaseBytes   int64 `json:"databaseBytes,omitempty"`
	BlobCount       int64 `json:"blobCount,omitempty"`
	BlobBytes       int64 `json:"blobBytes,omitempty"`
	BlobsCopied     int64 `json:"blobsCopied,omitempty"`
	BytesCopied     int64 `json:"bytesCopied,omitempty"`
	DamagedBlobs    int64 `json:"damaged,omitempty"`
	DurationSeconds int64 `json:"durationSeconds,omitempty"`

	UnregisteredTables []string `json:"unregisteredTables,omitempty"`
	Warnings           []string `json:"warnings,omitempty"`
	Encryption         string   `json:"encryption,omitempty"`

	// Retention, Retained and the deletion counters are recorded by every run that lists the target and prunes
	// (R-BAK-016); a create with --no-prune records neither Retention nor Retained.
	Retention          *Retention `json:"retention,omitempty"`
	Retained           []Retained `json:"retained,omitempty"`
	BackupsDeleted     []string   `json:"backupsDeleted,omitempty"`
	IncompleteDeleted  []string   `json:"incompleteDeleted,omitempty"`
	PoolObjectsDeleted int64      `json:"poolObjectsDeleted,omitempty"`
	PoolBytesReclaimed int64      `json:"poolBytesReclaimed,omitempty"`
	// PoolSweptAt is when a run last swept the pool; the sweep interval of R-BAK-017 counts from the newest value among
	// the recorded results.
	PoolSweptAt *time.Time `json:"poolSweptAt,omitempty"`
}

// Retention is the policy a run applied.
type Retention struct {
	KeepLast          int   `json:"keepLast"`
	KeepWithinSeconds int64 `json:"keepWithinSeconds"`
}

// Retained is one complete backup left after a run's deletions, the item of `storedBackups` in the summary of R-API-050.
type Retained struct {
	BackupID      string    `json:"backupId"`
	SnapshotAt    time.Time `json:"snapshotAt"`
	TroveVersion  string    `json:"troveVersion"`
	SchemaStep    int       `json:"schemaStep"`
	DatabaseBytes int64     `json:"databaseBytes"`
	BlobCount     int64     `json:"blobCount"`
	BlobBytes     int64     `json:"blobBytes"`
	Damaged       bool      `json:"damaged"`
}

// Runners is the set of implementations cmd/trove calls. A work package fills one field; until it does, the constructor
// of the command returns [NotBuilt].
type Runners struct {
	Creator  Creator
	Lister   Lister
	Verifier Verifier
	Deleter  Deleter
	Pruner   Pruner
	Restorer Restorer
}

// NotBuilt is the implementation every operation has until its work package replaces it: it returns an error that wraps
// [ErrNotBuilt] and exits 1, so a Job that ran it fails loudly instead of succeeding without a backup.
type NotBuilt struct{ Operation string }

// PostRestore implements PostRestorer.
func (n NotBuilt) PostRestore(context.Context, PostRestoreOptions) (*PostRestoreReport, error) {
	return nil, n.err()
}

func (n NotBuilt) err() error {
	return Failf(ExitFailed, CodeInternal, "backup %s: %w", n.Operation, ErrNotBuilt)
}

// Create implements Creator.
func (n NotBuilt) Create(context.Context, CreateOptions) (*Result, error) { return nil, n.err() }

// List implements Lister.
func (n NotBuilt) List(context.Context, ListOptions) ([]Listing, error) { return nil, n.err() }

// Verify implements Verifier.
func (n NotBuilt) Verify(context.Context, VerifyOptions) (*VerifyReport, error) { return nil, n.err() }

// Delete implements Deleter.
func (n NotBuilt) Delete(context.Context, DeleteOptions) (*Result, error) { return nil, n.err() }

// Prune implements Pruner.
func (n NotBuilt) Prune(context.Context) (*Result, error) { return nil, n.err() }

// Restore implements Restorer.
func (n NotBuilt) Restore(context.Context, RestoreOptions) (*RestoreReport, error) {
	return nil, n.err()
}
