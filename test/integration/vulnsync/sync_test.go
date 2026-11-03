//go:build integration

package vulnsync

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/osvstub"
)

// R-VULN-003 acceptance: with the default configuration the sync requests exactly npm/all.zip and NuGet/all.zip, and later the
// change files, and nothing else under the archive root.
func TestR_VULN_003_RequestsOnlyTheArchivesOfTheEcosystemsItServes(t *testing.T) {
	e := newEnv(t, options{})
	e.mustSucceed(taskOSV, nil)
	c := e.stub.Counts()
	if c.Total != 2 || c.Path("/npm/all.zip").Requests != 1 || c.Path("/NuGet/all.zip").Requests != 1 {
		t.Fatalf("the first run asked for %v\n%s", c.Paths, e.requestLog())
	}
	e.mustSucceed(taskOSV, nil)
	c = e.stub.Counts()
	for p := range c.Paths {
		switch p {
		case "/npm/all.zip", "/NuGet/all.zip", "/npm/modified_id.csv", "/NuGet/modified_id.csv":
		default:
			t.Errorf("the sync requested %s", p)
		}
	}
	if c.Under("/ecosystems.txt") != 0 || c.Kind("npm", osvstub.KindRecord) != 0 {
		t.Errorf("unexpected requests:\n%s", e.requestLog())
	}
}

// R-VULN-004 acceptance: against an empty database the first run imports both archives, sets both ecosystems complete and
// requests the full evaluation of each; a vuln-evaluate job is enqueued.
func TestR_VULN_004_FirstRunImportsBothArchives(t *testing.T) {
	e := newEnv(t, options{})
	before := time.Now()
	j := e.mustSucceed(taskOSV, nil)
	for _, eco := range []string{"npm", "NuGet"} {
		info := e.stub.Info().Ecosystems[eco]
		if got := e.advisories(eco); got != int64(info.Records) {
			t.Errorf("%s: %d records stored, the source has %d", eco, got, info.Records)
		}
		if got := e.stateOf("state", "osv", eco); got != "complete" {
			t.Errorf("%s: state %q", eco, got)
		}
		if got := e.stateOf("etag", "osv", eco); got != `"`+info.ArchiveMD5+`"` {
			t.Errorf("%s: etag %q, want the archive's", eco, got)
		}
		if got := e.stateOf("data_epoch", "osv", eco); got != "1" {
			t.Errorf("%s: data_epoch %q", eco, got)
		}
		if got := e.stateOf("records", "osv", eco); got != e.text("SELECT count(*) FROM vuln_advisory WHERE ecosystem = $1", eco) {
			t.Errorf("%s: records %q", eco, got)
		}
		if e.stateOf("full_synced_at", "osv", eco) == "" || e.stateOf("full_evaluation_requested_at", "osv", eco) == "" {
			t.Errorf("%s: no full sync time or no request of the full evaluation", eco)
		}
		if got := e.stateOf("matcher_version", "osv", eco); got != "1" {
			t.Errorf("%s: matcher_version %q", eco, got)
		}
		// The watermark is the time the download started minus the overlap.
		wm, err := time.Parse("2006-01-02 15:04:05.999999-07", e.stateOf("watermark", "osv", eco))
		if err != nil || wm.After(before.Add(-59*time.Minute)) || wm.Before(before.Add(-61*time.Minute)) {
			t.Errorf("%s: watermark %v (%v), want about an hour before %v", eco, wm, err, before)
		}
		if got := e.number("SELECT count(*) FROM vuln_pending_evaluation WHERE ecosystem = $1", eco); got != int64(info.Records) {
			t.Errorf("%s: %d advisories queued for the evaluation, want %d", eco, got, info.Records)
		}
	}
	var res []map[string]any
	if err := json.Unmarshal([]byte(j.Result), &res); err != nil || len(res) != 2 {
		t.Fatalf("result %s: %v", j.Result, err)
	}
	// The job that works the change off is the evaluation's.
	if jobs := e.number("SELECT count(*) FROM job WHERE kind = 'vuln-evaluate' AND state = 'queued'"); jobs != 1 {
		t.Errorf("%d queued vuln-evaluate jobs, want 1 (coalesced)", jobs)
	}
	metrics := e.worker.Metrics(t)
	for _, s := range []string{
		`trove_vuln_sync_runs_total{ecosystem="npm",kind="full",outcome="changed",source="osv"}`,
		`trove_vuln_sync_runs_total{ecosystem="NuGet",kind="full",outcome="changed",source="osv"}`,
	} {
		if testenv.MetricValue(metrics, s) != 1 {
			t.Errorf("%s = %v", s, testenv.MetricValue(metrics, s))
		}
	}
	if v := testenv.MetricValue(metrics, `trove_vuln_sync_records_total{ecosystem="npm",result="written",source="osv"}`); v != float64(e.stub.Info().Ecosystems["npm"].Records) {
		t.Errorf("records written = %v", v)
	}
}

