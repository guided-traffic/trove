package npm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/npm/npmtest"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
)

// scanning is the advisory data of a test: the formats.VulnerabilityData of a host that has wired the scanning.
type scanning struct {
	formats.VulnerabilityData
	state    advisory.DataState
	enabled  bool
	byDef    bool
	advs     map[string][]advisory.Advisory
	internal map[string]bool
	asked    [][]string
	counted  []string
}

func newScanning(advs ...advisory.Advisory) *scanning {
	s := &scanning{
		VulnerabilityData: formats.NoVulnerabilityData(), enabled: true, byDef: true, internal: map[string]bool{},
		state: advisory.DataState{Complete: true, SyncedAt: time.Now(), Epoch: 1},
		advs:  map[string][]advisory.Advisory{},
	}
	for _, a := range advs {
		for _, e := range a.Affected {
			one := a
			one.Affected = []advisory.Affected{e}
			s.advs[e.Name] = append(s.advs[e.Name], one)
		}
	}
	return s
}

func (s *scanning) State(string) advisory.DataState { return s.state }
func (s *scanning) Enabled() bool                   { return s.enabled }
func (s *scanning) LocalByDefault() bool            { return s.enabled && s.byDef }
func (s *scanning) ForNames(_ context.Context, _ string, names []string) (map[string][]advisory.Advisory, error) {
	s.asked = append(s.asked, slices.Clone(names))
	out := map[string][]advisory.Advisory{}
	for _, n := range names {
		if l := s.advs[n]; len(l) > 0 {
			out[n] = l
		}
	}
	return out, nil
}

func (s *scanning) InternalNames(_ context.Context, _ formats.ID, names []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, n := range names {
		if s.internal[n] {
			out[n] = true
		}
	}
	return out, nil
}

func (s *scanning) CountAudit(_ formats.ID, mode, outcome string) {
	s.counted = append(s.counted, mode+"/"+outcome)
}

func rng(events ...advisory.Event) advisory.Range {
	return advisory.Range{Type: advisory.RangeSemver, Events: events}
}

func intro(v string) advisory.Event {
	return advisory.Event{Kind: advisory.EventIntroduced, Version: v}
}
func fixed(v string) advisory.Event { return advisory.Event{Kind: advisory.EventFixed, Version: v} }
func last(v string) advisory.Event {
	return advisory.Event{Kind: advisory.EventLastAffected, Version: v}
}

func vulnAdvisory(id, pkg string, sev advisory.Severity, e advisory.Affected) advisory.Advisory {
	e.Name = pkg
	return advisory.Advisory{
		Ecosystem: "npm", ID: id, CanonicalID: id, Kind: advisory.KindVulnerability, Summary: "summary of " + id,
		Severity: sev, Affected: []advisory.Affected{e},
	}
}

// localHost is a repository of the class with the scanning's data.
func localHost(t *testing.T, class formats.Class, config string, data formats.VulnerabilityData) *formattest.Host {
	t.Helper()
	h := formattest.NewHost(New(), formattest.HostOptions{Class: class, Data: npmtest.New(), Vulnerabilities: data})
	h.Repo().Config = []byte(config)
	return h
}

type auditAnswer map[string][]struct {
	ID                 int64    `json:"id"`
	URL                string   `json:"url"`
	Title              string   `json:"title"`
	Severity           string   `json:"severity"`
	VulnerableVersions string   `json:"vulnerable_versions"`
	CWE                []string `json:"cwe"`
	CVSS               struct {
		Score        float64 `json:"score"`
		VectorString *string `json:"vectorString"`
	} `json:"cvss"`
}

func decodeAnswer(t *testing.T, r *formattest.Response) auditAnswer {
	t.Helper()
	if r.Code != http.StatusOK {
		t.Fatalf("status %d: %s", r.Code, r.Body)
	}
	var a auditAnswer
	if err := json.Unmarshal(r.Body.Bytes(), &a); err != nil {
		t.Fatalf("%v: %s", err, r.Body)
	}
	return a
}

