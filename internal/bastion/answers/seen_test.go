package answers

import (
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/iface"
)

// TestR_BAS_026_AnAppliedIDIsKeptForAtLeastTheFeedLookback: with a short period and grace period the entries are
// forgotten after period + grace + 15 s, but the feed sends an entry again for its lookback; the ID stays known
// that long, so a re-sent grants entry does not stamp a new receipt time and make every answer obtained since
// unusable again (Q-BAS-126, security finding B11).
func TestR_BAS_026_AnAppliedIDIsKeptForAtLeastTheFeedLookback(t *testing.T) {
	const period, grace = 30 * time.Second, 30 * time.Second
	log := NewFeedLog()
	grants := iface.FeedEntry{ID: "g1", Kind: iface.FeedGrants}
	if !log.Record(grants, t0) {
		t.Fatal("not applied")
	}
	// Two minutes later: past period + grace + margin, inside the lookback.
	now := t0.Add(2 * time.Minute)
	log.Prune(PruneCutoffs(now, period, grace))
	s := State{Now: now, Period: period, HistoryStart: t0.Add(-time.Hour), Log: log}
	if got := s.Check(Answer{Kind: Shape, ObtainedAt: now.Add(-time.Second)}); got != Usable {
		t.Fatalf("an answer obtained after the forgotten entry: %q", got)
	}
	if log.Record(grants, now) {
		t.Fatal("the entry was applied again while the feed still re-sends it")
	}
	if got := s.Check(Answer{Kind: Shape, ObtainedAt: now.Add(-time.Second)}); got != Usable {
		t.Fatalf("the re-sent entry made a newer answer unusable: %q", got)
	}
	// Past the lookback the ID goes as well.
	later := t0.Add(FeedLookback + SkewMargin + time.Second)
	log.Prune(PruneCutoffs(later, period, grace))
	if !log.Record(grants, later) {
		t.Fatal("the ID was kept past the lookback")
	}
}
