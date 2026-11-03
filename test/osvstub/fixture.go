package osvstub

import (
	"sort"
	"strings"
)

// The names, identifiers and versions of the fixture, for the tests of the packages that use the stub.
const (
	// NPM test packages (published by the test through the npm test upstream's control API or a repository).
	NPMVuln        = "trove-test-vuln"        // 1.0.0 affected, 1.0.1 fixed, CVSS 3.1 (7.5, high)
	NPMMal         = "trove-test-mal"         // 1.0.0 malicious (MAL-2026-1234)
	NPMLate        = "trove-test-late"        // 2.0.0 affected once the toggle ToggleLate is on
	NPMAlias       = "trove-test-alias"       // GHSA-aaaa-bbbb-cccc and CVE-2026-0001, 2.0.0 affected, 2.1.0 fixed
	NPMCVSS        = "trove-test-cvss"        // CVSS 4.0 and 3.1 vectors, 1.0.0 affected, 1.0.1 fixed
	NPMQualitative = "trove-test-qualitative" // database_specific.severity MODERATE only
	NPMNoSeverity  = "trove-test-nosev"       // no severity at all
	NPMBadRange    = "trove-test-badrange"    // fixed: "not-a-version"
	NPMList        = "trove-test-list"        // versions[] only: 1.0.0, 1.2.0
	NPMWithdrawn   = "trove-test-withdrawn"   // a withdrawn record
	NPMAcmeUI      = "@acme/ui"               // a MAL- record, as the claimed internal name
	NPMLeftPad     = "left-pad"               // last_affected 1.3.0 (the npm test upstream's own advisory is <=1.3.0)

	// NuGet test packages: names and versions of the NuGet test upstream's default corpus.
	NuGetHigh   = "Newtonsoft.Json"     // 13.0.1 affected (high), 13.0.3 fixed
	NuGetMal    = "Contoso.Json.Codecs" // 1.0.0 malicious (MAL-2026-1301)
	NuGetMedium = "Contoso.Util"        // 2.0.0 affected (medium, last_affected)
	NuGetLate   = "Contoso.Util"        // 2.1.0 affected once ToggleLate is on

	// Record identifiers.
	IDVuln        = "GHSA-aaaa-aaaa-0001"
	IDMal         = "MAL-2026-1234"
	IDLate        = "GHSA-aaaa-aaaa-0002"
	IDLeftPad     = "GHSA-aaaa-aaaa-0003"
	IDCVSS        = "GHSA-aaaa-aaaa-0004"
	IDQualitative = "GHSA-aaaa-aaaa-0005"
	IDNoSeverity  = "GHSA-aaaa-aaaa-0006"
	IDBadRange    = "GHSA-aaaa-aaaa-0007"
	IDList        = "GHSA-aaaa-aaaa-0008"
	IDWithdrawn   = "GHSA-aaaa-aaaa-0009"
	IDBigDetails  = "GHSA-aaaa-aaaa-0010"
	IDJSReference = "GHSA-aaaa-aaaa-0011"
	IDAlias       = "GHSA-aaaa-bbbb-cccc"
	IDAliasCVE    = "CVE-2026-0001"
	IDAcmeMal     = "MAL-2026-1235"
	IDInvalid     = "../x"
	IDNuGetHigh   = "GHSA-aaaa-aaaa-0101"
	IDNuGetMedium = "GHSA-aaaa-aaaa-0102"
	IDNuGetLate   = "GHSA-aaaa-aaaa-0103"
	IDNuGetMal    = "MAL-2026-1301"

	// Toggles: groups of records that are off at start.
	ToggleLate        = "late"         // IDLate (npm), IDNuGetLate (NuGet): an advisory that appears after a first sync
	ToggleInvalidID   = "invalid-id"   // a record with the id "../x" (npm)
	ToggleBigDetails  = "big-details"  // IDBigDetails: details of 100 KiB (npm)
	ToggleJSReference = "js-reference" // IDJSReference: one reference is a javascript: URL (npm)
	ToggleReal        = "real"         // the records frozen in testdata/real, with a new modified
	ToggleReference   = "reference"    // the npm records behind the public answers recorded in testdata/reference (R-VULN-017), with a new modified
)

// DetailsSize is the size of the details of the ToggleBigDetails record.
const DetailsSize = 100 * 1024

// Record is an OSV record as served: its id and its JSON body.
type Record struct {
	ID   string
	Body []byte
}

// Fixture is a dataset: the records every ecosystem starts with, groups of records that a toggle adds, and the
// catalog.
type Fixture struct {
	// Base is the start content, by ecosystem name ("npm", "NuGet").
	Base map[string][]Record
	// Toggles are groups of records by toggle name, then ecosystem; they are off at start.
	Toggles map[string]map[string][]Record
	// KEV is the start content of the catalog.
	KEV []KEVEntry
}