// TestR_VULN_017_LocalAnswersFromTrovesData: in mode local a local, a remote and a virtual repository answer the bulk
// endpoint from the advisory data, send nothing to a provider, and list an advisory only when it affects a requested
// version.
func TestR_VULN_017_LocalAnswersFromTrovesData(t *testing.T) {
	adv := vulnAdvisory("GHSA-aaaa-aaaa-0001", "trove-test-vuln", advisory.SeverityHigh,
		advisory.Affected{Ranges: []advisory.Range{rng(intro("1.0.0"), fixed("1.0.1"))}})
	adv.Aliases = []string{"CVE-2026-1"}
	adv.CWEIDs = []string{"CWE-79"}
	adv.CVSS = advisory.CVSS{Version: "3.1", Vector: "CVSS:3.1/AV:N", Score: 7.5}
	for _, class := range []formats.Class{formats.Local, formats.Remote, formats.Virtual} {
		t.Run(string(class), func(t *testing.T) {
			data := newScanning(adv)
			h := localHost(t, class, `{}`, data)
			r := audit(t, h, auditBulkPath, `{"trove-test-vuln":["1.0.0","1.0.1"],"unrelated":["1.0.0"]}`)
			a := decodeAnswer(t, r)
			if len(a) != 1 || len(a["trove-test-vuln"]) != 1 {
				t.Fatalf("answer %s", r.Body)
			}
			got := a["trove-test-vuln"][0]
			if got.URL != "https://github.com/advisories/GHSA-aaaa-aaaa-0001" || got.Title != "summary of GHSA-aaaa-aaaa-0001" ||
				got.Severity != "high" || got.VulnerableVersions != ">=1.0.0 <1.0.1" || !slices.Equal(got.CWE, []string{"CWE-79"}) ||
				got.CVSS.Score != 7.5 || got.CVSS.VectorString == nil || *got.CVSS.VectorString != "CVSS:3.1/AV:N" {
				t.Errorf("advisory %+v", got)
			}
			if want := advisory.NPMAuditID("GHSA-aaaa-aaaa-0001", 0); got.ID != want {
				t.Errorf("id %d, want %d (Q-VULN-17)", got.ID, want)
			}
			if r.Header().Get("npm-notice") != "" || r.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("headers %v", r.Header())
			}
			if len(data.asked) != 1 || !slices.Equal(data.asked[0], []string{"trove-test-vuln", "unrelated"}) {
				t.Errorf("the advisories were read in %d queries: %v", len(data.asked), data.asked)
			}
			if !slices.Equal(data.counted, []string{"local/ok"}) {
				t.Errorf("counted %v", data.counted)
			}
		})
	}
	t.Run("no requested version is affected", func(t *testing.T) {
		h := localHost(t, formats.Remote, `{}`, newScanning(adv))
		r := audit(t, h, auditBulkPath, `{"trove-test-vuln":["1.0.1","0.9.0"]}`)
		if r.Code != 200 || strings.TrimSpace(r.Body.String()) != "{}" {
			t.Errorf("%d %s", r.Code, r.Body)
		}
	})
}

