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
// The port is three groups, each implemented in files of the package that builds it: LocalStore by the local class
// (internal/formatdata/ansible/local_store.go), RemoteStore by the remote class (remote_store.go) and DocsStore by the documentation
// of collections (docs_store.go, R-ANS-033). The values that cross it are those of internal/formats/ansible/rows. The groups are
// frozen with this file: a change after it goes through the architecture
// reviewer, because several packages build against them at once.
//
// The reindex job's enqueue (the endpoint POST .../reindex, R-REPO-016) is the optional interface api/v1.Reindexer of the same value,
// not a method of this port; the step of the job itself is [LocalStore.RebuildRows].
type Store interface {
	LocalStore
	RemoteStore
	DocsStore
}

// LocalStore is the part of the data port the local class writes and reads (R-ANS-008 to R-ANS-012).
type LocalStore interface {
	// PutVersion records a stored version in the transaction that writes its item (OnCommit): the row of ansible_collection_versions
	// and the event package.published, without a journal entry (the deletion is the journaled event, R-ANS-029). A row of the same
	// name and version is replaced. The revision of the collection is raised by the caller, through the package writer.
	PutVersion(ctx context.Context, tx formats.Tx, v rows.VersionRow, a rows.Audit, p rows.PublishInfo) error
	// RemoveVersion deletes the row of a version in the transaction of the removal of its item, if it is still there, and with
	// tombstone true leaves the tombstone (name, version, sha256 of the archive that went) and writes the journaled event
	// ansible.version.deleted in the same transaction.
	RemoveVersion(ctx context.Context, tx formats.Tx, repoID int64, name, version string, sha256 []byte, tombstone bool, a rows.Audit) error
	// Version reads the row of a version; found is false when there is none.
	Version(ctx context.Context, repoID int64, name, version string) (rows.VersionRow, bool, error)
	// Versions lists the versions of a collection newest first by precedence (the sort key descending) with the count of all of them.
	Versions(ctx context.Context, repoID int64, name string, limit, offset int) (list []rows.VersionRow, total int, err error)
	// Collection reads what the collection document shows; found is false for a name without versions.
	Collection(ctx context.Context, repoID int64, name string) (rows.CollectionSummary, bool, error)
	// Tombstoned reads the digest of the deleted version of the name; ok is false when the number is free.
	Tombstoned(ctx context.Context, repoID int64, name, version string) (sha256 []byte, ok bool, err error)
	// ClearTombstone deletes a tombstone and writes ansible.tombstone.cleared, journaled, in one transaction; found is false when
	// the number has none.
	ClearTombstone(ctx context.Context, repoID int64, name, version string, a rows.Audit) (found bool, err error)
	// Tombstones lists the tombstones of a repository in name and version order after the cursor (the name and version of the
	// last one seen, "" for the first page).
	Tombstones(ctx context.Context, repoID int64, after string, limit int) ([]rows.Tombstone, error)
	// CreateTask records an import task, in the publish transaction (tx) or alone (nil) for a failed task, which also writes the
	// event ansible.import_task.failed.
	CreateTask(ctx context.Context, tx formats.Tx, t rows.Task, a rows.Audit) error
	// Task reads a task of the repository; found is false for an unknown id.
	Task(ctx context.Context, repoID int64, id string) (rows.Task, bool, error)
	// Tasks lists the tasks of a repository newest first, optionally of one state, collection and version, after the cursor (the id of the last one seen).
	Tasks(ctx context.Context, repoID int64, f rows.TaskFilter, after string, limit int) ([]rows.Task, error)
	// RebuildRows is one step of the job ansible-reindex (R-ANS-012): it reads the archive of every stored item after the checkpoint
	// through reader, rewrites the version rows from it and creates the rows of archives that have none. It returns the checkpoint to
	// continue from, and done true after the last package name. It never changes stored bytes.
	RebuildRows(ctx context.Context, repoID int64, reader rows.ArchiveReader, checkpoint string) (next string, done bool, err error)
}

