package ansible

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
)

// Store is the data port of the Ansible Galaxy format (Q-ARCH-193): everything the format needs of its own tables
// (internal/meta/postgres/migrations: 00068_ansible_collections.sql and 00069_ansible_import_tasks.sql, and the migration of
// ansible_upstream_versions that the remote class adds), behind an interface the format declares and internal/formatdata/ansible
// implements. The format runs no SQL (R-ARCH-008); the composition root binds the implementation per format ID and refuses to start
// without it ([Format.Descriptor] names the type).
//
// The port is two groups, each implemented in files of the package that builds the class: LocalStore by the local class
// (internal/formatdata/ansible/local_store.go) and RemoteStore by the remote class (remote_store.go). The values that cross it are
// those of internal/formats/ansible/rows. The two groups are frozen with this file: a change after it goes through the architecture
// reviewer, because several packages build against them at once.
//
// The reindex job's enqueue (the endpoint POST .../reindex, R-REPO-016) is the optional interface api/v1.Reindexer of the same value,
// not a method of this port; the step of the job itself is [LocalStore.RebuildRows].
type Store interface {
	LocalStore
	RemoteStore
}

// LocalStore is the part of the data port the local class writes and reads (R-ANS-008 to R-ANS-012).
type LocalStore interface {
	// PutVersion records a stored version in the transaction that writes its item (OnCommit): the row of ansible_collection_versions
	// and the raise of the collection's revision. A row of the same name and version is replaced (the idempotent retry stores the
	// same bytes).
	PutVersion(ctx context.Context, tx formats.Tx, v rows.VersionRow) error
	// RemoveVersion deletes the row of a version in the transaction of the removal of its item. With tombstone true it leaves a
	// tombstone (name, version, sha256) and writes the journaled event ansible.version.deleted in the same transaction; by is the
	// principal's id or "".
	RemoveVersion(ctx context.Context, tx formats.Tx, repoID int64, name, version string, tombstone bool, by string) error
	// Version reads the row of a version; found is false when there is none.
	Version(ctx context.Context, repoID int64, name, version string) (rows.VersionRow, bool, error)
	// Versions lists the versions of a collection newest first by precedence (the sort key descending) with the count of all of them.
	Versions(ctx context.Context, repoID int64, name string, limit, offset int) (list []rows.VersionRow, total int, err error)
	// Collection reads what the collection document shows; found is false for a name without versions.
	Collection(ctx context.Context, repoID int64, name string) (rows.CollectionSummary, bool, error)
	// Tombstoned reads the digest of the deleted version of the name; ok is false when the number is free.
	Tombstoned(ctx context.Context, repoID int64, name, version string) (sha256 []byte, ok bool, err error)
	// ClearTombstone deletes a tombstone and writes ansible.tombstone.cleared, journaled, in one transaction.
	ClearTombstone(ctx context.Context, repoID int64, name, version, by string) error
	// Tombstones lists the tombstones of a repository in name and version order after the cursor (the name and version of the
	// last one seen, "" for the first page).
	Tombstones(ctx context.Context, repoID int64, after string, limit int) ([]rows.Tombstone, error)
	// CreateTask records an import task, in the publish transaction or alone for a failed task.
	CreateTask(ctx context.Context, tx formats.Tx, t rows.Task) error
	// Task reads a task of the repository; found is false for an unknown id.
	Task(ctx context.Context, repoID int64, id string) (rows.Task, bool, error)
	// Tasks lists the tasks of a repository newest first, optionally of one state, after the cursor (the id of the last one seen).
	Tasks(ctx context.Context, repoID int64, state string, after string, limit int) ([]rows.Task, error)
	// RebuildRows is one step of the job ansible-reindex (R-ANS-012): it reads the archive of every stored item after the checkpoint
	// through reader, rewrites the version rows from it and creates the rows of archives that have none. It returns the checkpoint to
	// continue from, and done true after the last package name. It never changes stored bytes.
	RebuildRows(ctx context.Context, repoID int64, reader rows.ArchiveReader, checkpoint string) (next string, done bool, err error)
}

// RemoteStore is the part of the data port the remote class writes and reads (R-ANS-018).
type RemoteStore interface {
	// RecordAnnounced records the digests a remote's version documents announce in the fill's transaction; the first digest recorded
	// for a version stays. It returns the rows whose offered digest differs from the one that stays, as recorded.
	RecordAnnounced(ctx context.Context, tx formats.Tx, repoID int64, announced []rows.Announced) (changed []rows.Announced, err error)
	// Announced reads the digest recorded for a version; found is false when none was.
	Announced(ctx context.Context, repoID int64, name, version string) (rows.Announced, bool, error)
	// AnnouncedNames lists the versions recorded for a collection name: the virtual repository's cached-version set.
	AnnouncedNames(ctx context.Context, repoID int64, name string) ([]rows.Announced, error)
}
