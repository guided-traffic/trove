//go:build integration

package vulnsync

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/osvstub"
)

const (
	// malSize is the size of the largest record of the npm archive measured on 2026-11-07 (MAL-2026-16369); the stub's
	// MAL-2026-1234 is padded to it.
	malSize   = 1_148_610
	otherSize = 560_000
	oneMiB    = 1 << 20

	recordSizeEnv = "TROVE_VULNERABILITIES_SOURCES_OSV_MAX_RECORD_SIZE=1MiB"
)

// sha is the stored digest of a record.
func (e *env) sha(id string) string {
	e.t.Helper()
	return e.text("SELECT COALESCE((SELECT encode(record_sha256, 'hex') FROM vuln_advisory WHERE ecosystem = 'npm' AND id = $1), '')", id)
}

// padMeasured pads the stub's records to the shape of the measured archive: one record of the largest size and two of 560,000
// bytes (a hex text that deflates to about half, so it is the size that a bound meets and not the ratio).
func (e *env) padMeasured() {
	e.t.Helper()
	for id, size := range map[string]int{osvstub.IDMal: malSize, osvstub.IDVuln: otherSize, osvstub.IDAlias: otherSize} {
		if _, err := e.stub.Pad("npm", id, size, osvstub.PadHex); err != nil {
			e.t.Fatal(err)
		}
	}
}

// rejectedLines are the lines of the worker's log that report a rejected record.
func (e *env) rejectedLines() []string {
	var out []string
	for _, l := range strings.Split(e.worker.Logs.String(), "\n") {
		if strings.Contains(l, "an advisory record was rejected") {
			out = append(out, l)
		}
	}
	return out
}

// R-VULN-004 acceptance, Q-VULN-116: the archive of the owner's job (one record of 1,148,610 bytes, two of 560,000, about 200
// ordinary ones) under max_record_size 1 MiB: the run completes, the one record above the bound is rejected, counted and logged
// with its size, the limit and the key, and its stored row stays as it was; the two records below the bound are written.
func TestR_VULN_004_TheMeasuredArchiveCompletesWithOneRejectedRecord(t *testing.T) {
	e := newEnv(t, options{Filler: 200, Env: []string{recordSizeEnv}})
	e.mustSucceed(taskOSVFull, nil)
	malBefore, vulnBefore, aliasBefore := e.sha(osvstub.IDMal), e.sha(osvstub.IDVuln), e.sha(osvstub.IDAlias)
	count := e.advisories("npm")
	e.padMeasured()

	j := e.mustSucceed(taskOSVFull, nil)
	if !strings.Contains(j.Result, `"rejected":1`) {
		t.Errorf("result %s", j.Result)
	}
	if got := e.stateOf("rejected", "osv", "npm"); got != "1" {
		t.Errorf("rejected %s", got)
	}
	if got, limit := e.stateOf("full_rejected", "osv", "npm"), e.stateOf("record_limit", "osv", "npm"); got != "1" || limit != "1048576" {
		t.Errorf("the last full sync rejected %s with the limit %s", got, limit)
	}
	if got := e.stateOf("failures", "osv", "npm"); got != "0" {
		t.Errorf("failures %s", got)
	}
	if got := e.stateOf("last_error", "osv", "npm"); got != "" {
		t.Errorf("last_error %q", got)
	}
	if got := e.advisories("npm"); got != count {
		t.Errorf("%d records, %d before: the rejected record's row was deleted", got, count)
	}
	if got := e.sha(osvstub.IDMal); got != malBefore || got == "" {
		t.Errorf("the rejected record's row changed: %q, was %q", got, malBefore)
	}
	for id, before := range map[string]string{osvstub.IDVuln: vulnBefore, osvstub.IDAlias: aliasBefore} {
		if got := e.sha(id); got == before || got == "" {
			t.Errorf("%s was not written: %q, was %q", id, got, before)
		}
	}
	within(t, "the rejection in the log of the worker", 20*time.Second, func() bool { return len(e.rejectedLines()) > 0 })
	lines := e.rejectedLines()
	if len(lines) != 1 {
		t.Fatalf("%d rejection lines: %v", len(lines), lines)
	}
	for _, want := range []string{"WARN", "osv", "npm", osvstub.IDMal + ".json", "1148610", "1048576", "vulnerabilities.sources.osv.max_record_size"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("the log line lacks %q: %s", want, lines[0])
		}
	}
}

