package osv_test

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/osv"
)

// numericAdapter is a format whose versions are dotted integers: enough to tell an evaluable range from one that is not.
type numericAdapter struct{}

func (numericAdapter) Ecosystem() string { return "npm" }

func (numericAdapter) AdvisoryName(n string) (string, bool) {
	return strings.ToLower(n), n != "" && !strings.ContainsAny(n, " \t")
}

func (numericAdapter) AdvisoryVersion(v string) (string, bool) {
	for _, p := range strings.Split(v, ".") {
		if _, err := strconv.Atoi(p); err != nil {
			return v, false
		}
	}
	return v, v != ""
}

func (a numericAdapter) CompareVersions(x, y string) (int, error) {
	xs, ys := strings.Split(x, "."), strings.Split(y, ".")
	for i := range max(len(xs), len(ys)) {
		xi, yi := 0, 0
		var err error
		if i < len(xs) {
			if xi, err = strconv.Atoi(xs[i]); err != nil {
				return 0, err
			}
		}
		if i < len(ys) {
			if yi, err = strconv.Atoi(ys[i]); err != nil {
				return 0, err
			}
		}
		if xi != yi {
			if xi < yi {
				return -1, nil
			}
			return 1, nil
		}
	}
	return 0, nil
}

func decode(t *testing.T, body string) *osv.Record {
	t.Helper()
	r, err := osv.Decode([]byte(body), "npm", numericAdapter{})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return r
}

func rejected(t *testing.T, body string) {
	t.Helper()
	_, err := osv.Decode([]byte(body), "npm", numericAdapter{})
	if !errors.Is(err, osv.ErrRejected) {
		t.Fatalf("Decode = %v, want a rejection", err)
	}
}

const base = `"id":"GHSA-aaaa-aaaa-0001","modified":"2026-10-01T00:00:00Z"`

// R-VULN-007: the example vectors of the CVSS 3.1 and 4.0 specification documents and the scores published there. The 3.1
// vectors are the examples of the 3.1 specification's examples document (first.org/cvss/v3.1/examples); the 4.0 vectors are
// the extremes the 4.0 specification fixes, a 9.3 base score for full impact on the vulnerable system and none on the
// subsequent systems, and the official calculator's 10.0 and 0.0.
func TestR_VULN_007_SpecExampleVectors(t *testing.T) {
	for _, tt := range []struct {
		vector  string
		version string
		score   float64
		level   advisory.Severity
	}{
		{"CVSS:3.1/AV:N/AC:L/PR:N/UI:R/S:C/C:L/I:L/A:N", "3.1", 6.1, advisory.SeverityMedium},
		{"CVSS:3.1/AV:N/AC:L/PR:L/UI:N/S:C/C:L/I:L/A:N", "3.1", 6.4, advisory.SeverityMedium},
		{"CVSS:3.1/AV:N/AC:H/PR:N/UI:R/S:U/C:L/I:N/A:N", "3.1", 3.1, advisory.SeverityLow},
		{"CVSS:3.1/AV:N/AC:L/PR:L/UI:N/S:C/C:H/I:H/A:H", "3.1", 9.9, advisory.SeverityCritical},
		{"CVSS:3.1/AV:L/AC:L/PR:H/UI:N/S:U/C:L/I:L/A:L", "3.1", 4.2, advisory.SeverityMedium},
		{"CVSS:3.1/AV:N/AC:L/PR:L/UI:N/S:U/C:H/I:H/A:H", "3.1", 8.8, advisory.SeverityHigh},
		{"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:N/A:N", "3.1", 7.5, advisory.SeverityHigh},
		{"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H", "3.1", 9.8, advisory.SeverityCritical},
		{"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:C/C:N/I:H/A:N", "3.1", 8.6, advisory.SeverityHigh},
		{"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:C/C:H/I:H/A:H", "3.1", 10.0, advisory.SeverityCritical},
		{"CVSS:3.1/AV:L/AC:L/PR:N/UI:R/S:U/C:H/I:H/A:N", "3.1", 7.1, advisory.SeverityHigh},
		{"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:N", "3.1", 0.0, advisory.SeverityNone},
		{"CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:N/SI:N/SA:N", "4.0", 9.3, advisory.SeverityCritical},
		{"CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:H/SI:H/SA:H", "4.0", 10.0, advisory.SeverityCritical},
		{"CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:N/VI:N/VA:N/SC:N/SI:N/SA:N", "4.0", 0.0, advisory.SeverityNone},
		{"CVSS:3.0/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H", "3.0", 9.8, advisory.SeverityCritical},
		{"AV:N/AC:L/Au:N/C:P/I:P/A:P", "2.0", 7.5, advisory.SeverityHigh},
	} {
		t.Run(tt.vector, func(t *testing.T) {
			v, err := osv.ScoreVector(tt.vector)
			if err != nil {
				t.Fatal(err)
			}
			if v.Version != tt.version || v.Score != tt.score || v.Level != tt.level {
				t.Fatalf("got %s %.1f %s, want %s %.1f %s", v.Version, v.Score, v.Level, tt.version, tt.score, tt.level)
			}
		})
	}
	for _, bad := range []string{"", "CVSS:3.1/AV:N", "CVSS:9.9/AV:N", "not a vector", "CVSS:4.0/AV:N/AC:L", strings.Repeat("A", 600)} {
		if _, err := osv.ScoreVector(bad); !errors.Is(err, osv.ErrVector) {
			t.Errorf("ScoreVector(%.20q) = %v, want ErrVector", bad, err)
		}
	}
}

