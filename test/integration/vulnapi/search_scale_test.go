//go:build integration

package vulnapi

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_VULN_028_TheSearchesOfTheDashboardAtScale: 100,000 findings in two projects, 5,000 advisories each with an alias; the
// dashboard's searches (Q-UI-130) answer within the V-7 target: by alias, by canonical identifier, by package, by package
// prefix, by project, and the aggregate view over a project. A search by advisory is driven from the advisory side (the GIN
// index on the aliases, the (ecosystem, advisory) index of the findings), so its cost follows the matches, not the findings
// in the caller's scope. The median of five requests keeps a slow runner from failing the bound by one outlier.
func TestR_VULN_028_TheSearchesOfTheDashboardAtScale(t *testing.T) {
	e := newEnvWith(t, func(st *testenv.Stack) {
		st.Repository("shop/shop-npm", "local", "npm", `{}`)
	})
	e.st.Exec(`INSERT INTO vuln_advisory (ecosystem, id, canonical_id, modified, kind, aliases, summary, severity, severity_source, record_sha256)
		SELECT 'npm', 'GHSA-scale-' || g, 'GHSA-scale-' || g, now(), 'vulnerability', ARRAY['CVE-2099-' || g], 'scale advisory ' || g, 'high', 'cvss', '\x00'
		  FROM generate_series(0, 4999) g`)
	e.st.Exec(`INSERT INTO vuln_finding (id, repo_id, ecosystem, package_name, version, advisory_id, kind, severity, severity_rank, cvss_score,
			fix_available, status, advisory_modified, detected_at)
		SELECT gen_random_uuid(), r.id, 'npm', 'pkg-' || g, '1.0.' || g, 'GHSA-scale-' || (g % 5000), 'vulnerability',
		       (ARRAY['critical','high','medium','low'])[1 + g % 4], 5 - (g % 4), (g % 100) / 10.0, true,
		       (ARRAY['needs_triage','confirmed','blocked','dismissed'])[1 + g % 4], now(), now() - (g || ' seconds')::interval
		  FROM generate_series(1, 100000) g, repositories r WHERE r.key = CASE WHEN g % 2 = 0 THEN 'npm-local' ELSE 'shop-npm' END`)
	e.st.Exec(`ANALYZE vuln_finding`)
	e.st.Exec(`ANALYZE vuln_advisory`)
	list := "/api/v1/vulnerabilities/findings"
	for _, c := range []struct {
		name, path string
		items      int
	}{
		{"alias", list + q("advisory", "CVE-2099-17", "limit", "50"), 20},
		{"canonical", list + q("advisory", "GHSA-scale-17", "limit", "50"), 20},
		{"alias and project", list + q("advisory", "CVE-2099-17", "project", "shop", "limit", "50"), 20},
		{"alias and another project", list + q("advisory", "CVE-2099-17", "project", "it", "limit", "50"), 0},
		{"alias sorted by detected", list + q("advisory", "CVE-2099-17", "sort", "-detectedAt", "limit", "50"), 20},
		{"unknown identifier", list + q("advisory", "CVE-1999-0000", "limit", "50"), 0},
		{"package", list + q("package", "pkg-4711", "limit", "50"), 1},
		{"package prefix", list + q("packagePrefix", "pkg-4711", "limit", "50"), 11},
		{"project", list + q("project", "shop", "limit", "50"), 50},
		{"project sorted by detected", list + q("project", "shop", "sort", "-detectedAt", "limit", "50"), 50},
		{"summary of a project", "/api/v1/vulnerabilities/summary" + q("project", "shop"), -1},
	} {
		var samples []time.Duration
		for range 5 {
			start := time.Now()
			a := e.get(e.r, c.path, e.tokOlga)
			samples = append(samples, time.Since(start))
			if a.Status != 200 {
				t.Fatalf("%s: %d %s", c.name, a.Status, a.Body)
			}
			if c.items >= 0 {
				var p page
				if err := json.Unmarshal(a.Body, &p); err != nil || len(p.Items) != c.items {
					t.Fatalf("%s: %d items (%v), want %d", c.name, len(p.Items), err, c.items)
				}
			}
		}
		slices.Sort(samples)
		t.Logf("%s: median %s", c.name, samples[2])
		if samples[2] > 300*time.Millisecond {
			t.Errorf("%s: took %s, the bound is 300ms (V-7)", c.name, samples[2])
		}
	}
}
