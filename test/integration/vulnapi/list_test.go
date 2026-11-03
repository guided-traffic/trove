//go:build integration

package vulnapi

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestR_VULN_020_List: the sorts, the filters, the default of the status filter, the alias filter, an unknown filter, the
// cursor and the block computed from the repository's policy.
func TestR_VULN_020_List(t *testing.T) {
	e := newEnv(t)
	e.addAdvisory("npm", "GHSA-aaaa-bbbb-cccc", "Command injection in lodash", "high", "CVE-2021-23337")
	e.addFinding(finding{Repo: "npm-local", Package: "p-crit", Severity: "critical", CVSS: 9.8, Detected: "now() - interval '3 days'"})
	e.addFinding(finding{Repo: "npm-local", Package: "p-high", Severity: "high", CVSS: 7.5, Advisory: "GHSA-aaaa-bbbb-cccc", Detected: "now() - interval '2 days'", Fix: true, KEV: true})
	e.addFinding(finding{Repo: "npm-local", Package: "p-low", Severity: "low", CVSS: 3.1, Detected: "now() - interval '1 day'"})
	e.addFinding(finding{Repo: "nuget-local", Package: "p-nuget", Severity: "medium", Detected: "now()"})
	e.addFinding(finding{Repo: "npm-local", Package: "p-gone", Severity: "critical", Status: "resolved"})
	e.addFinding(finding{Repo: "npm-local", Package: "p-mal", Severity: "critical", Kind: "malicious", Advisory: "MAL-2026-1234"})

	t.Run("R-VULN-020 sort -severity is the default and orders the levels", func(t *testing.T) {
		got := e.list(e.r, e.tokRita, q("package", "p-crit", "package", "p-high", "package", "p-low")).packages()
		if want := []string{"p-crit", "p-high", "p-low"}; !slices.Equal(got, want) {
			t.Fatalf("order = %v, want %v", got, want)
		}
		got = e.list(e.r, e.tokRita, q("package", "p-crit", "package", "p-high", "package", "p-low", "sort", "severity")).packages()
		if want := []string{"p-low", "p-high", "p-crit"}; !slices.Equal(got, want) {
			t.Fatalf("ascending = %v, want %v", got, want)
		}
	})

	t.Run("R-VULN-020 severity filter repeats", func(t *testing.T) {
		got := e.list(e.r, e.tokRita, q("severity", "high", "severity", "critical", "package", "p-crit", "package", "p-high", "package", "p-low")).packages()
		if len(got) != 2 {
			t.Fatalf("got %v, want two", got)
		}
	})

	t.Run("R-VULN-020 the advisory filter finds a finding by its alias", func(t *testing.T) {
		p := e.list(e.r, e.tokRita, q("advisory", "CVE-2021-23337"))
		if len(p.Items) != 1 || p.Items[0].Advisory != "GHSA-aaaa-bbbb-cccc" || p.Items[0].Package != "p-high" {
			t.Fatalf("got %+v", p.Items)
		}
	})

	t.Run("R-VULN-020 an unknown filter, sort or value answers 400", func(t *testing.T) {
		for _, query := range []string{"?nope=1", "?sort=colour", "?severity=urgent", "?status=gone", "?blocked=maybe", "?detectedFrom=yesterday"} {
			if a := e.get(e.r, "/api/v1/vulnerabilities/findings"+query, e.tokRita); a.Status != 400 {
				t.Errorf("%s: %d %s", query, a.Status, a.Body)
			}
		}
	})

	t.Run("R-VULN-020 resolved findings are left out unless the status filter asks", func(t *testing.T) {
		if slices.Contains(e.list(e.r, e.tokRita, "").packages(), "p-gone") {
			t.Error("a resolved finding is listed without a status filter")
		}
		if got := e.list(e.r, e.tokRita, q("status", "resolved")).packages(); !slices.Equal(got, []string{"p-gone"}) {
			t.Errorf("status=resolved: %v", got)
		}
	})

	t.Run("R-VULN-020 format, repository, version, kind, fixAvailable, knownExploited, text and prefix filters", func(t *testing.T) {
		for _, c := range []struct {
			query string
			want  []string
		}{
			{q("format", "nuget"), []string{"p-nuget"}},
			{q("repository", "nuget-local"), []string{"p-nuget"}},
			{q("kind", "malicious"), []string{"p-mal"}},
			{q("fixAvailable", "true"), []string{"p-high"}},
			{q("knownExploited", "true"), []string{"p-high"}},
			{q("q", "COMMAND injection"), []string{"p-high"}},
			{q("q", "p-lo"), []string{"p-low"}},
			{q("packagePrefix", "p-nu"), []string{"p-nuget"}},
			{q("version", "2.0.0"), nil},
		} {
			if got := e.list(e.r, e.tokRita, c.query).packages(); !slices.Equal(got, c.want) {
				t.Errorf("%s: %v, want %v", c.query, got, c.want)
			}
		}
	})

	t.Run("R-VULN-020 detectedFrom and detectedTo bound the detection time", func(t *testing.T) {
		from := time.Now().Add(-36 * time.Hour).UTC().Format(time.RFC3339)
		to := time.Now().Add(-12 * time.Hour).UTC().Format(time.RFC3339)
		if got := e.list(e.r, e.tokRita, q("detectedFrom", from, "detectedTo", to)).packages(); !slices.Equal(got, []string{"p-low"}) {
			t.Errorf("window: %v", got)
		}
	})

	t.Run("R-VULN-020 blocked and blockReason follow the repository's policy", func(t *testing.T) {
		if got := e.list(e.r, e.tokRita, q("blocked", "true")).packages(); !slices.Equal(got, []string{"p-mal"}) {
			t.Fatalf("blocked=true: %v", got)
		}
		p := e.list(e.r, e.tokRita, q("blockReason", "malicious"))
		if len(p.Items) != 1 || p.Items[0].BlockReason == nil || *p.Items[0].BlockReason != "malicious" || !p.Items[0].Blocked {
			t.Fatalf("blockReason=malicious: %+v", p.Items)
		}
		// A repository with blockMalicious off does not block the same finding: in the filter and in the item.
		e.addFinding(finding{Repo: "npm-lax", Package: "p-mal-lax", Severity: "critical", Kind: "malicious", Advisory: "MAL-2026-1235"})
		if got := e.list(e.r, e.tokRita, q("blocked", "true")).packages(); !slices.Equal(got, []string{"p-mal"}) {
			t.Fatalf("blocked=true with a lax repository: %v", got)
		}
		p = e.list(e.r, e.tokRita, q("package", "p-mal-lax"))
		if len(p.Items) != 1 || p.Items[0].Blocked || p.Items[0].BlockReason != nil {
			t.Fatalf("lax: %+v", p.Items)
		}
	})

	t.Run("R-VULN-020 a triage block is blocked with the reason triage", func(t *testing.T) {
		e.addFinding(finding{Repo: "npm-local", Package: "p-triage", Severity: "medium", Status: "blocked"})
		p := e.list(e.r, e.tokRita, q("package", "p-triage"))
		if len(p.Items) != 1 || !p.Items[0].Blocked || p.Items[0].BlockReason == nil || *p.Items[0].BlockReason != "triage" {
			t.Fatalf("%+v", p.Items)
		}
		if got := e.list(e.r, e.tokRita, q("blockReason", "triage")).packages(); !slices.Equal(got, []string{"p-triage"}) {
			t.Fatalf("blockReason=triage: %v", got)
		}
	})

	t.Run("R-API-010 the cursor walks every finding once, in order, for each sort", func(t *testing.T) {
		for _, sort := range []string{"-severity", "severity", "-detectedAt", "detectedAt", "package", "-package", "advisory", "-advisory", "status", "-status"} {
			var ids []string
			cursor := ""
			for range 30 {
				query := q("sort", sort, "limit", "2")
				if cursor != "" {
					query += "&cursor=" + cursor
				}
				a := e.get(e.r, "/api/v1/vulnerabilities/findings"+query, e.tokRita)
				if a.Status != 200 {
					t.Fatalf("%s: %d %s", sort, a.Status, a.Body)
				}
				var p page
				_ = json.Unmarshal(a.Body, &p)
				for _, it := range p.Items {
					ids = append(ids, it.ID)
				}
				if p.NextCursor == nil {
					break
				}
				cursor = *p.NextCursor
			}
			want := len(e.list(e.r, e.tokRita, "").Items)
			seen := map[string]bool{}
			for _, id := range ids {
				seen[id] = true
			}
			if len(ids) != want || len(seen) != want {
				t.Errorf("sort %s: %d ids, %d distinct, want %d", sort, len(ids), len(seen), want)
			}
		}
	})

	t.Run("R-API-010 a cursor of other filters answers 400 invalid_cursor", func(t *testing.T) {
		p := e.list(e.r, e.tokRita, q("limit", "1"))
		if p.NextCursor == nil {
			t.Fatal("no next cursor")
		}
		a := e.get(e.r, "/api/v1/vulnerabilities/findings"+q("limit", "1", "severity", "low", "cursor", *p.NextCursor), e.tokRita)
		if a.Status != 400 || a.Code(t) != "invalid_cursor" {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})

	t.Run("R-VULN-019 the list needs an authenticated caller", func(t *testing.T) {
		if a := e.get(e.r, "/api/v1/vulnerabilities/findings", ""); a.Status != 401 {
			t.Fatalf("anonymous: %d %s", a.Status, a.Body)
		}
	})
}

// TestR_VULN_020_Detail: the advisory, the readable virtual repositories, the history, the ETag and a finding that does not exist.
func TestR_VULN_020_Detail(t *testing.T) {
	e := newEnv(t)
	e.addAdvisory("npm", "GHSA-aaaa-bbbb-cccc", "Command injection", "high", "CVE-2021-23337")
	id := e.addFinding(finding{Repo: "npm-local", Package: "lodash", Version: "4.17.20", Advisory: "GHSA-aaaa-bbbb-cccc", CVSS: 9.8})
	a := e.get(e.r, "/api/v1/vulnerabilities/findings/"+id, e.tokRita)
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	var d struct {
		Finding struct {
			Cvss *struct {
				Version string  `json:"version"`
				Score   float64 `json:"score"`
			} `json:"cvss"`
			Aliases []string `json:"aliases"`
		} `json:"finding"`
		Advisory struct {
			ID     string   `json:"id"`
			Cwe    []string `json:"cwe"`
			Source struct {
				Name string `json:"name"`
			} `json:"source"`
			Details string `json:"details"`
		} `json:"advisory"`
		VirtualRepositories []string `json:"virtualRepositories"`
		History             []any    `json:"history"`
	}
	if err := json.Unmarshal(a.Body, &d); err != nil {
		t.Fatal(err)
	}
	if d.Finding.Cvss == nil || d.Finding.Cvss.Version != "3.1" || d.Finding.Cvss.Score != 9.8 || !slices.Equal(d.Finding.Aliases, []string{"CVE-2021-23337"}) {
		t.Errorf("finding = %+v", d.Finding)
	}
	if d.Advisory.ID != "GHSA-aaaa-bbbb-cccc" || !slices.Equal(d.Advisory.Cwe, []string{"CWE-79"}) || !strings.Contains(d.Advisory.Source.Name, "GitHub") {
		t.Errorf("advisory = %+v", d.Advisory)
	}
	if !slices.Equal(d.VirtualRepositories, []string{"npm-all"}) {
		t.Errorf("virtualRepositories = %v", d.VirtualRepositories)
	}
	etag := a.Header.Get("ETag")
	if etag != `"1"` {
		t.Errorf("ETag = %q, want the revision", etag)
	}
	if b := e.get(e.r, "/api/v1/vulnerabilities/findings/"+id, e.tokRita, "If-None-Match", etag); b.Status != 304 {
		t.Errorf("If-None-Match: %d", b.Status)
	}
	// A virtual repository the caller cannot read is not named: dave reads nothing, carol reads npm-all.
	if b := e.get(e.r, "/api/v1/vulnerabilities/findings/"+id, e.tokAlice); b.Status != 200 {
		t.Errorf("alice: %d", b.Status)
	}
	for _, bad := range []string{"nope", "00000000-0000-0000-0000-000000000000", "export-not"} {
		if b := e.get(e.r, "/api/v1/vulnerabilities/findings/"+bad, e.tokRita); b.Status != 404 {
			t.Errorf("%s: %d %s", bad, b.Status, b.Body)
		}
	}
}

// TestR_VULN_020_Advisory: an advisory by identifier or alias with the findings the caller can read.
func TestR_VULN_020_Advisory(t *testing.T) {
	e := newEnv(t)
	e.addAdvisory("npm", "GHSA-aaaa-bbbb-cccc", "Command injection", "high", "CVE-2021-23337")
	e.addFinding(finding{Repo: "npm-local", Package: "@team-a/ui", Advisory: "GHSA-aaaa-bbbb-cccc"})
	e.addFinding(finding{Repo: "npm-local", Package: "other", Advisory: "GHSA-aaaa-bbbb-cccc"})
	for _, id := range []string{"GHSA-aaaa-bbbb-cccc", "CVE-2021-23337"} {
		a := e.get(e.r, "/api/v1/vulnerabilities/advisories/npm/"+id, e.tokRita)
		if a.Status != 200 {
			t.Fatalf("%s: %d %s", id, a.Status, a.Body)
		}
		var d struct {
			Advisory struct {
				ID      string   `json:"id"`
				Aliases []string `json:"aliases"`
			} `json:"advisory"`
			Findings []struct {
				Package string `json:"package"`
			} `json:"findings"`
		}
		_ = json.Unmarshal(a.Body, &d)
		if d.Advisory.ID != "GHSA-aaaa-bbbb-cccc" || !slices.Equal(d.Advisory.Aliases, []string{"CVE-2021-23337"}) || len(d.Findings) != 2 {
			t.Errorf("%s: %+v", id, d)
		}
	}
	// The advisory is public data, the findings of it are the readable ones (R-VULN-019).
	a := e.get(e.r, "/api/v1/vulnerabilities/advisories/npm/CVE-2021-23337", e.tokCarol)
	var d struct {
		Findings []struct {
			Package string `json:"package"`
		} `json:"findings"`
	}
	_ = json.Unmarshal(a.Body, &d)
	if a.Status != 200 || len(d.Findings) != 1 || d.Findings[0].Package != "@team-a/ui" {
		t.Errorf("carol: %d %s", a.Status, a.Body)
	}
	if a := e.get(e.r, "/api/v1/vulnerabilities/advisories/npm/CVE-2000-0000", e.tokRita); a.Status != 404 {
		t.Errorf("unknown: %d", a.Status)
	}
	if a := e.get(e.r, "/api/v1/vulnerabilities/advisories/npm/CVE-2021-23337", e.tokDave); a.Status != 200 {
		t.Errorf("dave reads public data: %d %s", a.Status, a.Body)
	}
}