// TestR_VULN_017_TheAnswerIsByteIdentical: two requests, and two replicas that read the same rows, give the same bytes,
// whatever order the names and versions arrive in.
func TestR_VULN_017_TheAnswerIsByteIdentical(t *testing.T) {
	a1 := vulnAdvisory("GHSA-aaaa-aaaa-0001", "pkg-a", advisory.SeverityHigh, advisory.Affected{EveryVersion: true})
	a2 := vulnAdvisory("GHSA-aaaa-aaaa-0002", "pkg-a", advisory.SeverityLow, advisory.Affected{Versions: []string{"1.2.0"}})
	b1 := vulnAdvisory("GHSA-aaaa-aaaa-0003", "pkg-b", advisory.SeverityCritical, advisory.Affected{Ranges: []advisory.Range{rng(intro("0"), last("2.0.0"))}})
	var first string
	for i, body := range []string{
		`{"pkg-a":["1.2.0","1.0.0"],"pkg-b":["1.0.0"]}`,
		`{"pkg-b":["1.0.0"],"pkg-a":["1.0.0","1.2.0","1.0.0"]}`,
	} {
		h := localHost(t, formats.Remote, `{}`, newScanning(a2, b1, a1))
		r := audit(t, h, auditBulkPath, body)
		if r.Code != 200 {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
		if i == 0 {
			first = r.Body.String()
			continue
		}
		if r.Body.String() != first {
			t.Errorf("body differs:\n%s\n%s", first, r.Body)
		}
	}
	got := auditAnswer{}
	_ = json.Unmarshal([]byte(first), &got)
	if n := len(got["pkg-a"]); n != 2 || got["pkg-a"][0].VulnerableVersions != "*" || got["pkg-a"][1].VulnerableVersions != "=1.2.0" {
		t.Errorf("pkg-a: %s", first)
	}
}

// TestR_VULN_017_VulnerableVersionsText: the ranges as node-semver text, and the completion of a version that the text
// does not match without pre-release inclusion.
func TestR_VULN_017_VulnerableVersionsText(t *testing.T) {
	for _, c := range []struct {
		name      string
		affected  advisory.Affected
		requested string
		want      string
	}{
		{"introduced and fixed", advisory.Affected{Ranges: []advisory.Range{rng(intro("1.0.0"), fixed("2.0.0"))}}, "1.5.0", ">=1.0.0 <2.0.0"},
		{"introduced and last affected", advisory.Affected{Ranges: []advisory.Range{rng(intro("1.0.0"), last("1.9.9"))}}, "1.5.0", ">=1.0.0 <=1.9.9"},
		{"open ended", advisory.Affected{Ranges: []advisory.Range{rng(intro("1.3.0"))}}, "1.5.0", ">=1.3.0"},
		{"from zero", advisory.Affected{Ranges: []advisory.Range{rng(intro("0"), fixed("2.0.0"))}}, "1.5.0", "<2.0.0"},
		{"from zero to the last", advisory.Affected{Ranges: []advisory.Range{rng(intro("0"), last("1.3.0"))}}, "1.3.0", "<=1.3.0"},
		{"every version", advisory.Affected{EveryVersion: true}, "9.9.9", "*"},
		{"explicit versions", advisory.Affected{Versions: []string{"1.0.0", "1.2.0"}}, "1.2.0", "=1.0.0 || =1.2.0"},
		{"two intervals", advisory.Affected{Ranges: []advisory.Range{rng(intro("1.0.0"), fixed("1.1.0"), intro("2.0.0"), fixed("2.1.0"))}}, "2.0.5", ">=1.0.0 <1.1.0 || >=2.0.0 <2.1.0"},
		{"events out of order", advisory.Affected{Ranges: []advisory.Range{rng(fixed("2.0.0"), intro("1.0.0"))}}, "1.5.0", ">=1.0.0 <2.0.0"},
		{"a pre-release inside the range is completed", advisory.Affected{Ranges: []advisory.Range{rng(intro("1.0.0"), fixed("2.0.0"))}}, "2.0.0-beta.1", ">=1.0.0 <2.0.0 || =2.0.0-beta.1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			adv := vulnAdvisory("GHSA-aaaa-aaaa-0001", "pkg", advisory.SeverityHigh, c.affected)
			h := localHost(t, formats.Remote, `{}`, newScanning(adv))
			a := decodeAnswer(t, audit(t, h, auditBulkPath, `{"pkg":["`+c.requested+`"]}`))
			if len(a["pkg"]) != 1 || a["pkg"][0].VulnerableVersions != c.want {
				t.Errorf("%+v, want %q", a["pkg"], c.want)
			}
		})
	}
	t.Run("an unevaluable range lists nothing and matches nothing", func(t *testing.T) {
		adv := vulnAdvisory("GHSA-aaaa-aaaa-0007", "pkg", advisory.SeverityHigh,
			advisory.Affected{Ranges: []advisory.Range{rng(intro("1.0.0"), fixed("not-a-version"))}})
		h := localHost(t, formats.Remote, `{}`, newScanning(adv))
		r := audit(t, h, auditBulkPath, `{"pkg":["1.5.0"]}`)
		if strings.TrimSpace(r.Body.String()) != "{}" {
			t.Errorf("%s", r.Body)
		}
	})
}

