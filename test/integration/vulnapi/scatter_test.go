//go:build integration

package vulnapi

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_VULN_020_Scatter: with three replicas of one installation, paging 120 findings with limit=1 yields 120 distinct ids,
// each page answered by another replica than the one before (the cursor holds the position, no replica holds state), and a
// status change through one replica is the next read of every other (R-VULN-027).
func TestR_VULN_020_Scatter(t *testing.T) {
	e := newEnv(t)
	replicas := []*testenv.Replica{
		e.r,
		e.st.Start(t, testenv.ReplicaOptions{Name: "web2", Env: []string{"TROVE_API_RATE_LIMITS_ENABLED=false"}}),
		e.st.Start(t, testenv.ReplicaOptions{Name: "web3", Env: []string{"TROVE_API_RATE_LIMITS_ENABLED=false"}}),
	}
	e.st.Exec(`INSERT INTO vuln_finding (id, repo_id, ecosystem, package_name, version, advisory_id, kind, severity, severity_rank, fix_available, status, advisory_modified, detected_at)
		SELECT gen_random_uuid(), r.id, 'npm', 'pkg-' || lpad(n::text, 3, '0'), '1.0.0', 'GHSA-aaaa-aaaa-0001', 'vulnerability',
		       (ARRAY['critical','high','medium','low'])[1 + n % 4], (ARRAY[5,4,3,2])[1 + n % 4], false, 'needs_triage', now(), now() - make_interval(mins => n)
		  FROM repositories r, generate_series(1, 120) n WHERE r.key = 'npm-local'`)
	for _, sort := range []string{"-severity", "package", "-detectedAt"} {
		seen := map[string]bool{}
		cursor := ""
		pages := 0
		for ; pages < 130; pages++ {
			query := q("limit", "1", "sort", sort)
			if cursor != "" {
				query += "&cursor=" + cursor
			}
			a := e.get(replicas[pages%3], "/api/v1/vulnerabilities/findings"+query, e.tokRita)
			if a.Status != 200 {
				t.Fatalf("%s page %d: %d %s", sort, pages, a.Status, a.Body)
			}
			var p page
			_ = json.Unmarshal(a.Body, &p)
			if len(p.Items) != 1 {
				t.Fatalf("%s page %d holds %d items", sort, pages, len(p.Items))
			}
			if seen[p.Items[0].ID] {
				t.Fatalf("%s: %s twice", sort, p.Items[0].ID)
			}
			seen[p.Items[0].ID] = true
			if p.NextCursor == nil {
				pages++
				break
			}
			cursor = *p.NextCursor
		}
		if len(seen) != 120 || pages != 120 {
			t.Errorf("sort %s: %d distinct ids over %d pages, want 120", sort, len(seen), pages)
		}
	}

	t.Run("R-VULN-027 a change through one replica is read through another at once", func(t *testing.T) {
		id := e.addFinding(finding{Repo: "npm-local", Package: "visible"})
		for i, r := range replicas {
			next := replicas[(i+1)%3]
			status := []string{"confirmed", "needs_triage", "confirmed"}[i]
			a := e.st.Do(r, "PATCH", "/api/v1/vulnerabilities/findings/"+id, e.tokRita, []byte(fmt.Sprintf(`{"status":%q}`, status)),
				"Content-Type", "application/merge-patch+json", "If-Match", "*")
			if a.Status != 200 {
				t.Fatalf("PATCH through %s: %d %s", r.Name, a.Status, a.Body)
			}
			b := e.get(next, "/api/v1/vulnerabilities/findings/"+id, e.tokRita)
			var d struct{ Finding struct{ Status string } }
			_ = json.Unmarshal(b.Body, &d)
			if b.Status != 200 || d.Finding.Status != status {
				t.Errorf("read through %s after the change through %s: %d %s", next.Name, r.Name, b.Status, b.Body)
			}
		}
	})
}
