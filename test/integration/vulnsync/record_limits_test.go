//go:build integration

package vulnsync

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/vuln/match"
	vsync "github.com/guided-traffic/trove/internal/vuln/sync"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/osvstub"
)

// workerWith starts a further worker replica whose configuration differs, and returns its sync service.
func (e *env) workerWith(name string, env ...string) *vsync.Service {
	e.t.Helper()
	// It runs no jobs: the test calls its service directly, and a job of the stack must run on the first worker.
	env = append([]string{"TROVE_JOBS_WORKERS_VULN=0"}, env...)
	r := e.st.Start(e.t, testenv.ReplicaOptions{Name: name, Role: "worker", Env: env})
	return r.App.VulnSync()
}

// R-VULN-004, Q-VULN-116: a checkpoint of one run is not resumed by a run whose max_record_size cuts the batches elsewhere: the
// run that resumes at the checkpoint of the first starts over instead of skipping batches of records it would have written
// differently.
func TestR_VULN_004_ACheckpointIsNotResumedUnderAnotherRecordSize(t *testing.T) {
	// 2,500 records per ecosystem are three batches of 1,000; the padded record is above 1 MiB and below 8 MiB.
	e := newEnv(t, options{Filler: 2500})
	if _, err := e.stub.Pad("npm", osvstub.IDMal, malSize, osvstub.PadHex); err != nil {
		t.Fatal(err)
	}
	wide := e.worker.App.VulnSync() // the default of 8 MiB
	narrow := e.workerWith("worker-narrow", recordSizeEnv)
	rec := &batches{}
	wide.SetBatchHook(rec.hook)
	narrowRec := &batches{}
	narrow.SetBatchHook(narrowRec.hook)
	run := &fakeRun{}

	ctx, cancel := context.WithCancelCause(context.Background())
	rec.release, rec.cancel = "npm/1", cancel
	err := wide.RunOSV(ctx, run, vsync.Params{}, false)
	cancel(nil)
	if !errors.Is(err, jobs.ErrReleased) {
		t.Fatalf("a released run returned %v", err)
	}
	if got := rec.take(); !slices.Equal(got, []string{"NuGet/1", "NuGet/2", "NuGet/3", "npm/1"}) {
		t.Fatalf("batches before the release: %v", got)
	}

	// The same job resumes on a worker whose limit rejects the padded record: another cut, so all three batches of npm run again.
	if err := narrow.RunOSV(context.Background(), run, vsync.Params{}, false); err != nil {
		t.Fatal(err)
	}
	if got, want := narrowRec.take(), []string{"npm/1", "npm/2", "npm/3"}; !slices.Equal(got, want) {
		t.Fatalf("batches of the run under another limit: %v, want %v (a resume would start at npm/2)", got, want)
	}
	if got := e.stateOf("rejected", "osv", "npm"); got != "1" {
		t.Errorf("rejected %s", got)
	}
}

// R-VULN-004, Q-VULN-122: a limit that was raised takes effect at the next run without a new archive: the run does not answer
// "unchanged", reads the archive again and applies the record the lower limit rejected.
func TestR_VULN_004_ARaisedRecordSizeAppliesTheRejectedRecordWithoutANewArchive(t *testing.T) {
	e := newEnv(t, options{Filler: 200, Env: []string{recordSizeEnv}})
	low := e.worker.App.VulnSync() // 1 MiB
	high := e.workerWith("worker-wide", "TROVE_VULNERABILITIES_SOURCES_OSV_MAX_RECORD_SIZE=8MiB")
	e.mustSucceed(taskOSVFull, nil)
	before := e.sha(osvstub.IDMal)
	if _, err := e.stub.Pad("npm", osvstub.IDMal, malSize, osvstub.PadHex); err != nil {
		t.Fatal(err)
	}
	e.mustSucceed(taskOSVFull, nil)
	if got := e.stateOf("full_rejected", "osv", "npm"); got != "1" {
		t.Fatalf("full_rejected %s", got)
	}

	// Under the same limit nothing is read again: the record keeps its row.
	if err := low.RunOSV(context.Background(), &fakeRun{}, vsync.Params{}, false); err != nil {
		t.Fatal(err)
	}
	if got := e.sha(osvstub.IDMal); got != before {
		t.Fatalf("the record changed under the limit that rejected it: %q, was %q", got, before)
	}

	// Under the raised limit the same archive is read again and the record is written.
	if err := high.RunOSV(context.Background(), &fakeRun{}, vsync.Params{}, false); err != nil {
		t.Fatal(err)
	}
	if got := e.sha(osvstub.IDMal); got == before || got == "" {
		t.Errorf("the record was not applied after the limit was raised: %q, was %q", got, before)
	}
	if got, limit := e.stateOf("full_rejected", "osv", "npm"), e.stateOf("record_limit", "osv", "npm"); got != "0" || limit != strconv.Itoa(8<<20) {
		t.Errorf("full_rejected %s, record_limit %s", got, limit)
	}
}