// TestR_VULN_017_SeverityTitleAndCWE: the mapping of levels, the title and CWE of a malicious record and the vector.
func TestR_VULN_017_SeverityTitleAndCWE(t *testing.T) {
	for sev, want := range map[advisory.Severity]string{
		advisory.SeverityCritical: "critical", advisory.SeverityHigh: "high", advisory.SeverityMedium: "moderate",
		advisory.SeverityLow: "low", advisory.SeverityNone: "info", advisory.SeverityUnknown: "high",
	} {
		adv := vulnAdvisory("GHSA-aaaa-aaaa-0001", "pkg", sev, advisory.Affected{EveryVersion: true})
		adv.Summary = ""
		h := localHost(t, formats.Remote, `{}`, newScanning(adv))
		a := decodeAnswer(t, audit(t, h, auditBulkPath, `{"pkg":["1.0.0"]}`))
		got := a["pkg"][0]
		if got.Severity != want || got.Title != "GHSA-aaaa-aaaa-0001" || got.CWE == nil || len(got.CWE) != 0 || got.CVSS.Score != 0 || got.CVSS.VectorString != nil {
			t.Errorf("%s: %+v", sev, got)
		}
	}
	mal := advisory.Advisory{
		Ecosystem: "npm", ID: "MAL-2026-1234", CanonicalID: "MAL-2026-1234", Kind: advisory.KindMalicious, Severity: advisory.SeverityNone,
		Affected: []advisory.Affected{{Name: "trove-test-mal", Versions: []string{"1.0.0"}}},
	}
	h := localHost(t, formats.Remote, `{}`, newScanning(mal))
	got := decodeAnswer(t, audit(t, h, auditBulkPath, `{"trove-test-mal":["1.0.0"]}`))["trove-test-mal"][0]
	if got.Severity != "critical" || got.Title != "Malicious code in trove-test-mal" || !slices.Equal(got.CWE, []string{"CWE-506"}) ||
		got.URL != "https://osv.dev/vulnerability/MAL-2026-1234" {
		t.Errorf("malicious: %+v", got)
	}
}

// TestR_VULN_017_InternalNamesAreWithheld: a name the installation treats as internal is not asked for and not answered,
// and the notice says how many were withheld.
func TestR_VULN_017_InternalNamesAreWithheld(t *testing.T) {
	mal := advisory.Advisory{
		Ecosystem: "npm", ID: "MAL-2026-1234", CanonicalID: "MAL-2026-1234", Kind: advisory.KindMalicious,
		Affected: []advisory.Affected{{Name: "trove-test-mal", Versions: []string{"1.0.0"}}},
	}
	ui := vulnAdvisory("GHSA-aaaa-aaaa-0009", "@acme/ui", advisory.SeverityHigh, advisory.Affected{EveryVersion: true})
	data := newScanning(mal, ui)
	data.internal["@acme/ui"] = true
	h := localHost(t, formats.Remote, `{}`, data)
	r := audit(t, h, auditBulkPath, `{"@acme/ui":["1.0.0"],"trove-test-mal":["1.0.0"]}`)
	a := decodeAnswer(t, r)
	if _, ok := a["@acme/ui"]; ok || len(a["trove-test-mal"]) != 1 {
		t.Errorf("answer %s", r.Body)
	}
	if want := "trove audited 1 of 2 packages from its own advisory data; 1 internal names were not matched against public advisories"; r.Header().Get("npm-notice") != want {
		t.Errorf("notice %q", r.Header().Get("npm-notice"))
	}
	if len(data.asked) != 1 || slices.Contains(data.asked[0], "@acme/ui") {
		t.Errorf("the internal name was read: %v", data.asked)
	}

	data.state.Stale, data.state.SyncedAt = true, time.Now().Add(-50*time.Hour)
	r = audit(t, h, auditBulkPath, `{"@acme/ui":["1.0.0"]}`)
	if n := r.Header().Get("npm-notice"); !strings.HasSuffix(n, "; advisory data is 2 days old") || strings.ContainsAny(n, "\r\n") {
		t.Errorf("stale notice %q", n)
	}
}

