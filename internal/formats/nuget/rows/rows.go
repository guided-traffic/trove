// Package rows holds the values that cross the data port of the NuGet format
// (Q-ARCH-193, D-1): the format, an edge package that runs no SQL, passes them to
// the store, and internal/formatdata/nuget, a domain package that may not import the
// format, receives them. Both import this leaf, which holds plain data and imports
// nothing of the module (the rule of WP3.0r: a format's rows leaf imports only
// internal/errs and the standard library).
//
// The store persists what the format decided. Every decision that needs the version
// order or the ID rules (the sort key, the URL form, the lowercased ID, SemVer 2.0.0
// classification) is the format's, so the values arrive computed.
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

// Origin values of nuget_version.origin.
const (
	OriginPush   = "push"
	OriginUpload = "upload"
	OriginCopy   = "copy"
	OriginCache  = "cache"
)

// Tombstone kinds of nuget_tombstone.kind.
const (
	TombstoneRemoved  = "removed"
	TombstoneReserved = "reserved"
)

// VersionInput is the NuGet row of one version to store in the transaction of a
// push, a copy or a remote cache fill (R-NUGET-005), with the package row locked and
// the core version row inserted by the core writer.
type VersionInput struct {
	RepoID    int64
	VersionID int64
	// LowerID is the normalized ID (packages.name), DeclaredID the casing of this
	// version's .nuspec, OriginalVersion the version string as the .nuspec has it and
	// LowerVersion the URL form (package_versions.version).
	LowerID, DeclaredID, OriginalVersion, LowerVersion string
	// SortKey is the order key of R-NUGET-003: byte order is version order.
	SortKey      []byte
	IsPrerelease bool
	IsSemver2    bool
	// PublishedAt is PostgreSQL's clock read after the row lock.
	PublishedAt time.Time
	// PackageTypes are the lowercased package type names, never empty.
	PackageTypes []string
	// Metadata is the extracted .nuspec metadata (R-NUGET-005), a JSON object.
	Metadata []byte
	// The parts of search_doc: IDTokens is the ID split at its separators, space
	// separated; Tags are the tags, space separated.
	IDTokens, Title, Tags, Authors, Summary, Description string
	// Origin is one of the Origin values.
	Origin string
	// FullVersion is the full normalized form (package_versions.display_version, which the
	// core writer stores). The database store ignores it; the in-memory twin has no core
	// rows and reads it.
	FullVersion string
	// SHA512 is the digest of the .nupkg, hex-encoded in the journal entry of the
	// published event (R-NUGET-033). The blob holds the digest; the store does not
	// compare it, so the format passes what the spool computed.
	SHA512 []byte
	// Files are the stored files the event lists (R-OPS-016): the .nupkg.
	Files []File
	// Audit says who published and where; the event is written for every origin but
	// OriginCache, which no request wrote.
	Audit Audit
	// Claim is the outcome of the namespace auto claim of the first version
	// (R-REPO-023), "" for none.
	Claim string
	// Shadowing is what the shadowing check found of a first version (R-REPO-048).
	Shadowing Shadowing
}

// File is a stored file as the published event lists it.
type File struct {
	Path string
	// SHA256 is the hex digest.
	SHA256 string
	Size   int64
}

// ShadowPair is a virtual repository and the remote member it contains that serves the
// ID of a first push.
type ShadowPair struct{ Virtual, Remote string }

// Shadowing is what the shadowing check of a first push found: the pairs a push on the
// strength of manage shadows, and whether a remote member could not be asked
// (R-REPO-048).
type Shadowing struct {
	Shadowed     []ShadowPair
	Undetermined bool
}

// VersionFacts are what the acknowledged-retry rule and the immutability rule read
// about a stored version (R-NUGET-006), from PostgreSQL alone.
type VersionFacts struct {
	// Listed is the listing state.
	Listed bool
	// SHA512 is the digest of the stored .nupkg.
	SHA512 []byte
	// PublishedBy is the principal id of the publisher, "" for none.
	PublishedBy string
	// PublishedAt is when the version was published and Now PostgreSQL's clock
	// when it was read.
	PublishedAt, Now time.Time
}

// Tombstone is the tombstone of a version number (R-NUGET-006).
type Tombstone struct {
	// SHA512 is the digest of the removed .nupkg, or for a reserved number the
	// journaled one (Q-NUGET-10).
	SHA512 []byte
	// Kind is TombstoneRemoved or TombstoneReserved.
	Kind string
}

// SymbolPackageInput is the symbol package of a version to store (R-NUGET-020).
type SymbolPackageInput struct {
	VersionID int64
	// ItemID is the .snupkg item.
	ItemID int64
}
