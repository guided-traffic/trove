//go:build integration

package vulnsync

import (
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/osvstub"
)

// R-VULN-004: one OSV sync runs at a time: both kinds share the dedup key, and a manual run while another is open answers 409
// with the running job; the slot is postponed, never lost.
func TestR_VULN_004_OneOSVSyncRunsAtATime(t *testing.T) {
	e := newEnv(t, options{})
	if err := e.stub.SetFault("npm", osvstub.Fault{Scope: "archive", DelayMS: 3000}); err != nil {
		t.Fatal(err)
	}
	if res := e.st.Do(e.web, "POST", "/api/v1/system/tasks/"+taskOSVFull+"/run", e.admin, nil); res.Status != 202 {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
	within(t, "the full sync running", 20*time.Second, func() bool {
		return e.number("SELECT count(*) FROM job WHERE kind = $1 AND state = 'running'", taskOSVFull) == 1
	})
	res := e.st.Do(e.web, "POST", "/api/v1/system/tasks/"+taskOSV+"/run", e.admin, nil)
	if res.Status != 409 {
		t.Fatalf("a second sync while one runs: %d %s", res.Status, res.Body)
	}
	if !strings.Contains(string(res.Body), "job") {
		t.Errorf("the 409 does not name the running job: %s", res.Body)
	}
	e.waitIdle(taskOSVFull)
}

// R-VULN-003: with the source off, or the scanning off, the data stays as it is and nothing is requested; turning a source off
// never deletes its data.
func TestR_VULN_003_AnOffSourceKeepsItsData(t *testing.T) {
	for _, tc := range []struct{ name, env string }{
		{"source off", "TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off"},
		{"scanning off", "TROVE_VULNERABILITIES_ENABLED=false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, options{})
			e.mustSucceed(taskOSV, nil)
			data, count := e.digest("npm"), e.advisories("npm")
			if err := e.worker.Stop(); err != nil {
				t.Fatal(err)
			}
			e.worker = e.st.Start(t, testenv.ReplicaOptions{Name: "worker2", Role: "worker", VulnAdapters: testAdapters, Env: []string{tc.env}})
			e.stub.ResetCounts()
			j := e.mustSucceed(taskOSVFull, nil)
			if !strings.Contains(j.Result, "skipped") {
				t.Errorf("result %s", j.Result)
			}
			if e.stub.Counts().Total != 0 {
				t.Errorf("requests with the source off:\n%s", e.requestLog())
			}
			if e.digest("npm") != data || e.advisories("npm") != count || e.stateOf("state", "osv", "npm") != "complete" {
				t.Errorf("the data changed")
			}
		})
	}
}
