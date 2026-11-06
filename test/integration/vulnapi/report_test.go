//go:build integration

package vulnapi

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestR_VULN_020_Export: the CSV with its header, the protection of a cell that starts like a formula, and the limit.
func TestR_VULN_020_Export(t *testing.T) {
	e := newEnv(t, "TROVE_VULNERABILITIES_EXPORT_MAX_ROWS=4")
	e.addAdvisory("npm", "GHSA-aaaa-bbbb-cccc", "=cmd|' /C calc'!A0", "high", "CVE-2021-23337")
	e.addFinding(finding{Repo: "npm-local", Package: `=HYPERLINK("http://x")`, Advisory: "GHSA-aaaa-bbbb-cccc", CVSS: 7.5, Fix: true})
	e.addFinding(finding{Repo: "npm-local", Package: "plain", Severity: "critical", Kind: "malicious", Advisory: "MAL-2026-1234"})

	a := e.get(e.r, "/api/v1/vulnerabilities/findings/export", e.tokRita)
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if ct := a.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") || !strings.Contains(a.Header.Get("Content-Disposition"), "attachment") {
		t.Errorf("headers: %q %q", ct, a.Header.Get("Content-Disposition"))
	}
	rows, err := csv.NewReader(strings.NewReader(string(a.Body))).ReadAll()
	if err != nil {
		t.Fatalf("%v\n%s", err, a.Body)
	}
	wantHeader := []string{
		"findingId", "status", "severity", "cvssScore", "cvssVersion", "kind", "knownExploited", "advisory", "aliases", "summary",
		"format", "repository", "package", "version", "fixAvailable", "fixedVersions", "blocked", "blockReason", "detectedAt", "resolvedAt", "dismissalReason",
	}
	if len(rows) != 3 || !slices.Equal(rows[0], wantHeader) {
		t.Fatalf("rows = %v", rows)
	}
	col := func(r []string, name string) string { return r[slices.Index(wantHeader, name)] }
	var formula, malicious []string
	for _, r := range rows[1:] {
		switch col(r, "kind") {
		case "malicious":
			malicious = r
		default:
			formula = r
		}
	}
	if got := col(formula, "package"); got != `'=HYPERLINK("http://x")` {
		t.Errorf("package cell = %q, want a leading apostrophe", got)
	}
	if got := col(formula, "summary"); got != `'=cmd|' /C calc'!A0` {
		t.Errorf("summary cell = %q, want a leading apostrophe", got)
	}
	if col(formula, "aliases") != "CVE-2021-23337" || col(formula, "fixAvailable") != "true" || col(formula, "fixedVersions") != "9.9.9" || col(formula, "cvssScore") != "7.5" {
		t.Errorf("formula row = %v", formula)
	}
	if col(malicious, "blocked") != "true" || col(malicious, "blockReason") != "malicious" || col(malicious, "format") != "npm" {
		t.Errorf("malicious row = %v", malicious)
	}

	t.Run("R-VULN-020 an export that would exceed max_rows answers 422 export_too_large before anything is streamed", func(t *testing.T) {
		for i := range 3 {
			e.addFinding(finding{Repo: "npm-local", Package: fmt.Sprintf("more-%d", i)})
		}
		a := e.get(e.r, "/api/v1/vulnerabilities/findings/export", e.tokRita)
		if a.Status != 422 || a.Code(t) != "export_too_large" || !strings.Contains(string(a.Body), "4") {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		// A filter that narrows the export under the limit works.
		if a := e.get(e.r, "/api/v1/vulnerabilities/findings/export"+q("package", "plain"), e.tokRita); a.Status != 200 {
			t.Errorf("filtered: %d %s", a.Status, a.Body)
		}
	})

	t.Run("R-VULN-020 the export takes no paging parameter", func(t *testing.T) {
		if a := e.get(e.r, "/api/v1/vulnerabilities/findings/export"+q("limit", "1"), e.tokRita); a.Status != 400 {
			t.Errorf("%d", a.Status)
		}
	})
}