// Ecosystems lists the ecosystem names of the fixture as ecosystems.txt lists them (case-insensitive order).
func (f *Fixture) Ecosystems() []string {
	var names []string
	for n := range f.Base {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	return names
}

func rec(a Advisory) Record { return Record{ID: a.ID, Body: a.JSON()} }

const (
	cvssHigh   = "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:N/A:N"                    // 7.5
	cvssMedium = "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:L"                    // 5.3
	cvss4      = "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:N/SI:N/SA:N" // 9.3
)

func ref(id string) []Reference {
	return []Reference{{Type: "ADVISORY", URL: "https://advisories.example.org/osvstub/" + id}}
}

func semver(events ...map[string]string) []Range { return []Range{{Type: "SEMVER", Events: events}} }

func ecosystemRange(events ...map[string]string) []Range {
	return []Range{{Type: "ECOSYSTEM", Events: events}}
}

func npmPkg(name string) Package { return Package{Ecosystem: "npm", Name: name} }

func nugetPkg(name string) Package { return Package{Ecosystem: "NuGet", Name: name} }

func ghsa(id, summary string, sev []Severity, aff ...Affected) Advisory {
	return Advisory{
		SchemaVersion: SchemaVersion, ID: id, Published: "2026-09-01T00:00:00Z", Summary: summary,
		Details: summary + ". Synthetic record of the trove test advisory source.", Severity: sev, Affected: aff,
		References: ref(id), DatabaseSpecific: map[string]any{"github_reviewed": true},
	}
}

func malicious(id, name string, pkg Package, versions ...string) Advisory {
	return Advisory{
		SchemaVersion: SchemaVersion, ID: id, Published: "2026-09-02T00:00:00Z",
		Summary:          "Malicious code in " + name + " (" + pkg.Ecosystem + ")",
		Details:          "Synthetic malicious-package record of the trove test advisory source.",
		Affected:         []Affected{{Package: pkg, Versions: versions}},
		References:       []Reference{{Type: "WEB", URL: "https://example.org/osvstub/" + id}},
		DatabaseSpecific: map[string]any{"malicious-packages-origins": []any{}},
	}
}

// DefaultFixture is the dataset for the test packages of test/npmstub and test/nugetstub. Every call builds the
// same set. The ids are synthetic (GHSA-aaaa-…, MAL-2026-…): none is a real advisory.
func DefaultFixture() *Fixture {
	vuln := ghsa(IDVuln, "trove-test-vuln before 1.0.1", []Severity{{Type: "CVSS_V3", Score: cvssHigh}},
		Affected{Package: npmPkg(NPMVuln), Ranges: semver(Introduced("0"), Fixed("1.0.1"))})
	alias := ghsa(IDAlias, "trove-test-alias before 2.1.0", []Severity{{Type: "CVSS_V3", Score: cvssMedium}},
		Affected{Package: npmPkg(NPMAlias), Ranges: semver(Introduced("2.0.0"), Fixed("2.1.0"))})
	alias.Aliases = []string{IDAliasCVE}
	aliasCVE := ghsa(IDAliasCVE, "trove-test-alias before 2.1.0 (the CVE record)", nil,
		Affected{Package: npmPkg(NPMAlias), Ranges: semver(Introduced("2.0.0"), Fixed("2.1.0"))})
	aliasCVE.Aliases = []string{IDAlias}
	aliasCVE.DatabaseSpecific = nil
	leftPad := ghsa(IDLeftPad, "left-pad pads too much", []Severity{{Type: "CVSS_V3", Score: cvssMedium}},
		Affected{Package: npmPkg(NPMLeftPad), Ranges: semver(Introduced("0"), LastAffected("1.3.0"))})
	both := ghsa(IDCVSS, "trove-test-cvss before 1.0.1", []Severity{{Type: "CVSS_V3", Score: cvssHigh}, {Type: "CVSS_V4", Score: cvss4}},
		Affected{Package: npmPkg(NPMCVSS), Ranges: semver(Introduced("0"), Fixed("1.0.1"))})
	qualitative := ghsa(IDQualitative, "trove-test-qualitative before 1.0.1", nil,
		Affected{Package: npmPkg(NPMQualitative), Ranges: semver(Introduced("0"), Fixed("1.0.1"))})
	qualitative.DatabaseSpecific = map[string]any{"github_reviewed": true, "severity": "MODERATE"}
	nosev := ghsa(IDNoSeverity, "trove-test-nosev before 1.0.1", nil,
		Affected{Package: npmPkg(NPMNoSeverity), Ranges: semver(Introduced("0"), Fixed("1.0.1"))})
	nosev.DatabaseSpecific = nil
	badRange := ghsa(IDBadRange, "trove-test-badrange with a fixed that is no version", []Severity{{Type: "CVSS_V3", Score: cvssMedium}},
		Affected{Package: npmPkg(NPMBadRange), Ranges: semver(Introduced("0"), Fixed("not-a-version"))})
	list := ghsa(IDList, "trove-test-list 1.0.0 and 1.2.0", []Severity{{Type: "CVSS_V3", Score: cvssMedium}},
		Affected{Package: npmPkg(NPMList), Versions: []string{"1.0.0", "1.2.0"}})
	withdrawn := ghsa(IDWithdrawn, "trove-test-withdrawn before 1.0.1", []Severity{{Type: "CVSS_V3", Score: cvssMedium}},
		Affected{Package: npmPkg(NPMWithdrawn), Ranges: semver(Introduced("0"), Fixed("1.0.1"))})
	withdrawn.Withdrawn = "2026-09-20T00:00:00Z"

	nugetHigh := ghsa(IDNuGetHigh, "Newtonsoft.Json before 13.0.3", []Severity{{Type: "CVSS_V3", Score: cvssHigh}},
		Affected{Package: nugetPkg(NuGetHigh), Ranges: ecosystemRange(Introduced("0"), Fixed("13.0.3"))})
	nugetMedium := ghsa(IDNuGetMedium, "Contoso.Util up to 2.0.0", []Severity{{Type: "CVSS_V3", Score: cvssMedium}},
		Affected{Package: nugetPkg(NuGetMedium), Ranges: ecosystemRange(Introduced("0"), LastAffected("2.0.0"))})

	late := ghsa(IDLate, "trove-test-late 2.0.0", []Severity{{Type: "CVSS_V3", Score: cvssHigh}},
		Affected{Package: npmPkg(NPMLate), Ranges: semver(Introduced("2.0.0"), Fixed("2.0.1"))})
	nugetLate := ghsa(IDNuGetLate, "Contoso.Util 2.1.0", []Severity{{Type: "CVSS_V3", Score: cvssHigh}},
		Affected{Package: nugetPkg(NuGetLate), Ranges: ecosystemRange(Introduced("2.1.0"), Fixed("2.2.0"))})

	invalid := ghsa(IDInvalid, "a record whose id is no identifier", nil,
		Affected{Package: npmPkg("trove-test-badid"), Ranges: semver(Introduced("0"), Fixed("1.0.1"))})
	big := ghsa(IDBigDetails, "a record with details of 100 KiB", []Severity{{Type: "CVSS_V3", Score: cvssMedium}},
		Affected{Package: npmPkg("trove-test-big"), Ranges: semver(Introduced("0"), Fixed("1.0.1"))})
	big.Details = strings.Repeat("Synthetic details of a record that is much too long. ", DetailsSize/52+1)[:DetailsSize]
	js := ghsa(IDJSReference, "a record with a javascript: reference", []Severity{{Type: "CVSS_V3", Score: cvssMedium}},
		Affected{Package: npmPkg("trove-test-js"), Ranges: semver(Introduced("0"), Fixed("1.0.1"))})
	js.References = []Reference{{Type: "WEB", URL: "javascript:alert(1)"}, {Type: "ADVISORY", URL: "https://advisories.example.org/osvstub/" + IDJSReference}}

	f := &Fixture{
		Base: map[string][]Record{
			"npm": {
				rec(vuln), rec(malicious(IDMal, NPMMal, npmPkg(NPMMal), "1.0.0")),
				rec(alias), rec(aliasCVE),
				rec(malicious(IDAcmeMal, NPMAcmeUI, npmPkg(NPMAcmeUI), "1.0.0", "2.0.0")),
				rec(leftPad), rec(both), rec(qualitative), rec(nosev), rec(badRange), rec(list), rec(withdrawn),
			},
			"NuGet": {
				rec(nugetHigh), rec(malicious(IDNuGetMal, NuGetMal, nugetPkg(NuGetMal), "1.0.0")), rec(nugetMedium),
			},
		},
		Toggles: map[string]map[string][]Record{
			ToggleLate:        {"npm": {rec(late)}, "NuGet": {rec(nugetLate)}},
			ToggleInvalidID:   {"npm": {rec(invalid)}},
			ToggleBigDetails:  {"npm": {rec(big)}},
			ToggleJSReference: {"npm": {rec(js)}},
			ToggleReal:        realGroups(),
			ToggleReference:   referenceGroups(),
		},
		KEV: []KEVEntry{
			{CVEID: "CVE-2026-9001", VendorProject: "Example", Product: "Example Server", DateAdded: "2026-10-05", Ransomware: "Known"},
			{CVEID: "CVE-2026-9002", VendorProject: "Example", Product: "Example Client", DateAdded: "2026-10-12"},
		},
	}
	return f
}
