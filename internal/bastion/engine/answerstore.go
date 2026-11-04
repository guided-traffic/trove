package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/answers"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/bastion/shared"
)

// refusal is a response of the main installation that is kept and replayed: the stored refusal of a URL answer
// (R-BAS-020), a probe answer (R-BAS-014) or a negative entry (R-BAS-024). The headers are the filtered replay
// headers of the response, never a Trove-Bastion-* header.
type refusal struct {
	Status int         `json:"status"`
	Header http.Header `json:"header,omitempty"`
	Body   []byte      `json:"body,omitempty"`
}

// maxRefusalBody bounds the body of a refusal that is kept; a larger one is not kept and is passed through.
const maxRefusalBody = 64 << 10

// answerVal is an answer of the main installation: the part the validity rules read ([answers.Answer]) and the
// payload of its kind (R-BAS-020).
type answerVal struct {
	answers.Answer
	// Subject is the payload of a credential answer.
	Subject iface.Subject `json:"subject,omitempty"`
	// Read and Status are the payload of a shape answer; Status is set with the shape none.
	Read   iface.Read `json:"read,omitempty"`
	Status int        `json:"status,omitempty"`
	// Allowed and Refusal are the payload of a URL answer: the main installation allowed the read, or this is
	// the refusal it gave.
	Allowed bool     `json:"allowed,omitempty"`
	Refusal *refusal `json:"refusal,omitempty"`
}

// answerStore keeps the answers: in the replica's memory, least recently used first (R-BAS-020), and with
// Valkey also as signed records in the shared answer store (R-BAS-034).
type answerStore struct {
	e      *engine
	mem    *lruCache[*answerVal]
	counts map[answers.Kind]*atomic.Int64
}

func newAnswerStore(e *engine) *answerStore {
	s := &answerStore{e: e, counts: map[answers.Kind]*atomic.Int64{
		answers.Credential: {}, answers.Shape: {}, answers.URL: {},
	}}
	s.mem = newLRU(e.cfg.Answers.MaxEntries, 0, func(_ string, v *answerVal) { s.count(v.Kind, -1) })
	return s
}

func (s *answerStore) count(k answers.Kind, d int64) {
	n := s.counts[k].Add(d)
	s.e.d.Metrics.Answers.WithLabelValues(string(k)).Set(float64(n))
}

func memKey(kind answers.Kind, key string) string { return string(kind) + "|" + key }

func sharedKind(k answers.Kind) shared.AnswerKind {
	switch k {
	case answers.Credential:
		return shared.AnswerCredential
	case answers.Shape:
		return shared.AnswerShape
	}
	return shared.AnswerURL
}

