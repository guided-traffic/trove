// Package rows holds the values that cross the data port of the npm format
// (Q-ARCH-193, D-1): the format, an edge package that runs no SQL, passes them to
// the store, and internal/formatdata/npm, a domain package that may not import the
// format, receives them. Both import this leaf, which holds plain data and
// imports nothing of the module.
//
// The store persists what the format decided. Every decision that needs the
// SemVer order (which version latest points to, whether the latest guard keeps
// it) is the format's, so the dist-tags travel as the complete map the package
// holds after the write.
package rows

import "time"

// Actor is who acts: the audit actor of the request, spelled with plain strings.
type Actor struct {
	// Kind is "user", "service_account", "ci", "break_glass" or "anonymous".
	Kind string
	// ID is the principal id, "" for none.
	ID         string
	Name       string
	TokenID    string
	AuthMethod string
}

// Audit is what an audit event of a write records beyond the write itself: the
// repository, the endpoint of the request and the actor.
type Audit struct {
	RepoKey  string
	Endpoint string
	Actor    Actor
}

// PublishInput is one version to store in the transaction of a publish
// (R-NPM-007 step 5), with the package row locked and the version row inserted by
// the core writer.
type PublishInput struct {
	RepoID    int64
	PackageID int64
	VersionID int64
	// Name is the normalized package name, NameFold its look-alike fold
	// (name.Fold), Version the canonical version.
	Name, NameFold, Version string
	// Manifest is the stored manifest (R-NPM-007), a JSON object.
	Manifest         []byte
	Integrity        string // sha512-<base64>
	Shasum           string // SHA-1, 40 hex characters
	FileCount        int64
	UnpackedSize     int64
	HasInstallScript bool
	HasShrinkwrap    bool
	// Readme and ReadmeFilename are the README of the tarball; the store bounds
	// the text to what the column takes.
	Readme, ReadmeFilename string
	// Tag is the dist-tag the request carried. Tags are the dist-tags the package
	// holds after the publish, and LatestKept says that the latest guard kept
	// latest where it was although the request carried it (R-NPM-016).
	Tag        string
	Tags       map[string]string
	LatestKept bool
	// At is PostgreSQL's clock read after the row lock (PackageWriter.BumpRevision).
	At time.Time
	// PublishedBy is the principal id of the publisher, "" for none.
	PublishedBy string
	// Claim is the outcome of the namespace auto claim, for the audit event.
	Claim string
	// Shadowing is what the shadowing check found of a first version (R-REPO-048).
	Shadowing Shadowing
	Audit     Audit
}

// VersionFacts are what the acknowledged-retry rule and the immutability rule read
// about a version (R-NPM-007 step 4, R-NPM-008), from PostgreSQL alone.
type VersionFacts struct {
	// Live is true when the version is stored and not trashed.
	Live bool
	// Tombstoned is true when the version was unpublished or moved out.
	Tombstoned bool
	// Integrity, PublishTag and PublishedBy are those of the live version.
	Integrity, PublishTag, PublishedBy string
	// PublishedAt is when the version was published and Age the time since, both on
	// PostgreSQL's clock.
	PublishedAt time.Time
	Age         time.Duration
}

// RetryInput records the acknowledgement of a publish retry (R-NPM-007 step 4): an
// audit event and no other change.
type RetryInput struct {
	Name, Version, Integrity, Tag string
	Audit                         Audit
}

// MismatchInput records that the manifest of a publish request differs from the
// package.json of its tarball (npm.publish.manifest_mismatch). Fields names the
// compared fields that differ; Mode is "reject" or "warn".
type MismatchInput struct {
	Name, Version string
	Fields        []string
	Mode          string
	Audit         Audit
}

// SetTagInput records the change of a dist-tag (R-NPM-016): Tags are the dist-tags
// after it, Previous the version Tag pointed to before ("" for none).
type SetTagInput struct {
	RepoID    int64
	PackageID int64
	Name      string
	Tag       string
	Version   string
	Previous  string
	Tags      map[string]string
	// At is PostgreSQL's clock read after the row lock.
	At    time.Time
	Audit Audit
}

// DeleteTagInput records the removal of a dist-tag other than latest.
type DeleteTagInput struct {
	RepoID    int64
	PackageID int64
	Name      string
	Tag       string
	Previous  string
	Tags      map[string]string
	At        time.Time
	Audit     Audit
}

