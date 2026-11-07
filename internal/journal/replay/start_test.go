package replay

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/journal"
)

type recordingReader struct{ since time.Time }

func (r *recordingReader) Entries(_ context.Context, since time.Time) ([]journal.Object, []journal.Unreadable, error) {
	r.since = since
	return nil, nil, nil
}

// TestR_HA_054_ReplayStartsAtTheEarlierOfTheRestorePointAndTheNewestAuditEvent: a restore point that lies later than the
// database must not skip the revocations in between (audit finding L2); an earlier one is kept as given.
func TestR_HA_054_ReplayStartsAtTheEarlierOfTheRestorePointAndTheNewestAuditEvent(t *testing.T) {
	restoredTo := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	newest := restoredTo.Add(-3 * time.Hour)
	for _, c := range []struct {
		name   string
		newest time.Time
		ok     bool
		want   time.Time
		line   bool
	}{
		{"the database is older than the operator says", newest, true, newest.Add(-time.Minute), true},
		{"the database is newer", restoredTo.Add(time.Hour), true, restoredTo.Add(-time.Minute), false},
		{"the database cannot say", time.Time{}, false, restoredTo.Add(-time.Minute), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rd := &recordingReader{}
			var out bytes.Buffer
			rp := New(Options{DB: &fakeRunner{}, Reader: rd, Newest: func(context.Context) (time.Time, bool, error) { return c.newest, c.ok, nil }})
			if _, err := rp.Replay(t.Context(), journal.ReplayOptions{RestoredTo: restoredTo, Margin: time.Minute, Out: &out}); err != nil {
				t.Fatal(err)
			}
			if !rd.since.Equal(c.want) {
				t.Errorf("the reader was asked from %s, want %s", rd.since, c.want)
			}
			if got := strings.Contains(out.String(), "replaying from it"); got != c.line {
				t.Errorf("announcement printed = %v, want %v:\n%s", got, c.line, out.String())
			}
		})
	}
}
