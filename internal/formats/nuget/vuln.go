package nuget

import (
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/id"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
)

// The format takes part in the vulnerability scanning through the optional adapter of the format contract (R-VULN-009,
// ADR-150): the core finds it with formats.CapabilitiesOf. The scanning matches the stored versions of a NuGet repository
// against the OSV records of the ecosystem "NuGet"; names are the lowercased IDs and versions the normalized versions of
// R-NUGET-003, in NuGet's version precedence.
var _ formats.VulnerabilityAdapter = (*Format)(nil)

// Ecosystem implements [formats.VulnerabilityAdapter]: the OSV ecosystem of NuGet.
func (*Format) Ecosystem() string { return "NuGet" }

// AdvisoryName implements [formats.VulnerabilityAdapter]: the ID of an advisory is lowercased as the canonical name of a
// stored package is (R-NUGET-003). An ID that no repository could hold (the upstream rule of R-NUGET-003) is refused.
func (*Format) AdvisoryName(n string) (string, bool) {
	if id.ValidateUpstream(n) != nil {
		return "", false
	}
	lower := id.Lower(n)
	if !id.ClientForm(lower) {
		return "", false
	}
	return lower, true
}

// AdvisoryVersion implements [formats.VulnerabilityAdapter]: a version of an advisory is normalized as
// package_versions.version of NuGet is (R-NUGET-003): the normalized version without build metadata, lowercased
// (Version.URLForm). A string NuGet does not parse is refused.
func (*Format) AdvisoryVersion(v string) (string, bool) {
	parsed, err := version.Parse(v)
	if err != nil {
		return "", false
	}
	return parsed.URLForm(), true
}

// CompareVersions implements [formats.VulnerabilityAdapter] with NuGet's version precedence (R-NUGET-003).
func (*Format) CompareVersions(a, b string) (int, error) {
	x, err := version.Parse(a)
	if err != nil {
		return 0, err
	}
	y, err := version.Parse(b)
	if err != nil {
		return 0, err
	}
	return version.Compare(x, y), nil
}
