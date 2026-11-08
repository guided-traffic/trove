// Package rows holds the values that cross the data port of the Debian (APT) format (Q-ARCH-193, D-1, R-DEB-008): the format, an
// edge package that runs no SQL, passes them to the store, and internal/formatdata/deb, a domain package that may not import the
// format, receives them. Both import this leaf, which holds plain data and imports nothing of the module.
//
// Keys cross the port as plain data: armored bytes, fingerprints and times. The OpenPGP library is a leaf of its own and the
// contracts of the format do not import it (apt-work-packages.md, WP0).
package rows

import (
	"encoding/json"
	"time"
)

// The states of a signing key (R-DEB-015).
const (
	KeyPrimary  = "primary"
	KeyNext     = "next"
	KeyWithheld = "withheld"
	KeyRetired  = "retired"
)

// The origins of a signing key.
const (
	KeyGenerated = "generated"
	KeyImported  = "imported"
)

// The states of a suite's published generation (R-DEB-014).
const (
	SuitePending = "pending"
	SuiteCurrent = "current"
	SuiteFailed  = "failed"
)

// The documents a remote stores of a release directory (deb_remote_releases.document).
const (
	DocInRelease = "InRelease"
	DocRelease   = "Release"
	DocAbsent    = "absent"
)

// Coordinate is a suite and component a package file is listed in (R-DEB-009).
type Coordinate struct {
	Suite     string
	Component string
}

// SuiteSeq is the pair of sequence numbers of a suite a client holds a changeSeq of (R-DEB-006, R-DEB-014).
type SuiteSeq struct {
	Suite        string
	ChangeSeq    int64
	PublishedSeq int64
}

// Binary is the row of a stored package file of a local repository (deb_binaries).
type Binary struct {
	ItemID       int64
	RepoID       int64
	Package      string
	Version      string
	Architecture string
	Source       string
	// Control is the normalized paragraph (R-DEB-007).
	Control     string
	Coordinates []Coordinate
}

// UploadInput is a package file to record in the transaction that writes its item (R-DEB-006).
type UploadInput struct {
	// Binary holds the repository and item ids, the identity, the normalized paragraph and the requested coordinates.
	Binary
	// Actor is the principal id of the upload, "" for none.
	Actor string
}

// UploadResult is what an upload recorded: whether the bytes are new, the coordinates the file has now, and the suites whose
// change_seq was raised, with the numbers the answer reports.
type UploadResult struct {
	// Created is false when the identical bytes were stored already and only coordinates were added.
	Created     bool
	Coordinates []Coordinate
	Suites      []SuiteSeq
}

// CoordinatesChange replaces the coordinates of a stored file (R-DEB-009).
type CoordinatesChange struct {
	RepoID      int64
	Path        string
	Coordinates []Coordinate
	Actor       string
}

// CoordinatesChanged is the outcome of a replacement: the file, what was added and removed, and the suites it raised.
type CoordinatesChanged struct {
	ItemID  int64
	SHA256  [32]byte
	Package string
	Version string
	Added   []Coordinate
	Removed []Coordinate
	Suites  []SuiteSeq
}

// RemovedFile is what the removal of a package file left to report: the identity the audit event and the journal entry name, and
// the suites whose content changed.
type RemovedFile struct {
	ItemID       int64
	Path         string
	SHA256       [32]byte
	Package      string
	Version      string
	Architecture string
	Suites       []string
}

// PackageDeleted is the data of the audit event deb.package.deleted with its journal entry (R-DEB-032).
type PackageDeleted struct {
	RepoID  int64
	RepoKey string
	RemovedFile
	Actor string
}

// CoordinatesRemoved is the data of the audit event deb.coordinates.removed with its journal entry (R-DEB-009, Q-DEB-34).
type CoordinatesRemoved struct {
	RepoID  int64
	RepoKey string
	Path    string
	SHA256  [32]byte
	Removed []Coordinate
	Actor   string
}

// Tombstone is the mark of a package file the journal replay withheld (deb_tombstones).
type Tombstone struct {
	RepoID    int64
	Path      string
	SHA256    [32]byte
	DeletedBy string
}

// ReplayResult is what a replay step did, in the words of the journal replay output (R-DEB-032).
type ReplayResult string

// The outcomes of a replay step.
const (
	Applied         ReplayResult = "applied"
	AlreadyInEffect ReplayResult = "already in effect"
	SubjectUnknown  ReplayResult = "subject unknown"
)

// Suite is the state of one suite of a repository (deb_suites), as the index state read reports it (R-DEB-014).
type Suite struct {
	RepoID      int64
	Suite       string
	ChangeSeq   int64
	BuiltSeq    int64
	Generation  int64
	ReleaseDate *time.Time
	PublishedAt *time.Time
	State       string
	Reason      string
	// Inputs is the fingerprint of each member's input of a virtual suite (R-DEB-026).
	Inputs    json.RawMessage
	CheckedAt *time.Time
}

// Key is a signing key of a repository without its private part (deb_signing_keys, R-DEB-015).
type Key struct {
	ID          string
	RepoID      int64
	Fingerprint string
	Algorithm   int16
	Bits        int
	KeyCreated  time.Time
	BoundSince  time.Time
	State       string
	Origin      string
	// PublicKey is the ASCII-armored certificate.
	PublicKey  string
	CreatedAt  time.Time
	CreatedBy  string
	PromotedAt *time.Time
	WithheldAt *time.Time
	RetiredAt  *time.Time
	RetiredBy  string
}

