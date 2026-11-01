package rows

// The values below are what the reads of the package view return (R-API-029,
// R-EP-016): what the screens show of a NuGet version beyond the generic rows. Like
// every value of this leaf they hold plain data; the view adapter reads the JSON.

// ViewSummary is the description and the tags of one version.
type ViewSummary struct {
	VersionID int64
	// Description is the description of the .nuspec, the summary when it has none.
	Description string
	// Tags are the tags of the .nuspec, split on white space.
	Tags []string
}

// ViewDetail is what the version screen reads of one version.
type ViewDetail struct {
	VersionID int64
	// Metadata is nuget_version.metadata, the JSON object of R-NUGET-005.
	Metadata []byte
	// Listed is the listing state.
	Listed bool
	// DeclaredID is the ID casing of this version's .nuspec.
	DeclaredID string
	// Readme is the text of the README file the .nuspec names, "" when there is none or
	// when it could not be read within the bounds of R-NUGET-024; ReadmeFile is the
	// package path of that file. Both are set only when the caller asked for the text.
	Readme, ReadmeFile string
}
