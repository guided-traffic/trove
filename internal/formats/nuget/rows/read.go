package rows

import "time"

// The values below are what the reads of the data port return: the version list of
// the flat container (R-NUGET-008), the rows the registration documents render from
// (R-NUGET-009) and the candidates of a search (R-NUGET-010). Like every value of
// this leaf they arrive computed: the version order is the sort key, and the format
// decides what a hive or a search shows.

// VersionEntry is a stored version as the lists render it. Trashed versions are not
// among them (R-NUGET-005: a version whose trashed_at is set is invisible to every
// NuGet document).
type VersionEntry struct {
	VersionID int64
	// LowerVersion is the URL form (package_versions.version), FullVersion the full
	// normalized form (package_versions.display_version).
	LowerVersion, FullVersion string
	// Downloads is the version's download count: the upstream's for a result of a forwarded
	// search (R-NUGET-016), 0 for a stored version until R-STOR-015 ships.
	Downloads int64
	// DeclaredID is the ID casing of this version's .nuspec.
	DeclaredID   string
	Listed       bool
	IsPrerelease bool
	IsSemver2    bool
	PublishedAt  time.Time
}

// FlatIndex is what the version list of the flat container renders from (R-NUGET-008):
// the package's revision (packages.rev_seq), read in the statement that reads the
// versions, and every stored version, ascending by sort key. Rev is 0 when the ID holds no
// version.
type FlatIndex struct {
	Rev      int64
	Versions []VersionEntry
}

// RegistrationVersion is a stored version with the metadata a catalog entry is
// built from (R-NUGET-009).
type RegistrationVersion struct {
	VersionEntry
	// PackageTypes are the lowercased package type names.
	PackageTypes []string
	// Metadata is nuget_version.metadata, the JSON object of R-NUGET-005.
	Metadata []byte
	// Forwarded is what an upstream's search result says of the package beyond the metadata:
	// nil for a stored version (R-NUGET-016, Q-NUGET-78).
	Forwarded *Forwarded
}

// RegistrationPackage is the package a registration document renders: its revision
// (packages.rev_seq, the ETag input of R-NUGET-002), the display name and every
// visible version, ascending by sort key.
type RegistrationPackage struct {
	Rev         int64
	DisplayName string
	Versions    []RegistrationVersion
}

// SearchQuery is one search over the local rows of a repository (R-NUGET-010). The
// terms are already split and folded by the format. A package matches when its latest
// version under Prerelease and SemVer2 (listed, not trashed) satisfies every term and the
// package type; the matches are in rank order: exact ID, ID prefix, ID token or substring,
// then the other fields, ties by lower_id.
type SearchQuery struct {
	RepoID int64
	// Terms are ANDed; Field is "" for free text, else a field of the query syntax of
	// R-NUGET-010, lowercased.
	Terms []SearchTerm
	// Prerelease and SemVer2 say which versions count as visible (R-NUGET-004).
	Prerelease, SemVer2 bool
	// PackageType keeps packages whose latest visible version declares it (lowercased);
	// "" is no filter.
	PackageType string
	// Offset and Limit cut a page out of the matches in rank order. Limit is positive.
	Offset, Limit int
	// KeysOnly leaves the metadata of the latest version out of the hits: a scan that
	// only decides which packages and versions a caller may read asks for no more.
	KeysOnly bool
	// PerVersion is the scan of a caller who may read only some versions (Q-NUGET-74): the
	// terms are tested on every listed visible version instead of the latest alone. A
	// package is a hit when some version satisfies every term but the version: terms and
	// every version: term names one of its listed visible versions; SearchHit.ListedMatch
	// says which versions satisfy the terms but the version: terms, and PackageType is not
	// applied. The format then applies the terms to the versions the caller may read. It
	// implies KeysOnly; the order is the one of the query without it.
	PerVersion bool
	// Only, when set, replaces the terms, the type and the page: the hits are the packages
	// holding one of these listed versions, each with those versions alone, and Latest is
	// the highest of them. The scan that found the versions has applied the terms and the
	// filters of the query already, and the caller's right to read them.
	Only []SearchVersion
}

// SearchVersion names a stored version: the lower-case ID and the URL form of the version.
type SearchVersion struct {
	LowerID, LowerVersion string
}

// SearchTerm is one term of a query.
type SearchTerm struct {
	Field string
	Value string
	// Phrase marks a quoted group.
	Phrase bool
}

// SearchHit is one matching package in rank order: its latest visible version with the
// metadata the result shows, and every listed visible version.
type SearchHit struct {
	LowerID string
	// DisplayName is packages.display_name, the casing the result shows as the ID.
	DisplayName string
	// Latest is the highest visible version; its metadata is empty for a KeysOnly query.
	Latest RegistrationVersion
	// Listed are the listed versions visible under the query, ascending, and ListedTypes
	// their lowercased package type names, index for index.
	Listed      []VersionEntry
	ListedTypes [][]string
	// ListedMatch is set by a PerVersion query, index for index with Listed: whether the
	// version satisfies every term of the query but the version: terms. A version without
	// an entry matches nothing.
	ListedMatch []bool
}
