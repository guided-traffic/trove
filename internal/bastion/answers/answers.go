// Package answers holds the types and the validity rules of the main installation's answers that a Bastion
// keeps (R-BAS-020, Q-BAS-8, Q-BAS-32): what an answer is, when it is usable, and the feed log the rule about
// revocations reads. It is a leaf: it imports the interface package and the standard library, so the engine,
// the shared state (which verifies signed records from Valkey) and the tests judge answers by the same code.
//
// The rules are the specification's, written once:
//
//  1. now < o + period, extended by the grace period only while the main installation is unreachable;
//  2. now < the token's expiry, never extended;
//  3. no feed entry that matches the answer was received after o − [SkewMargin];
//  4. o is not earlier than the start of the replica's feed history plus [SkewMargin].
//
// where o is the Bastion's clock when the request that produced the answer was sent. Work package 4 stores
// answers (memory) and work package 6 stores them signed in Valkey; neither decides validity itself.
package answers

import (
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/iface"
)

// SkewMargin is the 15 s of R-BAS-020: the main installation's revocation bound (at most 10 s, R-AUTH-023) and
// the clock skew between replicas.
const SkewMargin = 15 * time.Second

// FeedLookback is the lookback of the main installation's feed at its default (bastions.feed.lookback, R-BAS-015):
// a read returns the entries newer than its cursor minus this period, so an entry is sent again for that long
// after it was first received. The log keeps the IDs it applied for at least this long, so that a re-sent entry
// is not applied again (Q-BAS-126, H-470).
const FeedLookback = 5 * time.Minute

// PruneCutoffs returns the two cutoffs of [FeedLog.Prune] at the given time: entries received before the first
// no answer can depend on (period + grace period + [SkewMargin]), IDs received before the second can no longer
// be re-sent (the same period, and at least the [FeedLookback] plus the margin).
func PruneCutoffs(now time.Time, period, grace time.Duration) (entries, ids time.Time) {
	keep := period + grace + SkewMargin
	return now.Add(-keep), now.Add(-max(keep, FeedLookback+SkewMargin))
}

// Kind is the kind of an answer (R-BAS-020).
type Kind string

// The kinds of answers.
const (
	// Credential: what the main installation said about a credential (kind, token ID, principal ID, expiry).
	Credential Kind = "credential"
	// Shape: a credential's read shape on a repository.
	Shape Kind = "shape"
	// URL: whether a credential may read a cache key, or the stored refusal.
	URL Kind = "url"
)

// Answer is the part of an answer the validity rules read. The payloads (the subject, the shape, the stored
// refusal) belong to the engine; it embeds or wraps an Answer.
type Answer struct {
	Kind Kind
	// ObtainedAt is o: when the Bastion sent the request the answer came from.
	ObtainedAt time.Time
	// TokenID and PrincipalID identify the credential the answer is about; empty for an anonymous caller or an
	// invalid credential (an answer about those is matched by grants entries only).
	TokenID     string
	PrincipalID string
	// TokenExpiresAt is the token's expiry; nil when the token does not expire or the answer is not about a
	// valid token.
	TokenExpiresAt *time.Time
	// Repository is the qualified repository name "<projectKey>/<repoKey>" of a Shape or URL answer.
	Repository string
}

// Reason says why an answer is not usable.
type Reason string

// The reasons, one per rule; Usable is the empty reason.
const (
	Usable       Reason = ""
	PeriodOver   Reason = "period"
	TokenExpired Reason = "token_expired"
	Revoked      Reason = "revoked"
	BeforeStart  Reason = "history"
)

// State is what the rules are judged against.
type State struct {
	Now    time.Time
	Period time.Duration
	Grace  time.Duration
	// Unreachable is true while the replica treats the main installation as unreachable (R-BAS-027): only then
	// rule 1 is extended by Grace.
	Unreachable bool
	// HistoryStart is the start of the replica's feed history (R-BAS-026); the zero value means no history yet,
	// and no answer is usable.
	HistoryStart time.Time
	// Log is the feed log of rule 3; nil means no entry was received.
	Log *FeedLog
}

// Check applies the four rules to an answer and returns the first that fails.
func (s State) Check(a Answer) Reason {
	limit := s.Period
	if s.Unreachable {
		limit += s.Grace
	}
	if !s.Now.Before(a.ObtainedAt.Add(limit)) {
		return PeriodOver
	}
	if a.TokenExpiresAt != nil && !s.Now.Before(*a.TokenExpiresAt) {
		return TokenExpired
	}
	if s.Log != nil && s.Log.matches(a) {
		return Revoked
	}
	if s.HistoryStart.IsZero() || a.ObtainedAt.Before(s.HistoryStart.Add(SkewMargin)) {
		return BeforeStart
	}
	return Usable
}