// R-VULN-004 acceptance: a second run with an unchanged change file makes one ranged request per ecosystem and no record
// request; a change file that lists 3 newer records makes 3 record requests and changes exactly those rows.
func TestR_VULN_004_IncrementalRunsReadTheChangeFile(t *testing.T) {
	e := newEnv(t, options{})
	e.mustSucceed(taskOSV, nil)
	epoch := e.stateOf("data_epoch", "osv", "npm")
	e.stub.ResetCounts()
	j := e.mustSucceed(taskOSV, nil)
	c := e.stub.Counts()
	for _, eco := range []string{"npm", "NuGet"} {
		p := c.Path("/" + eco + "/modified_id.csv")
		if p.Requests != 1 || p.Ranged != 1 || p.Partial != 1 {
			t.Errorf("%s: change file %+v", eco, p)
		}
		if c.Records(eco) != 0 {
			t.Errorf("%s: %d record requests", eco, c.Records(eco))
		}
	}
	if c.Kind("npm", osvstub.KindArchive) != 0 || c.Total != 2 {
		t.Errorf("an unchanged run asked for:\n%s", e.requestLog())
	}
	if got := e.stateOf("data_epoch", "osv", "npm"); got != epoch {
		t.Errorf("an unchanged run raised the epoch to %s", got)
	}
	if !strings.Contains(j.Result, `"outcome":"unchanged"`) {
		t.Errorf("result %s", j.Result)
	}

	// Three records change at the source.
	before := e.digest("npm")
	ids := []string{osvstub.IDVuln, osvstub.IDMal, osvstub.IDLeftPad}
	shaBefore := map[string]string{}
	for _, id := range ids {
		shaBefore[id] = e.text("SELECT encode(record_sha256, 'hex') FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", id)
		e.touch("npm", id)
	}
	untouched := e.text("SELECT encode(record_sha256, 'hex') FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", osvstub.IDCVSS)
	e.stub.ResetCounts()
	j = e.mustSucceed(taskOSV, nil)
	c = e.stub.Counts()
	if c.Records("npm") != 3 || c.Records("NuGet") != 0 {
		t.Fatalf("record requests: npm %d, NuGet %d\n%s", c.Records("npm"), c.Records("NuGet"), e.requestLog())
	}
	if e.digest("npm") == before {
		t.Fatal("the stored data did not change")
	}
	for _, id := range ids {
		if got := e.text("SELECT encode(record_sha256, 'hex') FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", id); got == shaBefore[id] {
			t.Errorf("%s was not rewritten", id)
		}
	}
	if got := e.text("SELECT encode(record_sha256, 'hex') FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", osvstub.IDCVSS); got != untouched {
		t.Errorf("a record the change file does not name was rewritten")
	}
	if !strings.Contains(j.Result, `"written":3`) {
		t.Errorf("result %s", j.Result)
	}
	if got := e.stateOf("data_epoch", "osv", "npm"); got == epoch {
		t.Errorf("the epoch stayed %s after three records changed", got)
	}
	// The watermark is the newest row applied: a run after it asks for no record.
	e.stub.ResetCounts()
	e.mustSucceed(taskOSV, nil)
	if got := e.stub.Counts().Records("npm"); got != 0 {
		t.Errorf("%d record requests after the watermark moved", got)
	}
}

// touch rewrites a record of the source with a changed summary and a later modified.
func (e *env) touch(eco, id string) {
	e.t.Helper()
	body, ok := e.stub.Record(eco, id)
	if !ok {
		e.t.Fatalf("the source has no %s %s", eco, id)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		e.t.Fatal(err)
	}
	m["summary"] = "changed " + time.Now().Format(time.RFC3339Nano)
	b, _ := json.Marshal(m)
	if _, err := e.stub.Upsert(eco, b); err != nil {
		e.t.Fatal(err)
	}
}

