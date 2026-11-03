package nuget

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
)

// scanning is the advisory data of a test: the formats.VulnerabilityData of a host that has wired the scanning, with a page
// cache that keeps a rendering per key and counts the renderings.
type scanning struct {
	formats.VulnerabilityData
	state     advisory.DataState
	enabled   bool
	byDefault bool
	advs      []advisory.Advisory
	internal  map[string]bool
	pageKey   string
	pages     map[string][]byte
	renders   int
	counted   []string
}

func newScanning(advs ...advisory.Advisory) *scanning {
	return &scanning{
		VulnerabilityData: formats.NoVulnerabilityData(), enabled: true, byDefault: true, advs: advs, internal: map[string]bool{},
		state:   advisory.DataState{Complete: true, SyncedAt: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC), Epoch: 7},
		pageKey: "1.1", pages: map[string][]byte{},
	}
}

func (s *scanning) State(string) advisory.DataState { return s.state }
func (s *scanning) Enabled() bool                   { return s.enabled }
func (s *scanning) LocalByDefault() bool            { return s.enabled && s.byDefault }

func (s *scanning) CountAudit(id formats.ID, mode, outcome string) {
	s.counted = append(s.counted, string(id)+"/"+mode+"/"+outcome)
}

func (s *scanning) PageKey(string) string {
	return strconv.FormatInt(s.state.Epoch, 10) + "." + s.pageKey
}

