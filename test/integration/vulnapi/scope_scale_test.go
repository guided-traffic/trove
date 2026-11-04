//go:build integration

package vulnapi

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestR_VULN_019_ScopeOfManyRows: the scope of a caller whose grant is narrowed by a package pattern is decided for thousands of
// versions with one derived decision state (authz.ReadScope.MayRead). Whatever the number of rows, the caller reads exactly the
// findings of the pattern, on every page, in the count and by id, and never one of a package that only looks like it
// (R-VULN-019, H-488).
func TestR_VULN_019_ScopeOfManyRows(t *testing.T) {
	e := newEnv(t)
	// 3,000 findings in npm-local: every third is of @team-a/, the rest are other scopes, among them names that only start like
	// the pattern, and a plain name.
	e.st.Exec(`INSERT INTO vuln_finding (id, repo_id, ecosystem, package_name, version, advisory_id, kind, severity, severity_rank, cvss_score,
			fix_available, status, advisory_modified, detected_at)
		SELECT gen_random_uuid(), r.id, 'npm',
		       CASE g % 6 WHEN 0 THEN '@team-a/p' WHEN 1 THEN '@team-ab/p' WHEN 2 THEN '@team-b/team-a' WHEN 3 THEN 'team-a' WHEN 4 THEN '@team-a/q' ELSE 'plain' END || g,
		       '1.0.' || g, 'GHSA-many-' || (g % 50), 'vulnerability', 'high', 4, 7.5, false, 'needs_triage', now(), now() - (g || ' seconds')::interval
		  FROM generate_series(1, 3000) g, repositories r WHERE r.key = 'npm-local'`)
	e.st.Exec(`ANALYZE vuln_finding`)

	var want []string
	e.st.Query(`SELECT id::text FROM vuln_finding WHERE package_name LIKE '@team-a/%' ORDER BY id`, func(r pgx.Rows) error {
		var id string
		err := r.Scan(&id)
		want = append(want, id)
		return err
	})
	if len(want) != 1000 {
		t.Fatalf("seeded %d findings of the pattern, want 1000", len(want))
	}

	var got []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 20 {
			t.Fatal("the list does not end")
		}
		query := q("limit", "200")
		if cursor != "" {
			query = q("limit", "200", "cursor", cursor)
		}
		p := e.list(e.r, e.tokCarol, query)
		for _, it := range p.Items {
			got = append(got, it.ID)
			if it.Repository != "npm-local" || len(it.Package) < 8 || it.Package[:8] != "@team-a/" {
				t.Fatalf("the list shows %s %s, which the grant does not cover", it.Repository, it.Package)
			}
		}
		if p.NextCursor == nil {
			break
		}
		cursor = *p.NextCursor
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("the list shows %d findings, want the %d of the pattern (and no other)", len(got), len(want))
	}

	var s struct {
		NeedsTriage int64 `json:"needsTriage"`
	}
	a := e.get(e.r, "/api/v1/vulnerabilities/summary", e.tokCarol)
	if a.Status != 200 {
		t.Fatalf("summary: %d %s", a.Status, a.Body)
	}
	_ = json.Unmarshal(a.Body, &s)
	if s.NeedsTriage != int64(len(want)) {
		t.Errorf("the summary counts %d findings, want %d: %s", s.NeedsTriage, len(want), a.Body)
	}

	// A finding of a package that only looks like the pattern is a 404 by id, whichever of the lookalikes it is.
	for i, pkg := range []string{"@team-ab/p", "@team-b/team-a", "team-a"} {
		var id string
		e.st.Query(fmt.Sprintf(`SELECT id::text FROM vuln_finding WHERE package_name LIKE '%s%%' LIMIT 1`, pkg), func(r pgx.Rows) error { return r.Scan(&id) })
		if id == "" {
			t.Fatalf("no finding of %s seeded (case %d)", pkg, i)
		}
		if a := e.get(e.r, "/api/v1/vulnerabilities/findings/"+id, e.tokCarol); a.Status != 404 {
			t.Errorf("%s: %d %s, want 404", pkg, a.Status, a.Body)
		}
	}
}
