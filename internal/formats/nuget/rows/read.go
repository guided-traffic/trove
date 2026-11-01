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
	// DeclaredID is the ID casing of this version's .nuspec.
	DeclaredID   string
	Listed       bool
	IsPrerelease bool
	IsSemver2    bool
	PublishedAt  time.Time
}

// RegistrationVersion is a stored version with the metadata a catalog entry is
// built from (R-NUGET-009).
type RegistrationVersion struct {
	VersionEntry
	// PackageTypes are the lowercased package type names.
	PackageTypes []string
	// Metadata is nuget_version.metadata, the JSON object of R-NUGET-005.
	Metadata []byte
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
// terms are already split and folded by the format.
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
	Latest  RegistrationVersion
	// Listed are the listed versions visible under the query, ascending.
	Listed []VersionEntry
}