// TestR_VULN_017_TheDataIsNotComplete is the 503 of an npm import that has not finished, counted as unavailable.
func TestR_VULN_017_TheDataIsNotComplete(t *testing.T) {
	data := newScanning()
	data.state = advisory.DataState{}
	h := localHost(t, formats.Remote, `{}`, data)
	r := audit(t, h, auditBulkPath, `{"pkg":["1.0.0"]}`)
	if r.Code != 503 || errorCode(t, r) != errs.CodeVulnerabilityDataUnavailable || !strings.Contains(r.Body.String(), "not imported yet") {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	if !slices.Equal(data.counted, []string{"local/unavailable"}) {
		t.Errorf("counted %v", data.counted)
	}
}

// TestR_VULN_017_WhereLocalStillAnswers501: the cache view and an endpoint with exposure rules, npm 6's endpoints, and
// the modes that do not answer from trove's data.
func TestR_VULN_017_WhereLocalStillAnswers501(t *testing.T) {
	for _, c := range []struct {
		name   string
		class  formats.Class
		config string
		facts  formats.EndpointFacts
		view   bool
		data   func(*scanning)
	}{
		{"the cache view", formats.Remote, `{}`, formats.EndpointFacts{Name: "default", CacheFill: true}, true, nil},
		{"an endpoint with exposure rules", formats.Remote, `{}`, formats.EndpointFacts{Name: "customers", Rules: true, CacheFill: true}, false, nil},
		{"mode disabled", formats.Remote, `{"npm":{"audit":{"mode":"disabled"}}}`, formats.EndpointFacts{Name: "default", CacheFill: true}, false, nil},
		{"local while the scanning is off", formats.Remote, `{"npm":{"audit":{"local":true}}}`, formats.EndpointFacts{Name: "default", CacheFill: true}, false, func(s *scanning) { s.enabled = false }},
		{"mode forward on a local repository", formats.Local, `{"npm":{"audit":{"mode":"forward"}}}`, formats.EndpointFacts{Name: "default", CacheFill: true}, false, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := newScanning()
			if c.data != nil {
				c.data(data)
			}
			h := localHost(t, c.class, c.config, data)
			for _, path := range []string{auditBulkPath, "-/npm/v1/security/audits/quick", "-/npm/v1/security/audits"} {
				rq, err := h.NewRequest(context.Background(), http.MethodPost, path, strings.NewReader(`{"lodash":["4.17.21"]}`))
				if err != nil {
					t.Fatal(err)
				}
				rq.Endpoint, rq.CacheView = c.facts, c.view
				r := h.ServeRequest(rq, false)
				if r.Code != 501 || errorCode(t, r) != errs.CodeNPMAuditUnavailable {
					t.Errorf("%s: %d %s", path, r.Code, r.Body)
				}
			}
			if len(data.asked) != 0 {
				t.Errorf("the advisories were read")
			}
		})
	}

	t.Run("an endpoint that does not fill caches is answered", func(t *testing.T) {
		h := localHost(t, formats.Remote, `{}`, newScanning())
		rq, err := h.NewRequest(context.Background(), http.MethodPost, auditBulkPath, strings.NewReader(`{"lodash":["4.17.21"]}`))
		if err != nil {
			t.Fatal(err)
		}
		rq.Endpoint = formats.EndpointFacts{Name: "partners"}
		if r := h.ServeRequest(rq, false); r.Code != 200 {
			t.Errorf("%d %s", r.Code, r.Body)
		}
	})
	t.Run("npm 6's endpoints name the bulk endpoint where it is answered", func(t *testing.T) {
		h := localHost(t, formats.Local, `{}`, newScanning())
		r := audit(t, h, "-/npm/v1/security/audits/quick", `{}`)
		if r.Code != 501 || errorCode(t, r) != errs.CodeNPMAuditUnavailable || !strings.Contains(r.Body.String(), "bulk advisory endpoint") {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
	})
	t.Run("the body bounds apply in mode local", func(t *testing.T) {
		h := localHost(t, formats.Remote, `{"npm":{"audit":{"maxBodySize":"1KiB"}}}`, newScanning())
		r := audit(t, h, auditBulkPath, `{"a":["`+strings.Repeat("1", 2000)+`"]}`)
		if r.Code != 413 {
			t.Errorf("%d %s", r.Code, r.Body)
		}
		if r := audit(t, h, auditBulkPath, `[1]`); r.Code != 400 {
			t.Errorf("a body that is no object: %d", r.Code)
		}
	})
}

// TestR_VULN_017_AHostileBodyIsBounded (S4, Q-VULN-71): the versions of one name and of the whole body are capped in mode local,
// a cancelled request stops the evaluation, and a name with thousands of ranges is evaluated without normalizing the ranges
// per version.
func TestR_VULN_017_AHostileBodyIsBounded(t *testing.T) {
	versions := func(n int) string {
		var b strings.Builder
		for i := range n {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `"1.%d.%d"`, i/1000, i%1000)
		}
		return b.String()
	}
	h := localHost(t, formats.Remote, `{"npm":{"audit":{"maxBodySize":"10MiB"}}}`, newScanning())
	t.Run("one name over the per-name bound", func(t *testing.T) {
		r := audit(t, h, auditBulkPath, `{"a":[`+versions(auditMaxVersionsPerName+1)+`]}`)
		if r.Code != 413 || errorCode(t, r) != errs.CodeNPMTooLarge {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
	})
	t.Run("the bound itself is answered", func(t *testing.T) {
		if r := audit(t, h, auditBulkPath, `{"a":[`+versions(auditMaxVersionsPerName)+`]}`); r.Code != 200 {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
	})
	t.Run("the whole body over the total bound", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("{")
		per := versions(auditMaxVersionsPerName)
		for i := 0; i <= auditMaxVersions/auditMaxVersionsPerName; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `"p%d":[%s]`, i, per)
		}
		b.WriteString("}")
		r := audit(t, h, auditBulkPath, b.String())
		if r.Code != 413 || errorCode(t, r) != errs.CodeNPMTooLarge {
			t.Fatalf("%d %.200s", r.Code, r.Body)
		}
	})
	t.Run("many ranges and many versions are evaluated", func(t *testing.T) {
		entry := advisory.Affected{Name: "a"}
		for i := 0; i < 200; i++ {
			entry.Ranges = append(entry.Ranges, advisory.Range{Type: advisory.RangeSemver, Events: []advisory.Event{
				{Kind: advisory.EventIntroduced, Version: fmt.Sprintf("%d.0.0", i+10)}, {Kind: advisory.EventFixed, Version: fmt.Sprintf("%d.5.0", i+10)},
			}})
		}
		a := vulnAdvisory("GHSA-aaaa-bbbb-0001", "a", advisory.SeverityHigh, entry)
		d := localHost(t, formats.Remote, `{"npm":{"audit":{"maxBodySize":"10MiB"}}}`, newScanning(a))
		start := time.Now()
		r := audit(t, d, auditBulkPath, `{"a":[`+versions(auditMaxVersionsPerName)+`,"10.1.0"]}`)
		if r.Code != 413 && r.Code != 200 {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
		if took := time.Since(start); took > 5*time.Second {
			t.Errorf("the evaluation took %s", took)
		}
	})
}

