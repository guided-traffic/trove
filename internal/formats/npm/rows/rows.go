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
	Audit Audit
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
