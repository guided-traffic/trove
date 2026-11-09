package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/blob/restoreversions"
)

// TestR_HA_025_MaintenanceAndRestoreVersionsRefuseWhatIsIncomplete: a missing or malformed argument prints the usage and opens
// no connection.
func TestR_HA_025_MaintenanceAndRestoreVersionsRefuseWhatIsIncomplete(t *testing.T) {
	s := streams{stdin: strings.NewReader(""), stdout: io.Discard, stderr: io.Discard}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"maintenance"}, "trove admin maintenance pause-deletions"},
		{[]string{"maintenance", "frobnicate"}, "trove admin maintenance pause-deletions"},
		{[]string{"maintenance", "pause-deletions"}, "--reason"},
		{[]string{"storage", "restore-versions"}, "trove admin storage restore-versions"},
		{[]string{"storage", "restore-versions", "--before", "yesterday"}, "trove admin storage restore-versions"},
		{[]string{"storage", "restore-versions", "--before", "2026-11-09T10:00:00Z", "--concurrency", "0"}, "trove admin storage restore-versions"},
	} {
		err := adminCmd(tc.args, s)
		var u usageError
		if !errors.As(err, &u) || !strings.Contains(u.Error(), tc.want) {
			t.Errorf("%v: err = %v, want the usage naming %q", tc.args, err, tc.want)
		}
	}
	for _, want := range []string{"storage restore-versions", "maintenance pause-deletions", "resume-deletions"} {
		if !strings.Contains(adminUsage, want) {
			t.Errorf("the usage of trove admin does not list %q", want)
		}
	}
}

// TestR_HA_025_RestoreVersionsPrintsOneLinePerBlob: the text report names each blob that had no object with what happened to
// it, and the totals last.
func TestR_HA_025_RestoreVersionsPrintsOneLinePerBlob(t *testing.T) {
	var out bytes.Buffer
	rep := &restoreversions.Report{
		Before: time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC), Checked: 9, Missing: 4, Restored: 1, MarkersRemoved: 2, Remaining: 3,
		Entries: []restoreversions.Entry{
			{SHA256: "aa", Outcome: restoreversions.OutcomeRestored, MarkersRemoved: 2, ItemCount: 1},
			{SHA256: "bb", Outcome: restoreversions.OutcomeNoDeleteMarker, ItemCount: 2},
			{SHA256: "cc", Outcome: restoreversions.OutcomeNoVersion},
			{SHA256: "dd", Outcome: restoreversions.OutcomeStillMissing},
		},
	}
	if err := writeRestoreVersionsReport(&out, rep); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"blob aa: restored, 2 delete markers removed (1 items)",
		"blob bb: no delete marker younger than t",
		"blob cc: no object version below its delete markers",
		"blob dd: still without object after removing its delete markers",
		"before 2026-11-09T10:00:00Z: 9 blobs checked, 4 without object, 1 restored, 2 delete markers removed, 3 still without object",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, out.String())
		}
	}
}
