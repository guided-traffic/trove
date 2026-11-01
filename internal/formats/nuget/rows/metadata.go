package rows

// Metadata is the JSON object stored in nuget_version.metadata (R-NUGET-005, the table
// "Metadata extracted from the .nuspec"): what the ingestion extracted of the .nuspec and
// what the registration documents, the search results and the screens render from. The
// ingestion writes it with [Metadata]'s tags, the readers decode it with the same type, so
// the key names exist once. A member that the .nuspec leaves out is absent.
//
// Text values are trimmed and otherwise verbatim; ranges and target frameworks are the
// strings of the document (R-NUGET-009 renders them as they are).
type Metadata struct {
	Title        string `json:"title,omitempty"`
	Summary      string `json:"summary,omitempty"`
	Description  string `json:"description,omitempty"`
	ReleaseNotes string `json:"releaseNotes,omitempty"`
	Copyright    string `json:"copyright,omitempty"`
	Language     string `json:"language,omitempty"`
	// Authors is kept as one string.
	Authors string `json:"authors,omitempty"`
	// Tags are the tags element split on white space.
	Tags       []string `json:"tags,omitempty"`
	ProjectURL string   `json:"projectUrl,omitempty"`
	IconURL    string   `json:"iconUrl,omitempty"`
	LicenseURL string   `json:"licenseUrl,omitempty"`
	License    *License `json:"license,omitempty"`
	// Icon and Readme are the paths of embedded files, as the .nuspec spells them.
	Icon                     string `json:"icon,omitempty"`
	Readme                   string `json:"readme,omitempty"`
	RequireLicenseAcceptance bool   `json:"requireLicenseAcceptance,omitempty"`
	DevelopmentDependency    bool   `json:"developmentDependency,omitempty"`
	Serviceable              bool   `json:"serviceable,omitempty"`
	// MinClientVersion is the attribute of the metadata element, verbatim.
	MinClientVersion string      `json:"minClientVersion,omitempty"`
	Repository       *Repository `json:"repository,omitempty"`
	// DependencyGroups are the groups in document order; a flat dependency list is one
	// group without a target framework.
	DependencyGroups []DependencyGroup `json:"dependencyGroups,omitempty"`
	// PackageTypes are the declared types; none declared is absent, and the package then
	// has the type Dependency (nuget_version.package_types holds it lowercased).
	PackageTypes []PackageType `json:"packageTypes,omitempty"`
}

// License is the license element of the .nuspec.
type License struct {
	// Type is "expression" or "file".
	Type    string `json:"type"`
	Version string `json:"version,omitempty"`
	Value   string `json:"value"`
}

// Repository is the repository element of the .nuspec.
type Repository struct {
	Type   string `json:"type,omitempty"`
	URL    string `json:"url,omitempty"`
	Branch string `json:"branch,omitempty"`
	Commit string `json:"commit,omitempty"`
}

// DependencyGroup is a group of dependencies for one target framework.
type DependencyGroup struct {
	// TargetFramework is the attribute verbatim, absent for a group without one.
	TargetFramework string       `json:"targetFramework,omitempty"`
	Dependencies    []Dependency `json:"dependencies,omitempty"`
}

// Dependency is one dependency of a group.
type Dependency struct {
	ID string `json:"id"`
	// Version is the range attribute verbatim, absent when the .nuspec has none.
	Version string `json:"version,omitempty"`
	Include string `json:"include,omitempty"`
	Exclude string `json:"exclude,omitempty"`
}

// PackageType is a declared package type.
type PackageType struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}
