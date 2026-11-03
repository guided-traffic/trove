package advisory

import (
	"crypto/sha256"
	"encoding/binary"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Source is the attribution of an advisory (R-VULN-003): the UI and the API name it next to every advisory.
type Source struct {
	Name    string
	License string
	// URL links the record at its source.
	URL string
}

// Attribution of the identifier prefixes (R-VULN-003). The license of a record of another prefix is not stated:
// osv.dev states none for its aggregate.
const (
	sourceGHSAName    = "GitHub Advisory Database"
	sourceGHSALicense = "CC-BY 4.0"
	sourceMALName     = "OpenSSF malicious-packages"
	sourceMALLicense  = "Apache-2.0"
	sourceOSVName     = "osv.dev"
	osvRecordBase     = "https://osv.dev/vulnerability/"
	ghsaRecordBase    = "https://github.com/advisories/"
)

// SourceOf names the source of a record by its identifier prefix (R-VULN-003): GHSA- is the GitHub Advisory
// Database, MAL- the OpenSSF malicious-packages records, any other prefix osv.dev. The URL is the record at its source;
// for MAL- it is the record's page at osv.dev, the one address that exists for every record.
func SourceOf(id string) Source {
	switch {
	case strings.HasPrefix(id, "GHSA-"):
		return Source{Name: sourceGHSAName, License: sourceGHSALicense, URL: ghsaRecordBase + url.PathEscape(id)}
	case strings.HasPrefix(id, "MAL-"):
		return Source{Name: sourceMALName, License: sourceMALLicense, URL: osvRecordBase + url.PathEscape(id)}
	}
	return Source{Name: sourceOSVName, URL: osvRecordBase + url.PathEscape(id)}
}

// KEVSource is the attribution of the known-exploited flag (R-VULN-003).
func KEVSource() Source {
	return Source{Name: "CISA KEV", License: "CC0 1.0", URL: "https://www.cisa.gov/known-exploited-vulnerabilities-catalog"}
}

// URLOf is the advisory URL rule of R-VULN-017 `url`, shared by the npm audit answer and the NuGet page (whose
// NuGetAuditSuppress names an advisory by it, so it never changes for an identifier): https://github.com/advisories/<GHSA id>
// when the canonical identifier or an alias is a GHSA identifier, else https://osv.dev/vulnerability/<id> of the
// canonical identifier.
func URLOf(canonical string, aliases []string) string {
	if strings.HasPrefix(canonical, "GHSA-") {
		return ghsaRecordBase + url.PathEscape(canonical)
	}
	// The first alias in sorted order, so that the answer does not depend on the order a record lists them.
	sorted := slices.Clone(aliases)
	slices.Sort(sorted)
	for _, a := range sorted {
		if strings.HasPrefix(a, "GHSA-") {
			return ghsaRecordBase + url.PathEscape(a)
		}
	}
	return osvRecordBase + url.PathEscape(canonical)
}

// npmAuditIDBits is the width of the number: 53 bits is the largest integer a JavaScript number holds exactly, which
// pnpm and npm read the `id` into (Q-VULN-17).
const npmAuditIDBits = 53

// NPMAuditID is the numeric id of the npm audit answer for a canonical identifier (R-VULN-017, Q-VULN-17): the first
// 53 bits of SHA-256 of "npm:<canonical id>" as an integer, stable across replicas, releases and restores. attempt is
// 0 for the first hash input and n for "npm:<canonical id>:n", the next input taken when two identifiers of one answer
// collide.
func NPMAuditID(canonical string, attempt int) int64 {
	in := "npm:" + canonical
	if attempt > 0 {
		in += ":" + strconv.Itoa(attempt)
	}
	sum := sha256.Sum256([]byte(in))
	return int64(binary.BigEndian.Uint64(sum[:8]) >> (64 - npmAuditIDBits)) //nolint:gosec // at most 53 bits
}

// NPMAuditIDs assigns the ids of one answer. A collision within the answer is resolved by the next hash input for the
// identifier that sorts later, so the result depends on the set of identifiers and not on the order they arrive in
// (Q-VULN-17). Duplicates in the input map to one id.
func NPMAuditIDs(canonicals []string) map[string]int64 {
	sorted := slices.Clone(canonicals)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	out := make(map[string]int64, len(sorted))
	taken := make(map[int64]bool, len(sorted))
	for _, c := range sorted {
		for attempt := 0; ; attempt++ {
			id := NPMAuditID(c, attempt)
			if !taken[id] {
				taken[id] = true
				out[c] = id
				break
			}
		}
	}
	return out
}