func hashKey(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// sharedEnabled reports whether answers go to the shared store: Valkey is configured and available. The
// store signs and verifies its records itself (R-BAS-032, R-BAS-034), so the engine never sees a record that
// failed its signature.
func (e *engine) sharedEnabled() bool {
	return e.d.Shared != nil && e.d.Shared.Answers != nil && e.d.Shared.Shared != nil && e.d.Shared.Shared()
}

// state is what the validity rules of R-BAS-020 are judged against, now: the link's view of reachability, the
// history start and the feed log.
func (e *engine) state() answers.State { return e.d.Link.AnswerState(e.now()) }

// get returns the usable answer of a kind and key, or nil. An answer that fails a rule is dropped.
func (s *answerStore) get(ctx context.Context, kind answers.Kind, key string) *answerVal {
	mk := memKey(kind, key)
	v, ok := s.mem.get(mk)
	if !ok {
		v, ok = s.fromShared(ctx, kind, mk)
	}
	m := s.e.d.Metrics.AnswersTotal
	if !ok {
		m.WithLabelValues(string(kind), metrics.AnswerMiss).Inc()
		return nil
	}
	switch s.e.state().Check(v.Answer) {
	case answers.Usable:
		m.WithLabelValues(string(kind), metrics.AnswerHit).Inc()
		return v
	case answers.Revoked:
		m.WithLabelValues(string(kind), metrics.AnswerDropped).Inc()
	default:
		m.WithLabelValues(string(kind), metrics.AnswerExpired).Inc()
	}
	s.mem.remove(mk)
	return nil
}

// fromShared reads a signed record from the shared store and puts the answer into memory.
func (s *answerStore) fromShared(ctx context.Context, kind answers.Kind, mk string) (*answerVal, bool) {
	if !s.e.sharedEnabled() {
		return nil, false
	}
	rec, ok, err := s.e.d.Shared.Answers.Get(ctx, sharedKind(kind), hashKey(mk))
	if err != nil || !ok {
		return nil, false
	}
	var v answerVal
	if json.Unmarshal(rec, &v) != nil || v.Kind != kind {
		s.e.d.Metrics.AnswersTotal.WithLabelValues(string(kind), metrics.AnswerBadSignature).Inc()
		_ = s.e.d.Shared.Answers.Delete(ctx, sharedKind(kind), hashKey(mk))
		return nil, false
	}
	if s.mem.put(mk, &v, 0) {
		s.count(kind, 1)
	}
	return &v, true
}

// put keeps an answer, in memory and, with Valkey, as a signed record. The answer is judged by its own times
// at every use, so the order in which an answer and a feed entry arrive does not matter (R-BAS-020).
func (s *answerStore) put(ctx context.Context, kind answers.Kind, key string, v *answerVal) {
	v.Kind = kind
	mk := memKey(kind, key)
	if s.mem.put(mk, v, 0) {
		s.count(kind, 1)
	}
	if !s.e.sharedEnabled() {
		return
	}
	payload, err := json.Marshal(v)
	if err != nil {
		return
	}
	ttl := s.e.cfg.Answers.Period + s.e.cfg.Answers.GracePeriod
	_ = s.e.d.Shared.Answers.Put(ctx, sharedKind(kind), hashKey(mk), payload, ttl)
}

func (s *answerStore) credential(ctx context.Context, credKey string) *answerVal {
	return s.get(ctx, answers.Credential, credKey)
}

func shapeKey(credKey, repo string) string { return credKey + "|" + repo }

func (s *answerStore) shape(ctx context.Context, credKey, repo string) *answerVal {
	return s.get(ctx, answers.Shape, shapeKey(credKey, repo))
}

func urlKey(credKey, cacheKey string) string { return credKey + "|" + cacheKey }

func (s *answerStore) url(ctx context.Context, credKey, cacheKey string) *answerVal {
	return s.get(ctx, answers.URL, urlKey(credKey, cacheKey))
}

// identity returns the facts of a subject that tie an answer to the feed's entries and to the token's expiry.
func identity(sub iface.Subject) (tokenID, principalID string, exp *time.Time) {
	if sub.Kind != iface.SubjectToken {
		return "", "", nil
	}
	return sub.TokenID, sub.PrincipalID, sub.ExpiresAt
}

// answerBase returns the part of an answer the validity rules read, for a credential that turned out to be sub.
func answerBase(kind answers.Kind, o time.Time, sub iface.Subject, repo string) answers.Answer {
	id, pid, exp := identity(sub)
	return answers.Answer{Kind: kind, ObtainedAt: o, TokenID: id, PrincipalID: pid, TokenExpiresAt: exp, Repository: repo}
}

// Drop implements [link.AnswerSink]: the entries of one feed page delete the answers they match from the
// replica's memory and, best effort, from the shared store (R-BAS-026), in one pass over the answers however
// many entries the page holds (Q-BAS-126). The validity rules hold whether or not anything is deleted here, so a
// failure to delete costs memory only.
func (s *answerStore) Drop(entries []iface.FeedEntry) {
	m := newEntryMatcher(entries)
	if m.empty() {
		return
	}
	s.dropWhere(m.matches)
}

// entryMatcher is what a set of feed entries makes unusable (R-BAS-015, rule 3 of R-BAS-020): the token or
// principal of an answer is named; a grants entry stales every shape and URL answer; a content entry stales the
// URL answers of the repositories it names. A settings entry invalidates no answer.
type entryMatcher struct {
	tokens, principals, repos map[string]struct{}
	grants                    bool
}

func newEntryMatcher(entries []iface.FeedEntry) entryMatcher {
	m := entryMatcher{tokens: map[string]struct{}{}, principals: map[string]struct{}{}, repos: map[string]struct{}{}}
	for _, e := range entries {
		switch e.Kind {
		case iface.FeedToken:
			for _, id := range e.Tokens {
				m.tokens[id] = struct{}{}
			}
		case iface.FeedPrincipal:
			for _, id := range e.Principals {
				m.principals[id] = struct{}{}
			}
		case iface.FeedGrants:
			m.grants = true
		case iface.FeedContent:
			for _, r := range e.Repositories {
				m.repos[r] = struct{}{}
			}
		}
	}
	return m
}

func (m entryMatcher) empty() bool {
	return !m.grants && len(m.tokens) == 0 && len(m.principals) == 0 && len(m.repos) == 0
}

func (m entryMatcher) matches(a answers.Answer) bool {
	if _, ok := m.tokens[a.TokenID]; ok && a.TokenID != "" {
		return true
	}
	if _, ok := m.principals[a.PrincipalID]; ok && a.PrincipalID != "" {
		return true
	}
	if m.grants && (a.Kind == answers.Shape || a.Kind == answers.URL) {
		return true
	}
	if a.Kind == answers.URL {
		if _, ok := m.repos[a.Repository]; ok {
			return true
		}
	}
	return false
}

// dropWhere deletes the answers and private copies the predicate selects from memory, and their records from
// the shared store in the background (best effort, bounded by a timeout).
func (s *answerStore) dropWhere(drop func(answers.Answer) bool) {
	type rec struct {
		kind shared.AnswerKind
		key  string
	}
	var gone []rec
	shared := s.e.sharedEnabled()
	s.mem.retain(func(mk string, v *answerVal) bool {
		if !drop(v.Answer) {
			return true
		}
		if shared { // a SHA-256 and a hex string per deleted answer: only for a record that exists
			gone = append(gone, rec{sharedKind(v.Kind), hashKey(mk)})
		}
		return false
	})
	s.e.priv.retain(func(_ string, d *privDoc) bool { return !drop(d.Answer) })
	if len(gone) == 0 {
		return
	}
	s.e.background(func() {
		ctx, cancel := context.WithTimeout(context.Background(), sharedDropTimeout)
		defer cancel()
		for _, g := range gone {
			_ = s.e.d.Shared.Answers.Delete(ctx, g.kind, g.key)
		}
	})
}

// sharedDropTimeout bounds the deletion of shared records after a feed entry.
const sharedDropTimeout = 5 * time.Second

// DropAll implements [link.AnswerSink]: a reset of the feed deletes every answer.
func (s *answerStore) DropAll() { s.dropWhere(func(answers.Answer) bool { return true }) }

// prune drops what no answer can use any more: answers past the period and the grace period.
func (s *answerStore) prune(now time.Time) {
	limit := s.e.cfg.Answers.Period + s.e.cfg.Answers.GracePeriod
	s.mem.retain(func(_ string, v *answerVal) bool { return now.Before(v.ObtainedAt.Add(limit)) })
}

// negEntry is a 404 marked absent, kept per cache key (R-BAS-024).
type negEntry struct {
	Repository string    `json:"repository"`
	StoredAt   time.Time `json:"storedAt"`
	Refusal    refusal   `json:"refusal"`
}

func (e *engine) negGet(ctx context.Context, cacheKey string) *negEntry {
	if e.cfg.Cache.NegativeMaxEntries == 0 {
		return nil
	}
	n, ok := e.neg.get(cacheKey)
	if !ok && e.sharedEnabled() {
		n, ok = e.negFromShared(ctx, cacheKey)
	}
	if !ok {
		return nil
	}
	if !e.now().Before(n.StoredAt.Add(e.cfg.Cache.NegativePeriod)) {
		e.neg.remove(cacheKey)
		return nil
	}
	return n
}

func (e *engine) negFromShared(ctx context.Context, cacheKey string) (*negEntry, bool) {
	h := hashKey(cacheKey)
	rec, ok, err := e.d.Shared.Answers.Get(ctx, shared.AnswerNegative, h)
	if err != nil || !ok {
		return nil, false
	}
	var n negEntry
	if json.Unmarshal(rec, &n) != nil {
		_ = e.d.Shared.Answers.Delete(ctx, shared.AnswerNegative, h)
		return nil, false
	}
	e.neg.put(cacheKey, &n, 0)
	return &n, true
}

func (e *engine) negPut(ctx context.Context, cacheKey string, n *negEntry) {
	if e.cfg.Cache.NegativeMaxEntries == 0 {
		return
	}
	e.neg.put(cacheKey, n, 0)
	if !e.sharedEnabled() {
		return
	}
	payload, err := json.Marshal(n)
	if err != nil {
		return
	}
	_ = e.d.Shared.Answers.Put(ctx, shared.AnswerNegative, hashKey(cacheKey), payload, e.cfg.Cache.NegativePeriod)
}

// privDoc is the last copy of a document filtered for one caller (R-BAS-023): memory only, never written to
// the cache backend or to the shared store. Answer carries the identity and the time of the last validation,
// so the rules of R-BAS-020 decide whether the copy may be served while the main installation is unreachable.
type privDoc struct {
	answers.Answer
	Status int
	Header http.Header
	Body   []byte
	ETag   string
}

func privKey(credKey, cacheKey string) string { return credKey + "|" + cacheKey }

// privGet returns the caller's private copy of a document; it is served without validation only while the main
// installation is unreachable, and only while its answer rules hold.
func (e *engine) privGet(credKey, cacheKey string) *privDoc {
	d, ok := e.priv.get(privKey(credKey, cacheKey))
	if !ok {
		return nil
	}
	return d
}

func (e *engine) privPut(credKey, cacheKey string, d *privDoc) {
	if e.cfg.Cache.PrivateDocumentsMaxBytes == 0 {
		return
	}
	e.priv.put(privKey(credKey, cacheKey), d, int64(len(d.Body)+len(d.ETag)+256))
}

func (e *engine) privUsable(d *privDoc) bool {
	return e.state().Usable(d.Answer)
}
