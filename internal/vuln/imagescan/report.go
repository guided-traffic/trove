package imagescan

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/vuln/advisory"
)

// maxTextBytes and the bounds of a detection: a report is the analyser's answer to an untrusted image, so what it says is cut before it
// reaches a row (R-VULN-006 applies the same limits to an advisory record).
const (
	maxSummaryBytes    = 1 << 10
	maxDetailsBytes    = 16 << 10
	maxReferences      = 64
	maxComponents      = 256
	maxAdvisories      = 50_000
	maxComponentField  = 512
	maxCWEs            = 100
	maxAdvisoryIDBytes = 128
)

var (
	advisoryID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	cweID      = regexp.MustCompile(`^CWE-[0-9]+$`)
)

// Component is one package inside an image that an advisory affects (R-VULN-012): an operating-system or a language package, and the
// version the image carries.
type Component struct {
	// Type is the ecosystem of the package as the analyser names it: apk, deb, rpm, npm, gobinary, pip and so on.
	Type string `json:"type"`
	// Class is os-pkgs for an operating-system package and lang-pkgs for a language package.
	Class        string `json:"class"`
	Name         string `json:"name"`
	Version      string `json:"version"`
	FixedVersion string `json:"fixedVersion,omitempty"`
	// Path is where a language package was found in the image, empty for an operating-system package.
	Path string `json:"path,omitempty"`
}

// Advisory is one advisory of a detection and the packages of the image it affects.
type Advisory struct {
	ID             string
	Title          string
	Description    string
	Severity       advisory.Severity
	SeveritySource string
	CVSSScore      float64 // -1 for none
	CVSSVersion    string
	CVSSVector     string
	References     []string
	CWEs           []string
	Published      time.Time
	Modified       time.Time
	Components     []Component
}

