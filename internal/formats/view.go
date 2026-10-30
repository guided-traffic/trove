package formats

import "context"

// DataPorts is the part of the [Host] that hands out the data port of a format
// (C-3). The package view reads format data through it and needs nothing else of
// the host.
type DataPorts interface {
	// FormatData returns the data port bound to a format, nil for a format
	// without Descriptor.DataPort.
	FormatData(id ID) any
}

// ViewAdapter is a format's part of the package screens (R-API-029): what the
// generic package and version rows do not hold - the description, the keywords,
// the licenses, the format-specific metadata and the README - and the format's
// notion of version order. The core answers everything the generic rows carry
// (names, versions, repositories, times, sizes, downloads, files) and asks the
// adapter for the rest; the adapter reads its own side tables through its data
// port and runs no SQL (R-ARCH-008).
//
// A format without a view adapter (View returns nil) has no package screens: its
// packages are not listed.
type ViewAdapter interface {
	// Compare orders two normalized versions by the format's precedence: negative
	// when a is older than b. It is a total order over the versions the format
	// stores; a string that is no version of the format sorts before every version
	// and equal strings compare equal.
	Compare(a, b string) int
	// Prerelease reports whether a normalized version is a pre-release.
	Prerelease(version string) bool
	// Describe reads, for versions the caller may read, what the screens show of
	// each. A version the format has no data for is absent from the answer. visible
	// says which versions of the same package the caller may read, so that data
	// that names other versions (npm's dist-tags) leaks none that is hidden.
	Describe(ctx context.Context, ports DataPorts, vs []ViewVersion, want ViewFields, visible func(version string) bool) (map[VersionID]ViewData, error)
	// Find returns the ids of at most limit+1 versions in the repositories (by key)
	// whose description contains Text (case-insensitively) or whose keywords
	// contain Keyword exactly. It does not know what a caller may read: the core
	// intersects the ids with the versions the caller may read before it counts
	// anything, so an id of a hidden version changes no answer.
	Find(ctx context.Context, ports DataPorts, f ViewFind, limit int) ([]VersionID, error)
	// Withheld returns the ids of the stored versions in the repositories (by key)
	// that the format does not serve, for example a version a restore's replay
	// tombstoned (R-NPM-047). The screens list none of them.
	Withheld(ctx context.Context, ports DataPorts, repos []string) ([]VersionID, error)
}

// ViewVersion names a stored version for the adapter.
type ViewVersion struct {
	ID      VersionID
	Repo    RepoRef
	Package string
	Version string
}

// ViewFields says how much of a version the adapter has to read; a README is up to
// 64 KiB and the list of packages never needs one.
type ViewFields uint8

const (
	// ViewSummary is the description and the keywords.
	ViewSummary ViewFields = 1 << iota
	// ViewDetails adds the licenses, whether a README exists and the metadata.
	ViewDetails
	// ViewReadme adds the README text and its file name.
	ViewReadme
)

// Has reports whether f includes every field of o.
func (f ViewFields) Has(o ViewFields) bool { return f&o == o }

// ViewData is what the adapter knows of one version.
type ViewData struct {
	Description string
	Keywords    []string
	// Licenses are the declared SPDX identifiers or the expression; nil for none.
	Licenses  []string
	HasReadme bool
	// Metadata is the format-specific object of the version details; a member that
	// does not apply is absent. The format page owns its schema.
	Metadata map[string]any
	// Readme and ReadmeFile are the README source and the name of its file; set
	// only with ViewReadme.
	Readme     string
	ReadmeFile string
}

// ViewFind is a search the adapter answers (ViewAdapter.Find): one of Text and
// Keyword is set.
type ViewFind struct {
	Repos   []string
	Text    string
	Keyword string
}

// View is nil: the format has no package screens.
func (Base) View() ViewAdapter { return nil }