// R-VULN-007: the rules in order.
func TestR_VULN_007_Severity(t *testing.T) {
	const v40, v31 = "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:N/SI:N/SA:N", "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:N/A:N"
	t.Run("the newest CVSS version decides", func(t *testing.T) {
		r := decode(t, `{`+base+`,"severity":[{"type":"CVSS_V3","score":"`+v31+`"},{"type":"CVSS_V4","score":"`+v40+`"}]}`)
		a := r.Advisory
		if a.Severity != advisory.SeverityCritical || a.SeveritySource != advisory.SourceCVSS || a.CVSS.Version != "4.0" || a.CVSS.Score != 9.3 {
			t.Fatalf("got %+v", a)
		}
	})
	t.Run("a vector that does not parse is skipped and the next older version tried", func(t *testing.T) {
		r := decode(t, `{`+base+`,"severity":[{"type":"CVSS_V4","score":"CVSS:4.0/AV:N"},{"type":"CVSS_V3","score":"`+v31+`"}]}`)
		if a := r.Advisory; a.CVSS.Version != "3.1" || a.CVSS.Score != 7.5 || a.Severity != advisory.SeverityHigh {
			t.Fatalf("got %+v", a)
		}
	})
	t.Run("the qualitative field is a fallback", func(t *testing.T) {
		r := decode(t, `{`+base+`,"database_specific":{"severity":"MODERATE"}}`)
		if a := r.Advisory; a.Severity != advisory.SeverityMedium || a.SeveritySource != advisory.SourceAdvisory || a.CVSS.Present() {
			t.Fatalf("got %+v", a)
		}
	})
	t.Run("neither is unknown", func(t *testing.T) {
		r := decode(t, `{`+base+`}`)
		if a := r.Advisory; a.Severity != advisory.SeverityUnknown || a.SeveritySource != advisory.SourceNone {
			t.Fatalf("got %+v", a)
		}
	})
	t.Run("a malicious record is critical without a score", func(t *testing.T) {
		r := decode(t, `{"id":"MAL-2026-1","modified":"2026-10-01T00:00:00Z","severity":[{"type":"CVSS_V3","score":"`+v31+`"}]}`)
		a := r.Advisory
		if a.Kind != advisory.KindMalicious || a.Severity != advisory.SeverityCritical || a.SeveritySource != advisory.SourceMalicious || a.CVSS.Present() {
			t.Fatalf("got %+v", a)
		}
	})
	t.Run("an alias with a vector decides when the canonical record has none, the highest level winning", func(t *testing.T) {
		own := osv.Own{Level: advisory.SeverityMedium, Source: advisory.SourceAdvisory}
		lo := osv.Own{Level: advisory.SeverityLow, Source: advisory.SourceCVSS, CVSS: advisory.CVSS{Version: "3.1", Vector: "x", Score: 3.1}}
		hi := osv.Own{Level: advisory.SeverityHigh, Source: advisory.SourceCVSS, CVSS: advisory.CVSS{Version: "3.1", Vector: "y", Score: 7.5}}
		none := osv.Own{Level: advisory.SeverityUnknown, Source: advisory.SourceNone}
		if got := osv.Decide(own, []osv.Own{lo, none, hi}); got != hi {
			t.Fatalf("got %+v", got)
		}
		if got := osv.Decide(own, []osv.Own{none}); got != own {
			t.Fatalf("no alias vector: got %+v", got)
		}
		own.Source, own.CVSS = advisory.SourceCVSS, lo.CVSS
		if got := osv.Decide(own, []osv.Own{hi}); got != own {
			t.Fatalf("the canonical vector stands: got %+v", got)
		}
		mal := osv.Own{Level: advisory.SeverityCritical, Source: advisory.SourceMalicious}
		if got := osv.Decide(mal, []osv.Own{hi}); got != mal {
			t.Fatalf("malicious stands: got %+v", got)
		}
	})
}

