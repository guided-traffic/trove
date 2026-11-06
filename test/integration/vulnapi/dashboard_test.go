//go:build integration

package vulnapi

import (
	"encoding/csv"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// dashboardEnv adds a second project to the env: shop, with the repository shop-npm that only the instance-wide readers (rita,
// olga) may read. The findings of the two projects share one advisory group, so a search by alias crosses projects.
func dashboardEnv(t *testing.T) *env {
	t.Helper()
	e := newEnvWith(t, func(st *testenv.Stack) {
		st.Repository("shop/shop-npm", "local", "npm", `{"local":{"packageOwnership":"off"}}`)
	})
	e.addAdvisory("npm", "GHSA-aaaa-bbbb-cccc", "Command injection in lodash", "high", "CVE-2021-23337")
	e.addAdvisory("npm", "GHSA-dddd-eeee-ffff", "Prototype pollution", "medium", "CVE-2022-0001")
	e.addFinding(finding{Repo: "npm-local", Package: "lodash", Advisory: "GHSA-aaaa-bbbb-cccc", Severity: "high"})
	e.addFinding(finding{Repo: "shop-npm", Package: "lodash", Advisory: "GHSA-aaaa-bbbb-cccc", Severity: "high"})
	e.addFinding(finding{Repo: "shop-npm", Package: "minimist", Advisory: "GHSA-dddd-eeee-ffff", Severity: "medium"})
	e.addFinding(finding{Repo: "shop-npm", Package: "leftpad", Advisory: "GHSA-dddd-eeee-ffff", Severity: "low", Status: "resolved"})
	return e
}

// R-VULN-020, R-VULN-021, Q-UI-130: the instance-wide dashboard reads the findings API without a project filter and narrows it
// with `project`. These cases pin what the dashboard relies on: the item's project, the filter on list, summary, trend and
// export, the alias search across projects, and an empty answer, never a 404, for a project the caller cannot read.
func TestR_VULN_020_TheProjectFilterAndTheSearchAcrossProjects(t *testing.T) {
	e := dashboardEnv(t)

	t.Run("R-VULN-020 an item names its project and the instance-wide list holds every project", func(t *testing.T) {
		p := e.list(e.r, e.tokRita, "")
		projects := map[string]int{}
		for _, it := range p.Items {
			projects[it.Project]++
			if !strings.HasPrefix(it.Repository, it.Project+"/") {
				t.Errorf("project %q does not match repository %q", it.Project, it.Repository)
			}
		}
		if projects["it"] != 1 || projects["shop"] != 2 {
			t.Fatalf("open findings per project = %v", projects)
		}
	})

	t.Run("R-VULN-020 project narrows the list; it repeats; an unknown project is empty, not 404", func(t *testing.T) {
		if got := e.list(e.r, e.tokRita, q("project", "shop")).packages(); !slices.Equal(sorted(got), []string{"lodash", "minimist"}) {
			t.Errorf("project=shop: %v", got)
		}
		if got := e.list(e.r, e.tokRita, q("project", "it")).packages(); !slices.Equal(got, []string{"lodash"}) {
			t.Errorf("project=it: %v", got)
		}
		if n := len(e.list(e.r, e.tokRita, q("project", "it", "project", "shop")).Items); n != 3 {
			t.Errorf("project=it&project=shop: %d items, want 3", n)
		}
		if n := len(e.list(e.r, e.tokRita, q("project", "ghost")).Items); n != 0 {
			t.Errorf("project=ghost: %d items", n)
		}
	})

	t.Run("R-VULN-020 project and repository intersect", func(t *testing.T) {
		if n := len(e.list(e.r, e.tokRita, q("project", "shop", "repository", "it/npm-local")).Items); n != 0 {
			t.Errorf("shop and it/npm-local: %d items, want 0", n)
		}
		if n := len(e.list(e.r, e.tokRita, q("project", "shop", "repository", "shop/shop-npm")).Items); n != 2 {
			t.Errorf("shop and shop/shop-npm: %d items, want 2", n)
		}
	})

	t.Run("R-VULN-020 a malformed project key is 400", func(t *testing.T) {
		for _, key := range []string{"Bad Key", "a/b", "-x"} {
			if a := e.get(e.r, "/api/v1/vulnerabilities/findings"+q("project", key), e.tokRita); a.Status != 400 {
				t.Errorf("project=%q: %d %s", key, a.Status, a.Body)
			}
		}
	})

	t.Run("R-VULN-020 a search by alias finds the finding of every project; the package search combines with it", func(t *testing.T) {
		p := e.list(e.r, e.tokRita, q("advisory", "CVE-2021-23337"))
		var projects []string
		for _, it := range p.Items {
			projects = append(projects, it.Project)
		}
		if !slices.Equal(sorted(projects), []string{"it", "shop"}) {
			t.Fatalf("advisory=CVE-2021-23337 found projects %v", projects)
		}
		if n := len(e.list(e.r, e.tokRita, q("advisory", "CVE-2021-23337", "project", "shop")).Items); n != 1 {
			t.Errorf("alias and project: %d items, want 1", n)
		}
		if got := e.list(e.r, e.tokRita, q("advisory", "GHSA-dddd-eeee-ffff", "package", "minimist")).packages(); !slices.Equal(got, []string{"minimist"}) {
			t.Errorf("canonical id and package: %v", got)
		}
		if n := len(e.list(e.r, e.tokRita, q("advisory", "CVE-0000-0000")).Items); n != 0 {
			t.Errorf("unknown alias: %d items", n)
		}
	})

	t.Run("R-VULN-019 a caller without a grant on the project sees an empty answer for it", func(t *testing.T) {
		if n := len(e.list(e.r, e.tokAlice, q("project", "shop")).Items); n != 0 {
			t.Errorf("alice, project=shop: %d items", n)
		}
		if n := len(e.list(e.r, e.tokAlice, q("advisory", "CVE-2021-23337")).Items); n != 1 {
			t.Errorf("alice's alias search: %d items, want her one", n)
		}
	})

	t.Run("R-VULN-021 the summary and the trend take the project filter", func(t *testing.T) {
		count := func(query string) (int64, int64) {
			a := e.get(e.r, "/api/v1/vulnerabilities/summary"+query, e.tokRita)
			if a.Status != 200 {
				t.Fatalf("summary%s: %d %s", query, a.Status, a.Body)
			}
			var s struct {
				BySeverity struct{ High, Medium int64 } `json:"bySeverity"`
			}
			_ = json.Unmarshal(a.Body, &s)
			return s.BySeverity.High, s.BySeverity.Medium
		}
		if h, m := count(""); h != 2 || m != 1 {
			t.Errorf("all projects: high %d medium %d, want 2 and 1", h, m)
		}
		if h, m := count(q("project", "it")); h != 1 || m != 0 {
			t.Errorf("project=it: high %d medium %d, want 1 and 0", h, m)
		}
		if h, m := count(q("project", "shop")); h != 1 || m != 1 {
			t.Errorf("project=shop: high %d medium %d, want 1 and 1", h, m)
		}
		today := func(query string) int64 {
			a := e.get(e.r, "/api/v1/vulnerabilities/trend"+query, e.tokRita)
			if a.Status != 200 {
				t.Fatalf("trend%s: %d %s", query, a.Status, a.Body)
			}
			var tr struct {
				Points []struct{ Counts struct{ High, Medium int64 } } `json:"points"`
			}
			_ = json.Unmarshal(a.Body, &tr)
			last := tr.Points[len(tr.Points)-1].Counts
			return last.High + last.Medium
		}
		if all, shop := today(""), today("?project=shop"); all != 3 || shop != 2 {
			t.Errorf("trend today: all %d shop %d, want 3 and 2", all, shop)
		}
	})

	t.Run("R-VULN-020 the export takes the project filter and carries the project column", func(t *testing.T) {
		a := e.get(e.r, "/api/v1/vulnerabilities/findings/export"+q("project", "shop"), e.tokRita)
		if a.Status != 200 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		rows, err := csv.NewReader(strings.NewReader(string(a.Body))).ReadAll()
		if err != nil || len(rows) != 3 {
			t.Fatalf("rows = %v, %v", rows, err)
		}
		col := slices.Index(rows[0], "project")
		for _, r := range rows[1:] {
			if r[col] != "shop" {
				t.Errorf("row %v is not of project shop", r)
			}
		}
	})
}

// Q-UI-130, R-VULN-013: a holder of vulnerabilities:triage triages a finding of any project from the dashboard; a holder of
// content:metadata-read alone reads every project and changes nothing.
func TestR_VULN_013_TheAuditorTriagesAcrossProjectsAndAReadOnlyHolderOnlyReads(t *testing.T) {
	e := dashboardEnv(t)
	id := e.list(e.r, e.tokOlga, q("project", "shop", "package", "minimist")).Items[0].ID

	readOnly := e.st.Token(e.olga, scope.Scope{Inherit: true, System: []string{"content:metadata-read"}})
	if n := len(e.list(e.r, readOnly, "").Items); n != 3 {
		t.Fatalf("content:metadata-read lists %d open findings, want all 3", n)
	}
	a := e.get(e.r, "/api/v1/vulnerabilities/findings/"+id, readOnly)
	if a.Status != 200 {
		t.Fatalf("detail: %d %s", a.Status, a.Body)
	}
	if b := e.patch(readOnly, id, a.Header.Get("ETag"), `{"status":"confirmed"}`); b.Status != 403 {
		t.Errorf("a read-only holder triaged: %d %s", b.Status, b.Body)
	}

	a = e.get(e.r, "/api/v1/vulnerabilities/findings/"+id, e.tokOlga)
	if b := e.patch(e.tokOlga, id, a.Header.Get("ETag"), `{"status":"confirmed"}`); b.Status != 200 {
		t.Fatalf("the auditor's triage in project shop: %d %s", b.Status, b.Body)
	}
	if got := e.list(e.r, e.tokOlga, q("project", "shop", "status", "confirmed")).packages(); !slices.Equal(got, []string{"minimist"}) {
		t.Errorf("confirmed in shop: %v", got)
	}
}

func sorted(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}
