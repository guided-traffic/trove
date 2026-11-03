//go:build integration

package vulnrestore

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/osvstub"
)

func readyz(t *testing.T, r *testenv.Replica) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+r.OpsAddr()+"/readyz", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := testenv.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	return res.StatusCode
}

// TestR_VULN_024_DatabaseGauges: the five gauges read from PostgreSQL follow the rows, once per interval and without a query
// per scrape, and a source that fails three times does not make any replica unready (R-VULN-024). The values are the inputs
// of TroveVulnerabilityDataStale, TroveVulnerabilitySyncFailing and TroveVulnerabilityClaimedNameMalicious.
func TestR_VULN_024_DatabaseGauges(t *testing.T) {
	e := newEnv(t)
	a, b := e.web("web-a"), e.web("web-b")

	// The state of a source that failed three consecutive runs, last successful two days ago, and a malicious advisory
	// for a claimed name (the test MAL- record for @acme/ui).
	e.st.Exec(`INSERT INTO vuln_source_state (source, ecosystem, state, data_epoch, synced_at, attempted_at, failures, records, last_error)
		VALUES ('osv', 'npm', 'complete', 3, now() - interval '2 days', now(), 3, 231000, '{"code":"upstream_unavailable"}')`)
	e.st.Exec(`INSERT INTO vuln_claimed_match (ecosystem, advisory_id, name, kind) VALUES ('npm', $1, '@acme/ui', 'malicious')`, osvstub.IDMal)

	for _, r := range []*testenv.Replica{a, b} {
		var text string
		testenv.WaitFor(t, "the gauges on "+r.Name, 20*time.Second, func() bool {
			text = r.Metrics(t)
			return testenv.MetricValue(text, `trove_vuln_claimed_name_matches{ecosystem="npm",kind="malicious"}`) == 1
		})
		if v := testenv.MetricValue(text, `trove_vuln_source_consecutive_failures{ecosystem="npm",source="osv"}`); v != 3 {
			t.Errorf("%s: consecutive failures = %v, want 3", r.Name, v)
		}
		if v := testenv.MetricValue(text, `trove_vuln_source_records{ecosystem="npm",source="osv"}`); v != 231000 {
			t.Errorf("%s: records = %v, want 231000", r.Name, v)
		}
		last := testenv.MetricValue(text, `trove_vuln_source_last_success_timestamp_seconds{ecosystem="npm",source="osv"}`)
		if age := float64(time.Now().Unix()) - last; age < 2*86400-120 || age > 2*86400+120 {
			t.Errorf("%s: the last success is %.0f s old, want about two days", r.Name, age)
		}
		if v := testenv.MetricValue(text, `trove_vuln_claimed_name_matches{ecosystem="npm",kind="vulnerability"}`); v != 0 {
			t.Errorf("%s: claimed vulnerability matches = %v, want a series with 0", r.Name, v)
		}
		if got := readyz(t, r); got != 200 {
			t.Errorf("%s: /readyz = %d with a failing source, want 200 (no readiness impact, R-VULN-024)", r.Name, got)
		}
	}

	// The series follow the rows: the failures are reset and the match is gone.
	e.st.Exec(`UPDATE vuln_source_state SET failures = 0`)
	e.st.Exec(`DELETE FROM vuln_claimed_match`)
	testenv.WaitFor(t, "the gauges to follow the rows", 20*time.Second, func() bool {
		text := a.Metrics(t)
		return testenv.MetricValue(text, `trove_vuln_source_consecutive_failures{ecosystem="npm",source="osv"}`) == 0 &&
			testenv.MetricValue(text, `trove_vuln_claimed_name_matches{ecosystem="npm",kind="malicious"}`) == 0
	})
}

// TestR_VULN_024_GaugesWithoutAnyRow: an installation with nothing stored has the series of the configured sources at 0 (the
// stale alert fires on them), none for the KEV source whose mode is off, and the findings gauge without findings.
func TestR_VULN_024_GaugesWithoutAnyRow(t *testing.T) {
	e := newEnv(t)
	r := e.web("web-a")
	var text string
	testenv.WaitFor(t, "the gauges", 20*time.Second, func() bool {
		text = r.Metrics(t)
		return testenv.MetricValue(text, `trove_vuln_source_last_success_timestamp_seconds{ecosystem="npm",source="osv"}`) == 0
	})
	if v := testenv.MetricValue(text, `trove_vuln_findings{kind="malicious",severity="critical",status="blocked"}`); v != 0 {
		t.Errorf("findings gauge without findings = %v, want a series with 0", v)
	}
	if v := testenv.MetricValue(text, `trove_vuln_source_last_success_timestamp_seconds{ecosystem="NuGet",source="osv"}`); v != -1 {
		t.Errorf("a series for NuGet, which the configuration narrowed away: %v", v)
	}
}