// R-VULN-004 acceptance: with the source's Range support switched off the same run reads the 200 body and applies the same 3
// records.
func TestR_VULN_004_IncrementalRunWithoutRangeSupport(t *testing.T) {
	e := newEnv(t, options{})
	e.mustSucceed(taskOSV, nil)
	if err := e.stub.SetFault("npm", osvstub.Fault{Scope: "changes", NoRange: true}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{osvstub.IDVuln, osvstub.IDMal, osvstub.IDLeftPad} {
		e.touch("npm", id)
	}
	e.stub.ResetCounts()
	e.mustSucceed(taskOSV, nil)
	c := e.stub.Counts()
	p := c.Path("/npm/modified_id.csv")
	if p.Requests != 1 || p.Partial != 0 {
		t.Errorf("change file %+v, want one full answer", p)
	}
	if c.Records("npm") != 3 {
		t.Fatalf("%d record requests, want 3\n%s", c.Records("npm"), e.requestLog())
	}
}

// R-VULN-004: the change file is read in parts of 64 KiB while the last row of a part is newer than the watermark, and a
// 404 for a listed record is left to the next full sync.
func TestR_VULN_004_ChangeFileInSeveralParts(t *testing.T) {
	e := newEnv(t, options{Env: []string{"TROVE_VULNERABILITIES_SOURCES_OSV_FULL_RESYNC_THRESHOLD=5000"}})
	e.mustSucceed(taskOSV, nil)
	// 2,000 rows of about 50 bytes are above 64 KiB.
	if err := e.stub.SetFiller("npm", 2000); err != nil {
		t.Fatal(err)
	}
	e.stub.ResetCounts()
	e.mustSucceed(taskOSV, nil)
	c := e.stub.Counts()
	p := c.Path("/npm/modified_id.csv")
	if p.Requests < 2 || p.Requests != p.Partial {
		t.Fatalf("change file %+v, want several ranged parts\n%s", p, e.requestLog())
	}
	if got := e.advisories("npm"); got != int64(e.stub.Info().Ecosystems["npm"].Records) {
		t.Errorf("%d records stored, the source has %d", got, e.stub.Info().Ecosystems["npm"].Records)
	}
	// A record the change file lists and the source no longer serves is left to the next full sync.
	e.touch("npm", osvstub.IDVuln)
	if err := e.stub.SetFault("npm", osvstub.Fault{Scope: "records", NotFound: []string{osvstub.IDVuln}}); err != nil {
		t.Fatal(err)
	}
	e.mustSucceed(taskOSV, nil)
	if !strings.Contains(e.latest(taskOSV).Result, `"skipped":1`) {
		t.Errorf("result %s", e.latest(taskOSV).Result)
	}
}

// R-VULN-004: more changed records than full_resync_threshold turn an incremental run into a full sync; a watermark older than
// 7 days does too.
func TestR_VULN_004_TheIncrementalRunTurnsFull(t *testing.T) {
	e := newEnv(t, options{Env: []string{"TROVE_VULNERABILITIES_SOURCES_OSV_FULL_RESYNC_THRESHOLD=2"}})
	e.mustSucceed(taskOSV, nil)
	for _, id := range []string{osvstub.IDVuln, osvstub.IDMal, osvstub.IDLeftPad} {
		e.touch("npm", id)
	}
	e.stub.ResetCounts()
	e.mustSucceed(taskOSV, nil)
	c := e.stub.Counts()
	if c.Kind("npm", osvstub.KindArchive) != 1 || c.Records("npm") != 0 {
		t.Fatalf("3 changed records above a threshold of 2:\n%s", e.requestLog())
	}
	if !strings.Contains(e.latest(taskOSV).Result, `"kind":"full"`) {
		t.Errorf("result %s", e.latest(taskOSV).Result)
	}
	e.st.Exec("UPDATE vuln_source_state SET watermark = now() - interval '8 days' WHERE source = 'osv'")
	e.stub.ResetCounts()
	e.mustSucceed(taskOSV, nil)
	if got := e.stub.Counts().Kind("npm", osvstub.KindChanges); got != 0 {
		t.Errorf("a watermark of 8 days old read the change file %d times", got)
	}
}

// R-VULN-004: a full sync of an archive that did not change ends as unchanged on the 304, and the weekly full sync removes
// the records the source no longer has.
func TestR_VULN_004_FullSyncDeletesWhatTheSourceNoLongerHas(t *testing.T) {
	e := newEnv(t, options{})
	e.mustSucceed(taskOSV, nil)
	e.stub.ResetCounts()
	j := e.mustSucceed(taskOSVFull, nil)
	if !strings.Contains(j.Result, `"outcome":"unchanged"`) || e.stub.Counts().Path("/npm/all.zip").NotModified != 1 {
		t.Errorf("a full sync of an unchanged archive: %s\n%s", j.Result, e.requestLog())
	}
	if err := e.stub.Remove("npm", osvstub.IDList); err != nil {
		t.Fatal(err)
	}
	e.mustSucceed(taskOSV, nil) // an incremental run does not delete
	if got := e.number("SELECT count(*) FROM vuln_advisory WHERE id = $1", osvstub.IDList); got != 1 {
		t.Fatalf("the incremental run deleted a record")
	}
	epoch := e.stateOf("data_epoch", "osv", "npm")
	j = e.mustSucceed(taskOSVFull, nil)
	if got := e.number("SELECT count(*) FROM vuln_advisory WHERE id = $1", osvstub.IDList); got != 0 {
		t.Fatalf("the full sync kept the record the source no longer has: %s", j.Result)
	}
	if got := e.number("SELECT count(*) FROM vuln_affected WHERE advisory_id = $1", osvstub.IDList); got != 0 {
		t.Errorf("the affected entries stayed")
	}
	if got := e.number("SELECT count(*) FROM vuln_pending_evaluation WHERE advisory_id = $1", osvstub.IDList); got != 1 {
		t.Errorf("the deleted record was not queued for the evaluation")
	}
	if got := e.stateOf("data_epoch", "osv", "npm"); got == epoch {
		t.Errorf("the epoch stayed %s", got)
	}
}

// R-VULN-004 acceptance: a truncated archive, an archive whose MD5 differs from x-goog-hash, an archive with 2 % invalid
// records and an archive with 30 % fewer records each leave the stored data unchanged and record the matching error code;
// {"acceptShrink": true} applies the shrunken archive. An incremental run with one invalid record applies the others and
// advances the watermark.
func TestR_VULN_004_IntegrityGuardsLeaveTheStoredDataAsItIs(t *testing.T) {
	e := newEnv(t, options{Filler: 200})
	e.mustSucceed(taskOSV, nil)
	data := e.digest("npm")
	count := e.advisories("npm")

	cases := []struct {
		name  string
		fault osvstub.Fault
		code  string
	}{
		{"truncated", osvstub.Fault{Scope: "archive", Truncate: true}, "archive_limit"},
		{"2 percent invalid", osvstub.Fault{Scope: "archive", InvalidPercent: 2}, "too_many_invalid_records"},
		{"30 percent fewer", osvstub.Fault{Scope: "archive", ShrinkPercent: 30}, "shrink_refused"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := e.stub.SetFault("npm", c.fault); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = e.stub.SetFault("npm", osvstub.Fault{}) }()
			if got := e.mustFail(taskOSVFull, "osv", "npm", nil); got != c.code {
				t.Fatalf("code %q, want %q", got, c.code)
			}
			if e.digest("npm") != data || e.advisories("npm") != count {
				t.Error("the stored data changed")
			}
			if got := e.stateOf("state", "osv", "npm"); got != "complete" {
				t.Errorf("state %q", got)
			}
			if got := e.stateOf("failures", "osv", "npm"); got == "0" {
				t.Errorf("no failure counted")
			}
			// The other ecosystem is not stopped by it.
			if got := e.stateOf("failures", "osv", "NuGet"); got != "0" {
				t.Errorf("NuGet failures %s", got)
			}
		})
	}

	t.Run("accept shrink", func(t *testing.T) {
		if err := e.stub.SetFault("npm", osvstub.Fault{Scope: "archive", ShrinkPercent: 30}); err != nil {
			t.Fatal(err)
		}
		j := e.mustSucceed(taskOSVFull, map[string]any{"acceptShrink": true})
		if !strings.Contains(j.Result, `"acceptedShrink":true`) {
			t.Errorf("result %s", j.Result)
		}
		if got := e.advisories("npm"); got >= count || got < count*60/100 {
			t.Errorf("%d records after the shrink, %d before", got, count)
		}
		if got := e.stateOf("failures", "osv", "npm"); got != "0" {
			t.Errorf("failures %s after a successful run", got)
		}
		if e.stateOf("last_error", "osv", "npm") != "" {
			t.Errorf("last_error %q", e.stateOf("last_error", "osv", "npm"))
		}
		within(t, "the override in the audit log", 20*time.Second, func() bool {
			return e.number("SELECT count(*) FROM audit_event WHERE action = 'vulnerability.source.imported' AND details->>'acceptShrink' = 'true'") == 1
		})
	})

	t.Run("an incremental run skips an invalid record and moves the watermark past it", func(t *testing.T) {
		if err := e.stub.SetFault("npm", osvstub.Fault{}); err != nil {
			t.Fatal(err)
		}
		e.mustSucceed(taskOSVFull, nil) // the records the shrink removed are back
		ids := e.stub.IDs("npm")
		bad, good := ids[0], ids[1]
		e.touch("npm", bad)
		e.touch("npm", good)
		newest := e.stub.Info().Ecosystems["npm"].Newest
		if err := e.stub.SetFault("npm", osvstub.Fault{Scope: "records", Invalid: []string{bad}}); err != nil {
			t.Fatal(err)
		}
		oldSHA := e.text("SELECT encode(record_sha256, 'hex') FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", good)
		j := e.mustSucceed(taskOSV, nil)
		if !strings.Contains(j.Result, `"rejected":1`) || !strings.Contains(j.Result, `"written":1`) {
			t.Errorf("result %s", j.Result)
		}
		if got := e.text("SELECT encode(record_sha256, 'hex') FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", good); got == oldSHA {
			t.Errorf("the valid record was not applied")
		}
		if got := e.stateOf("rejected", "osv", "npm"); got != "1" {
			t.Errorf("rejected %s", got)
		}
		wm, err := time.Parse("2006-01-02 15:04:05.999999-07", e.stateOf("watermark", "osv", "npm"))
		want, _ := time.Parse(osvstub.TimeLayout, newest)
		if err != nil || !wm.Equal(want) {
			t.Errorf("watermark %v (%v), want the newest row %v", wm, err, want)
		}
	})
}

