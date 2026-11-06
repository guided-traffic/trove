//go:build integration

package vulnapi

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_VULN_020_TheFirstPageAtScale: 100,000 findings; the first page of every sort answers within the V-7 target (300 ms,
// R-VULN-028) for an administrator. A caller whose grant is narrowed by a package pattern pays the read of the repository's
// finding triples and one decision per triple (H-488, Q-VULN-77), which the test records and bounds at one second. The
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

// TestR_VULN_028_TheFirstPageOfAPatternGrantAtTheShapeOfTheRun: 100,000 findings in the shape of the performance run (90,000 npm
// in two repositories, 10,000 NuGet, ten versions of every package, one finding per version) and a caller whose grants are the
// package patterns of V-7b. The median of the first page is logged, and bounded at the V-7 target of R-VULN-028 only with the
// performance gate (TROVE_PERF_GATE): the shared stack's load is not the reference environment's.
func TestR_VULN_028_TheFirstPageOfAPatternGrantAtTheShapeOfTheRun(t *testing.T) {
	e := newEnv(t)
	grace := e.st.User("grace")
	tok := e.st.Token(grace, scope.Default())
	e.st.Policy("perf-readers", []string{"read"}, []string{"npm-local", "npm-mirror", "nuget-local"}, grace)
	pol := e.st.Text(`SELECT id::text FROM authz_policy WHERE name = 'perf-readers'`)
	e.st.Exec(`INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) VALUES ($1::uuid, 'package', 'include', 'trove-test-filler-0*'), ($1::uuid, 'package', 'include', 'Trove.Test.Filler00*')`, pol)
	seed := func(repo, ecosystem string, from, to int, name string) {
		e.st.Exec(`INSERT INTO vuln_finding (id, repo_id, ecosystem, package_name, version, advisory_id, kind, severity, severity_rank, cvss_score,
				fix_available, fixed_versions, status, advisory_modified)
			SELECT gen_random_uuid(), r.id, $2::text, $5::text || lpad((g / 10)::text, 4, '0'), '1.' || (g % 10) || '.0', 'GHSA-perf-' || $1::text || g,
			       'vulnerability', (ARRAY['critical','high','medium','low'])[1 + g % 4], 5 - (g % 4), 5.0, true, '{9.9.9}', 'needs_triage', now()
			  FROM generate_series($3::int, $4::int) g, repositories r WHERE r.key = $1::text`, repo, ecosystem, from, to-1, name)
	}
	seed("npm-local", "npm", 0, 45000, "trove-test-filler-")
	seed("npm-mirror", "npm", 45000, 90000, "trove-test-filler-")
	seed("nuget-local", "NuGet", 0, 10000, "trove.test.filler")
	e.st.Exec(`ANALYZE vuln_finding`)
	// The policy is read when a replica starts: the one of newEnv started before it.
	web := e.st.Start(t, testenv.ReplicaOptions{Name: "web2", Env: []string{"TROVE_API_RATE_LIMITS_ENABLED=false"}})
	var samples []time.Duration
	for range 30 {
		start := time.Now()
		p := e.list(web, tok, q("limit", "50"))
		samples = append(samples, time.Since(start))
		if len(p.Items) != 50 {
			t.Fatalf("%d items", len(p.Items))
		}
	}
	slices.Sort(samples)
	t.Logf("pattern grant at the shape of the run: first page median %s, p90 %s, max %s", samples[len(samples)/2], samples[len(samples)*9/10], samples[len(samples)-1])
	if os.Getenv("TROVE_PERF_GATE") != "" && samples[len(samples)/2] > 300*time.Millisecond {
		t.Errorf("the first page took %s, the bound is 300ms", samples[len(samples)/2])
	}
}
