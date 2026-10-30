package formats

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/guided-traffic/trove/internal/formats/untrusted"
	"github.com/guided-traffic/trove/internal/repository"
)

// RepoRef names a repository the way a storage hook meets it.
type RepoRef struct {
	// ID is the numeric id of the repository row.
	ID     int64
	Key    string
	Class  Class
	Format ID
}

// ItemRef names an item: its row id, its repository and its canonical path.
type ItemRef struct {
	ID   int64
	Repo RepoRef
	Path string
}

// PackageID, VersionID and RevSeq are the ids of the generic package rows and the
// package's revision counter (packages.rev_seq, R-ARCH-009).
type (
	PackageID int64
	VersionID int64
	RevSeq    int64
)

// VersionRef names a version of a package.
type VersionRef struct {
	Repo    RepoRef
	Package string
	Version string
	ID      VersionID
}

// Digests are the digests of a body. A digest the pipeline did not compute is nil.
type Digests struct{ SHA256, SHA512, SHA1, MD5 []byte }

// IngestOptions are what a format asks of the upload pipeline for one request
// (R-ARCH-014 step 3).
type IngestOptions struct {
	// Expect are the digests the protocol announced (npm's integrity is a SHA-512);
	// a body that differs is refused with 409 and nothing is stored.
	Expect Digests
	// MediaType is stored with the item.
	MediaType string
	// MaxSize is a bound below the repository's and the system's; 0 keeps them.
	MaxSize int64
}

// Spooled is a body in the replica-local spool (R-STOR-004). The core owns its
// lifetime: [Ingestor.Close] and the dispatcher remove it.
type Spooled interface {
	Digests() Digests
	Size() int64
	// Open reads the spool again, for Inspect; it never reads the object store.
	Open() (io.ReadCloser, error)
	// ReaderAt serves a ZIP central directory.
	ReaderAt() (io.ReaderAt, error)
}

// InspectInput is the content Inspect reads.
type InspectInput struct {
	Path      Path
	MediaType string
	Body      Spooled
	// Limits are untrusted.DefaultLimits tightened by Descriptor.Limits and
	// bounded by the document limits; the ctx carries the deadline (R-ARCH-028).
	Limits untrusted.Limits
}

// Inspection is what a format learned of an upload.
type Inspection struct {
	// Package and Version are what the content declares; authorization checks
	// this name (R-ARCH-014 step 5).
	Package, Version string
	// Properties are namespaced; only keys below Descriptor.PropertyPrefix.
	Properties map[string]string
	// Payload is format data handed to OnCommit: the parsed manifest.
	Payload any
}

// Origin says why an item was committed (C-1).
type Origin uint8

const (
	// OriginUpload is a client's upload through the generic layer or a format
	// route.
	OriginUpload Origin = iota + 1
	// OriginFill is the artifact of a remote repository stored as an item of it
	// (R-REPO-025 "artifacts become items of R").
	OriginFill
)

func (o Origin) String() string {
	switch o {
	case OriginUpload:
		return "upload"
	case OriginFill:
		return "fill"
	}
	return "unknown"
}

// CommitEvent is what OnCommit learns of a commit. It runs in the transaction
// that wrote the item row.
type CommitEvent struct {
	Origin Origin
	Repo   RepoRef
	Item   ItemRef
	Path   Path
	Size   int64
	// Digests are those of the stored content.
	Digests Digests
	// UpstreamURL is the final upstream URL without user info (OriginFill).
	UpstreamURL string
	// Inspection is what Inspect returned for an upload; nil for a fill, which the
	// format inspects in PrepareFill.
	Inspection *Inspection
	// Payload is Commit.Payload, or the fill's Prepared.Data.
	Payload any
	// Actor is the principal id behind the commit, "" for none.
	Actor string
	// Replaced is true when the commit replaced other content at the path.
	Replaced bool
	// Packages writes the generic package rows in this transaction (C-2).
	Packages PackageWriter
	// Data is the format's bound data port (C-3); nil without Descriptor.DataPort.
	Data any
}

// RemovedItem is an item a removal took away.
type RemovedItem struct {
	Item ItemRef
	// Version is the version the item belonged to; 0 for none.
	Version VersionID
}

// ItemsEvent is what OnItemsRemoved and OnItemsRestored learn.
type ItemsEvent struct {
	Repo  RepoRef
	Items []RemovedItem
	// Reason is "delete", "trash", "move", "purge" or "restore".
	Reason   string
	Actor    string
	Packages PackageWriter
	Data     any
}

// PackageRef names a package row.
type PackageRef struct {
	ID   PackageID
	Repo RepoRef
	// Name is the normalized name.
	Name string
	// RevSeq is the revision as read under the lock.
	RevSeq RevSeq
}