// FixedVersions lists the versions of the components that have a fix, sorted and without repeats.
func (a Advisory) FixedVersions() []string {
	var out []string
	for _, c := range a.Components {
		if c.FixedVersion != "" && !contains(out, c.FixedVersion) {
			out = append(out, c.FixedVersion)
		}
	}
	sort.Strings(out)
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Detection is what one detection says about an image.
type Detection struct {
	OSFamily, OSName string
	// EOSL says that the distribution no longer provides security updates, so that an absence of findings says little.
	EOSL       bool
	Advisories []Advisory
}

// report is the part of the analyser's JSON report that is read: schema version 2 of Trivy.
type report struct {
	SchemaVersion int `json:"SchemaVersion"`
	Metadata      struct {
		OS *struct {
			Family string `json:"Family"`
			Name   string `json:"Name"`
			EOSL   bool   `json:"EOSL"`
		} `json:"OS"`
	} `json:"Metadata"`
	Results []struct {
		Class           string          `json:"Class"`
		Type            string          `json:"Type"`
		Target          string          `json:"Target"`
		Vulnerabilities []vulnerability `json:"Vulnerabilities"`
	} `json:"Results"`
}

type vulnerability struct {
	VulnerabilityID  string          `json:"VulnerabilityID"`
	PkgName          string          `json:"PkgName"`
	PkgPath          string          `json:"PkgPath"`
	InstalledVersion string          `json:"InstalledVersion"`
	FixedVersion     string          `json:"FixedVersion"`
	Severity         string          `json:"Severity"`
	SeveritySource   string          `json:"SeveritySource"`
	Title            string          `json:"Title"`
	Description      string          `json:"Description"`
	References       []string        `json:"References"`
	PrimaryURL       string          `json:"PrimaryURL"`
	CweIDs           []string        `json:"CweIDs"`
	CVSS             map[string]cvss `json:"CVSS"`
	PublishedDate    *time.Time      `json:"PublishedDate"`
	LastModifiedDate *time.Time      `json:"LastModifiedDate"`
}

type cvss struct {
	V2Vector  string  `json:"V2Vector"`
	V3Vector  string  `json:"V3Vector"`
	V40Vector string  `json:"V40Vector"`
	V2Score   float64 `json:"V2Score"`
	V3Score   float64 `json:"V3Score"`
	V40Score  float64 `json:"V40Score"`
}

// ParseReport reads the analyser's JSON report within limit bytes and turns it into a detection, one advisory per identifier with the
// components it affects. A report of another schema version is refused: the fields it might have moved are not guessed.
func ParseReport(r io.Reader, limit int64) (Detection, error) {
	var rep report
	dec := json.NewDecoder(io.LimitReader(r, limit+1))
	if err := dec.Decode(&rep); err != nil {
		return Detection{}, fmt.Errorf("the analyser's report: %w", err)
	}
	if dec.More() {
		return Detection{}, fmt.Errorf("the analyser's report holds more than one document or exceeds %d bytes", limit)
	}
	if rep.SchemaVersion != 2 {
		return Detection{}, fmt.Errorf("the analyser's report has schema version %d, this release reads 2", rep.SchemaVersion)
	}
	det := Detection{}
	if rep.Metadata.OS != nil {
		det.OSFamily, det.OSName, det.EOSL = cut(rep.Metadata.OS.Family, 64), cut(rep.Metadata.OS.Name, 64), rep.Metadata.OS.EOSL
	}
	byID := map[string]*Advisory{}
	for _, res := range rep.Results {
		for _, v := range res.Vulnerabilities {
			if err := addVulnerability(byID, res.Class, res.Type, v); err != nil {
				return Detection{}, err
			}
		}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	det.Advisories = make([]Advisory, 0, len(ids))
	for _, id := range ids {
		a := byID[id]
		sort.Slice(a.Components, func(i, j int) bool { return lessComponent(a.Components[i], a.Components[j]) })
		det.Advisories = append(det.Advisories, *a)
	}
	return det, nil
}

func lessComponent(a, b Component) bool {
	if a.Type != b.Type {
		return a.Type < b.Type
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	if a.Version != b.Version {
		return a.Version < b.Version
	}
	return a.Path < b.Path
}

// addVulnerability merges one entry of the report into its advisory. An identifier the rule of R-VULN-006 refuses is dropped; nothing an
// image contains decides what an identifier looks like.
func addVulnerability(byID map[string]*Advisory, class, typ string, v vulnerability) error {
	if !advisoryID.MatchString(v.VulnerabilityID) || len(v.VulnerabilityID) > maxAdvisoryIDBytes {
		return nil
	}
	a := byID[v.VulnerabilityID]
	if a == nil {
		if len(byID) >= maxAdvisories {
			return fmt.Errorf("the analyser's report names more than %d advisories", maxAdvisories)
		}
		a = newAdvisory(v)
		byID[v.VulnerabilityID] = a
	}
	if len(a.Components) >= maxComponents {
		return nil
	}
	c := Component{
		Type: cut(typ, 64), Class: cut(class, 32), Name: cut(v.PkgName, maxComponentField), Version: cut(v.InstalledVersion, maxComponentField),
		FixedVersion: cut(v.FixedVersion, maxComponentField), Path: cut(v.PkgPath, maxComponentField),
	}
	for _, have := range a.Components {
		if have == c {
			return nil
		}
	}
	a.Components = append(a.Components, c)
	return nil
}

func newAdvisory(v vulnerability) *Advisory {
	a := &Advisory{
		ID: v.VulnerabilityID, Title: cut(clean(v.Title), maxSummaryBytes), Description: cut(clean(v.Description), maxDetailsBytes),
		Severity: severityOf(v.Severity), SeveritySource: cut(v.SeveritySource, 64), CVSSScore: -1,
	}
	if a.Title == "" {
		a.Title = cut(clean(firstLine(v.Description)), maxSummaryBytes)
	}
	a.CVSSScore, a.CVSSVersion, a.CVSSVector = bestCVSS(v.SeveritySource, v.CVSS)
	for _, u := range v.References {
		if len(a.References) >= maxReferences {
			break
		}
		if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
			a.References = append(a.References, cut(u, 2048))
		}
	}
	if v.PrimaryURL != "" && len(a.References) < maxReferences && (strings.HasPrefix(v.PrimaryURL, "https://") || strings.HasPrefix(v.PrimaryURL, "http://")) {
		a.References = append([]string{cut(v.PrimaryURL, 2048)}, a.References...)
	}
	for _, c := range v.CweIDs {
		if len(a.CWEs) >= maxCWEs {
			break
		}
		if cweID.MatchString(c) {
			a.CWEs = append(a.CWEs, c)
		}
	}
	if v.PublishedDate != nil {
		a.Published = v.PublishedDate.UTC()
	}
	if v.LastModifiedDate != nil {
		a.Modified = v.LastModifiedDate.UTC()
	}
	return a
}

// severityOf maps the analyser's level onto trove's (R-VULN-007): the analyser has already chosen the vendor's severity for the distribution,
// which is more exact for an operating-system package than the highest CVSS.
func severityOf(s string) advisory.Severity {
	switch strings.ToUpper(s) {
	case "CRITICAL":
		return advisory.SeverityCritical
	case "HIGH":
		return advisory.SeverityHigh
	case "MEDIUM":
		return advisory.SeverityMedium
	case "LOW":
		return advisory.SeverityLow
	}
	return advisory.SeverityUnknown
}

// bestCVSS picks the score of the source that decided the severity, else of the source with the newest vector; a score of another
// source would contradict the level shown next to it. The sources are tried in sorted order so that two runs agree.
func bestCVSS(source string, all map[string]cvss) (float64, string, string) {
	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names)
	if c, ok := all[source]; ok {
		if sc, ver, vec := cvssOf(c); sc >= 0 {
			return sc, ver, vec
		}
	}
	for _, n := range names {
		if sc, ver, vec := cvssOf(all[n]); sc >= 0 {
			return sc, ver, vec
		}
	}
	return -1, "", ""
}

func cvssOf(c cvss) (float64, string, string) {
	switch {
	case c.V40Score > 0:
		return c.V40Score, "4.0", cut(c.V40Vector, 256)
	case c.V3Score > 0:
		ver := "3.1"
		if strings.HasPrefix(c.V3Vector, "CVSS:3.0") {
			ver = "3.0"
		}
		return c.V3Score, ver, cut(c.V3Vector, 256)
	case c.V2Score > 0:
		return c.V2Score, "2.0", cut(c.V2Vector, 256)
	}
	return -1, "", ""
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// clean removes what R-VULN-006 removes from advisory text: control characters except tab and newline, the bidirectional and zero-width
// format characters and the line and paragraph separators.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n':
			return r
		case r < 0x20 || r == 0x7f:
			return -1
		case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r >= 0x200B && r <= 0x200F, r == 0xFEFF, r == 0x00AD, r == 0x2028, r == 0x2029:
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
}

// cut shortens s to at most n bytes on a character boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	return strings.ToValidUTF8(s, "")
}
