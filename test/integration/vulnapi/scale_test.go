//go:build integration

package vulnapi

import (
	"slices"
	"testing"
	"time"
)

// TestR_VULN_020_TheFirstPageAtScale: 100,000 findings; the first page of every sort answers within the V-7 target (300 ms,
// R-VULN-028) for an administrator. A caller whose grant is narrowed by a package pattern pays the decision point's row-by-row
// read of the repository's findings (H-488: about 300 ms at this size, which the test records and bounds at one second). The
// statement orders by the indexes of 00039 (Q-VULN-68); the median of five requests keeps a slow runner from failing the
// bound by one outlier.
func TestR_VULN_020_TheFirstPageAtScale(t *testing.T) {
	e := newEnv(t)
	e.st.Exec(`INSERT INTO vuln_finding (id, repo_id, ecosystem, package_name, version, advisory_id, kind, severity, severity_rank, cvss_score,
			fix_available, status, advisory_modified, detected_at)
		SELECT gen_random_uuid(), r.id, 'npm', CASE WHEN g % 10 = 0 THEN '@team-a/p' ELSE 'pkg-' END || g, '1.0.' || g, 'GHSA-scale-' || (g % 5000), 'vulnerability',
		       (ARRAY['critical','high','medium','low'])[1 + g % 4], 5 - (g % 4), (g % 100) / 10.0, true,
		       (ARRAY['needs_triage','confirmed','blocked','dismissed'])[1 + g % 4], now(), now() - (g || ' seconds')::interval
		  FROM generate_series(1, 100000) g, repositories r WHERE r.key = 'npm-local'`)
	e.st.Exec(`ANALYZE vuln_finding`)
	for _, who := range []struct {
		name, token string
		limit       time.Duration
	}{{"admin", e.tokRita, 300 * time.Millisecond}, {"pattern grant", e.tokCarol, time.Second}} {
		for _, sort := range []string{"severity", "-severity", "detectedAt", "-detectedAt", "package", "-package", "advisory", "-advisory", "status", "-status"} {
			var samples []time.Duration
			for range 5 {
				start := time.Now()
				p := e.list(e.r, who.token, q("sort", sort, "limit", "50"))
				samples = append(samples, time.Since(start))
				if len(p.Items) != 50 {
					t.Fatalf("%s %s: %d items", who.name, sort, len(p.Items))
				}
			}
			slices.Sort(samples)
			t.Logf("%s sort=%s: first page median %s", who.name, sort, samples[2])
			if limit := who.limit; samples[2] > limit {
				t.Errorf("%s sort=%s: the first page took %s, the bound is %s", who.name, sort, samples[2], limit)
			}
		}
	}
}