// R-VULN-004 acceptance, Q-VULN-120: the same archive under the default max_record_size (8 MiB) is imported whole.
func TestR_VULN_004_TheMeasuredArchiveImportsWholeUnderTheDefaultRecordSize(t *testing.T) {
	e := newEnv(t, options{Filler: 200})
	e.mustSucceed(taskOSVFull, nil)
	before := map[string]string{}
	ids := []string{osvstub.IDMal, osvstub.IDVuln, osvstub.IDAlias}
	for _, id := range ids {
		before[id] = e.sha(id)
	}
	e.padMeasured()
	e.mustSucceed(taskOSVFull, nil)
	for _, id := range ids {
		if got := e.sha(id); got == before[id] || got == "" {
			t.Errorf("%s was not written: %q, was %q", id, got, before[id])
		}
	}
	if got := e.stateOf("rejected", "osv", "npm"); got != "0" {
		t.Errorf("rejected %s", got)
	}
	if lines := e.rejectedLines(); len(lines) != 0 {
		t.Errorf("rejection lines: %v", lines)
	}
}

// R-VULN-004 acceptance, Q-VULN-116: the ratio bound of one entry is above what deflate reaches, so a record of one repeated
// character, which deflate packs to about 1000:1, is imported and not rejected.
func TestR_VULN_004_AnEntryThatCompressesAboveOneHundredToOneIsImported(t *testing.T) {
	e := newEnv(t, options{Filler: 200})
	e.mustSucceed(taskOSVFull, nil)
	before := e.sha(osvstub.IDVuln)
	if _, err := e.stub.Pad("npm", osvstub.IDVuln, 400_000, osvstub.PadRepeat); err != nil {
		t.Fatal(err)
	}
	j := e.mustSucceed(taskOSVFull, nil)
	if strings.Contains(j.Result, `"rejected":1`) {
		t.Errorf("result %s", j.Result)
	}
	if got := e.stateOf("rejected", "osv", "npm"); got != "0" {
		t.Errorf("rejected %s", got)
	}
	if got := e.sha(osvstub.IDVuln); got == before || got == "" {
		t.Errorf("the record was not written: %q, was %q", got, before)
	}
	if lines := e.rejectedLines(); len(lines) != 0 {
		t.Errorf("rejection lines: %v", lines)
	}
}

// R-VULN-004 acceptance, Q-VULN-116: an incremental run rejects a fetched record above max_record_size, logs it, applies the other
// records of the change file and moves the watermark.
func TestR_VULN_004_AnIncrementalRunRejectsARecordAboveTheRecordSize(t *testing.T) {
	e := newEnv(t, options{Filler: 200, Env: []string{recordSizeEnv}})
	e.mustSucceed(taskOSVFull, nil)
	malBefore, vulnBefore := e.sha(osvstub.IDMal), e.sha(osvstub.IDVuln)
	mark := e.stateOf("watermark", "osv", "npm")
	if _, err := e.stub.Pad("npm", osvstub.IDMal, malSize, osvstub.PadHex); err != nil {
		t.Fatal(err)
	}
	if _, err := e.stub.Pad("npm", osvstub.IDVuln, 3000, osvstub.PadHex); err != nil {
		t.Fatal(err)
	}
	j := e.mustSucceed(taskOSV, nil)
	if !strings.Contains(j.Result, `"kind":"incremental"`) || !strings.Contains(j.Result, `"rejected":1`) {
		t.Errorf("result %s", j.Result)
	}
	if got := e.sha(osvstub.IDMal); got != malBefore {
		t.Errorf("the rejected record's row changed: %q, was %q", got, malBefore)
	}
	if got := e.sha(osvstub.IDVuln); got == vulnBefore || got == "" {
		t.Errorf("the other record was not applied: %q, was %q", got, vulnBefore)
	}
	if got := e.stateOf("watermark", "osv", "npm"); got == mark {
		t.Errorf("the watermark stayed at %s", got)
	}
	within(t, "the rejection in the log of the worker", 20*time.Second, func() bool { return len(e.rejectedLines()) == 1 })
	l := e.rejectedLines()[0]
	for _, want := range []string{osvstub.IDMal + ".json", "1048576", "vulnerabilities.sources.osv.max_record_size"} {
		if !strings.Contains(l, want) {
			t.Errorf("the log line lacks %q: %s", want, l)
		}
	}
}

