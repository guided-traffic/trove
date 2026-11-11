// Package rows holds the values that cross the data port of the Ansible Galaxy format (Q-ARCH-193): the rows of the format's tables
// as plain structs whose fields are the columns of R-ANS-011. The format (an edge package) and its data port (a domain package)
// import neither each other; both import this leaf, so the port can be implemented and checked against the format's interface
// without a cycle.
//
// [Task] and [CollectionSummary] are also the inputs of the renderers of internal/formats/ansible/galaxydoc, which aliases them. The
// package imports the standard library only (the archtest rule of a rows leaf).
package rows

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"
)

// The states of an import task.
const (
	TaskCompleted = "completed"
	TaskFailed    = "failed"
)

// TaskMessage is one message of an import task.
type TaskMessage struct {
	Level, Message string
	Time           time.Time
}

// Task is an import task (R-ANS-009), the row of ansible_import_tasks, and the neutral form the document is rendered from.
type Task struct {
	ID                              string
	State                           string
	Namespace, Name, Version        string
	CreatedAt, StartedAt, UpdatedAt time.Time
	FinishedAt                      time.Time
	ErrorCode, ErrorDescription     string
	Messages                        []TaskMessage
	// RepoID and CreatedBy are columns the document does not show.
	RepoID    int64
	CreatedBy string
}

// TaskFilter narrows a list of import tasks (R-ANS-009, Q-API-262): a state ("" for any), a collection name "<ns>.<name>" and a version as
// written; a field left empty does not narrow.
type TaskFilter struct{ State, Name, Version string }

// CollectionSummary is what the collection document shows, computed from the version rows of one collection name.
type CollectionSummary struct {
	Namespace, Name string
	// CollectionID is the UUID of the collection ([CollectionUUID]).
	CollectionID string
	// HighestVersion is the newest version by precedence, "" when none is listed.
	HighestVersion       string
	CreatedAt, UpdatedAt time.Time
}

// VersionRow is a row of ansible_collection_versions: the metadata of one stored version of a local repository (R-ANS-011).
type VersionRow struct {
	RepoID int64
	// Name is "<ns>.<name>" and Version the version as written.
	Name, Version string
	// SortKey is the memcmp-ordered precedence key of internal/formats/ansible/version, build metadata excluded.
	SortKey []byte
	// ItemID is the item of the archive.
	ItemID int64
	SHA256 [32]byte
	Size   int64
	// RequiresAnsible is "" for a version without one.
	RequiresAnsible string
	Dependencies    map[string]string
	// CollectionInfo is the trimmed collection_info of the archive (internal/formats/ansible/collection.Archive.Info).
	CollectionInfo json.RawMessage
	PublishedBy    string
	PublishedAt    time.Time
}

// Tombstone is a row of ansible_tombstones: a version that was deleted and whose number is not accepted again (R-ANS-012).
type Tombstone struct {
	RepoID        int64
	Name, Version string
	SHA256        [32]byte
	DeletedAt     time.Time
	DeletedBy     string
}

// Announced is a row of ansible_upstream_versions: the digest and size a remote's version document announced for a version (R-ANS-018).
// The first digest recorded for a version stays.
type Announced struct {
	RepoID          int64
	Name, Version   string
	SHA256          [32]byte
	Size            int64
	RequiresAnsible string
	RecordedAt      time.Time
}

// ArchiveFacts is what the manifest of a stored archive says (R-ANS-010): the identity and the metadata of a version row.
type ArchiveFacts struct {
	Namespace, Name, Version string
	Info                     json.RawMessage
	Dependencies             map[string]string
	RequiresAnsible          string
}

// ArchiveReader reads the manifest of the stored archive of an item for the reindex (R-ANS-012): the two documents only, by ranged
// reads. The format implements it over the blob store; the port calls it for every archive it finds without a current row.
type ArchiveReader interface {
	ReadArchive(ctx context.Context, ref ArchiveRef) (ArchiveFacts, error)
}

// CollectionUUID is the stable id of a collection of a local repository, the collection.id of its documents (R-ANS-004): a version 5
// style UUID derived from the repository id and the collection name, so it is the same on every replica and after a restore and needs
// no column.
func CollectionUUID(repoID int64, name string) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "trove-ansible-collection/%d/%s", repoID, name))
	b := sum[:16]
	b[6] = b[6]&0x0f | 0x50 // version 5
	b[8] = b[8]&0x3f | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Actor is the principal behind a write, as the audit event describes it.
type Actor struct {
	Kind, ID, Name, TokenID, AuthMethod string
}

// Audit is the audit context of a write: the repository key, the endpoint and the actor, from the request's subject.
type Audit struct {
	RepoKey, Endpoint string
	Actor             Actor
}

// PublishInfo is what the event package.published of a stored version adds to the row: the claim the first version made and what the
// shadowing check found (R-REPO-048).
type PublishInfo struct {
	// Claim is the result of the namespace auto claim ("" for none).
	Claim string
	// ShadowUndetermined says that whether a remote member serves the name could not be determined.
	ShadowUndetermined bool
	// Shadowed are the virtual repositories and the remote members a first publish shadows, on the strength of manage.
	Shadowed []ShadowPair
}

// ShadowPair is a virtual repository and the remote member whose package of the name the publish shadows.
type ShadowPair struct{ Virtual, Remote string }

// ArchiveRef names the stored archive of an item for the reindex: the item, its blob and its size.
type ArchiveRef struct {
	ItemID int64
	Path   string
	SHA256 [32]byte
	Size   int64
}

// ViewSummary is what the package list shows of a version: the description and the tags.
type ViewSummary struct {
	// VersionID is the id of the generic version row the screens key by.
	VersionID                  int64
	Name, Version, Description string
	Tags                       []string
}

// ViewDetail is what the version screen shows: the stored metadata and, when asked for, the README.
type ViewDetail struct {
	ViewSummary
	RequiresAnsible string
	Dependencies    map[string]string
	CollectionInfo  json.RawMessage
	SHA256          [32]byte
	Size            int64
	PublishedAt     time.Time
	// Readme and ReadmeFile are the README text (at most 64 KiB) and the name of its file inside the archive; empty when the archive
	// has none or it was not asked for.
	Readme, ReadmeFile string
}