// R-VULN-006: the validation and normalization rules.
func TestR_VULN_006_Validation(t *testing.T) {
	t.Run("an identifier that does not satisfy the rule is rejected", func(t *testing.T) {
		for _, id := range []string{"../x", "", "-lead", "a b", strings.Repeat("a", 129), "a/b"} {
			rejected(t, fmt.Sprintf(`{"id":%q,"modified":"2026-10-01T00:00:00Z"}`, id))
		}
	})
	t.Run("modified, withdrawn and the schema version", func(t *testing.T) {
		rejected(t, `{"id":"A-1","modified":"not-a-timestamp"}`)
		rejected(t, `{"id":"A-1"}`)
		rejected(t, `{"id":"A-1","modified":"2026-10-01T00:00:00Z","withdrawn":"yesterday"}`)
		rejected(t, `{"id":"A-1","modified":"2026-10-01T00:00:00Z","schema_version":"2.0.0"}`)
		decode(t, `{"id":"A-1","modified":"2026-10-01T00:00:00Z","schema_version":"1.7.0"}`)
		decode(t, `{"id":"A-1","modified":"2026-10-01T00:00:00.123456Z"}`)
	})
	t.Run("a record that is not JSON of the shape is rejected", func(t *testing.T) {
		rejected(t, `not json`)
		rejected(t, `{"id":"A-1","modified":"2026-10-01T00:00:00Z","affected":"x"}`)
		rejected(t, `{"id":"A-1","modified":"2026-10-01T00:00:00Z"} {"a":1}`)
		rejected(t, `{"id":"A-1","modified":"2026-10-01T00:00:00Z","affected":[{"package":{"ecosystem":"npm","name":"a"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0","fixed":"1"}]}]}]}`)
		rejected(t, `{"a":`+strings.Repeat("[", 100)+strings.Repeat("]", 100)+`}`)
	})
	t.Run("the texts are cut on a character boundary and cleaned", func(t *testing.T) {
		details := strings.Repeat("ä", 100<<10) // two bytes each
		r := decode(t, `{`+base+`,"summary":"a\u0000b\u001bc\td\ne","details":"`+details+`"}`)
		a := r.Advisory
		if a.Summary != "abc\td\ne" {
			t.Fatalf("summary = %q", a.Summary)
		}
		if len(a.Details) != osv.MaxDetails || strings.ContainsRune(a.Details, '�') {
			t.Fatalf("details has %d bytes", len(a.Details))
		}
		r = decode(t, `{`+base+`,"summary":"`+strings.Repeat("x", 4096)+`"}`)
		if len(r.Advisory.Summary) != osv.MaxSummary {
			t.Fatalf("summary has %d bytes", len(r.Advisory.Summary))
		}
	})
	t.Run("references keep at most 64 entries with an http or https URL", func(t *testing.T) {
		var refs []string
		for i := range 80 {
			refs = append(refs, fmt.Sprintf(`{"type":"WEB","url":"https://example.org/%d"}`, i))
		}
		refs = append(refs, `{"type":"WEB","url":"javascript:alert(1)"}`, `{"type":"WEB","url":"ftp://example.org/x"}`, `{"type":"WEB","url":"//example.org/x"}`)
		r := decode(t, `{`+base+`,"references":[{"type":"WEB","url":"javascript:alert(1)"},{"type":"ADVISORY","url":"http://example.org/a"},`+strings.Join(refs, ",")+`]}`)
		got := r.Advisory.References
		if len(got) != osv.MaxReferences || got[0].URL != "http://example.org/a" {
			t.Fatalf("%d references, first %+v", len(got), got[0])
		}
		for _, g := range got {
			if !strings.HasPrefix(g.URL, "http") {
				t.Fatalf("kept %q", g.URL)
			}
		}
	})
	t.Run("kind, aliases, withdrawn and cwe", func(t *testing.T) {
		r := decode(t, `{"id":"GHSA-aaaa-bbbb-cccc","modified":"2026-10-01T00:00:00Z","withdrawn":"2026-10-02T00:00:00Z","published":"2026-09-01T00:00:00Z",`+
			`"aliases":["CVE-2026-0001","CVE-2026-0001","GHSA-aaaa-bbbb-cccc","bad alias","../x"],"database_specific":{"cwe_ids":["CWE-79","nope","CWE-79","CWE-1321"]}}`)
		a := r.Advisory
		if a.Kind != advisory.KindVulnerability || !a.IsWithdrawn() || a.Published.IsZero() {
			t.Fatalf("got %+v", a)
		}
		if fmt.Sprint(a.Aliases) != "[CVE-2026-0001]" || fmt.Sprint(a.CWEIDs) != "[CWE-79 CWE-1321]" {
			t.Fatalf("aliases %v cwe %v", a.Aliases, a.CWEIDs)
		}
		if a.CanonicalID != a.ID {
			t.Fatalf("canonical before the group is known: %q", a.CanonicalID)
		}
		if !a.Modified.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("modified %v", a.Modified)
		}
		if decode(t, `{"id":"MAL-2026-1","modified":"2026-10-01T00:00:00Z"}`).Advisory.Kind != advisory.KindMalicious {
			t.Fatal("MAL- is not malicious")
		}
	})
	t.Run("the digest is that of the record as received", func(t *testing.T) {
		a := decode(t, `{`+base+`}`)
		b := decode(t, `{`+base+` }`)
		if a.SHA256 == b.SHA256 {
			t.Fatal("two byte strings, one digest")
		}
	})
}