// TestR_VULN_017_TheDefaultMode: local while the installation answers audits from its data, else the mode of R-NPM-038.
func TestR_VULN_017_TheDefaultMode(t *testing.T) {
	for _, c := range []struct {
		class    formats.Class
		config   string
		scanning bool // the installation's default is local
		enabled  bool
		want     string
	}{
		{formats.Remote, `{}`, true, true, auditLocal},
		{formats.Local, `{}`, true, true, auditLocal},
		{formats.Virtual, `{}`, true, true, auditLocal},
		{formats.Remote, `{}`, false, true, auditForward},
		{formats.Virtual, `{}`, false, true, auditForward},
		{formats.Local, `{}`, false, true, auditDisabled},
		{formats.Remote, `{}`, false, false, auditForward},
		{formats.Remote, `{"npm":{"audit":{"local":true}}}`, false, true, auditLocal},
		{formats.Remote, `{"npm":{"audit":{"local":true}}}`, false, false, auditDisabled},
		{formats.Remote, `{"npm":{"audit":{"local":true,"mode":"forward"}}}`, true, true, auditLocal},
		// npm.audit.local false hands the answer to the frozen mode of v0.15.1 (Q-VULN-74).
		{formats.Remote, `{"npm":{"audit":{"local":false}}}`, true, true, auditForward},
		{formats.Local, `{"npm":{"audit":{"local":false}}}`, true, true, auditDisabled},
		{formats.Remote, `{"npm":{"audit":{"local":false,"mode":"disabled"}}}`, true, true, auditDisabled},
		{formats.Remote, `{"npm":{"audit":{"local":false,"mode":"forward"}}}`, true, true, auditForward},
		// a stored mode "local" of a document written before the member existed is no mode: the default applies.
		{formats.Remote, `{"npm":{"audit":{"mode":"local"}}}`, true, true, auditLocal},
		{formats.Remote, `{"npm":{"audit":{"mode":"forward"}}}`, true, true, auditForward},
		{formats.Local, `{"npm":{"audit":{"mode":"forward"}}}`, true, true, auditDisabled},
		{formats.Remote, `{"npm":{"audit":{"mode":"disabled"}}}`, true, true, auditDisabled},
	} {
		data := newScanning()
		data.enabled, data.byDef = c.enabled, c.scanning
		h := localHost(t, c.class, c.config, data)
		if got := auditModeOf(h, c.class, settingsOf(h.Repo().Config).Audit); got != c.want {
			t.Errorf("%s %s scanning=%v enabled=%v: mode %s, want %s", c.class, c.config, c.scanning, c.enabled, got, c.want)
		}
	}
	// A host that has not wired the scanning keeps the defaults of R-NPM-038.
	h := localHost(t, formats.Remote, `{}`, nil)
	if got := auditModeOf(h, formats.Remote, auditSettings{}); got != auditForward {
		t.Errorf("unwired host: %s", got)
	}
}

// TestR_VULN_017_APrereleaseInsideAnAffectedRange is the criterion of 2.0.0-beta.1 in >=1.0.0 <2.0.0 (OSV order): listed,
// and the text carries it so that npm's own range test reports it.
func TestR_VULN_017_APrereleaseInsideAnAffectedRange(t *testing.T) {
	adv := vulnAdvisory("GHSA-aaaa-aaaa-0001", "pkg", advisory.SeverityHigh, advisory.Affected{Ranges: []advisory.Range{rng(intro("1.0.0"), fixed("2.0.0"))}})
	h := localHost(t, formats.Remote, `{}`, newScanning(adv))
	a := decodeAnswer(t, audit(t, h, auditBulkPath, `{"pkg":["2.0.0-beta.1","1.5.0"]}`))
	if len(a["pkg"]) != 1 || a["pkg"][0].VulnerableVersions != ">=1.0.0 <2.0.0 || =2.0.0-beta.1" {
		t.Errorf("%+v", a)
	}
}