// SigningKey is a key as the index build signs with it: the opened armored secret key, for one signing call. It is never sent in an
// answer, written to a log or kept beyond the call (R-DEB-015).
type SigningKey struct {
	Key
	Secret []byte
}

// NewKey is a key to add as the next key (R-DEB-017): generated by trove, or imported.
type NewKey struct {
	RepoID int64
	// Generate asks for a key generated by trove; otherwise Armored is imported.
	Generate bool
	// Armored is the ASCII-armored secret key to import; Passphrase unlocks it, used once and never stored.
	Armored    []byte
	Passphrase []byte
	Actor      string
}

// Promotion is the outcome of a promotion: the key that signs now and the one that retired.
type Promotion struct {
	Old Key
	New Key
}

// Reinstated is the outcome of the reinstatement of a withheld key: the key and the state it returned to.
type Reinstated struct {
	Key Key
}

// KeyPromotedEntry is the entry of deb.signing_key.promoted the journal replay applies (R-DEB-032).
type KeyPromotedEntry struct {
	RepoID         int64
	OldFingerprint string
	NewFingerprint string
}

// KeyRetiredEntry is the entry of deb.signing_key.retired the journal replay applies (R-DEB-032).
type KeyRetiredEntry struct {
	RepoID      int64
	Fingerprint string
}

// IndexEntry is one paragraph of an index a build writes: the file's normalized paragraph and the fields the index computes from
// the item (R-DEB-011).
type IndexEntry struct {
	Suite        string
	Component    string
	Architecture string
	Package      string
	Version      string
	Control      string
	// Filename is the canonical pool path; Size, MD5 and SHA256 are the item's blob's.
	Filename string
	Size     int64
	MD5      string
	SHA256   string
}

// BuildInputs are what a build reads in its snapshot (R-DEB-010): the suites it builds and the paragraphs of each.
type BuildInputs struct {
	Suites []BuildSuite
}

// BuildSuite is one suite of a build: its sequence numbers, the previous Release Date and the paragraphs of its coordinates.
type BuildSuite struct {
	Suite              Suite
	PreviousDate       *time.Time
	PreviousFiles      map[string]string
	Entries            []IndexEntry
	ExtraArchitectures []string
}

// GenerationRecord is the outcome of a build for one suite, written in the publication's transaction (R-DEB-010, R-DEB-012).
type GenerationRecord struct {
	RepoID      int64
	Suite       string
	ReadSeq     int64
	Generation  int64
	ReleaseDate time.Time
	// Files maps the path relative to dists/<suite>/ to the SHA-256 (hex) of the file.
	Files    map[string]string
	SignedBy []string
	State    string
	Reason   string
}

// Release is the stored release document of a release directory of a remote (deb_remote_releases).
type Release struct {
	RepoID      int64
	Dir         string
	Document    string
	SHA256      [32]byte
	ReleaseDate *time.Time
	ByHash      bool
	VerifiedBy  string
	FloorDate   *time.Time
	StoredAt    time.Time
}

// ReleaseRecord is a release document a fill stores (R-DEB-019, R-DEB-020): the anti-rollback decision is made on it in OnFill.
type ReleaseRecord struct {
	RepoID      int64
	Dir         string
	Document    string
	SHA256      [32]byte
	ReleaseDate time.Time
	ByHash      bool
	// VerifiedBy is the fingerprint that verified the document, "" when the remote has no upstreamKeys.
	VerifiedBy string
	// Absent records that the upstream has no such suite (Q-DEB-41); the other fields are then unused.
	Absent bool
}

// ReleaseDecision is the answer to a ReleaseRecord: whether the document is stored (FillReplaceStored) or kept out as older than the
// floor (R-DEB-019).
type ReleaseDecision struct {
	Stored     bool
	RolledBack bool
}

// Entry is one parsed row of a remote's index (deb_remote_entries): the digest and size the index lists for a path.
type Entry struct {
	Dir          string
	IndexPath    string
	Package      string
	Version      string
	Architecture string
	Path         string
	SHA256       [32]byte
	Size         int64
}

// NameScope is the scope a remote is asked about a name in (Q-DEB-37): the suites, components and architectures the virtual
// repository serves, or those of a mirror.
type NameScope struct {
	RepoID        int64
	Name          string
	Suites        []string
	Components    []string
	Architectures []string
}

// MirrorScope is what a mirror keeps of an archive (R-DEB-024): the suites, components and architectures of a pass.
type MirrorScope struct {
	RepoID        int64
	Suites        []string
	Components    []string
	Architectures []string
	Sources       bool
}

// VirtualInputs are what a build of a virtual repository reads in its snapshot (R-DEB-026): its members' stored state.
type VirtualInputs struct {
	Suites  []Suite
	Members []VirtualMember
}

// VirtualMember is one member of a virtual repository as a build merges it, in member order.
type VirtualMember struct {
	RepoID int64
	Key    string
	// Local is true for a local member, whose published generation is merged.
	Local bool
	// Releases are the stored releases of a remote member's directories.
	Releases []Release
	// Date is the Date of the member's release the previous generation merged (Q-DEB-43).
	Date *time.Time
}

// VersionView is what a package screen shows of one version (R-DEB-030).
type VersionView struct {
	VersionID     int64
	Architectures []string
	Coordinates   []Coordinate
	Description   string
	Fields        map[string]string
}