// TombstoneInput writes the tombstone of a live version, reading its integrity
// from the version row, stores the dist-tags the package holds without it and
// records npm.unpublish with its journal entry (R-NPM-008, R-NPM-016, R-NPM-017,
// Q-NPM-12). Source is "unpublish", "move" or "journal".
type TombstoneInput struct {
	RepoID    int64
	PackageID int64
	VersionID int64
	Name      string
	Version   string
	Source    string
	// By is the principal id of the actor; a tombstone needs one.
	By   string
	Tags map[string]string
	At   time.Time
	// Audit is the audit context of the request; the journal entry names By.
	Audit Audit
}

// LocalPackage is what a package document renders from (R-NPM-010): the rows of
// one package read in one statement, so the revision, the dist-tags and the
// versions are those of one snapshot. Versions lists the versions the document
// holds, those that are neither trashed nor tombstoned.
type LocalPackage struct {
	PackageID int64
	// RevSeq is packages.rev_seq as the statement read it.
	RevSeq int64
	// CreatedAt is packages.created_at and ModifiedAt npm_packages.modified_at,
	// both from PostgreSQL's clock (R-NPM-012).
	CreatedAt, ModifiedAt time.Time
	Tags                  map[string]string
	Versions              []LocalVersion
	// Readme and ReadmeFilename are those of the version the latest tag points to.
	Readme, ReadmeFilename string
}

// LocalVersion is one version of a document: its stored manifest (R-NPM-007), the
// deprecation message ("" for none) and the publish time.
type LocalVersion struct {
	Version     string
	Manifest    []byte
	Deprecated  string
	PublishedAt time.Time
}

// LiveVersion is a version a document holds, with the id of its version row.
type LiveVersion struct {
	ID      int64
	Version string
}

// LocalTags are the dist-tags of a package with the revision they were read at.
type LocalTags struct {
	RevSeq int64
	Tags   map[string]string
}

// ShadowPair names a virtual repository and the remote member of it that serves the
// name a publish gives its local repository (R-REPO-048).
type ShadowPair struct{ Virtual, Remote string }

// Shadowing is what the shadowing check found when the publish proceeded: the pairs
// (V, R) that serve the name, for repository.remote_package_shadowed, and whether a
// remote member could not be asked (the publish event records shadowCheck:
// undetermined).
type Shadowing struct {
	Shadowed     []ShadowPair
	Undetermined bool
}

// RemoteVersion is the record of one version a stored upstream document lists
// (npm_remote_versions, R-NPM-024): what tarball serving needs, so that no tarball
// request parses a document. Integrity and Shasum are the upstream's dist values
// ("" for none) and PublishedAt its time[version] (zero for none).
type RemoteVersion struct {
	Version, Integrity, Shasum string
	PublishedAt                time.Time
}

// RemoteFill is the document a metadata fill of a remote repository stores, as the
// fill transaction records it (R-NPM-024). DocModified is its time.modified, zero
// when it has none or none that parses; FetchedAt is the time the core stores the
// document with, on PostgreSQL's clock, which caps the stored DocModified.
type RemoteFill struct {
	RepoID      int64
	Name        string
	DocSHA256   [32]byte
	DocModified time.Time
	FetchedAt   time.Time
	Versions    []RemoteVersion
	Audit       Audit
}

// RemoteApplied is what applying a fill did. Stale says that the document is a stale
// copy of the stored one (older time.modified), Superseded that the stored entry was
// fetched later than a document of the same or an unknown time.modified (the core keeps
// the later one, R-REPO-026): in both cases nothing was changed and the caller keeps
// the stored document. Newer says that the document has a later time.modified than the
// stored one although the stored entry was fetched later, so the entry takes it. Removed
// are the versions that went from listed to unlisted; Relisted those that came back.
type RemoteApplied struct {
	Stale, Superseded, Newer bool
	Removed                  []RemoteVersion
	Relisted                 []string
}

// TarballFacts is everything the serving of one tarball of a remote repository
// reads in one statement (R-NPM-025, R-NPM-026): the version's record, the state of
// the package document's cache entry and the settings that decide.
type TarballFacts struct {
	// Known says that a stored document listed the version at some time: a record exists.
	Known  bool
	Listed bool
	// Integrity and Shasum are the upstream's digests of the version.
	Integrity, Shasum string
	// DocStored says that the package document is stored (variant full); DocGone is
	// when it was marked gone upstream, zero for not gone.
	DocStored bool
	DocGone   time.Time
	// DocCurrent says that the document is not expired on PostgreSQL's clock and was
	// validated (a 200 or a 304) after the last restore of the installation: a cached
	// tarball waits for such a document (Q-NPM-13).
	DocCurrent bool
	// ServeRemoved is npm.serveRemovedVersions of the repository.
	ServeRemoved bool
}