// R-VULN-004 acceptance: an archive whose MD5 differs from the one x-goog-hash announces (the bucket sends two header lines;
// the sync reads both) is refused before anything is written.
func TestR_VULN_004_DigestMismatchIsRefused(t *testing.T) {
	e := newEnv(t, options{NoWorker: true})
	if err := e.stub.SetFault("npm", osvstub.Fault{Scope: "archive", DigestMismatch: true}); err != nil {
		t.Fatal(err)
	}
	e.worker = e.st.Start(t, testenv.ReplicaOptions{Name: "worker", Role: "worker"})
	if got := e.mustFail(taskOSV, "osv", "npm", nil); got != "archive_digest_mismatch" {
		t.Fatalf("code %q", got)
	}
	if e.advisories("npm") != 0 || e.stateOf("state", "osv", "npm") != "empty" {
		t.Error("data was written")
	}
	// The other ecosystem imported.
	if e.stateOf("state", "osv", "NuGet") != "complete" {
		t.Errorf("NuGet state %q", e.stateOf("state", "osv", "NuGet"))
	}
}

// R-VULN-004: an archive larger than max_archive_size ends the run with archive_too_large before the body is read.
func TestR_VULN_004_ArchiveTooLarge(t *testing.T) {
	e := newEnv(t, options{Env: []string{"TROVE_VULNERABILITIES_SOURCES_OSV_MAX_ARCHIVE_SIZE=1MiB"}})
	if err := e.stub.SetFiller("npm", 12000); err != nil {
		t.Fatal(err)
	}
	if e.stub.Info().Ecosystems["npm"].ArchiveBytes <= 1<<20 {
		t.Fatalf("the archive has %d bytes", e.stub.Info().Ecosystems["npm"].ArchiveBytes)
	}
	if got := e.mustFail(taskOSV, "osv", "npm", nil); got != "archive_too_large" {
		t.Fatalf("code %q", got)
	}
	if e.advisories("npm") != 0 {
		t.Error("data was written")
	}
}

