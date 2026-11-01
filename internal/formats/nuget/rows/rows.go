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
	// version's .nuspec, OriginalVersion the version string as the .nuspec has it.
	LowerID, DeclaredID, OriginalVersion string
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