// TestR_VULN_021_Trend: five findings detected on day 1 and two resolved on day 3 show 5 for days 1 and 2 and 3 from day 3 on.
func TestR_VULN_021_Trend(t *testing.T) {
	e := newEnv(t)
	day := func(n int) string {
		return fmt.Sprintf("(date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC') - interval '%d days' + interval '12 hours'", n)
	}
	// Day 1 is four days ago; day 3 is two days ago; today is day 5.
	for i := range 5 {
		f := finding{Repo: "npm-local", Package: fmt.Sprintf("p%d", i), Severity: "high", Detected: day(4)}
		if i < 2 {
			f.Status, f.Resolved = "resolved", day(2)
		}
		e.addFinding(f)
	}
	// One detected before the range, still open: it is in every count.
	e.addFinding(finding{Repo: "npm-local", Package: "old", Severity: "low", Detected: day(30)})
	from := time.Now().UTC().AddDate(0, 0, -5).Format(time.DateOnly)
	to := time.Now().UTC().Format(time.DateOnly)
	a := e.get(e.r, "/api/v1/vulnerabilities/trend"+q("from", from, "to", to), e.tokRita)
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	var tr struct {
		Basis  string `json:"basis"`
		Points []struct {
			Date   string
			Counts struct{ High, Low int64 }
		} `json:"points"`
	}
	_ = json.Unmarshal(a.Body, &tr)
	var high []int64
	for _, p := range tr.Points {
		high = append(high, p.Counts.High)
		if p.Counts.Low != 1 {
			t.Errorf("%s: low = %d, want 1", p.Date, p.Counts.Low)
		}
	}
	if want := []int64{0, 5, 5, 3, 3, 3}; !slices.Equal(high, want) {
		t.Errorf("high per day = %v, want %v\n%s", high, want, a.Body)
	}
	if tr.Basis == "" {
		t.Error("the response does not say how it counts")
	}
	for _, bad := range []string{q("from", "2020-01-01", "to", to), q("from", to, "to", from), q("from", "yesterday")} {
		if a := e.get(e.r, "/api/v1/vulnerabilities/trend"+bad, e.tokRita); a.Status != 400 {
			t.Errorf("%s: %d %s", bad, a.Status, a.Body)
		}
	}
	// The default range is the last 90 days.
	var dflt struct{ Points []any }
	a = e.get(e.r, "/api/v1/vulnerabilities/trend", e.tokRita)
	_ = json.Unmarshal(a.Body, &dflt)
	if a.Status != 200 || len(dflt.Points) != 90 {
		t.Errorf("default range: %d, %d points", a.Status, len(dflt.Points))
	}
	if a := e.get(e.r, "/api/v1/vulnerabilities/trend"+q("kind", "malicious", "from", from, "to", to), e.tokRita); a.Status != 200 {
		t.Errorf("kind filter: %d", a.Status)
	}
}

// TestR_VULN_021_Sources: the state of the sources after a failed run, the staleness, and the permission.
func TestR_VULN_021_Sources(t *testing.T) {
	e := newEnv(t)
	e.st.Exec(`INSERT INTO vuln_source_state (source, ecosystem, state, synced_at, full_synced_at, attempted_at, failures, last_error, records, rejected, unevaluable)
		VALUES ('osv', 'npm', 'complete', now() - interval '2 days', now() - interval '2 days', now() - interval '1 minute', 1,
		        '{"type":"/api/v1/problems/archive_too_large","title":"Archive too large","status":502,"code":"archive_too_large"}', 231000, 3, 7),
		       ('osv', 'NuGet', 'complete', now() - interval '1 hour', now() - interval '1 hour', now() - interval '1 hour', 0, NULL, 4000, 0, 0)`)
	a := e.get(e.r, "/api/v1/vulnerabilities/sources", e.tokRita)
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	var s struct {
		Items []struct {
			Source, Ecosystem, Mode, State string
			Stale                          bool
			Failures, Records, Rejected    int
			Unevaluable                    int
			LastError                      *struct{ Code string }
		}
	}
	if err := json.Unmarshal(a.Body, &s); err != nil {
		t.Fatal(err)
	}
	by := map[string]int{}
	for i, it := range s.Items {
		by[it.Source+"/"+it.Ecosystem] = i
	}
	npm, nuget, kev := s.Items[by["osv/npm"]], s.Items[by["osv/NuGet"]], s.Items[by["kev/"]]
	if npm.Failures != 1 || npm.LastError == nil || npm.LastError.Code != "archive_too_large" || !npm.Stale || npm.Records != 231000 || npm.Rejected != 3 || npm.Unevaluable != 7 || npm.Mode != "online" {
		t.Errorf("npm = %+v", npm)
	}
	if nuget.Stale || nuget.Failures != 0 || nuget.LastError != nil {
		t.Errorf("nuget = %+v", nuget)
	}
	if kev.State != "empty" || kev.Mode != "online" {
		t.Errorf("kev = %+v", kev)
	}
	if a := e.get(e.r, "/api/v1/vulnerabilities/sources", e.tokAlice); a.Status != 403 {
		t.Errorf("without settings:read: %d", a.Status)
	}
	if a := e.get(e.r, "/api/v1/vulnerabilities/sources", e.tokOlga); a.Status != 200 {
		t.Errorf("an auditor (settings:read): %d", a.Status)
	}
}