// R-VULN-004 acceptance, Q-VULN-121, R-OPS-019: a run that a bound or a guard ended says what was crossed, in the last error of
// the sources screen and in the error of the job.
func TestR_VULN_004_AFailedRunNamesWhatWasCrossed(t *testing.T) {
	e := newEnv(t, options{Filler: 200})
	e.mustSucceed(taskOSVFull, nil)

	lastError := func() (code, detail string) {
		t.Helper()
		a := e.st.Do(e.web, "GET", "/api/v1/vulnerabilities/sources", e.admin, nil)
		if a.Status != 200 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		var s struct {
			Items []struct {
				Source, Ecosystem string
				LastError         *struct{ Code, Detail string }
			}
		}
		if err := json.Unmarshal(a.Body, &s); err != nil {
			t.Fatal(err)
		}
		for _, it := range s.Items {
			if it.Source == "osv" && it.Ecosystem == "npm" && it.LastError != nil {
				return it.LastError.Code, it.LastError.Detail
			}
		}
		t.Fatalf("no last error of npm: %s", a.Body)
		return "", ""
	}
	jobError := func() (detail string, attempt int) {
		t.Helper()
		id := e.text("SELECT id::text FROM job WHERE kind = $1 ORDER BY created_at DESC LIMIT 1", taskOSVFull)
		a := e.st.Do(e.web, "GET", "/api/v1/system/jobs/"+id, e.admin, nil)
		if a.Status != 200 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		var j struct {
			Error *struct {
				Detail  string
				Attempt int
			}
		}
		if err := json.Unmarshal(a.Body, &j); err != nil || j.Error == nil {
			t.Fatalf("job %s: %v", a.Body, err)
		}
		return j.Error.Detail, j.Error.Attempt
	}

	t.Run("a truncated archive", func(t *testing.T) {
		if err := e.stub.SetFault("npm", osvstub.Fault{Scope: "archive", Truncate: true}); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = e.stub.SetFault("npm", osvstub.Fault{}) }()
		e.mustFail(taskOSVFull, "osv", "npm", nil)
		const sentence = "the archive is not a valid zip archive: "
		code, detail := lastError()
		if code != "archive_limit" || !strings.HasPrefix(detail, sentence) {
			t.Errorf("last error %q: %q", code, detail)
		}
		jd, attempt := jobError()
		if !strings.Contains(jd, sentence) || attempt != 1 {
			t.Errorf("job error %q, attempt %d", jd, attempt)
		}
	})
	t.Run("too many rejected records", func(t *testing.T) {
		if err := e.stub.SetFault("npm", osvstub.Fault{Scope: "archive", InvalidPercent: 2}); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = e.stub.SetFault("npm", osvstub.Fault{}) }()
		e.mustFail(taskOSVFull, "osv", "npm", nil)
		code, detail := lastError()
		re := regexp.MustCompile(`^\d+ of \d+ records were rejected by validation \(\d+\.\d\d %\), more than the fixed 1 %; the first: `)
		if code != "too_many_invalid_records" || !re.MatchString(detail) {
			t.Errorf("last error %q: %q", code, detail)
		}
	})
	t.Run("a shrink", func(t *testing.T) {
		if err := e.stub.SetFault("npm", osvstub.Fault{Scope: "archive", ShrinkPercent: 30}); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = e.stub.SetFault("npm", osvstub.Fault{}) }()
		e.mustFail(taskOSVFull, "osv", "npm", nil)
		code, detail := lastError()
		if code != "shrink_refused" || !strings.Contains(detail, "vulnerabilities.sources.osv.max_shrink (0.2)") {
			t.Errorf("last error %q: %q", code, detail)
		}
	})
}
