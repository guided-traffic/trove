package replay

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
)

// R-CI-020 (Q-CI-11): a replayed mapping disable suspends the later mappings of its tier, and the replay prints each as
// suspended, on a line of its own with its detail.
func TestR_CI_020_TheReplayPrintsTheFurtherResultsOfAnEntry(t *testing.T) {
	journal.Register(journal.Handler{
		Domain: "test", Events: []string{"alsotest.disable"},
		Apply: func(_ context.Context, _ meta.Tx, _ journal.Env, o journal.Object) (journal.Result, error) {
			r := o.Result(journal.Applied, "gitlab/first")
			later := o.Result(journal.Suspended, "gitlab/later")
			later.Detail = []byte(`{"reason":"first_match_fall_through"}`)
			r.Also = []journal.Result{later}
			return r, nil
		},
	})
	s := &memStore{objs: map[string][]byte{}}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	put(t, s, 1, now.Add(time.Second), "alsotest.disable")
	rd := NewReader(s, prefix)
	rd.now = func() time.Time { return now.Add(time.Minute) }
	var out bytes.Buffer
	sum, err := New(Options{DB: &fakeRunner{}, Reader: rd}).Replay(t.Context(), journal.ReplayOptions{RestoredTo: now, Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	want := "alsotest.disable gitlab/first " + id(1) + " applied\n" +
		"alsotest.disable gitlab/later " + id(1) + " suspended\n" +
		`  {"reason":"first_match_fall_through"}` + "\n"
	if out.String() != want {
		t.Errorf("printed:\n%s\nwant:\n%s", out.String(), want)
	}
	if sum.Counts[journal.Applied] != 1 || sum.Counts[journal.Suspended] != 1 || !strings.Contains(sum.Results[1].Subject, "later") {
		t.Errorf("summary = %+v", sum)
	}
}