// TestR_VULN_021_SystemInfo: the block vulnerabilityData of the installation facts.
func TestR_VULN_021_SystemInfo(t *testing.T) {
	e := newEnv(t)
	e.st.Exec(`INSERT INTO vuln_source_state (source, ecosystem, state, synced_at, failures) VALUES ('osv', 'npm', 'complete', now(), 2)`)
	a := e.get(e.r, "/api/v1/system/info", e.tokRita)
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	var info struct {
		VulnerabilityData *struct {
			Enforcement string
			Sources     []struct {
				Source, Ecosystem, State, Mode string
				Failures                       int
				Stale                          bool
			}
		}
	}
	_ = json.Unmarshal(a.Body, &info)
	if info.VulnerabilityData == nil || info.VulnerabilityData.Enforcement != "enforce" {
		t.Fatalf("%s", a.Body)
	}
	found := false
	for _, s := range info.VulnerabilityData.Sources {
		if s.Source == "osv" && s.Ecosystem == "npm" && s.State == "complete" && s.Failures == 2 && !s.Stale {
			found = true
		}
	}
	if !found {
		t.Errorf("sources = %+v", info.VulnerabilityData.Sources)
	}
}

// TestR_VULN_021_Summary: the counts, the blocked versions, the coverage of the repositories and the age of the data.
func TestR_VULN_021_Summary(t *testing.T) {
	e := newEnv(t)
	e.st.Exec(`INSERT INTO vuln_source_state (source, ecosystem, state, synced_at) VALUES ('osv', 'npm', 'complete', now())`)
	e.addFinding(finding{Repo: "npm-local", Package: "a", Severity: "critical", Kind: "malicious", Advisory: "MAL-2026-1234"})
	e.addFinding(finding{Repo: "npm-local", Package: "a", Severity: "high", Advisory: "GHSA-aaaa-aaaa-0002", Status: "blocked", Fix: true})
	e.addFinding(finding{Repo: "npm-local", Package: "b", Severity: "high", Status: "confirmed"})
	e.addFinding(finding{Repo: "npm-local", Package: "c", Severity: "low", Status: "dismissed", DismissalReason: "risk_accepted"})
	e.addFinding(finding{Repo: "npm-local", Package: "d", Severity: "low", Status: "resolved"})
	a := e.get(e.r, "/api/v1/vulnerabilities/summary", e.tokRita)
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	var s struct {
		BySeverity                                 struct{ Critical, High, Low int64 }
		ByKind                                     struct{ Vulnerability, Malicious int64 }
		NeedsTriage, BlockedVersions, FixAvailable int64
		Coverage                                   struct{ Evaluated, PendingData, Internal, NotCovered int }
		Data                                       []struct {
			Ecosystem, State string
			Stale            bool
		}
	}
	_ = json.Unmarshal(a.Body, &s)
	// Open: the malicious one, the blocked one and the confirmed one. Dismissed and resolved are not open.
	if s.BySeverity.Critical != 1 || s.BySeverity.High != 2 || s.BySeverity.Low != 0 || s.ByKind.Malicious != 1 || s.ByKind.Vulnerability != 2 {
		t.Errorf("counts: %s", a.Body)
	}
	if s.NeedsTriage != 1 || s.FixAvailable != 1 {
		t.Errorf("needsTriage %d, fixAvailable %d", s.NeedsTriage, s.FixAvailable)
	}
	// Package a holds a malicious finding and a block set in triage: one blocked version, not two.
	if s.BlockedVersions != 1 {
		t.Errorf("blockedVersions = %d, want 1", s.BlockedVersions)
	}
	// The remote repository's upstream is not a public registry: not covered. The npm and NuGet repositories are matched.
	if s.Coverage.NotCovered < 1 || s.Coverage.Evaluated < 1 {
		t.Errorf("coverage = %+v", s.Coverage)
	}
	foundNPM := false
	for _, d := range s.Data {
		if d.Ecosystem == "npm" && d.State == "complete" && !d.Stale {
			foundNPM = true
		}
	}
	if !foundNPM {
		t.Errorf("data = %+v", s.Data)
	}
	if a := e.get(e.r, "/api/v1/vulnerabilities/summary"+q("repository", "it/nuget-local"), e.tokRita); a.Status != 200 {
		t.Errorf("repository filter: %d", a.Status)
	}
}