// Usable reports whether the answer may be used.
func (s State) Usable(a Answer) bool { return s.Check(a) == Usable }

// FeedLog is the record of the feed entries a replica received, with their receipt times (R-BAS-026). It makes
// every matching answer unusable by rule 3 whatever the order in which an answer and an entry reach the
// replica, which closes the race of an answer fetched before a revocation and stored after the answer was
// evicted. It is safe for concurrent use.
type FeedLog struct {
	mu         sync.Mutex
	seen       map[string]time.Time
	tokens     map[string]time.Time
	principals map[string]time.Time
	repos      map[string]time.Time
	grants     time.Time
}

// NewFeedLog returns an empty log.
func NewFeedLog() *FeedLog {
	return &FeedLog{seen: map[string]time.Time{}, tokens: map[string]time.Time{}, principals: map[string]time.Time{}, repos: map[string]time.Time{}}
}

// Record applies an entry received at the given time and reports whether it was new: an entry whose ID was
// applied before (the lookback of the feed re-sends entries) changes nothing. A reset is not an entry; see
// [FeedLog.Reset].
func (l *FeedLog) Record(e iface.FeedEntry, received time.Time) (applied bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, dup := l.seen[e.ID]; dup {
		return false
	}
	l.seen[e.ID] = received
	switch e.Kind {
	case iface.FeedToken:
		for _, id := range e.Tokens {
			l.tokens[id] = received
		}
	case iface.FeedPrincipal:
		for _, id := range e.Principals {
			l.principals[id] = received
		}
	case iface.FeedGrants:
		l.grants = received
	case iface.FeedContent:
		for _, r := range e.Repositories {
			l.repos[r] = received
		}
	case iface.FeedSettings:
		// A settings entry makes the replica handshake again; it invalidates no answer by itself.
	}
	return true
}

// Prune forgets what no answer can still depend on: entries received before cutoff, which the caller sets to
// now − (period + grace period + [SkewMargin]) (R-BAS-026), and the IDs applied before idCutoff, which is not
// later than that and not later than now − ([FeedLookback] + [SkewMargin]); see [PruneCutoffs]. An ID that is
// forgotten while the feed still re-sends its entry would apply the entry again and make every answer obtained
// since its first receipt unusable once more.
func (l *FeedLog) Prune(cutoff, idCutoff time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range []map[string]time.Time{l.tokens, l.principals, l.repos} {
		for k, t := range m {
			if t.Before(cutoff) {
				delete(m, k)
			}
		}
	}
	for k, t := range l.seen {
		if t.Before(idCutoff) {
			delete(l.seen, k)
		}
	}
	if l.grants.Before(cutoff) {
		l.grants = time.Time{}
	}
}

// Reset forgets every entry, on a reset answer of the feed: the caller drops its answers and takes the reset
// time as the start of its feed history ([State].HistoryStart, rule 4, R-BAS-026).
func (l *FeedLog) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = map[string]time.Time{}
	l.tokens = map[string]time.Time{}
	l.principals = map[string]time.Time{}
	l.repos = map[string]time.Time{}
	l.grants = time.Time{}
}

// ContentSince reports whether a content entry that names the repository was received after the given time. The
// engine asks it of a cache entry with validatedAt − [SkewMargin]: a withdrawal makes the entry unservable also
// while the main installation is unreachable (R-BAS-022, R-BAS-027).
func (l *FeedLog) ContentSince(repo string, after time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	t, ok := l.repos[repo]
	return ok && t.After(after)
}

// matches reports rule 3: an entry that matches the answer was received after o − SkewMargin.
func (l *FeedLog) matches(a Answer) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	after := a.ObtainedAt.Add(-SkewMargin)
	hit := func(t time.Time, ok bool) bool { return ok && t.After(after) }
	if a.TokenID != "" {
		t, ok := l.tokens[a.TokenID]
		if hit(t, ok) {
			return true
		}
	}
	if a.PrincipalID != "" {
		t, ok := l.principals[a.PrincipalID]
		if hit(t, ok) {
			return true
		}
	}
	if a.Kind == Shape || a.Kind == URL {
		if l.grants.After(after) {
			return true
		}
	}
	if a.Kind == URL && a.Repository != "" {
		t, ok := l.repos[a.Repository]
		if hit(t, ok) {
			return true
		}
	}
	return false
}
