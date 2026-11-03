package engine

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/answers"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/bastion/shared"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func warm(t *testing.T, s *siteHarness) {
	t.Helper()
	s.site.artifacts[tgz] = payload(1000)
	if w := s.get(tgz, "Authorization", bearer(s.full)); w.Code != http.StatusOK {
		t.Fatalf("warm: %d", w.Code)
	}
}

// TestR_BAS_020_AnAnswerObtainedBeforeARevocationIsNeverUsedAfterIt: an answer whose request left before a token
// entry arrived is not used, also when it reaches the replica (or Valkey) after the entry was applied.
func TestR_BAS_020_AnAnswerObtainedBeforeARevocationIsNeverUsedAfterIt(t *testing.T) {
	t.Run("an answer in memory", func(t *testing.T) {
		s := newSiteHarness(t, options{})
		warm(t, s)
		before := s.main.count()
		s.get(tgz, "Authorization", bearer(s.full))
		if s.main.count() != before {
			t.Fatal("the answer was not used before the revocation")
		}
		s.clock.advance(time.Second)
		s.feed.Record(iface.FeedEntry{ID: "r1", Kind: iface.FeedToken, Tokens: []string{"full"}}, s.clock.now())
		// the main installation now calls the token invalid
		s.site.mu.Lock()
		delete(s.site.readers, s.full)
		s.site.mu.Unlock()
		w := s.get(tgz, "Authorization", bearer(s.full))
		if w.Code != http.StatusUnauthorized || problemCode(w) != "invalid_token" {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		if s.main.count() == before {
			t.Fatal("the revoked token's answer was used")
		}
		if counterValue(t, s.harness, "answers", "credential", "dropped") == 0 && counterValue(t, s.harness, "answers", "shape", "dropped") == 0 {
			t.Fatal("the dropped answer is not counted")
		}
	})
	t.Run("an answer that reaches the replica after the entry", func(t *testing.T) {
		s := newSiteHarness(t, options{})
		s.clock.advance(time.Minute)
		s.feed.Record(iface.FeedEntry{ID: "r1", Kind: iface.FeedToken, Tokens: []string{"tok-1"}}, s.clock.now())
		sub := tokenSubject("tok-1", nil)
		// obtained a second before the entry was received, stored after it
		late := &answerVal{Answer: answerBase(answers.Credential, s.clock.now().Add(-time.Second), sub, ""), Subject: sub}
		s.e.answers.put(context.Background(), answers.Credential, "late", late)
		if s.e.answers.credential(context.Background(), "late") != nil {
			t.Fatal("an answer obtained before the revocation was used after it")
		}
		// an answer whose request left more than 15 s after the entry was received is fine
		fresh := &answerVal{Answer: answerBase(answers.Credential, s.clock.now().Add(16*time.Second), sub, ""), Subject: sub}
		s.clock.advance(20 * time.Second)
		s.e.answers.put(context.Background(), answers.Credential, "fresh", fresh)
		if s.e.answers.credential(context.Background(), "fresh") == nil {
			t.Fatal("a later answer was refused")
		}
	})
	t.Run("an answer that reaches Valkey after the entry", func(t *testing.T) {
		st := shared.NewLocal(1, nil)
		st.Shared = func() bool { return true }
		a := newSiteHarness(t, options{signer: true, state: st})
		warm(t, a)
		b := newHarness(t, options{signer: true, state: st, cache: a.cache, main: a.main, clock: a.clock})
		// replica B received the revocation after A obtained the answers
		a.clock.advance(time.Second)
		b.feed.Record(iface.FeedEntry{ID: "r1", Kind: iface.FeedToken, Tokens: []string{"full"}}, a.clock.now())
		before := a.main.count()
		b.get(tgz, "Authorization", bearer(a.full))
		if a.main.count() == before {
			t.Fatal("replica B used the revoked token's answer from Valkey")
		}
	})
}

func TestR_BAS_020_AnAnswerIsUsableForThePeriodAndThenAskedAgain(t *testing.T) {
	s := newSiteHarness(t, options{})
	warm(t, s)
	before := s.main.count()
	s.clock.advance(s.cfg.Bastion.Answers.Period - time.Second)
	s.cfg.Bastion.Cache.ArtifactRevalidateAfter = time.Hour
	s.get(tgz, "Authorization", bearer(s.full))
	if s.main.count() != before {
		t.Fatal("the answer was dropped inside its period")
	}
	// A read in the last fifth of the period renews the answer in the background (R-BAS-020); let that end, so
	// that the test does not depend on how fast the goroutine runs, and then let a whole period pass again.
	s.e.wg.Wait()
	before = s.main.count()
	s.clock.advance(s.cfg.Bastion.Answers.Period + time.Second)
	if w := s.get(tgz, "Authorization", bearer(s.full)); w.Code != 200 {
		t.Fatal(w.Code)
	}
	reqs := s.main.requests()[before:]
	if len(reqs) == 0 {
		t.Fatal("the answer outlived its period")
	}
	if counterValue(t, s.harness, "answers", "shape", "expired") == 0 {
		t.Fatal("the expiry is not counted")
	}
}

func TestR_BAS_020_ATokenIsRefusedFromItsExpiryOnAlsoWhileTheMainInstallationIsUnreachable(t *testing.T) {
	exp := epoch.Add(30 * time.Second)
	s := newSiteHarness(t, options{})
	s.site.readers[s.full].expires = &exp
	warm(t, s)
	s.clock.advance(20 * time.Second)
	before := s.main.count()
	if w := s.get(tgz, "Authorization", bearer(s.full)); w.Code != 200 || s.main.count() != before {
		t.Fatalf("before the expiry: %d", w.Code)
	}
	s.clock.advance(11 * time.Second)
	s.setUnreachable(true)
	w := s.get(tgz, "Authorization", bearer(s.full))
	if w.Code == 200 || w.Body.Len() > 0 && w.Body.Len() == 1000 {
		t.Fatalf("an expired token read while the main installation was unreachable: %d", w.Code)
	}
	if w.Code != 503 {
		t.Fatalf("%d", w.Code)
	}
	// reachable again: the main installation is asked and says what it says
	s.setUnreachable(false)
	s.site.readers[s.full].expires = nil
	if w := s.get(tgz, "Authorization", bearer(s.full)); w.Code != 200 {
		t.Fatalf("%d", w.Code)
	}
}

func TestR_BAS_020_ARefusedAnswerIsKeptLikeAnAllowedOne(t *testing.T) {
	s := newSiteHarness(t, options{})
	warm(t, s)
	for i := 0; i < 5; i++ {
		w := s.get(tgz, "Authorization", bearer(s.bad))
		if w.Code != http.StatusUnauthorized || problemCode(w) != "invalid_token" {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	if n := s.main.count(); n != 2 {
		t.Fatalf("%d requests: an invalid token costs the main installation one request per period", n)
	}
	if n := s.guard.failedCount(); n != 1 {
		t.Fatalf("%d failed authentications counted, want the one the main installation reported", n)
	}
	s.clock.advance(s.cfg.Bastion.Answers.Period + time.Second)
	s.get(tgz, "Authorization", bearer(s.bad))
	if s.main.count() != 3 {
		t.Fatalf("%d requests after the period", s.main.count())
	}
}

func TestR_BAS_020_TheAnswersAreBoundedAndCounted(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.e.answers.mem.maxItems = 5
	for i := 0; i < 20; i++ {
		tok := newToken(t, "pat")
		s.site.addReader(tok, &reader{id: "t" + itoa(i), read: iface.ReadFull})
		s.site.artifacts[tgz] = payload(100)
		if w := s.get(tgz, "Authorization", bearer(tok)); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	if n := s.e.answers.mem.len(); n > 5 {
		t.Fatalf("%d answers held, at most 5", n)
	}
	total := 0.0
	for _, k := range []string{"credential", "shape", "url"} {
		total += readCounter(s.m.Answers.WithLabelValues(k))
	}
	if total != float64(s.e.answers.mem.len()) {
		t.Fatalf("the gauge says %v, memory holds %d", total, s.e.answers.mem.len())
	}
}

// TestR_BAS_020_AnswersAreSharedThroughTheSharedStoreAndAnUnreadableRecordIsIgnored: a second replica uses the
// answers the first one stored; a record the store hands back that the engine cannot read is ignored, counted and
// deleted. The signature of a record (a forged or a moved one) is the store's: the Valkey form signs on Put and
// verifies on Get, and its tests are in internal/bastion/shared; the engine hands it plain records.
func TestR_BAS_020_AnswersAreSharedThroughTheSharedStoreAndAnUnreadableRecordIsIgnored(t *testing.T) {
	st := shared.NewLocal(1, nil)
	st.Shared = func() bool { return true }
	a := newSiteHarness(t, options{signer: true, state: st})
	warm(t, a)
	b := newHarness(t, options{signer: true, state: st, cache: a.cache, main: a.main, clock: a.clock})
	before := a.main.count()
	w := b.get(tgz, "Authorization", bearer(a.full))
	if w.Code != 200 || a.main.count() != before {
		t.Fatalf("replica B did not use the answers of replica A: %d, %d new requests", w.Code, a.main.count()-before)
	}
	key := memKey(answers.Shape, shapeKey("garbled", "npm-all"))
	_ = st.Answers.Put(context.Background(), shared.AnswerShape, hashKey(key), []byte(`not json`), time.Hour)
	if b.e.answers.shape(context.Background(), "garbled", "npm-all") != nil {
		t.Fatal("an unreadable record was accepted")
	}
	if counterValue(t, b, "answers", "shape", "bad_signature") != 1 {
		t.Fatal("the unreadable record is not counted")
	}
	if _, ok, _ := st.Answers.Get(context.Background(), shared.AnswerShape, hashKey(key)); ok {
		t.Fatal("the unreadable record was not deleted")
	}
}

// TestR_BAS_026_AFeedEntryDeletesTheAnswersItMatchesAndAResetDeletesAll: the engine is the link's AnswerSink.
func TestR_BAS_026_AFeedEntryDeletesTheAnswersItMatchesAndAResetDeletesAll(t *testing.T) {
	s := newSiteHarness(t, options{})
	warm(t, s)
	if s.e.answers.mem.len() == 0 {
		t.Fatal("warm left no answers")
	}
	other := newToken(t, "pat")
	s.site.addReader(other, &reader{id: "other", read: iface.ReadFull})
	s.get(tgz, "Authorization", bearer(other))
	held := s.e.answers.mem.len()

	s.main.mu.Lock()
	s.main.feedAns = iface.FeedAnswer{Entries: []iface.FeedEntry{{ID: "e1", At: s.clock.now(), Kind: iface.FeedToken, Tokens: []string{"full"}}}, Cursor: iface.Cursor{At: epoch, ID: "e1"}.String()}
	s.main.mu.Unlock()
	s.clock.advance(time.Second)
	s.tick()
	if got := s.e.answers.mem.len(); got >= held || got == 0 {
		t.Fatalf("answers held %d -> %d: only those of the revoked token go", held, got)
	}
	s.main.mu.Lock()
	s.main.feedAns = iface.FeedAnswer{Reset: true, Cursor: iface.Cursor{At: epoch}.String()}
	s.main.mu.Unlock()
	s.tick()
	if got := s.e.answers.mem.len(); got != 0 {
		t.Fatalf("a reset left %d answers", got)
	}
}

func TestR_BAS_020_ANoHistoryMeansNoAnswerIsUsed(t *testing.T) {
	// A replica whose link never read the feed has no history: it is unreachable, and the answers the shared
	// cache holds are not used (rule 4) - the request is a 503, not a hit.
	a := newSiteHarness(t, options{})
	warm(t, a)
	b := newHarness(t, options{cache: a.cache, main: a.main, clock: a.clock, noFeed: true, state: shared.NewLocal(1, a.clock.now)})
	if !b.link.State().HistoryStart.IsZero() {
		t.Fatal("a link without a successful feed read has a history start")
	}
	before := a.main.count()
	if w := b.get(tgz, "Authorization", bearer(a.full)); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("an answer was used before the feed history started: %d", w.Code)
	}
	if a.main.count() != before {
		t.Fatal("a request went to the main installation while the link has no history")
	}
	// rule 4: an answer obtained before the history start plus 15 s is not used. A reset starts the history now.
	s := newSiteHarness(t, options{})
	warm(t, s)
	s.clock.advance(time.Second)
	s.main.mu.Lock()
	s.main.feedAns = iface.FeedAnswer{Reset: true, Cursor: iface.Cursor{At: s.clock.now()}.String()}
	s.main.mu.Unlock()
	s.tick()
	before = s.main.count()
	s.get(tgz, "Authorization", bearer(s.full))
	if s.main.count() == before {
		t.Fatal("an answer older than the feed history was used")
	}
}

func TestR_BAS_020_AnAnswerInTheLastFifthOfItsPeriodIsRefreshedInTheBackgroundWithOneCollapsedCheck(t *testing.T) {
	s := newSiteHarness(t, options{cfg: []func(*configT){func(c *configT) { c.Bastion.Cache.ArtifactRevalidateAfter = time.Hour }}})
	warm(t, s)
	s.clock.advance(s.cfg.Bastion.Answers.Period / 5 * 4)
	s.clock.advance(time.Second)
	before := s.main.count()
	s.main.delay = 100 * time.Millisecond
	for i := 0; i < 20; i++ {
		if w := s.get(tgz, "Authorization", bearer(s.full)); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	waitFor(t, "the refresh", func() bool { return s.main.count() > before })
	s.e.wg.Wait()
	reqs := s.main.requests()[before:]
	if len(reqs) != 1 || reqs[0].Kind != "check" {
		t.Fatalf("%+v", reqs)
	}
	// the answer is renewed: its time is the time of the refresh
	sa := s.e.answers.shape(context.Background(), credentialKey(s.full), "npm-all")
	if sa == nil || !sa.ObtainedAt.Equal(s.clock.now()) {
		t.Fatalf("the shape answer was not renewed: %+v", sa)
	}
	// inside the first four fifths nothing is refreshed
	s2 := newSiteHarness(t, options{})
	warm(t, s2)
	b2 := s2.main.count()
	s2.clock.advance(time.Minute)
	s2.get(tgz, "Authorization", bearer(s2.full))
	s2.e.wg.Wait()
	if s2.main.count() != b2 {
		t.Fatal("a young answer was refreshed")
	}
}

func TestR_BAS_020_TheCounterAndTheGaugeOfTheAnswersFollowTheLookups(t *testing.T) {
	s := newSiteHarness(t, options{})
	warm(t, s)
	s.get(tgz, "Authorization", bearer(s.full))
	if counterValue(t, s.harness, "answers", "credential", metrics.AnswerHit) == 0 || counterValue(t, s.harness, "answers", "shape", metrics.AnswerMiss) == 0 {
		t.Fatal("hits and misses are not counted")
	}
	if readCounter(s.m.Answers.WithLabelValues("credential")) != 1 || readCounter(s.m.Answers.WithLabelValues("shape")) != 1 {
		t.Fatal("the gauges do not hold the answers")
	}
}
