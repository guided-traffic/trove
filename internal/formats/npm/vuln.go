package npm

import (
	"regexp"
	"strings"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/name"
	"github.com/guided-traffic/trove/internal/formats/npm/semver"
)

// The format takes part in the vulnerability scanning through the optional adapter of the format contract (R-VULN-009,
// ADR-150): the core finds it with formats.CapabilitiesOf. The scanning matches the stored versions of an npm repository
// against the OSV records of the ecosystem "npm", and the order of versions is SemVer 2.0 precedence with build metadata
// ignored, which is semver.Compare; SEMVER and ECOSYSTEM ranges of npm use the same order.
var _ formats.VulnerabilityAdapter = (*Format)(nil)

// Ecosystem implements [formats.VulnerabilityAdapter]: the OSV ecosystem of npm.
func (*Format) Ecosystem() string { return "npm" }

// AdvisoryName implements [formats.VulnerabilityAdapter]: a package name of an advisory is normalized as a stored name is
// (R-NPM-002), case kept; a name that cannot be a package name is refused.
func (*Format) AdvisoryName(n string) (string, bool) {
	parsed, err := name.Parse(n)
	if err != nil {
		return "", false
	}
	return parsed.String(), true
}

// AdvisoryVersion implements [formats.VulnerabilityAdapter]: a version of an advisory is normalized as package_versions.version
// of npm is (R-NPM-007): the SemVer 2.0 version without build metadata. A string that is no SemVer version is refused, and a
// range with such an event is not evaluable.
func (*Format) AdvisoryVersion(v string) (string, bool) {
	parsed, err := semver.Parse(v)
	if err != nil {
		// Advisories name a partial version ("13.0") in ECOSYSTEM ranges; npm's own tooling and osv-scanner read it as 13.0.0
		// (Q-VULN-36). Only advisory versions are completed: a stored version is canonical.
		if partialVersion.MatchString(v) {
			full, err := semver.Parse(v + strings.Repeat(".0", 2-strings.Count(v, ".")))
			if err != nil {
				return "", false
			}
			return full.Canonical(), true
		}
		return "", false
	}
	return parsed.Canonical(), true
}

var partialVersion = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)

// CompareVersions implements [formats.VulnerabilityAdapter] with SemVer 2.0 precedence (build metadata ignored).
func (*Format) CompareVersions(a, b string) (int, error) {
	x, err := semver.Parse(a)
	if err != nil {
		return 0, err
	}
	y, err := semver.Parse(b)
	if err != nil {
		return 0, err
	}
	return semver.Compare(x, y), nil
}