// TestR_VULN_020_PackageViews: the per-version view with the evaluation state and the block, and the per-package counts.
func TestR_VULN_020_PackageViews(t *testing.T) {
	e := newEnv(t)
	e.st.Exec(`INSERT INTO vuln_source_state (source, ecosystem, state, synced_at) VALUES ('osv', 'npm', 'complete', now())`)
	for _, repo := range []string{"npm-local", "npm-mirror", "npm-off"} {
		e.addVersion(repo, "trove-test-mal", "1.0.0")
		e.addVersion(repo, "trove-test-mal", "2.0.0")
	}
	e.addFinding(finding{Repo: "npm-local", Package: "trove-test-mal", Version: "1.0.0", Kind: "malicious", Severity: "critical", Advisory: "MAL-2026-1234"})
	e.addFinding(finding{Repo: "npm-local", Package: "trove-test-mal", Version: "2.0.0", Severity: "low", Advisory: "GHSA-aaaa-aaaa-0005"})
	e.addFinding(finding{Repo: "npm-local", Package: "trove-test-mal", Version: "2.0.0", Severity: "low", Advisory: "GHSA-aaaa-aaaa-0006", Status: "dismissed", DismissalReason: "mitigated"})
	id := packageID("npm", "trove-test-mal")

	a := e.get(e.r, "/api/v1/packages/"+id+"/versions/1.0.0/vulnerabilities", e.tokAlice)
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	var v struct {
		Version      string
		Repositories []struct {
			Repository, Evaluation string
			Blocked                bool
			BlockReason            *string
			SyncedAt               *string
			Findings               []struct{ Advisory string }
		}
	}
	_ = json.Unmarshal(a.Body, &v)
	got := map[string]int{}
	for i, r := range v.Repositories {
		got[r.Repository] = i
	}
	local, mirror, off := v.Repositories[got["it/npm-local"]], v.Repositories[got["it/npm-mirror"]], v.Repositories[got["it/npm-off"]]
	if local.Evaluation != "evaluated" || local.SyncedAt == nil || !local.Blocked || local.BlockReason == nil || *local.BlockReason != "malicious" || len(local.Findings) != 1 {
		t.Errorf("npm-local = %+v", local)
	}
	if mirror.Evaluation != "not_covered" || mirror.Blocked || len(mirror.Findings) != 0 {
		t.Errorf("npm-mirror = %+v", mirror)
	}
	if off.Evaluation != "not_covered" || off.Blocked {
		t.Errorf("npm-off = %+v", off)
	}

	a = e.get(e.r, "/api/v1/packages/"+id+"/vulnerabilities", e.tokAlice)
	var p struct {
		Versions []struct {
			Version string
			Open    struct{ Critical, Low int64 }
		}
	}
	_ = json.Unmarshal(a.Body, &p)
	if a.Status != 200 || len(p.Versions) != 2 || p.Versions[0].Open.Critical != 1 || p.Versions[1].Open.Low != 1 {
		t.Errorf("package counts: %d %s", a.Status, a.Body)
	}
	if a := e.get(e.r, "/api/v1/packages/"+id+"/versions/9.9.9/vulnerabilities", e.tokAlice); a.Status != 404 {
		t.Errorf("a version nobody holds: %d", a.Status)
	}
	if a := e.get(e.r, "/api/v1/packages/pkg_notanid/vulnerabilities", e.tokAlice); a.Status != 404 {
		t.Errorf("a bad package id: %d", a.Status)
	}
	if a := e.get(e.r, "/api/v1/packages/"+id+"/vulnerabilities", e.tokDave); a.Status != 404 {
		t.Errorf("dave: %d", a.Status)
	}
}