func (s *scanning) All(context.Context, string) ([]advisory.Advisory, error) {
	return s.advs, nil
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

func (s *scanning) Page(ctx context.Context, key string, render func(context.Context) ([]byte, error)) ([]byte, error) {
	if b, ok := s.pages[key]; ok {
		return b, nil
	}
	s.renders++
	b, err := render(ctx)
	if err == nil {
		s.pages[key] = b
	}
	return b, err
}

func nugetAdvisory(id string, sev advisory.Severity, kind advisory.Kind, es ...advisory.Affected) advisory.Advisory {
	return advisory.Advisory{Ecosystem: "NuGet", ID: id, CanonicalID: id, Kind: kind, Severity: sev, Affected: es}
}

func rangeOf(events ...advisory.Event) advisory.Range {
	return advisory.Range{Type: advisory.RangeEcosystem, Events: events}
}

func introduced(v string) advisory.Event {
	return advisory.Event{Kind: advisory.EventIntroduced, Version: v}
}

func fixedAt(v string) advisory.Event { return advisory.Event{Kind: advisory.EventFixed, Version: v} }

func lastAffected(v string) advisory.Event {
	return advisory.Event{Kind: advisory.EventLastAffected, Version: v}
}

func vulnHost(t *testing.T, class formats.Class, config string, data formats.VulnerabilityData) *formattest.Host {
	t.Helper()
	h := newHost(t, formattest.HostOptions{Class: class, Vulnerabilities: data})
	h.Repo().Config = []byte(config)
	return h
}

type serviceIndexDoc struct {
	Resources []struct {
		ID   string `json:"@id"`
		Type string `json:"@type"`
	} `json:"resources"`
}

func listsVulnerabilityInfo(t *testing.T, r *formattest.Response) bool {
	t.Helper()
	var doc serviceIndexDoc
	if err := json.Unmarshal(r.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	for _, res := range doc.Resources {
		if res.Type == "VulnerabilityInfo/6.7.0" {
			if res.ID != feedOfMain+"vulnerabilities/index.json" {
				t.Errorf("@id %s", res.ID)
			}
			return true
		}
	}
	return false
}

// TestR_VULN_018_TheServiceIndexListsVulnerabilityInfo: every class lists the resource while the mode is local and the NuGet
// data is complete, and the index's ETag covers that state, so that a client's cached index gains the entry (R-NUGET-002).
func TestR_VULN_018_TheServiceIndexListsVulnerabilityInfo(t *testing.T) {
	for _, class := range []formats.Class{formats.Local, formats.Remote, formats.Virtual} {
		t.Run(string(class), func(t *testing.T) {
			data := newScanning()
			h := vulnHost(t, class, `{}`, data)
			if !listsVulnerabilityInfo(t, get(t, h, http.MethodGet, "v3/index.json")) {
				t.Fatal("the entry is not listed while the data is complete")
			}
		})
	}
	data := newScanning()
	data.state = advisory.DataState{}
	h := vulnHost(t, formats.Local, `{}`, data)
	without := get(t, h, http.MethodGet, "v3/index.json")
	if listsVulnerabilityInfo(t, without) {
		t.Error("the entry is listed while the data is not complete")
	}
	data.state = advisory.DataState{Complete: true, SyncedAt: time.Now(), Epoch: 1}
	with := get(t, h, http.MethodGet, "v3/index.json")
	if !listsVulnerabilityInfo(t, with) {
		t.Error("the entry is not listed once the data is complete")
	}
	if with.Header().Get("ETag") == without.Header().Get("ETag") {
		t.Error("the ETag does not cover whether the entry is listed")
	}
	cached := get(t, h, http.MethodGet, "v3/index.json", func(r *http.Request) { r.Header.Set("If-None-Match", without.Header().Get("ETag")) })
	if cached.Code != http.StatusOK {
		t.Errorf("a client's cached index without the entry got %d", cached.Code)
	}
	for _, c := range []struct{ name, config string }{
		{"mode off", `{"nuget":{"vulnerabilityInfo":"off"}}`},
		{"mode local with the scanning disabled", `{"nuget":{"vulnerabilityInfo":"local"}}`},
	} {
		d := newScanning()
		d.enabled = c.name != "mode local with the scanning disabled"
		if got := listsVulnerabilityInfo(t, get(t, vulnHost(t, formats.Remote, c.config, d), http.MethodGet, "v3/index.json")); got {
			t.Errorf("%s lists the entry", c.name)
		}
	}
	// A host that has not wired the scanning, and an installation whose default is not local, keep the index as it was.
	d := newScanning()
	d.byDefault = false
	if listsVulnerabilityInfo(t, get(t, vulnHost(t, formats.Remote, `{}`, d), http.MethodGet, "v3/index.json")) {
		t.Error("the entry is listed where the default is not local")
	}
	if listsVulnerabilityInfo(t, get(t, newHost(t, formattest.HostOptions{}), http.MethodGet, "v3/index.json")) {
		t.Error("the entry is listed by a host without the scanning")
	}
}

// TestR_VULN_018_TheIndexHasOneEntry is the document at <feed>/vulnerabilities/index.json.
func TestR_VULN_018_TheIndexHasOneEntry(t *testing.T) {
	data := newScanning()
	h := vulnHost(t, formats.Remote, `{}`, data)
	r := get(t, h, http.MethodGet, "v3/vulnerabilities/index.json")
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	want := `[{"@name":"trove","@id":"` + feedOfMain + `vulnerabilities/trove.json","@updated":"2026-11-03T12:00:00Z","comment":"Advisory data mirrored by trove"}]`
	if r.Body.String() != want {
		t.Errorf("body %s", r.Body)
	}
	if cc := r.Header().Get("Cache-Control"); cc != "private, max-age=1800" || !strings.HasPrefix(r.Header().Get("ETag"), `W/"`) {
		t.Errorf("headers %v", r.Header())
	}
	data.state.Epoch++
	if get(t, h, http.MethodGet, "v3/vulnerabilities/index.json").Header().Get("ETag") == r.Header().Get("ETag") {
		t.Error("the ETag does not follow the data epoch")
	}
}

// TestR_VULN_018_ThePage renders the advisories of the data: the lower-case ID, the severity numbers, the url rule, the
// intervals, the withheld internal name.
func TestR_VULN_018_ThePage(t *testing.T) {
	json1 := nugetAdvisory("GHSA-aaaa-aaaa-0101", advisory.SeverityHigh, advisory.KindVulnerability,
		advisory.Affected{Name: "Newtonsoft.Json", Ranges: []advisory.Range{rangeOf(introduced("13.0.1"), fixedAt("13.0.3"))}})
	json1.Aliases = []string{"CVE-2026-9"}
	mal := nugetAdvisory("MAL-2026-1301", advisory.SeverityNone, advisory.KindMalicious,
		advisory.Affected{Name: "Contoso.Json.Codecs", Versions: []string{"1.0.0"}})
	util := nugetAdvisory("GHSA-aaaa-aaaa-0102", advisory.SeverityMedium, advisory.KindVulnerability,
		advisory.Affected{Name: "Contoso.Util", Ranges: []advisory.Range{rangeOf(introduced("0"), lastAffected("2.0.0"))}})
	all := nugetAdvisory("GHSA-aaaa-aaaa-0200", advisory.SeverityUnknown, advisory.KindVulnerability,
		advisory.Affected{Name: "Everything", EveryVersion: true})
	low := nugetAdvisory("GHSA-aaaa-aaaa-0201", advisory.SeverityLow, advisory.KindVulnerability,
		advisory.Affected{Name: "Lowly", Ranges: []advisory.Range{rangeOf(introduced("1.0.0"), fixedAt("1.5.0"), introduced("2.0.0"))}},
		advisory.Affected{Name: "Lowly", Ranges: []advisory.Range{rangeOf(introduced("0"), fixedAt("0.5.0"))}},
	)
	crit := nugetAdvisory("GHSA-aaaa-aaaa-0202", advisory.SeverityCritical, advisory.KindVulnerability,
		advisory.Affected{Name: "Bad.Range", Ranges: []advisory.Range{rangeOf(introduced("1.0.0"), fixedAt("not-a-version"))}},
		advisory.Affected{Name: "Bad.Range", Ranges: []advisory.Range{{Type: "GIT", Events: []advisory.Event{introduced("0")}}}},
		advisory.Affected{Name: "Dup", Versions: []string{"1.0.0.0"}},
		advisory.Affected{Name: "Dup", Versions: []string{"1.0.0"}},
	)
	data := newScanning(json1, mal, util, all, low, crit)
	data.internal["Contoso.Util"] = true
	h := vulnHost(t, formats.Local, `{}`, data)
	r := get(t, h, http.MethodGet, "v3/vulnerabilities/trove.json")
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	var page map[string][]vulnEntry
	if err := json.Unmarshal(r.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	want := map[string][]vulnEntry{
		"newtonsoft.json":     {{2, "https://github.com/advisories/GHSA-aaaa-aaaa-0101", "[13.0.1, 13.0.3)"}},
		"contoso.json.codecs": {{3, "https://osv.dev/vulnerability/MAL-2026-1301", "[1.0.0]"}},
		"everything":          {{2, "https://github.com/advisories/GHSA-aaaa-aaaa-0200", "(, )"}},
		"lowly": {
			{0, "https://github.com/advisories/GHSA-aaaa-aaaa-0201", "[1.0.0, 1.5.0)"},
			{0, "https://github.com/advisories/GHSA-aaaa-aaaa-0201", "[2.0.0, )"},
			{0, "https://github.com/advisories/GHSA-aaaa-aaaa-0201", "(, 0.5.0)"},
		},
		"dup": {{3, "https://github.com/advisories/GHSA-aaaa-aaaa-0202", "[1.0.0]"}},
	}
	if len(page) != len(want) {
		t.Errorf("names %v, want %d", page, len(want))
	}
	for id, w := range want {
		got := page[id]
		if len(got) != len(w) {
			t.Errorf("%s: %+v, want %+v", id, got, w)
			continue
		}
		for i := range w {
			if got[i] != w[i] {
				t.Errorf("%s[%d]: %+v, want %+v", id, i, got[i], w[i])
			}
		}
	}
	if _, ok := page["contoso.util"]; ok {
		t.Error("an internal name is on the page")
	}
	if _, ok := page["bad.range"]; ok {
		t.Error("a range that cannot be evaluated is on the page")
	}
	if r.Header().Get("Cache-Control") != "private, max-age=1800" {
		t.Errorf("Cache-Control %q", r.Header().Get("Cache-Control"))
	}
}

// TestR_VULN_018_ThePageIsCachedAndValidated: the second request is a cache hit, a matching validator is a 304, and an epoch
// that moves changes the ETag and the page that is rendered.
func TestR_VULN_018_ThePageIsCachedAndValidated(t *testing.T) {
	adv := nugetAdvisory("GHSA-aaaa-aaaa-0101", advisory.SeverityHigh, advisory.KindVulnerability, advisory.Affected{Name: "Pkg", EveryVersion: true})
	data := newScanning(adv)
	h := vulnHost(t, formats.Remote, `{}`, data)
	first := get(t, h, http.MethodGet, "v3/vulnerabilities/trove.json")
	second := get(t, h, http.MethodGet, "v3/vulnerabilities/trove.json")
	if data.renders != 1 || first.Body.String() != second.Body.String() {
		t.Errorf("rendered %d times", data.renders)
	}
	etag := first.Header().Get("ETag")
	cond := get(t, h, http.MethodGet, "v3/vulnerabilities/trove.json", func(r *http.Request) { r.Header.Set("If-None-Match", etag) })
	if cond.Code != http.StatusNotModified || cond.Body.Len() != 0 || cond.Header().Get("ETag") != etag || cond.Header().Get("Cache-Control") != "private, max-age=1800" {
		t.Errorf("conditional: %d %v", cond.Code, cond.Header())
	}
	if head := get(t, h, http.MethodHead, "v3/vulnerabilities/trove.json"); head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") == "" {
		t.Errorf("HEAD: %d", head.Code)
	}

	// The page is one per installation: another repository of another class has the same validator.
	other := get(t, vulnHost(t, formats.Local, `{}`, data), http.MethodGet, "v3/vulnerabilities/trove.json")
	if other.Header().Get("ETag") != etag {
		t.Error("two repositories have two validators for one page")
	}

	for _, change := range []struct {
		name string
		do   func()
	}{
		{"a data epoch", func() { data.state.Epoch++ }},
		{"a claims or repo_config epoch", func() { data.pageKey = "2.1" }},
	} {
		prev := get(t, h, http.MethodGet, "v3/vulnerabilities/trove.json").Header().Get("ETag")
		change.do()
		r := get(t, h, http.MethodGet, "v3/vulnerabilities/trove.json")
		if r.Header().Get("ETag") == prev {
			t.Errorf("%s keeps the ETag", change.name)
		}
		if again := get(t, h, http.MethodGet, "v3/vulnerabilities/trove.json", func(q *http.Request) { q.Header.Set("If-None-Match", prev) }); again.Code != 200 {
			t.Errorf("%s: the old validator got %d", change.name, again.Code)
		}
	}
	if data.renders != 3 {
		t.Errorf("rendered %d times, want one per page key", data.renders)
	}
}

// TestR_VULN_018_WhereTheDocumentsAreNotServed: 404 where the repository does not serve trove's data, 503 before the NuGet
// data is complete.
func TestR_VULN_018_WhereTheDocumentsAreNotServed(t *testing.T) {
	for _, path := range []string{"v3/vulnerabilities/index.json", "v3/vulnerabilities/trove.json"} {
		off := vulnHost(t, formats.Remote, `{"nuget":{"vulnerabilityInfo":"off"}}`, newScanning())
		if r := get(t, off, http.MethodGet, path); r.Code != http.StatusNotFound {
			t.Errorf("%s in mode off: %d", path, r.Code)
		}
		if r := get(t, newHost(t, formattest.HostOptions{}), http.MethodGet, path); r.Code != http.StatusNotFound {
			t.Errorf("%s without the scanning: %d", path, r.Code)
		}
		data := newScanning()
		data.state = advisory.DataState{}
		r := get(t, vulnHost(t, formats.Remote, `{}`, data), http.MethodGet, path)
		if r.Code != http.StatusServiceUnavailable || !strings.Contains(r.Body.String(), errs.CodeVulnerabilityDataUnavailable) {
			t.Errorf("%s before the data is complete: %d %s", path, r.Code, r.Body)
		}
	}
}

// TestR_VULN_024_ThePageRequestsAreCounted: a request of the page, the data of NuGet's audit, is counted in
// trove_vuln_audit_requests_total with format nuget and mode local, by outcome; the index is discovery and not counted.
func TestR_VULN_024_ThePageRequestsAreCounted(t *testing.T) {
	data := newScanning()
	h := vulnHost(t, formats.Remote, `{}`, data)
	get(t, h, http.MethodGet, "v3/vulnerabilities/index.json")
	get(t, h, http.MethodGet, "v3/vulnerabilities/trove.json")
	data.state = advisory.DataState{}
	get(t, h, http.MethodGet, "v3/vulnerabilities/trove.json")
	get(t, vulnHost(t, formats.Remote, `{"nuget":{"vulnerabilityInfo":"off"}}`, data), http.MethodGet, "v3/vulnerabilities/trove.json")
	want := []string{"nuget/local/ok", "nuget/local/unavailable", "nuget/local/refused"}
	if strings.Join(data.counted, ",") != strings.Join(want, ",") {
		t.Errorf("counted %v, want %v", data.counted, want)
	}
}

// TestR_VULN_018_SettingsAreSchemaChecked: nuget.vulnerabilityInfo takes local and off on every class, and upstream is refused
// until R-NUGET-023 is built.
func TestR_VULN_018_SettingsAreSchemaChecked(t *testing.T) {
	if got := settingsOf([]byte(`{"nuget":{"vulnerabilityInfo":"local"}}`)).VulnerabilityInfo; got != VulnInfoLocal {
		t.Errorf("read %q", got)
	}
	if got := settingsOf([]byte(`{"nuget":{"vulnerabilityInfo":"upstream"}}`)).VulnerabilityInfo; got != "" {
		t.Errorf("upstream read as %q", got)
	}
	for _, c := range []struct {
		d      *scanning
		cfg    string
		want   string
		reason string
	}{
		{newScanning(), `{}`, VulnInfoLocal, "the default while the scanning is on"},
		{func() *scanning { s := newScanning(); s.byDefault = false; return s }(), `{}`, VulnInfoOff, "the default while the OSV source is off"},
		{func() *scanning { s := newScanning(); s.byDefault = false; return s }(), `{"nuget":{"vulnerabilityInfo":"local"}}`, VulnInfoLocal, "local set"},
		{func() *scanning { s := newScanning(); s.enabled = false; return s }(), `{"nuget":{"vulnerabilityInfo":"local"}}`, VulnInfoOff, "local while the scanning is disabled"},
	} {
		if got := vulnInfoModeOf(c.d, settingsOf([]byte(c.cfg))); got != c.want {
			t.Errorf("%s: %q, want %q", c.reason, got, c.want)
		}
	}
}