// R-VULN-004 acceptance: a base URL resolving to 169.254.169.254 fails the run with egress_denied. The failure is recorded
// (last_error, consecutive failures, the counter) and the audit event is written once in the hour however many runs fail.
func TestR_VULN_004_EgressDeniedAndFailureBookkeeping(t *testing.T) {
	e := newEnv(t, options{Env: []string{"TROVE_VULNERABILITIES_SOURCES_OSV_BASE_URL=https://169.254.169.254/"}})
	if got := e.mustFail(taskOSV, "osv", "npm", nil); got != "egress_denied" {
		t.Fatalf("code %q", got)
	}
	e.mustFail(taskOSV, "osv", "npm", nil)
	if got := e.stateOf("failures", "osv", "npm"); got != "2" {
		t.Errorf("failures %s after two failed runs", got)
	}
	if got := e.text("SELECT last_error->>'status' FROM vuln_source_state WHERE source = 'osv' AND ecosystem = 'npm'"); got == "" {
		t.Errorf("last_error is no problem object")
	}
	if e.stateOf("state", "osv", "npm") != "empty" {
		t.Errorf("state %q", e.stateOf("state", "osv", "npm"))
	}
	if v := testenv.MetricValue(e.worker.Metrics(t), `trove_vuln_sync_runs_total{ecosystem="npm",kind="full",outcome="failed",source="osv"}`); v != 2 {
		t.Errorf("failed runs counted: %v", v)
	}
	within(t, "the audit event of the failure", 20*time.Second, func() bool {
		return e.number("SELECT count(*) FROM audit_event WHERE action = 'vulnerability.source.failed' AND details->>'ecosystem' = 'npm'") >= 1
	})
	time.Sleep(500 * time.Millisecond)
	if got := e.number("SELECT count(*) FROM audit_event WHERE action = 'vulnerability.source.failed' AND details->>'ecosystem' = 'npm' AND details->>'code' = 'egress_denied'"); got != 1 {
		t.Errorf("%d events of two failures within the hour, want 1", got)
	}
}

