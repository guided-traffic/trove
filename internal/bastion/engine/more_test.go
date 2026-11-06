package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/answers"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
)

func TestR_BAS_011_TheCheckAnswersTheSubjectsTheBastionDoesNotServe(t *testing.T) {
	seven := 7
	for name, c := range map[string]struct {
		subject iface.Subject
		status  int
		code    string
		retry   string
	}{
		"invalid":           {iface.Subject{Kind: iface.SubjectInvalid}, 401, "invalid_token", ""},
		"anonymous refused": {iface.Subject{Kind: iface.SubjectAnonymousRefused}, 401, "credentials_required", ""},
		"throttled":         {iface.Subject{Kind: iface.SubjectThrottled, RetryAfterSeconds: &seven}, 429, "rate_limited", "7"},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSiteHarness(t, options{})
			warm(t, s)
			other := newToken(t, "pat")
			s.main.checkFn = func(iface.CheckRequest) (iface.CheckAnswer, error) { return iface.CheckAnswer{Subject: c.subject}, nil }
			w := s.get(tgz, "Authorization", bearer(other))
			if w.Code != c.status || problemCode(w) != c.code || (c.retry != "" && w.Header().Get("Retry-After") != c.retry) {
				t.Fatalf("%d %s %v", w.Code, w.Body, w.Header())
			}
			if c.subject.Kind == iface.SubjectInvalid {
				if s.guard.failedCount() != 1 {
					t.Fatal("the failed authentication is not counted")
				}
				before := s.main.count()
				s.get(tgz, "Authorization", bearer(other))
				if s.main.count() != before {
					t.Fatal("a kept invalid answer was asked again")
				}
			}
		})
	}
}

func TestR_BAS_011_AnUnknownRepositoryInTheCheckIsNone(t *testing.T) {
	s := newSiteHarness(t, options{})
	warm(t, s)
	other := newToken(t, "pat")
	s.main.checkFn = func(iface.CheckRequest) (iface.CheckAnswer, error) {
		return iface.CheckAnswer{Subject: tokenSubject("x", nil), Repositories: map[string]iface.RepositoryRead{}}, nil
	}
	if w := s.get(tgz, "Authorization", bearer(other)); w.Code != 404 {
		t.Fatalf("%d", w.Code)
	}
}

func TestR_BAS_018_TheCacheKeyOfADocumentIsLearnedFromVaryAndCollapsedWaitersFindIt(t *testing.T) {
	m := &fakeMain{delay: 50 * time.Millisecond}
	s := newSiteHarness(t, options{main: m})
	s.site.docs[doc] = "document"
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := s.get(doc, "Authorization", bearer(s.full), "Accept", "application/json"); w.Code != 200 || w.Body.String() != "document" {
				t.Errorf("%d %q", w.Code, w.Body)
			}
		}()
	}
	wg.Wait()
	forwards := 0
	for _, r := range m.requests() {
		if r.Kind == mainclient.KindForward {
			forwards++
		}
	}
	if forwards != 1 {
		t.Fatalf("%d forwards", forwards)
	}
}

// A request that looked at the cache before the leader of its document stored the entry, and joined the fetch only
// after the leader ended it, is the leader of a flight that nobody waits for: it must find the entry the ended
// flight left and decide again instead of fetching a second time. The window is a few microseconds wide, so the
// test sets it up by hand: the request carries the cache key and the Vary knowledge it had before the entry existed.
func TestR_BAS_018_ALateJoinerOfAnEndedFlightFindsTheLeadersEntryAndDoesNotFetchAgain(t *testing.T) {
	m := &fakeMain{}
	s := newSiteHarness(t, options{main: m})
	s.site.docs[doc] = "document"
	if w := s.get(doc, "Authorization", bearer(s.full), "Accept", "application/json"); w.Code != 200 {
		t.Fatalf("%d", w.Code)
	}
	before := m.count(mainclient.KindForward)
	if before != 1 {
		t.Fatalf("%d forwards by the leader", before)
	}
	s.e.vary.remove(doc) // this replica has not learned the Vary of the target (yet)
	r := httptest.NewRequest(http.MethodGet, doc, nil)
	r.Header.Set("Accept", "application/json")
	q := &rq{e: s.e, r: r, target: doc, baseKey: doc, cacheKey: doc}
	if !s.e.miss(q) {
		t.Fatal("the late joiner did not decide again")
	}
	if got := m.count(mainclient.KindForward); got != before {
		t.Fatalf("%d forwards, want %d", got, before)
	}
}

// A fetch that began before this replica knew the Vary of the target leads under the plain key, and its entry is stored
// after the names are learned. A request that computes the full key in between must wait for that flight and not
// lead a fetch of its own.
func TestR_BAS_018_ARequestWithTheFullKeyWaitsForTheFlightOfThePlainKey(t *testing.T) {
	m := &fakeMain{}
	s := newSiteHarness(t, options{main: m})
	s.site.docs[doc] = "document"
	c, leader := s.e.fetches.join(doc) // the leader of the first fetch, which has not stored its entry yet
	if !leader {
		t.Fatal("not the leader")
	}
	s.e.vary.put(doc, []string{"Accept"}, 0) // learned from its response
	r := httptest.NewRequest(http.MethodGet, doc, nil)
	r.Header.Set("Accept", "application/json")
	q := &rq{e: s.e, r: r, target: doc, baseKey: doc}
	s.e.computeKey(q)
	if q.cacheKey == doc {
		t.Fatal("the key does not carry the Vary")
	}
	done := make(chan bool)
	go func() { done <- s.e.miss(q) }()
	select {
	case <-done:
		t.Fatal("the request did not wait for the flight")
	case <-time.After(100 * time.Millisecond):
	}
	s.e.fetches.finish(doc, c, nil, nil)
	if !<-done {
		t.Fatal("the request did not decide again")
	}
	if n := m.count(mainclient.KindForward); n != 0 {
		t.Fatalf("%d forwards", n)
	}
}