// ViewStore is the part of the data port the package screens read (R-ANS-026, R-EP-016): the statements are in
// internal/formatdata/ansible/view_store.go. It is a separate interface because the contract's DataPort ([Store]) is the port of the
// protocol and the writes; the same bound value implements both.
type ViewStore interface {
	// ViewSummaries reads the description and the tags of the given generic versions; a version without a row is absent.
	ViewSummaries(ctx context.Context, ids []int64) ([]rows.ViewSummary, error)
	// ViewDetails reads the stored metadata of the given generic versions and, with readme, the text of their README.
	ViewDetails(ctx context.Context, ids []int64, readme bool) ([]rows.ViewDetail, error)
	// ViewFind returns the ids of at most limit generic versions of the repositories (by key) whose description contains text
	// (case-insensitively) or whose tags contain keyword exactly; exactly one of the two is set.
	ViewFind(ctx context.Context, repos []string, text, keyword string, limit int) ([]int64, error)
	// ViewStored lists the versions a local repository serves of a collection, newest first.
	ViewStored(ctx context.Context, repoID int64, name string, limit int) ([]string, error)
	// ViewWithheld lists the generic versions of the repositories (by key) whose number a tombstone withholds.
	ViewWithheld(ctx context.Context, repos []string) ([]int64, error)
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

// DocsStore is the part of the data port that keeps the documentation of a collection version (R-ANS-033 to R-ANS-036): the tables
// ansible_version_docs, ansible_doc_entries, ansible_doc_files and ansible_doc_fetches (migrations ansible_doc_tables and ansible_doc_fetches). The documentation is
// derived data: a local repository's rows are generated from the archive (source generated), a remote's are parsed from the stored
// docs-blob document (source upstream), a virtual repository has none of its own. The writes join the transaction of the publish
// (OnCommit), of the fill (OnFill) or of the removal (OnItemsRemoved); the reads never leave the rows (R-ANS-036).
type DocsStore interface {
	// PutDocs replaces the documentation of a version in the transaction of the publish (OnCommit) or of the fill (OnFill): the version's
	// rows are deleted and written again, and the fetch row of the version, if any, is deleted.
	PutDocs(ctx context.Context, tx formats.Tx, repoID int64, name, version string, d rows.DocSet) error
	// RemoveDocs deletes the rows of a version (OnItemsRemoved); a missing one is not an error.
	RemoveDocs(ctx context.Context, tx formats.Tx, repoID int64, name, version string) error
	// SetDocFetch records, in tx, the fetch row of a remote version: state queued, unavailable or none (rows.DocFetch*), with the reason
	// (rows.DocReason*, "" for queued and none).
	SetDocFetch(ctx context.Context, tx formats.Tx, repoID int64, name, version, state, reason string) error
	// EnqueueDocsFill queues the job ansible-docs-fill in tx and writes the queued fetch row; it queues nothing when a job for the version
	// exists (the key of the job, R-ANS-042).
	EnqueueDocsFill(ctx context.Context, tx formats.Tx, repoID int64, name, version string) error
	// DocState reads the state of the documentation of a version; the state is none when there is nothing, and a queued fetch row older
	// than one hour counts as none (R-ANS-033).
	DocState(ctx context.Context, repoID int64, name, version string) (rows.DocState, error)
	// DocTree reads the tree of a version: the entries and the file titles, with q applied (a nil q is no search) and the counts before q.
	DocTree(ctx context.Context, repoID int64, name, version string, q *rows.DocQuery) (rows.DocTree, error)
	// DocEntry reads one entry; found is false for an entry that does not exist.
	DocEntry(ctx context.Context, repoID int64, name, version, typ, entry string) (rows.DocEntry, bool, error)
	// DocFile reads the README or a Markdown file; found is false for a name that is none.
	DocFile(ctx context.Context, repoID int64, name, version, file string) (rows.DocFile, bool, error)
	// DocSet reads the whole set of a version, for the protocol document; found is false when the version has no rows.
	DocSet(ctx context.Context, repoID int64, name, version string) (rows.DocSet, bool, error)
	// RebuildDocs is the documentation step of the job ansible-reindex for a local repository: it reads the archive of every stored item
	// after the checkpoint through reader and rewrites the rows of the versions whose generator is older than docgen.Version or that have
	// none. It returns the checkpoint to continue from and done true after the last package name.
	RebuildDocs(ctx context.Context, repoID int64, reader rows.ArchiveReader, checkpoint string) (next string, done bool, err error)
	// RebuildRemoteDocs re-derives a remote's rows from its stored docs-blob documents; the same checkpoint rule as RebuildDocs.
	RebuildRemoteDocs(ctx context.Context, repoID int64, checkpoint string) (next string, done bool, err error)
}
