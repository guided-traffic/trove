package sync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// R-VULN-006: the canonical identifier of an alias group is the smallest MAL- identifier, else the smallest GHSA-, else the
// smallest.
func TestR_VULN_006_CanonicalIdentifier(t *testing.T) {
	for _, tt := range []struct {
		ids  []string
		want string
	}{
		{[]string{"CVE-2026-0001", "GHSA-aaaa-bbbb-cccc"}, "GHSA-aaaa-bbbb-cccc"},
		{[]string{"GHSA-bbbb", "CVE-2026-0001", "MAL-2026-2", "MAL-2026-10", "GHSA-aaaa"}, "MAL-2026-10"},
		{[]string{"GHSA-b", "GHSA-a"}, "GHSA-a"},
		{[]string{"OSV-2026-2", "CVE-2026-9", "CVE-2026-10"}, "CVE-2026-10"},
		{[]string{"only"}, "only"},
	} {
		if got := canonicalOf(tt.ids); got != tt.want {
			t.Errorf("canonicalOf(%v) = %q, want %q", tt.ids, got, tt.want)
		}
	}
}

// R-VULN-006: records that name each other, directly or through a chain, are one group, whichever order they arrive in.
func TestR_VULN_006_AliasGroups(t *testing.T) {
	row := func(id string, aliases ...string) store.GroupRow {
		return store.GroupRow{ID: id, CanonicalID: id, Aliases: aliases}
	}
	rows := []store.GroupRow{
		row("GHSA-1", "CVE-1"), row("CVE-1", "GHSA-1"), // direct
		row("GHSA-2", "CVE-2"), row("OSV-2", "CVE-2"), // one name aliased by two records
		row("A-3", "B-3"), row("B-3", "C-3"), row("C-3"), // a chain
		row("SOLO-4"),
	}
	groups := groupRows(rows)
	var got [][]string
	for _, g := range groups {
		var ids []string
		for _, m := range g.members {
			ids = append(ids, m.ID)
		}
		got = append(got, ids)
	}
	want := [][]string{{"A-3", "B-3", "C-3"}, {"CVE-1", "GHSA-1"}, {"GHSA-2", "OSV-2"}, {"SOLO-4"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("groups %v, want %v", got, want)
	}
	// The same rows in another order are the same groups.
	slices.Reverse(rows)
	var again [][]string
	for _, g := range groupRows(rows) {
		var ids []string
		for _, m := range g.members {
			ids = append(ids, m.ID)
		}
		again = append(again, ids)
	}
	if !reflect.DeepEqual(again, want) {
		t.Fatalf("groups of the reversed rows %v", again)
	}
}

// R-VULN-004: the bucket sends two x-goog-hash lines, and the digest is in the second; a client that reads only the first line
// never sees it. The value is Base64.
func TestR_VULN_004_GoogHashReadsEveryLine(t *testing.T) {
	const md5b64 = "1B2M2Y8AsgTpgAmY7PhCfg==" // MD5 of the empty string
	for name, h := range map[string]http.Header{
		"two lines":  {"X-Goog-Hash": {"crc32c=AAAAAA==", "md5=" + md5b64}},
		"one line":   {"X-Goog-Hash": {"crc32c=AAAAAA==, md5=" + md5b64}},
		"md5 first":  {"X-Goog-Hash": {"md5=" + md5b64, "crc32c=AAAAAA=="}},
		"spaced out": {"X-Goog-Hash": {" crc32c=AAAAAA== ,  md5=" + md5b64}},
	} {
		sum, found := googMD5(h)
		if !found || len(sum) != 16 || sum[0] != 0xd4 {
			t.Errorf("%s: %x, %v", name, sum, found)
		}
	}
	if _, found := googMD5(http.Header{"X-Goog-Hash": {"crc32c=AAAAAA=="}}); found {
		t.Error("an md5 was found in a header that has none")
	}
	if _, found := googMD5(http.Header{}); found {
		t.Error("an md5 was found without a header")
	}
	if sum, found := googMD5(http.Header{"X-Goog-Hash": {"md5=not-base64!"}}); !found || sum != nil {
		t.Errorf("an md5 that does not decode: %x, %v (found with no digest, which no content equals)", sum, found)
	}
}

func TestParseContentRange(t *testing.T) {
	for in, want := range map[string][3]int64{
		"bytes 0-65535/8700000": {0, 65535, 8700000},
		"bytes 65536-70000/*":   {65536, 70000, 0},
	} {
		a, b, c, ok := parseContentRange(in)
		if !ok || a != want[0] || b != want[1] || c != want[2] {
			t.Errorf("%s: %d %d %d %v", in, a, b, c, ok)
		}
	}
	for _, in := range []string{"", "bytes", "bytes 1-2", "items 0-1/2", "bytes a-b/c", "bytes 0-1/x"} {
		if _, _, _, ok := parseContentRange(in); ok {
			t.Errorf("%q parsed", in)
		}
	}
}

// R-VULN-004: the change file is newest first and is read until the row at or before the watermark; a row that does not
// parse is counted and skipped.
func TestR_VULN_004_ChangeRows(t *testing.T) {
	wm := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	file := "2026-11-03T00:00:00.000000Z,GHSA-3\n" +
		"garbage\n" +
		"2026-11-02T00:00:00.000000Z,GHSA-2\r\n" +
		"2026-11-01T12:00:00.000000Z,GHSA-AT\n" + // at the watermark: not newer
		"2026-10-01T00:00:00.000000Z,GHSA-1\n"
	sink := &rowSink{watermark: wm}
	var carry []byte
	if err := scanLines([]byte(file), &carry, sink, true); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range sink.rows {
		ids = append(ids, r.ID)
	}
	if !slices.Equal(ids, []string{"GHSA-3", "GHSA-2"}) || sink.bad != 1 || !sink.done {
		t.Fatalf("rows %v, bad %d, done %v", ids, sink.bad, sink.done)
	}

	// A part ends in the middle of a row: the rest is carried into the next part.
	sink = &rowSink{watermark: wm}
	carry = nil
	if err := scanLines([]byte("2026-11-03T00:00:00.000000Z,GHSA-3\n2026-11-02T00:00:0"), &carry, sink, false); err != nil {
		t.Fatal(err)
	}
	if len(sink.rows) != 1 || string(carry) != "2026-11-02T00:00:0" {
		t.Fatalf("rows %d, carry %q", len(sink.rows), carry)
	}
	buf := append(carry, []byte("0.000000Z,GHSA-2\n")...)
	carry = nil
	if err := scanLines(buf, &carry, sink, false); err != nil || len(sink.rows) != 2 || sink.rows[1].ID != "GHSA-2" {
		t.Fatalf("rows %+v (%v)", sink.rows, err)
	}

	// More rows than the threshold stop the read and turn the run full.
	sink = &rowSink{watermark: wm, limit: 2}
	carry = nil
	_ = scanLines([]byte(strings.Repeat("2026-11-03T00:00:00.000000Z,GHSA-x\n", 5)), &carry, sink, true)
	if !sink.over || !sink.done {
		t.Errorf("over %v, done %v with %d rows", sink.over, sink.done, len(sink.rows))
	}
}

// R-VULN-027: the replica's view of the state is read once per change of the area and not per call, an ecosystem never imported
// is the zero state, stale is computed from the age, and a replica that cannot read keeps the last state it had.
func TestR_VULN_027_StatesAreReadOncePerChange(t *testing.T) {
	reg, err := cache.NewRegistry(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	s := newStates(nil, reg, 24*time.Hour, func() time.Time { return now }, slog.New(slog.DiscardHandler))
	reads := 0
	rows := []store.StateSummary{{Ecosystem: "npm", Complete: true, DataEpoch: 3, SyncedAt: now.Add(-time.Hour)}, {Ecosystem: "NuGet", DataEpoch: 0}}
	var fail error
	s.list = func(context.Context) ([]store.StateSummary, error) {
		reads++
		return rows, fail
	}
	st := s.State("npm")
	if !st.Complete || st.Stale || st.Epoch != 3 || !st.SyncedAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("state %+v", st)
	}
	for range 50 {
		s.State("npm")
	}
	if reads != 1 {
		t.Fatalf("%d reads for 51 calls", reads)
	}
	if st := s.State("PyPI"); st.Complete || st.Epoch != 0 || !st.SyncedAt.IsZero() {
		t.Errorf("an ecosystem never imported: %+v", st)
	}
	if st := s.State("NuGet"); st.Complete || st.Stale {
		t.Errorf("an ecosystem whose first import has not finished: %+v", st)
	}

	// A change of the area empties the cache; the next call reads again and sees the new state.
	rows[0].DataEpoch = 4
	reg.ApplyChange(cluster.AreaVulnData, vuln.DataChangePayloadVersion, nil)
	if st := s.State("npm"); st.Epoch != 4 {
		t.Errorf("after a change: %+v", st)
	}

	// Stale from the age of the last run, the data still complete.
	now = now.Add(48 * time.Hour)
	if st := s.State("npm"); !st.Complete || !st.Stale {
		t.Errorf("48 hours after the last run: %+v", st)
	}

	// An unreadable database says nothing about the data: the last state stays.
	fail = errors.New("database down")
	reg.ApplyChange(cluster.AreaVulnData, vuln.DataChangePayloadVersion, nil)
	if st := s.State("npm"); !st.Complete || st.Epoch != 4 {
		t.Errorf("with the database down: %+v", st)
	}
}

// R-VULN-004: the problem object of a failed run carries the code, and the audit event of a failure is written once an hour.
func TestR_VULN_004_ProblemJSON(t *testing.T) {
	at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	body, code := problemJSON(problemf(errs.CodeShrinkRefused, "too much", nil), at, at)
	if code != "shrink_refused" || !strings.Contains(string(body), `"code":"shrink_refused"`) || !strings.Contains(string(body), `"eventAt":"2026-11-03T12:00:00Z"`) {
		t.Errorf("%s (%s)", body, code)
	}
	body, code = problemJSON(errors.New("boom: secret detail"), at, time.Time{})
	if code != "internal" || strings.Contains(string(body), "secret detail") || strings.Contains(string(body), "eventAt") {
		t.Errorf("an unclassified error leaks or carries an event time: %s", body)
	}
	var f *failure
	if !errors.As(problemf("x", "y", io.EOF), &f) || !errors.Is(f, io.EOF) {
		t.Error("a failure does not unwrap")
	}
}

// R-VULN-004 "Failure", Q-VULN-80: the problem of a run that its context ended, or whose handler panicked, says what ended it;
// a cancel is the one end that is not a failure, and the value of a panic never reaches a reader.
func TestR_VULN_004_TheProblemOfARunThatDidNotEndSaysWhatEndedIt(t *testing.T) {
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name   string
		err    error
		code   string
		status float64
		cancel bool
	}{
		{"lease lost", interrupted(jobs.ErrLeaseLost), errs.CodeSyncLeaseLost, 503, false},
		{"cancelled", interrupted(jobs.ErrCancelRequested), errs.CodeSyncCancelled, 409, true},
		{"timeout", interrupted(jobs.ErrTimeout), errs.CodeSyncInterrupted, 503, false},
		{"no cause", interrupted(context.Canceled), errs.CodeSyncInterrupted, 503, false},
		{"panic", panicked("secret value of the panic"), errs.CodeSyncPanicked, 500, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body, code := problemJSON(tt.err, at, time.Time{})
			var p map[string]any
			if err := json.Unmarshal(body, &p); err != nil {
				t.Fatal(err)
			}
			if code != tt.code || p["code"] != tt.code || p["status"] != tt.status {
				t.Errorf("%s: code %q, problem %s", tt.name, code, body)
			}
			if _, ok := errs.Lookup(tt.code); !ok {
				t.Errorf("%s is not in the registry", tt.code)
			}
			if strings.Contains(string(body), "secret value") {
				t.Errorf("the value of a panic reaches the problem: %s", body)
			}
			if got := errors.Is(tt.err, jobs.ErrCancelRequested); got != tt.cancel {
				t.Errorf("counted as a cancel: %v", got)
			}
		})
	}
	if body, _ := problemJSON(interrupted(jobs.ErrTimeout), at, time.Time{}); !strings.Contains(string(body), jobs.ErrTimeout.Error()) {
		t.Errorf("an interruption does not name its cause: %s", body)
	}
}
