//go:build integration

package vulnapi

import (
	"encoding/csv"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

// seedTeams inserts the findings the scope tests ask about: three of @team-a, two of other packages in npm-local and one in
// nuget-local.
func (e *env) seedTeams() (teamA []string, other []string) {
	for i, p := range []string{"@team-a/ui", "@team-a/api", "@team-a/db"} {
		sev := []string{"critical", "high", "low"}[i]
		teamA = append(teamA, e.addFinding(finding{Repo: "npm-local", Package: p, Severity: sev, Detected: "now() - interval '2 days'", Fix: i == 0}))
	}
	other = append(other,
		e.addFinding(finding{Repo: "npm-local", Package: "@team-b/core", Severity: "critical", Detected: "now() - interval '2 days'"}),
		e.addFinding(finding{Repo: "npm-local", Package: "left-pad", Severity: "high", Kind: "malicious", Advisory: "MAL-2026-1234"}),
		e.addFinding(finding{Repo: "nuget-local", Package: "Contoso.Json", Severity: "medium"}))
	return teamA, other
}

// TestR_VULN_019_Scope: a caller whose only grant is read on npm-local for @team-a/* sees the findings of those packages in the
// list, the summary counts, the trend and the export, and a 404 for any other finding.
func TestR_VULN_019_Scope(t *testing.T) {
	e := newEnv(t)
	teamA, other := e.seedTeams()

	t.Run("R-VULN-019 the list shows only the packages of the grant", func(t *testing.T) {
		got := e.list(e.r, e.tokCarol, "").packages()
		slices.Sort(got)
		if want := []string{"@team-a/api", "@team-a/db", "@team-a/ui"}; !slices.Equal(got, want) {
			t.Fatalf("carol sees %v, want %v", got, want)
		}
		if n := len(e.list(e.r, e.tokRita, "").Items); n != 6 {
			t.Errorf("an administrator sees %d findings, want 6", n)
		}
		if n := len(e.list(e.r, e.tokOlga, "").Items); n != 6 {
			t.Errorf("an auditor sees %d findings, want 6", n)
		}
		if n := len(e.list(e.r, e.tokDave, "").Items); n != 0 {
			t.Errorf("a caller without a grant sees %d findings, want 0", n)
		}
		if n := len(e.list(e.r, e.tokAlice, "").Items); n != 6 {
			t.Errorf("a reader of both repositories sees %d findings, want 6", n)
		}
	})

	t.Run("R-VULN-019 a filter never widens the grant", func(t *testing.T) {
		if got := e.list(e.r, e.tokCarol, q("package", "left-pad")).packages(); len(got) != 0 {
			t.Errorf("package filter: %v", got)
		}
		if got := e.list(e.r, e.tokCarol, q("repository", "nuget-local")).packages(); len(got) != 0 {
			t.Errorf("repository filter: %v", got)
		}
		if got := e.list(e.r, e.tokCarol, q("q", "team-b")).packages(); len(got) != 0 {
			t.Errorf("text filter: %v", got)
		}
		// npm-all is a virtual repository over npm-local that carol may read: it stands for the members, narrowed again.
		got := e.list(e.r, e.tokCarol, q("repository", "npm-all")).packages()
		if len(got) != 3 {
			t.Errorf("a virtual repository filter: %v", got)
		}
	})

	t.Run("R-VULN-019 the same user's GET of another package's finding answers 404", func(t *testing.T) {
		for _, id := range other {
			if a := e.get(e.r, "/api/v1/vulnerabilities/findings/"+id, e.tokCarol); a.Status != 404 {
				t.Errorf("%s: %d %s", id, a.Status, a.Body)
			}
		}
		if a := e.get(e.r, "/api/v1/vulnerabilities/findings/"+teamA[0], e.tokCarol); a.Status != 200 {
			t.Errorf("a finding of the grant: %d %s", a.Status, a.Body)
		}
	})

	t.Run("R-VULN-019 the summary counts the readable findings only", func(t *testing.T) {
		var s struct {
			BySeverity                struct{ Critical, High, Medium, Low int64 } `json:"bySeverity"`
			ByKind                    struct{ Vulnerability, Malicious int64 }    `json:"byKind"`
			NeedsTriage, FixAvailable int64
		}
		a := e.get(e.r, "/api/v1/vulnerabilities/summary", e.tokCarol)
		if a.Status != 200 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		_ = json.Unmarshal(a.Body, &s)
		if s.BySeverity.Critical != 1 || s.BySeverity.High != 1 || s.BySeverity.Low != 1 || s.BySeverity.Medium != 0 || s.ByKind.Malicious != 0 || s.NeedsTriage != 3 || s.FixAvailable != 1 {
			t.Errorf("carol's summary: %s", a.Body)
		}
		a = e.get(e.r, "/api/v1/vulnerabilities/summary", e.tokRita)
		_ = json.Unmarshal(a.Body, &s)
		if s.BySeverity.Critical != 2 || s.BySeverity.High != 2 || s.BySeverity.Medium != 1 || s.ByKind.Malicious != 1 || s.NeedsTriage != 6 {
			t.Errorf("rita's summary: %s", a.Body)
		}
	})

	t.Run("R-VULN-019 the trend counts the readable findings only", func(t *testing.T) {
		today := time.Now().UTC().Format(time.DateOnly)
		var tr struct {
			Points []struct {
				Date   string
				Counts struct{ Critical, High, Medium, Low int64 }
			} `json:"points"`
		}
		a := e.get(e.r, "/api/v1/vulnerabilities/trend"+q("from", today, "to", today), e.tokCarol)
		if a.Status != 200 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		_ = json.Unmarshal(a.Body, &tr)
		if len(tr.Points) != 1 || tr.Points[0].Counts.Critical != 1 || tr.Points[0].Counts.High != 1 || tr.Points[0].Counts.Low != 1 || tr.Points[0].Counts.Medium != 0 {
			t.Errorf("carol's trend: %s", a.Body)
		}
	})

	t.Run("R-VULN-019 the export holds the readable findings only", func(t *testing.T) {
		a := e.get(e.r, "/api/v1/vulnerabilities/findings/export", e.tokCarol)
		if a.Status != 200 || !strings.HasPrefix(a.Header.Get("Content-Type"), "text/csv") {
			t.Fatalf("%d %s %s", a.Status, a.Header.Get("Content-Type"), a.Body)
		}
		rows, err := csv.NewReader(strings.NewReader(string(a.Body))).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 4 {
			t.Fatalf("%d rows, want a header and three: %s", len(rows), a.Body)
		}
		for _, r := range rows[1:] {
			// A package name that starts with @ is a formula to a spreadsheet and carries the apostrophe of R-VULN-020.
			if !strings.HasPrefix(r[slices.Index(rows[0], "package")], "'@team-a/") {
				t.Errorf("a row of another package: %v", r)
			}
		}
	})

	t.Run("R-VULN-019 a cursor grants nothing: another caller's cursor is filtered again", func(t *testing.T) {
		p := e.list(e.r, e.tokRita, q("limit", "1"))
		if p.NextCursor == nil {
			t.Fatal("no cursor")
		}
		a := e.get(e.r, "/api/v1/vulnerabilities/findings"+q("limit", "50", "cursor", *p.NextCursor), e.tokCarol)
		var got page
		_ = json.Unmarshal(a.Body, &got)
		for _, it := range got.Items {
			if !strings.HasPrefix(it.Package, "@team-a/") {
				t.Errorf("a finding outside the grant behind a foreign cursor: %s", it.Package)
			}
		}
	})

	t.Run("R-VULN-019 the package views are narrowed the same way", func(t *testing.T) {
		e.addVersion("npm-local", "@team-b/core", "1.0.0")
		e.addVersion("npm-local", "@team-a/ui", "1.0.0")
		id := packageID("npm", "@team-b/core")
		if a := e.get(e.r, "/api/v1/packages/"+id+"/vulnerabilities", e.tokCarol); a.Status != 404 {
			t.Errorf("carol on a package she cannot read: %d %s", a.Status, a.Body)
		}
		if a := e.get(e.r, "/api/v1/packages/"+id+"/versions/1.0.0/vulnerabilities", e.tokCarol); a.Status != 404 {
			t.Errorf("carol on a version she cannot read: %d %s", a.Status, a.Body)
		}
		if a := e.get(e.r, "/api/v1/packages/"+id+"/vulnerabilities", e.tokRita); a.Status != 200 {
			t.Errorf("rita: %d %s", a.Status, a.Body)
		}
	})
}

// TestR_VULN_019_Auditor: an auditor sees every finding and may triage it.
func TestR_VULN_019_Auditor(t *testing.T) {
	e := newEnv(t)
	teamA, other := e.seedTeams()
	if n := len(e.list(e.r, e.tokOlga, "").Items); n != 6 {
		t.Fatalf("the auditor sees %d findings", n)
	}
	for _, id := range append(slices.Clone(teamA), other...) {
		if a := e.patch(e.tokOlga, id, `*`, `{"status":"confirmed"}`); a.Status != 200 {
			t.Errorf("auditor triage %s: %d %s", id, a.Status, a.Body)
		}
	}
	// The 403 on the download of the version is the file service's (Q-AUTHZ-172); this tier does not reach it.
}

// TestR_VULN_019_ScopedToken: a service-account token scoped to the capability alone, of an administrator account, carries the
// capability and no repository right: it reads no finding, and every PATCH with it answers 404.
func TestR_VULN_019_ScopedToken(t *testing.T) {
	e := newEnv(t)
	teamA, _ := e.seedTeams()
	if n := len(e.list(e.r, e.tokBot, "").Items); n != 0 {
		t.Errorf("the scoped token lists %d findings", n)
	}
	if a := e.get(e.r, "/api/v1/vulnerabilities/findings/"+teamA[0], e.tokBot); a.Status != 404 {
		t.Errorf("GET: %d %s", a.Status, a.Body)
	}
	if a := e.patch(e.tokBot, teamA[0], `*`, `{"status":"confirmed"}`); a.Status != 404 {
		t.Errorf("PATCH: %d %s", a.Status, a.Body)
	}
	if st := e.st.Text(`SELECT status FROM vuln_finding WHERE id = $1::uuid`, teamA[0]); st != "needs_triage" {
		t.Errorf("the finding is %s", st)
	}
	a := e.bulk(e.tokBot, `{"ids":["`+teamA[0]+`"],"status":"confirmed"}`)
	if a.Status != 200 || !strings.Contains(string(a.Body), `"status":404`) {
		t.Errorf("bulk: %d %s", a.Status, a.Body)
	}
}