// TestR_VULN_015_MissingData: onMissingData block shows a version of an ecosystem without data blocked as missing_data, and the
// version is pending_data.
func TestR_VULN_015_MissingData(t *testing.T) {
	e := newEnv(t)
	e.addVersion("npm-strict", "pkg", "1.0.0")
	e.addVersion("npm-local", "pkg", "1.0.0")
	e.st.Exec(`INSERT INTO vuln_source_state (source, ecosystem, state) VALUES ('osv', 'npm', 'empty')`)
	a := e.get(e.r, "/api/v1/packages/"+packageID("npm", "pkg")+"/versions/1.0.0/vulnerabilities", e.tokRita)
	var v struct {
		Repositories []struct {
			Repository, Evaluation string
			Blocked                bool
			BlockReason            *string
		}
	}
	_ = json.Unmarshal(a.Body, &v)
	if a.Status != 200 || len(v.Repositories) != 2 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	for _, r := range v.Repositories {
		switch r.Repository {
		case "it/npm-strict":
			if r.Evaluation != "pending_data" || !r.Blocked || r.BlockReason == nil || *r.BlockReason != "missing_data" {
				t.Errorf("npm-strict = %+v", r)
			}
		default:
			if r.Evaluation != "pending_data" || r.Blocked {
				t.Errorf("%s = %+v", r.Repository, r)
			}
		}
	}
}

// TestR_VULN_010_ClaimedNameMatches: the list of the public advisories for internal names, and its permission.
func TestR_VULN_010_ClaimedNameMatches(t *testing.T) {
	e := newEnv(t)
	e.st.Exec(`INSERT INTO namespace_claims (repo_id, pattern, source, scope) SELECT id, '@acme/*', 'manual', 'members' FROM repositories WHERE key = 'npm-local'`)
	for _, n := range []string{"@acme/ui", "@acme/core", "@acme/zzz"} {
		e.st.Exec(`INSERT INTO vuln_claimed_match (ecosystem, advisory_id, name, kind) VALUES ('npm', 'MAL-2026-1235', $1, 'malicious')`, n)
	}
	var names []string
	cursor := ""
	for range 5 {
		a := e.get(e.r, "/api/v1/vulnerabilities/claimed-name-matches?limit=2"+cursor, e.tokRita)
		if a.Status != 200 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		var p struct {
			Items []struct {
				Name, Advisory, Kind string
				ClaimRepository      *string
			}
			NextCursor *string
		}
		_ = json.Unmarshal(a.Body, &p)
		for _, it := range p.Items {
			names = append(names, it.Name)
			if it.ClaimRepository == nil || *it.ClaimRepository != "it/npm-local" || it.Kind != "malicious" {
				t.Errorf("%+v", it)
			}
		}
		if p.NextCursor == nil {
			break
		}
		cursor = "&cursor=" + *p.NextCursor
	}
	if want := []string{"@acme/core", "@acme/ui", "@acme/zzz"}; !slices.Equal(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
	if a := e.get(e.r, "/api/v1/vulnerabilities/claimed-name-matches", e.tokAlice); a.Status != 403 {
		t.Errorf("without repositories:read-config: %d", a.Status)
	}
}