func TestR_BAS_019_TheForwardedFamilyDecidesWhichHeaderCarriesTheHost(t *testing.T) {
	s := newSiteHarness(t, options{cfg: []func(*configT){func(c *configT) {
		c.Server.TrustedProxies = []string{"10.244.0.1"}
		c.Server.ForwardedHeader = "forwarded"
	}}})
	do := func(hdr, val string) int {
		r := httptest.NewRequest(http.MethodGet, "/api/npm/web/npm-all/lodash", nil)
		r.Host = "ingress.internal"
		r.RemoteAddr = "10.244.0.1:1"
		r.Header.Set(hdr, val)
		w := httptest.NewRecorder()
		s.e.ServeHTTP(w, r)
		return w.Code
	}
	if do("Forwarded", `for=203.0.113.7;host="packages.example.com";proto=https`) == http.StatusMisdirectedRequest {
		t.Fatal("the Forwarded host was ignored")
	}
	if do("X-Forwarded-Host", "packages.example.com") != http.StatusMisdirectedRequest {
		t.Fatal("the other family was read")
	}
}

func TestR_BAS_026_TheJanitorDropsWhatNoAnswerCanUseAnyMore(t *testing.T) {
	s := newSiteHarness(t, options{})
	warm(t, s)
	s.feed.Record(iface.FeedEntry{ID: "old", Kind: iface.FeedToken, Tokens: []string{"x"}}, s.clock.now())
	if s.e.answers.mem.len() == 0 {
		t.Fatal("no answers")
	}
	s.e.markVolatile("k")
	s.clock.advance(s.cfg.Bastion.Answers.Period + s.cfg.Bastion.Answers.GracePeriod + time.Minute)
	s.e.janitor()
	if n := s.e.answers.mem.len(); n != 0 {
		t.Fatalf("%d answers after period and grace", n)
	}
	if s.e.isVolatile("k") {
		t.Fatal("the volatile mark outlived its time")
	}
	// the feed entry is forgotten: a new one with the same ID is applied again
	if !s.feed.Record(iface.FeedEntry{ID: "old", Kind: iface.FeedToken, Tokens: []string{"x"}}, s.clock.now()) {
		t.Fatal("the feed log was not pruned")
	}
	s.e.answers.put(context.Background(), answers.Credential, "k", &answerVal{Answer: answerBase(answers.Credential, s.clock.now(), tokenSubject("a", nil), "")})
	s.e.answers.prune(s.clock.now())
	if s.e.answers.mem.len() != 1 {
		t.Fatal("a fresh answer was pruned")
	}
}

func TestR_BAS_020_AnAnonymousCallerSharesItsAnswersAndTheShapeNoneIsKeptForIt(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.readers[""].read = iface.ReadNone
	s.site.artifacts[tgz] = []byte("x")
	if w := s.get(tgz); w.Code != 404 {
		t.Fatalf("%d", w.Code)
	}
	before := s.main.count()
	for i := 0; i < 5; i++ {
		if w := s.get(other); w.Code != 404 {
			t.Fatalf("%d", w.Code)
		}
	}
	if s.main.count() != before {
		t.Fatal("the none answer of the anonymous caller was not used")
	}
}

// TestR_BAS_030_AFillLeavesNoSpoolFileBehind: the fill of a miss is spooled by the cache below its volume and
// removed whether it streams, answers a Range request from the committed bytes, or is aborted.
func TestR_BAS_030_AFillLeavesNoSpoolFileBehind(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.artifacts[tgz] = payload(100 << 10)
	if w := s.get(tgz, "Authorization", bearer(s.full)); w.Code != 200 {
		t.Fatal(w.Code)
	}
	other := "/api/npm/web/npm-all/other-1.0.0.tgz"
	s.site.artifacts[other] = payload(100 << 10)
	if w := s.get(other, "Authorization", bearer(s.full), "Range", "bytes=10-99"); w.Code != http.StatusPartialContent {
		t.Fatal(w.Code)
	}
	s.cache.fault.SetFailPut(true)
	third := "/api/npm/web/npm-all/third-1.0.0.tgz"
	s.site.artifacts[third] = payload(100 << 10)
	if w := s.get(third, "Authorization", bearer(s.full), "Range", "bytes=10-99"); w.Code != http.StatusPartialContent {
		t.Fatalf("a Range request while the backend refuses writes: %d", w.Code)
	}
	// A fill runs on beyond its client's response (fills are decoupled from clients, R-BAS-025): the spool is removed
	// when the fill ends, so the check waits for the engine's background work first.
	s.e.wg.Wait()
	if left := s.cache.files("tmp"); len(left) != 0 {
		t.Fatalf("%d spool files left: %v", len(left), left)
	}
}