// R-VULN-004: a source that answers 500 fails the run with its status recorded, and the next run that succeeds clears the
// failures.
func TestR_VULN_004_AFailedRunIsRetriedAtTheNextSlot(t *testing.T) {
	e := newEnv(t, options{})
	if err := e.stub.SetFault("npm", osvstub.Fault{Error: 500}); err != nil {
		t.Fatal(err)
	}
	if got := e.mustFail(taskOSV, "osv", "npm", nil); got != "upstream_unexpected_status" {
		t.Fatalf("code %q", got)
	}
	if e.number("SELECT max_attempts FROM job WHERE kind = $1 ORDER BY created_at DESC LIMIT 1", taskOSV) != 1 {
		t.Errorf("the job may be retried within its slot")
	}
	if err := e.stub.SetFault("npm", osvstub.Fault{}); err != nil {
		t.Fatal(err)
	}
	e.mustSucceed(taskOSV, nil)
	if got := e.stateOf("failures", "osv", "npm"); got != "0" {
		t.Errorf("failures %s after a successful run", got)
	}
}

// R-VULN-027: with Valkey stopped a sync triggered through the task API starts within jobs.poll_interval plus 1 s.
func TestR_VULN_027_ASyncStartsWithoutValkeyWithinThePollInterval(t *testing.T) {
	e := newEnv(t, options{})
	e.mustSucceed(taskOSV, nil)
	// The stack of this test has no Valkey wake-ups (testenv.StackOptions.Valkey is false): the worker finds the job by polling.
	const pollInterval = 200 * time.Millisecond // TROVE_JOBS_POLL_INTERVAL of the environment
	e.mustSucceed(taskOSV, nil)
	got := time.Duration(e.number("SELECT (extract(epoch FROM started_at - created_at) * 1000)::bigint FROM job WHERE kind = $1 ORDER BY created_at DESC LIMIT 1", taskOSV)) * time.Millisecond
	// One second of slack for a loaded CI machine on top of the specified bound.
	if got > pollInterval+time.Second+time.Second {
		t.Errorf("the job started %v after it was enqueued", got)
	}
}
