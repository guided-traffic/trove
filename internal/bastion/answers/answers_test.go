package answers

import (
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/iface"
)

var t0 = time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

func state(now time.Time, log *FeedLog) State {
	return State{Now: now, Period: 5 * time.Minute, Grace: 15 * time.Minute, HistoryStart: t0.Add(-time.Hour), Log: log}
}

func TestR_BAS_020_ThePeriodAndTheGraceBoundAnAnswer(t *testing.T) {
	a := Answer{Kind: Credential, ObtainedAt: t0, TokenID: "t1"}
	cases := []struct {
		name        string
		at          time.Duration
		unreachable bool
		want        Reason
	}{
		{"fresh", time.Minute, false, Usable},
		{"at the period", 5 * time.Minute, false, PeriodOver},
		{"past the period, main reachable", 6 * time.Minute, false, PeriodOver},
		{"past the period, within the grace period, unreachable", 19 * time.Minute, true, Usable},
		{"past period and grace, unreachable", 20 * time.Minute, true, PeriodOver},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := state(t0.Add(c.at), NewFeedLog())
			s.Unreachable = c.unreachable
			if got := s.Check(a); got != c.want {
				t.Fatalf("Check = %q, want %q", got, c.want)
			}
		})
	}
}

func TestR_BAS_020_ATokensExpiryIsNeverExtended(t *testing.T) {
	exp := t0.Add(30 * time.Second)
	a := Answer{Kind: Credential, ObtainedAt: t0, TokenID: "t1", TokenExpiresAt: &exp}
	s := state(t0.Add(time.Minute), NewFeedLog())
	s.Unreachable = true
	if got := s.Check(a); got != TokenExpired {
		t.Fatalf("while unreachable: %q, want %q", got, TokenExpired)
	}
}

func TestR_BAS_020_AnAnswerObtainedBeforeARevocationIsNeverUsedAfterIt(t *testing.T) {
	log := NewFeedLog()
	// The answer left at t0, the entry arrived at t0+2s; the answer reaches the replica after the entry was applied.
	log.Record(iface.FeedEntry{ID: "e1", Kind: iface.FeedToken, Tokens: []string{"t1"}}, t0.Add(2*time.Second))
	s := state(t0.Add(3*time.Second), log)
	if got := s.Check(Answer{Kind: Credential, ObtainedAt: t0, TokenID: "t1"}); got != Revoked {
		t.Fatalf("an answer from before the entry: %q", got)
	}
	// An answer whose request left 16 s after the entry was received is newer than the revocation.
	if got := s.Check(Answer{Kind: Credential, ObtainedAt: t0.Add(18 * time.Second), TokenID: "t1"}); got != Usable {
		t.Fatalf("an answer from after the entry plus the margin: %q", got)
	}
	// Another token is untouched.
	if got := s.Check(Answer{Kind: Credential, ObtainedAt: t0, TokenID: "t2"}); got != Usable {
		t.Fatalf("another token: %q", got)
	}
}

func TestR_BAS_020_EachFeedKindMatchesItsAnswers(t *testing.T) {
	log := NewFeedLog()
	at := t0.Add(time.Second)
	log.Record(iface.FeedEntry{ID: "p", Kind: iface.FeedPrincipal, Principals: []string{"u1"}}, at)
	log.Record(iface.FeedEntry{ID: "c", Kind: iface.FeedContent, Repositories: []string{"npm-local"}}, at)
	s := state(t0.Add(2*time.Second), log)
	cases := []struct {
		name string
		a    Answer
		want Reason
	}{
		{"credential of the principal", Answer{Kind: Credential, ObtainedAt: t0, PrincipalID: "u1"}, Revoked},
		{"url answer of the repository", Answer{Kind: URL, ObtainedAt: t0, Repository: "npm-local"}, Revoked},
		{"url answer of another repository", Answer{Kind: URL, ObtainedAt: t0, Repository: "npm-other"}, Usable},
		{"shape answer is not about content", Answer{Kind: Shape, ObtainedAt: t0, Repository: "npm-local"}, Usable},
		{"credential answer is not about grants", Answer{Kind: Credential, ObtainedAt: t0, PrincipalID: "u2"}, Usable},
	}
	for _, c := range cases {
		if got := s.Check(c.a); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	log.Record(iface.FeedEntry{ID: "g", Kind: iface.FeedGrants}, at)
	for _, a := range []Answer{{Kind: Shape, ObtainedAt: t0, Repository: "x"}, {Kind: URL, ObtainedAt: t0, Repository: "y"}} {
		if got := s.Check(a); got != Revoked {
			t.Errorf("after grants: %v is %q", a.Kind, got)
		}
	}
	if got := s.Check(Answer{Kind: Credential, ObtainedAt: t0, PrincipalID: "u2"}); got != Usable {
		t.Errorf("a credential answer after grants: %q", got)
	}
}

func TestR_BAS_026_AReplicaWithoutHistoryUsesNoAnswerAndOldOnesStayOut(t *testing.T) {
	a := Answer{Kind: Credential, ObtainedAt: t0, TokenID: "t1"}
	s := state(t0.Add(time.Second), NewFeedLog())
	s.HistoryStart = time.Time{}
	if got := s.Check(a); got != BeforeStart {
		t.Fatalf("no history: %q", got)
	}
	s.HistoryStart = t0.Add(-10 * time.Second) // o is earlier than the start plus the margin
	if got := s.Check(a); got != BeforeStart {
		t.Fatalf("history starting just before o: %q", got)
	}
	s.HistoryStart = t0.Add(-16 * time.Second)
	if got := s.Check(a); got != Usable {
		t.Fatalf("history starting a margin before o: %q", got)
	}
}

func TestR_BAS_026_AnEntryIsAppliedOnceAndPruned(t *testing.T) {
	log := NewFeedLog()
	e := iface.FeedEntry{ID: "e1", Kind: iface.FeedToken, Tokens: []string{"t1"}}
	if !log.Record(e, t0) || log.Record(e, t0.Add(time.Second)) {
		t.Fatal("an entry was applied twice or not at all")
	}
	log.Prune(t0.Add(time.Minute), t0.Add(time.Minute))
	if !log.Record(e, t0.Add(2*time.Minute)) {
		t.Fatal("a pruned entry is unknown again, so a later re-send applies it again")
	}
	log.Reset()
	s := state(t0.Add(3*time.Minute), log)
	if got := s.Check(Answer{Kind: Credential, ObtainedAt: t0.Add(2*time.Minute + time.Second), TokenID: "t1"}); got != Usable {
		t.Fatalf("after a reset the log is empty: %q", got)
	}
}