// R-VULN-006: affected entries.
func TestR_VULN_006_Affected(t *testing.T) {
	t.Run("entries of another ecosystem are ignored, names normalized, a refused name dropped and counted", func(t *testing.T) {
		r := decode(t, `{`+base+`,"affected":[`+
			`{"package":{"ecosystem":"PyPI","name":"x"},"versions":["1.0.0"]},`+
			`{"package":{"ecosystem":"npm","name":"Left-Pad"},"versions":["1.0.0"]},`+
			`{"package":{"ecosystem":"npm","name":"bad name"},"versions":["1.0.0"]}]}`)
		if len(r.Advisory.Affected) != 1 || r.Advisory.Affected[0].Name != "left-pad" || r.Unevaluable != 1 {
			t.Fatalf("got %+v, unevaluable %d", r.Advisory.Affected, r.Unevaluable)
		}
	})
	t.Run("ranges, fixed, versions and every_version", func(t *testing.T) {
		r := decode(t, `{`+base+`,"affected":[`+
			`{"package":{"ecosystem":"npm","name":"a"},"ranges":[{"type":"SEMVER","events":[{"introduced":"1.0.0"},{"fixed":"1.0.1"}]},{"type":"GIT","events":[{"introduced":"abc"}]}],"versions":["1.0.0","1.0.0","x"]},`+
			`{"package":{"ecosystem":"npm","name":"b"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"}]}]},`+
			`{"package":{"ecosystem":"npm","name":"c"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"}]}],"versions":["1.0.0"]},`+
			`{"package":{"ecosystem":"npm","name":"d"}}]}`)
		aff := r.Advisory.Affected
		if len(aff) != 3 || r.Unevaluable != 0 {
			t.Fatalf("got %d entries, unevaluable %d", len(aff), r.Unevaluable)
		}
		a := aff[0]
		if len(a.Ranges) != 1 || a.Ranges[0].Type != advisory.RangeSemver || fmt.Sprint(a.Versions) != "[1.0.0]" || fmt.Sprint(a.Fixed) != "[1.0.1]" || a.EveryVersion {
			t.Fatalf("entry a: %+v", a)
		}
		if !aff[1].EveryVersion || aff[2].EveryVersion {
			t.Fatalf("every_version: b %v, c %v", aff[1].EveryVersion, aff[2].EveryVersion)
		}
	})
	t.Run("a range with a version the format cannot parse is stored and counted", func(t *testing.T) {
		r := decode(t, `{`+base+`,"affected":[{"package":{"ecosystem":"npm","name":"a"},"ranges":[{"type":"SEMVER","events":[{"introduced":"1.0.0"},{"fixed":"not-a-version"}]}],"versions":["2.0.0"]}]}`)
		if r.Unevaluable != 1 || len(r.Advisory.Affected) != 1 || fmt.Sprint(r.Advisory.Affected[0].Versions) != "[2.0.0]" {
			t.Fatalf("unevaluable %d, %+v", r.Unevaluable, r.Advisory.Affected)
		}
	})
	t.Run("a record that lists too many entries is rejected", func(t *testing.T) {
		one := `{"package":{"ecosystem":"npm","name":"a"},"versions":["1.0.0"]}`
		rejected(t, `{`+base+`,"affected":[`+strings.Repeat(one+",", osv.MaxAffected)+one+`]}`)
	})
}