// PackageRow is a package as the package view reads it.
type PackageRow struct {
	Ref         PackageRef
	DisplayName string
	Attributes  json.RawMessage
}

// VersionRow is a live version of a package.
type VersionRow struct {
	ID             VersionID
	Version        string
	DisplayVersion string
	// LeadItem is the item id of the version's lead item; 0 for none.
	LeadItem   int64
	Attributes json.RawMessage
	CreatedAt  time.Time
	CreatedBy  string
}

// NewVersion is a version to insert.
type NewVersion struct {
	Version, DisplayVersion string
	// LeadItem is linked to the version as its lead item; nil for none.
	LeadItem   *ItemRef
	Attributes json.RawMessage
	// CreatedBy is a principal id, "" for none.
	CreatedBy string
}

// JobSpec is a job a hook enqueues in its transaction (R-ARCH-016).
type JobSpec struct {
	Kind     string
	Args     any
	DedupKey string
}

// OwnershipWrite says what a write is for the ownership rule of R-AUTHZ-018: a
// publish adds a version, a change of a dist-tag does not (it is a write by an
// owner or a writer, never a claim), and a first version creates the package and
// so claims it.
type OwnershipWrite struct {
	Publish      bool
	FirstVersion bool
}

// AutoClaimResult is the outcome of the namespace auto claim (R-REPO-023): Created
// with the claim's Pattern, or the Reason nothing was created ("disabled",
// "covered", "skipped", "limit").
type AutoClaimResult = repository.AutoClaimResult

// PackageWriter writes the generic package rows inside a storage transaction
// (R-ARCH-009, C-2). It is bound to the transaction of the hook or of
// [Host.Write] that handed it out, and to nothing else.
//
// Every call that changes the rows of a package raises packages.rev_seq of that
// package once per transaction, however many calls there are, so a write of
// generic rows cannot forget the revision (Q-ARCH-212). A change that writes only
// the format's own side rows - a dist-tag - calls BumpRevision, which does the
// same and returns the clock read after the row lock.
//
// No SQL leaves the core: a format names packages and versions by their values.
type PackageWriter interface {
	// EnsureLocked locks the package row of (repository, normalized name) FOR
	// UPDATE and inserts it when it is missing. It does not bump the revision of a
	// package that exists; a created package starts at revision 1.
	EnsureLocked(ctx context.Context, repo RepoRef, name, displayName string) (pkg PackageRef, created bool, err error)
	// Versions lists the live versions of a locked package.
	Versions(ctx context.Context, pkg PackageRef) ([]VersionRow, error)
	// InsertVersion adds a version and links its lead item.
	InsertVersion(ctx context.Context, pkg PackageRef, v NewVersion) (VersionID, error)
	// TrashOrDeleteVersion trashes a version, or deletes its row when purge is set.
	TrashOrDeleteVersion(ctx context.Context, v VersionID, purge bool) error
	SetPackageAttributes(ctx context.Context, pkg PackageRef, attrs json.RawMessage) error
	SetVersionAttributes(ctx context.Context, v VersionID, attrs json.RawMessage) error
	// LinkItem attaches an item to a version; lead makes it the version's lead item.
	LinkItem(ctx context.Context, it ItemRef, v VersionID, lead bool) error
	// SetProperties replaces the namespaced properties of an item; only keys below
	// Descriptor.PropertyPrefix are accepted.
	SetProperties(ctx context.Context, it ItemRef, props map[string]string) error
	// BumpRevision raises rev_seq of the package (idempotent within the
	// transaction) and returns it with clock_timestamp() read after the row lock
	// (R-NPM-012: times come from PostgreSQL after the lock).
	BumpRevision(ctx context.Context, pkg PackageRef) (RevSeq, time.Time, error)
	// Claim checks ownership and claims the package in the commit transaction
	// (R-AUTHZ-018; C-14): 403 package_owned_by_other_owner. w says what kind of
	// write it is, so a publish and a tag change are told apart. A writer bound to
	// no request - a fill, a job - answers errs.Internal: those paths decide no
	// ownership.
	Claim(ctx context.Context, t Target, w OwnershipWrite) error
	// AutoClaim applies the namespace auto claim of R-REPO-023 (C-15) and returns
	// what it did, which the audit event of the publish records. A writer bound to
	// no request answers errs.Internal.
	AutoClaim(ctx context.Context, name string) (AutoClaimResult, error)
	// Notify attaches a notification to the transaction: a change area writes its
	// change-log row in it, a signal area is published after the commit
	// (R-ARCH-018). A rollback sends neither.
	Notify(area string, payload any) error
	// Enqueue enqueues a job in the transaction (R-ARCH-016).
	Enqueue(ctx context.Context, j JobSpec) error
}
