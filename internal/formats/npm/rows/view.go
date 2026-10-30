package rows

// The values of the package view (R-API-029) that cross the data port: what the
// screens show of a version beyond the generic rows. The format reads them through
// its port and turns them into formats.ViewData.

// ViewSummary is the description and the keywords of a version's manifest.
type ViewSummary struct {
	VersionID   int64
	Description string
	Keywords    []string
}

// ViewDetail is a version as the details page shows it. Readme and ReadmeFilename
// are filled only when the read asked for them.
type ViewDetail struct {
	VersionID      int64
	Manifest       []byte
	Deprecated     string
	HasReadme      bool
	Readme         string
	ReadmeFilename string
	// DistTags are the dist-tags of the package the version belongs to, in that
	// repository.
	DistTags map[string]string
}