// R-VULN-008: the catalog.
func TestR_VULN_008_DecodeKEV(t *testing.T) {
	good := `{"title":"x","vulnerabilities":[` +
		`{"cveID":"CVE-2026-0001","dateAdded":"2026-02-03","dueDate":"2026-03-01","knownRansomwareCampaignUse":"Known"},` +
		`{"cveID":"CVE-2021-44228","dateAdded":"2021-12-10"},` +
		`{"cveID":"CVE-2026-0001","dateAdded":"2026-01-01"}]}`
	es, err := osv.DecodeKEV([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 2 || es[0].CVE != "CVE-2026-0001" || es[0].DateAdded.Format("2006-01-02") != "2026-01-01" {
		t.Fatalf("got %+v", es)
	}
	if es[1].DueDate.IsZero() != true || es[0].Ransomware != "Known" && es[0].Ransomware != "" {
		t.Fatalf("got %+v", es)
	}
	for name, body := range map[string]string{
		"no array":     `{"title":"x"}`,
		"bad id":       `{"vulnerabilities":[{"cveID":"CVE-26-1","dateAdded":"2026-01-01"}]}`,
		"bad date":     `{"vulnerabilities":[{"cveID":"CVE-2026-0001","dateAdded":"01/02/2026"}]}`,
		"bad due date": `{"vulnerabilities":[{"cveID":"CVE-2026-0001","dateAdded":"2026-01-01","dueDate":"soon"}]}`,
		"not json":     `<html>`,
		"too many":     `{"vulnerabilities":[` + strings.Repeat(`{"cveID":"CVE-2026-0001","dateAdded":"2026-01-01"},`, osv.MaxKEVEntries) + `{"cveID":"CVE-2026-0002","dateAdded":"2026-01-01"}]}`,
	} {
		if _, err := osv.DecodeKEV([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := osv.DecodeKEV(make([]byte, osv.MaxKEVBytes+1)); err == nil {
		t.Error("an oversized catalog was accepted")
	}
	if es, err := osv.DecodeKEV([]byte(`{"vulnerabilities":[]}`)); err != nil || len(es) != 0 {
		t.Errorf("an empty catalog: %v %v", es, err)
	}
}