// R-VULN-021, Q-VULN-122: the rejected count of the last full sync stays on the sources screen's data after the next incremental
// run, which counts its own.
func TestR_VULN_021_TheRejectedCountOfTheLastFullSyncSurvivesAnIncrementalRun(t *testing.T) {
	e := newEnv(t, options{Filler: 200, Env: []string{recordSizeEnv}})
	if _, err := e.stub.Pad("npm", osvstub.IDMal, malSize, osvstub.PadHex); err != nil {
		t.Fatal(err)
	}
	e.mustSucceed(taskOSVFull, nil)
	// The watermark is an overlap before the download; move it past the padded record, so that the incremental run fetches only
	// the record touched next and rejects none.
	e.st.Exec("UPDATE vuln_source_state SET watermark = now() WHERE source = 'osv' AND ecosystem = 'npm'")
	e.touch("npm", osvstub.IDVuln)
	j := e.mustSucceed(taskOSV, nil)
	if !strings.Contains(j.Result, `"kind":"incremental"`) {
		t.Fatalf("result %s", j.Result)
	}
	a := e.st.Do(e.web, "GET", "/api/v1/vulnerabilities/sources", e.admin, nil)
	var s struct {
		Items []struct {
			Source, Ecosystem      string
			Rejected, FullRejected int
		}
	}
	if a.Status != 200 || json.Unmarshal(a.Body, &s) != nil {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	for _, it := range s.Items {
		if it.Source == "osv" && it.Ecosystem == "npm" {
			if it.Rejected != 0 || it.FullRejected != 1 {
				t.Errorf("rejected %d, fullRejected %d: want the incremental run's 0 and the full sync's 1", it.Rejected, it.FullRejected)
			}
			return
		}
	}
	t.Fatalf("no npm row: %s", a.Body)
}

// R-VULN-004, Q-VULN-122: a row carries the adapter version that wrote it, and a row of another version is not skipped by its
// digest: a full run writes it again although the record is the one stored.
func TestR_VULN_004_ARowOfAnOlderAdapterIsNotSkippedByItsDigest(t *testing.T) {
	e := newEnv(t, options{Filler: 50})
	e.mustSucceed(taskOSVFull, nil)
	current := strconv.Itoa(match.AdapterVersion("npm"))
	if got := e.number("SELECT count(*) FROM vuln_advisory WHERE ecosystem = 'npm' AND adapter_version::text <> $1", current); got != 0 {
		t.Fatalf("%d rows do not carry the adapter version %s", got, current)
	}
	e.st.Exec("UPDATE vuln_advisory SET adapter_version = 0 WHERE ecosystem = 'npm' AND id = $1", osvstub.IDVuln)
	e.touch("npm", osvstub.IDAlias) // a new archive, so that the run reads it; the record of the older adapter is not the one that changed
	j := e.mustSucceed(taskOSVFull, nil)
	if !strings.Contains(j.Result, `"written":2`) {
		t.Errorf("result %s: the row of the older adapter was not written again", j.Result)
	}
	if got := e.text("SELECT adapter_version::text FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", osvstub.IDVuln); got != current {
		t.Errorf("adapter_version %s, want %s", got, current)
	}
}

// R-VULN-004, Q-VULN-122: a row of an older adapter that a rejected record keeps through a run is counted and named in the log of
// the worker, and is written when its record next passes.
func TestR_VULN_004_AKeptRowOfAnOlderAdapterIsNamedInTheLogAndWrittenWhenItPasses(t *testing.T) {
	e := newEnv(t, options{Filler: 200, Env: []string{recordSizeEnv}})
	e.mustSucceed(taskOSVFull, nil)
	e.st.Exec("UPDATE vuln_advisory SET adapter_version = 0 WHERE ecosystem = 'npm' AND id = $1", osvstub.IDMal)
	if _, err := e.stub.Pad("npm", osvstub.IDMal, malSize, osvstub.PadHex); err != nil {
		t.Fatal(err)
	}
	e.mustSucceed(taskOSVFull, nil)
	within(t, "the older adapter's row in the log", 20*time.Second, func() bool {
		return regexp.MustCompile(`written by an older adapter[^\n]*` + osvstub.IDMal).MatchString(e.worker.Logs.String())
	})
	if got := e.text("SELECT adapter_version::text FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", osvstub.IDMal); got != "0" {
		t.Errorf("the kept row's adapter version is %s", got)
	}
	// The record passes the next time (it is small again): the row is written, with the current adapter version.
	if _, err := e.stub.Pad("npm", osvstub.IDMal, 5000, osvstub.PadHex); err != nil {
		t.Fatal(err)
	}
	e.mustSucceed(taskOSVFull, nil)
	if got := e.text("SELECT adapter_version::text FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1", osvstub.IDMal); got != strconv.Itoa(match.AdapterVersion("npm")) {
		t.Errorf("the row was not written when its record passed: adapter version %s", got)
	}
}

// R-VULN-004, Q-VULN-121: an archive above max_archive_size fails the run with the limit, its key and the size the source declared.
func TestR_VULN_004_AnArchiveAboveTheArchiveSizeNamesTheLimitTheKeyAndTheSize(t *testing.T) {
	e := newEnv(t, options{Filler: 300, Env: []string{"TROVE_VULNERABILITIES_SOURCES_OSV_MAX_ARCHIVE_SIZE=1MiB"}})
	if _, err := e.stub.Pad("npm", osvstub.IDMal, 2_600_000, osvstub.PadHex); err != nil {
		t.Fatal(err)
	}
	if got := e.mustFail(taskOSVFull, "osv", "npm", nil); got != "archive_too_large" {
		t.Fatalf("code %q", got)
	}
	detail := e.text("SELECT last_error->>'detail' FROM vuln_source_state WHERE source = 'osv' AND ecosystem = 'npm'")
	re := regexp.MustCompile(`^the archive is larger than vulnerabilities\.sources\.osv\.max_archive_size \(1048576 bytes\): \d+ bytes$`)
	if !re.MatchString(detail) {
		t.Errorf("detail %q", detail)
	}
}

// R-OPS-019, Q-OPS-186: the URL of a request that fails reaches neither the last error of the source nor the error of the job
// beyond scheme and host: not the userinfo, not a token in the path.
func TestR_OPS_019_AFailedRequestNamesNeitherTheUserinfoNorThePathOfTheSource(t *testing.T) {
	e := newEnv(t, options{Filler: 10, Env: []string{"TROVE_VULNERABILITIES_SOURCES_OSV_BASE_URL=https://ci-user:hunter2@127.0.0.1:1/tok-secret-99/"}})
	if got := e.mustFail(taskOSVFull, "osv", "npm", nil); got == "" {
		t.Fatal("no last error")
	}
	detail := e.text("SELECT COALESCE(last_error::text, '') FROM vuln_source_state WHERE source = 'osv' AND ecosystem = 'npm'")
	jobErr := e.latest(taskOSVFull).Error
	// The error of the job and the last error of the source carry scheme and host only. The upstream log of the outbound client is
	// the operator's own log and names the request path, never the userinfo.
	for _, text := range []string{detail, jobErr} {
		for _, secret := range []string{"hunter2", "tok-secret-99", "ci-user"} {
			if strings.Contains(text, secret) {
				t.Errorf("%q appears in %.400s", secret, text)
			}
		}
	}
	for _, secret := range []string{"hunter2", "ci-user"} {
		if strings.Contains(e.worker.Logs.String(), secret) {
			t.Errorf("the userinfo %q appears in the log of the worker", secret)
		}
	}
	if !strings.Contains(jobErr, "https://127.0.0.1:1") {
		t.Errorf("the job error names no host: %s", jobErr)
	}
}
