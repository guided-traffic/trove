package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/cache"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/bastion/shared"
)

// ---- R-BAS-023 --------------------------------------------------------------------------------------------

func TestR_BAS_023_ASharedDocumentIsServedToEveryFullReaderWithoutARequestWithinThePeriod(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.docs[doc] = "all packages"
	if w := s.get(doc, "Authorization", bearer(s.full)); w.Code != 200 || w.Body.String() != "all packages" {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
	second := newToken(t, "pat")
	s.site.addReader(second, &reader{id: "full2", read: iface.ReadFull})
	before := s.main.count()
	if w := s.get(doc, "Authorization", bearer(second)); w.Code != 200 || w.Body.String() != "all packages" {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
	reqs := s.main.requests()[before:]
	if len(reqs) != 1 || reqs[0].Kind != mainclient.KindCheck {
		t.Fatalf("a second full reader costs one check and no document request: %+v", reqs)
	}
	before = s.main.count()
	for i := 0; i < 10; i++ {
		s.get(doc, "Authorization", bearer(second))
		s.get(doc, "Authorization", bearer(s.full))
	}
	if s.main.count() != before {
		t.Fatalf("%d requests within metadata_revalidate_after", s.main.count()-before)
	}
	if counterValue(t, s.harness, "requests", metrics.ClassShared, metrics.ResultHit) < 20 {
		t.Fatal("the hits are not counted")
	}
	ent, ok := s.e.loadEntry(context.Background(), doc+"")
	if ok && ent.Kind == cache.EntryDocument {
		return
	}
	// the key carries the Accept the first request learned from Vary
	if len(s.cache.entryRecords()) == 0 {
		t.Fatal("no document entry")
	}
}

func TestR_BAS_023_ASharedDocumentIsAskedAboutAgainAfterThePeriodAndANewVersionAppears(t *testing.T) {
	s := newSiteHarness(t, options{cfg: []func(*configT){func(c *configT) { c.Bastion.Answers.Period = time.Hour }}})
	s.site.docs[doc] = "version 1"
	s.get(doc, "Authorization", bearer(s.full))
	before := s.main.count()
	s.clock.advance(s.cfg.Bastion.Cache.MetadataRevalidateAfter + time.Second)
	// unchanged: a conditional request, answered 304, and the entry is renewed
	if w := s.get(doc, "Authorization", bearer(s.full)); w.Code != 200 || w.Body.String() != "version 1" {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
	reqs := s.main.requests()[before:]
	if len(reqs) != 1 || reqs[0].Kind != mainclient.KindConditional || reqs[0].INM == "" {
		t.Fatalf("%+v", reqs)
	}
	s.get(doc, "Authorization", bearer(s.full))
	if s.main.count() != before+1 {
		t.Fatal("the 304 did not renew the entry")
	}
	// a new version: the next revalidation replaces the entry (H-384)
	s.site.mu.Lock()
	s.site.docs[doc] = "version 2"
	s.site.mu.Unlock()
	s.site.bump()
	s.clock.advance(s.cfg.Bastion.Cache.MetadataRevalidateAfter + time.Second)
	if w := s.get(doc, "Authorization", bearer(s.full)); w.Code != 200 || w.Body.String() != "version 2" {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
	if w := s.get(doc, "Authorization", bearer(s.full)); w.Body.String() != "version 2" {
		t.Fatalf("%q", w.Body)
	}
}

func TestR_BAS_023_APartialReaderNeverReceivesTheSharedDocumentAndTheSharedEntryStaysUntouched(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.docs[doc] = "all packages"
	s.get(doc, "Authorization", bearer(s.full))
	var key string
	var rec []byte
	for k, v := range s.cache.entryRecords() {
		key, rec = k, v
	}
	before := s.main.count()
	for i := 0; i < 5; i++ {
		w := s.get(doc, "Authorization", bearer(s.part))
		if w.Code != 200 || w.Body.String() != "filtered:all packages" {
			t.Fatalf("round %d: %d %q", i, w.Code, w.Body)
		}
	}
	reqs := s.main.requests()[before:]
	var cond int
	for _, r := range reqs {
		if r.Kind == mainclient.KindConditional {
			cond++
			if r.Header.Get("Authorization") != bearer(s.part) {
				t.Fatal("the conditional request did not carry the caller's credential")
			}
		}
	}
	if cond != 5 {
		t.Fatalf("%d conditional requests for 5 partial reads: %+v", cond, reqs)
	}
	if after := s.cache.entryRecords(); string(after[key]) != string(rec) {
		t.Fatal("the shared entry was touched")
	}
	if len(s.cache.entryRecords()) != 1 || s.cache.objectCount() != 1 {
		t.Fatal("a private copy reached the backend")
	}
	// from the second request on the validator is the caller's own copy, and a 304 serves it
	last := reqs[len(reqs)-1]
	if !strings.HasPrefix(last.INM, `"p`) {
		t.Fatalf("the validator of the last conditional request is %q, the caller's private copy's", last.INM)
	}
	if counterValue(t, s.harness, "requests", metrics.ClassPrivate, metrics.ResultChecked) == 0 {
		t.Fatal("a private copy was never served after a 304")
	}
}

// TestR_BAS_023_A304ForAPrivateDocumentAnswersTheCallerWithTheSharedEntrysBytes: the Bastion's conditional request
// names the shared entry's validator; for a partial reader whose document is the same bytes the main installation
// answers 304, marked private. The caller sent no condition and gets the bytes as a 200, the entry stays untouched.
func TestR_BAS_023_A304ForAPrivateDocumentAnswersTheCallerWithTheSharedEntrysBytes(t *testing.T) {
	s := newSiteHarness(t, options{})
	same := newToken(t, "pat")
	s.site.addReader(same, &reader{id: "same", read: iface.ReadPartial, sameDoc: true})
	s.site.docs[doc] = "all packages"
	s.get(doc, "Authorization", bearer(s.full))
	rec := s.cache.entryRecords()
	for i := 0; i < 2; i++ {
		w := s.get(doc, "Authorization", bearer(same))
		if w.Code != 200 || w.Body.String() != "all packages" {
			t.Fatalf("round %d: %d %q", i, w.Code, w.Body)
		}
	}
	for k, v := range s.cache.entryRecords() {
		if string(rec[k]) != string(v) {
			t.Fatal("the shared entry was renewed on behalf of a partial reader")
		}
	}
}

func TestR_BAS_023_SearchAndEveryOtherPassAnswerGoToTheMainInstallationPerCallerAndAreNeverKept(t *testing.T) {
	s := newSiteHarness(t, options{})
	for i := 0; i < 2; i++ {
		if w := s.get("/api/npm/npm-all/-/v1/search?text=acme", "Authorization", bearer(s.full)); w.Code != 200 || !strings.Contains(w.Body.String(), "objects") {
			t.Fatalf("%d", w.Code)
		}
	}
	n := 0
	for _, r := range s.main.requests() {
		if r.Kind == mainclient.KindForward && strings.Contains(r.Target, "search") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d search requests, want 2", n)
	}
	if s.cache.entryCount() != 0 || s.cache.objectCount() != 0 {
		t.Fatal("a search result was kept")
	}
}

func TestR_BAS_023_APrivateCopyIsBoundedInMemoryAndAnOversizedOneIsNotKept(t *testing.T) {
	s := newSiteHarness(t, options{cfg: []func(*configT){func(c *configT) { c.Bastion.Cache.PrivateDocumentsMaxBytes = 4096 }}})
	for i := 0; i < 20; i++ {
		s.site.docs[fmt.Sprintf("%s-%d", doc, i)] = strings.Repeat("x", 1000)
		s.get(fmt.Sprintf("%s-%d", doc, i), "Authorization", bearer(s.part))
	}
	if c := s.e.priv.cost; c > 4096 {
		t.Fatalf("%d bytes of private copies, at most 4096", c)
	}
	s.site.docs[doc+"-huge"] = strings.Repeat("y", 100000)
	before := s.main.count()
	for i := 0; i < 2; i++ {
		if w := s.get(doc+"-huge", "Authorization", bearer(s.part)); w.Code != 200 || w.Body.Len() != 100000+len("filtered:") {
			t.Fatalf("%d %d", w.Code, w.Body.Len())
		}
	}
	if s.main.count()-before != 2 { // two plain fetches: nothing was kept to validate
		t.Fatalf("%d requests", s.main.count()-before)
	}
}

func TestR_BAS_023_ASharedDocumentIsServedPastItsPeriodWhileTheMainInstallationIsUnreachable(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.docs[doc] = "all packages"
	s.site.docs[doc+"-p"] = "other"
	s.get(doc, "Authorization", bearer(s.full))
	s.get(doc+"-p", "Authorization", bearer(s.part))
	s.setUnreachable(true)
	s.clock.advance(7 * time.Minute) // past the answers' period and the revalidation period, inside the grace period
	before := s.main.count()
	if w := s.get(doc, "Authorization", bearer(s.full)); w.Code != 200 || w.Body.String() != "all packages" {
		t.Fatalf("%d", w.Code)
	}
	if w := s.get(doc+"-p", "Authorization", bearer(s.part)); w.Code != 200 || w.Body.String() != "filtered:other" {
		t.Fatalf("a private copy: %d %q", w.Code, w.Body)
	}
	if s.main.count() != before {
		t.Fatal("a request went to the main installation")
	}
}

// ---- R-BAS-024 --------------------------------------------------------------------------------------------

func anonRequest(s *siteHarness, path, remote string) int {
	r := httptest.NewRequest(http.MethodGet, "https://packages.example.com"+path, nil)
	r.RemoteAddr = remote
	w := httptest.NewRecorder()
	s.e.ServeHTTP(w, r)
	return w.Code
}

func TestR_BAS_024_AMissingNameIsAbsorbedAtTheBastionForFullReaders(t *testing.T) {
	s := newSiteHarness(t, options{})
	names := 20
	for i := 0; i < 1000; i++ {
		path := fmt.Sprintf("/api/npm/npm-all/missing-%d", i%names)
		if code := anonRequest(s, path, fmt.Sprintf("198.51.100.%d:1", i%100)); code != http.StatusNotFound {
			t.Fatalf("%d", code)
		}
	}
	forwards, checks := 0, 0
	for _, r := range s.main.requests() {
		switch r.Kind {
		case mainclient.KindForward:
			forwards++
		case mainclient.KindCheck:
			checks++
		}
	}
	if forwards > names || checks > 1 {
		t.Fatalf("%d forwards and %d checks for %d names", forwards, checks, names)
	}
	if counterValue(t, s.harness, "requests", metrics.ClassAbsent, metrics.ResultHit) < 900 {
		t.Fatal("the replays are not counted")
	}
	// the body of the replay is the main installation's
	r := httptest.NewRequest(http.MethodGet, "https://packages.example.com/api/npm/npm-all/missing-1", nil)
	r.RemoteAddr = "192.0.2.1:1"
	w := httptest.NewRecorder()
	s.e.ServeHTTP(w, r)
	if w.Code != 404 || !strings.Contains(w.Body.String(), "not_found") || w.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("%d %s %v", w.Code, w.Body, w.Header())
	}
}

func TestR_BAS_024_APartialReaderAsksTheMainInstallationEveryTime(t *testing.T) {
	s := newSiteHarness(t, options{})
	for i := 0; i < 2; i++ {
		if w := s.get("/api/npm/npm-all/missing", "Authorization", bearer(s.part)); w.Code != 404 {
			t.Fatalf("%d", w.Code)
		}
	}
	n := 0
	for _, r := range s.main.requests() {
		if r.Kind == mainclient.KindForward {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d requests for a missing name asked by a partial reader, want 2", n)
	}
	// and the partial reader's miss never fills the negative cache
	if w := s.get("/api/npm/npm-all/missing", "Authorization", bearer(s.full)); w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestR_BAS_024_ANegativeEntryLastsTheNegativePeriodAndIsBounded(t *testing.T) {
	s := newSiteHarness(t, options{cfg: []func(*configT){func(c *configT) { c.Bastion.Answers.Period = time.Hour }}})
	get := func() { s.get("/api/npm/npm-all/missing", "Authorization", bearer(s.full)) }
	get()
	get()
	if s.main.count() != 1 {
		t.Fatalf("%d requests", s.main.count())
	}
	s.clock.advance(s.cfg.Bastion.Cache.NegativePeriod + time.Second)
	get()
	if s.main.count() != 2 {
		t.Fatalf("%d requests after the negative period", s.main.count())
	}
	s.e.neg.maxItems = 3
	for i := 0; i < 10; i++ {
		s.get(fmt.Sprintf("/api/npm/npm-all/m%d", i), "Authorization", bearer(s.full))
	}
	if n := s.e.neg.len(); n > 3 {
		t.Fatalf("%d negative entries, at most 3", n)
	}
}

func TestR_BAS_024_ACallerWithoutAnAnswerGetsOneCheckBeforeANegativeEntryIsReplayed(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.get("/api/npm/npm-all/missing", "Authorization", bearer(s.full))
	second := newToken(t, "pat")
	s.site.addReader(second, &reader{id: "full2", read: iface.ReadFull})
	before := s.main.count()
	for i := 0; i < 3; i++ {
		if w := s.get("/api/npm/npm-all/missing", "Authorization", bearer(second)); w.Code != 404 {
			t.Fatal(w.Code)
		}
	}
	reqs := s.main.requests()[before:]
	if len(reqs) != 1 || reqs[0].Kind != mainclient.KindCheck {
		t.Fatalf("%+v", reqs)
	}
	// a partial reader's check says partial: its request goes to the main installation
	if w := s.get("/api/npm/npm-all/missing", "Authorization", bearer(s.part)); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if last := s.main.requests(); last[len(last)-1].Kind != mainclient.KindForward {
		t.Fatal("a partial reader was answered from the negative cache")
	}
}

func TestR_BAS_024_NegativeEntriesAreSharedThroughTheSharedStore(t *testing.T) {
	st := shared.NewLocal(1, nil)
	st.Shared = func() bool { return true }
	a := newSiteHarness(t, options{signer: true, state: st})
	a.get("/api/npm/npm-all/missing", "Authorization", bearer(a.full))
	b := newHarness(t, options{signer: true, state: st, cache: a.cache, main: a.main, clock: a.clock})
	// replica B has the answers of replica A and the negative entry: no request
	before := a.main.count()
	if w := b.get("/api/npm/npm-all/missing", "Authorization", bearer(a.full)); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if a.main.count() != before {
		t.Fatalf("%d requests", a.main.count()-before)
	}
	// The record is plain JSON in the store; the Valkey form signs it and verifies it on the way back
	// (internal/bastion/shared), so a forged or a moved record never reaches the engine.
	rec, ok, _ := st.Answers.Get(context.Background(), shared.AnswerNegative, hashKey("/api/npm/npm-all/missing"))
	if !ok || !json.Valid(rec) {
		t.Fatalf("no readable shared record: %v %q", ok, rec)
	}
}

// ---- R-BAS-025 --------------------------------------------------------------------------------------------

func TestR_BAS_025_MissesNeverPutMoreThanTheBudgetOntoTheMainInstallation(t *testing.T) {
	m := &fakeMain{delay: 5 * time.Millisecond}
	s := newSiteHarness(t, options{main: m, cfg: []func(*configT){func(c *configT) {
		c.Bastion.Main.MaxConcurrent, c.Bastion.Replicas, c.Bastion.Main.QueueTimeout = 8, 2, 20*time.Millisecond
	}}})
	m.budget = mainclient.NewBudget(mainclient.BudgetOptions{
		MaxConcurrent: s.cfg.Bastion.Main.MaxConcurrent, Replicas: s.cfg.Bastion.Replicas, AnonymousShare: s.cfg.Bastion.Main.AnonymousShare,
		QueueTimeout: s.cfg.Bastion.Main.QueueTimeout, Metrics: s.m,
	})
	for i := 0; i < 500; i++ {
		s.site.artifacts[fmt.Sprintf("/api/npm/npm-all/p%d.tgz", i)] = []byte("x")
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	for i := 0; i < 500; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path := fmt.Sprintf("/api/npm/npm-all/p%d.tgz", i)
			var code int
			if i%2 == 0 {
				code = s.get(path, "Authorization", bearer(s.full)).Code
			} else {
				code = anonRequest(s, path, "198.51.100.1:1")
			}
			mu.Lock()
			codes[code]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if p := m.peak.Load(); p > 4 {
		t.Fatalf("%d requests in flight at the main installation, the replica's share of the budget is 4", p)
	}
	if p := m.peakAnon.Load(); p > 1 {
		t.Fatalf("%d anonymous requests in flight, at most 1", p)
	}
	if codes[503] == 0 || codes[200] == 0 {
		t.Fatalf("%v: some requests must queue out with 503 and some must be served", codes)
	}
	if readCounter(s.m.BudgetExhausted.WithLabelValues("authenticated"))+readCounter(s.m.BudgetExhausted.WithLabelValues("anonymous")) == 0 {
		t.Fatal("the exhausted budget is not counted")
	}
	if readCounter(s.m.MainInFlight.WithLabelValues("authenticated"))+readCounter(s.m.MainInFlight.WithLabelValues("anonymous")) != 0 {
		t.Fatal("slots were not released")
	}
	if readCounter(s.m.MainBudget.WithLabelValues("authenticated")) != 4 || readCounter(s.m.MainBudget.WithLabelValues("anonymous")) != 1 {
		t.Fatal("the budget gauges are wrong")
	}
}

func TestR_BAS_025_ConcurrentColdRequestsForOneTarballCauseOneRequestPerReplica(t *testing.T) {
	m := &fakeMain{delay: 50 * time.Millisecond}
	s := newSiteHarness(t, options{main: m})
	body := payload(100000)
	s.site.artifacts[tgz] = body
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := s.get(tgz, "Authorization", bearer(s.full)); w.Code != 200 || w.Body.Len() != len(body) {
				t.Errorf("%d %d", w.Code, w.Body.Len())
			}
		}()
	}
	wg.Wait()
	if n := m.count(); n != 1 {
		t.Fatalf("%d requests to the main installation, want 1", n)
	}
}

func TestR_BAS_025_AWaiterIsServedOnlyWhenItsOwnAnswerAllowsIt(t *testing.T) {
	m := &fakeMain{delay: 50 * time.Millisecond}
	s := newSiteHarness(t, options{main: m})
	s.site.artifacts[other] = []byte("not for acme")
	var wg sync.WaitGroup
	results := make([]*httptest.ResponseRecorder, 2)
	for i, tok := range []string{s.full, s.part} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = s.get(other, "Authorization", bearer(tok))
		}()
		time.Sleep(10 * time.Millisecond)
	}
	wg.Wait()
	if results[0].Code != 200 || results[0].Body.String() != "not for acme" {
		t.Fatalf("full: %d", results[0].Code)
	}
	if results[1].Code != 404 {
		t.Fatalf("a partial waiter was served the leader's bytes: %d %q", results[1].Code, results[1].Body)
	}
	hasOwn := false
	for _, r := range m.requests() {
		if r.Header.Get("Authorization") == bearer(s.part) {
			hasOwn = true
		}
	}
	if !hasOwn {
		t.Fatal("the partial waiter made no request of its own")
	}
}

func TestR_BAS_025_APrivateResultIsNotCollapsedAcrossCallers(t *testing.T) {
	m := &fakeMain{delay: 20 * time.Millisecond}
	s := newSiteHarness(t, options{main: m})
	s.site.docs[doc] = "all"
	parts := []string{s.part}
	for i := 0; i < 4; i++ {
		tok := newToken(t, "pat")
		s.site.addReader(tok, &reader{id: fmt.Sprintf("part%d", i), read: iface.ReadPartial})
		parts = append(parts, tok)
	}
	// the first round learns that the document is private
	var wg sync.WaitGroup
	for _, tok := range parts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := s.get(doc, "Authorization", bearer(tok)); w.Code != 200 || w.Body.String() != "filtered:all" {
				t.Errorf("%d %q", w.Code, w.Body)
			}
		}()
	}
	wg.Wait()
	if !s.e.isVolatile(doc) {
		t.Fatal("the key is not marked as not shareable")
	}
	before := m.count()
	start := time.Now()
	for _, tok := range parts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.get(doc, "Authorization", bearer(tok))
		}()
	}
	wg.Wait()
	if m.count()-before != len(parts) {
		t.Fatalf("%d requests for %d callers", m.count()-before, len(parts))
	}
	if time.Since(start) > 15*time.Second {
		t.Fatal("the callers queued behind each other")
	}
}

func TestR_BAS_025_ConcurrentColdRequestsAcrossReplicasCauseOneRequestWithTheBucketBackend(t *testing.T) {
	st := shared.NewLocal(1, nil)
	st.Shared = func() bool { return true }
	bucket := func(c *configT) { c.Bastion.Cache.Backend = "bucket" }
	m := &fakeMain{delay: 150 * time.Millisecond}
	a := newSiteHarness(t, options{signer: true, state: st, main: m, cfg: []func(*configT){bucket}})
	b := newHarness(t, options{signer: true, state: st, cache: a.cache, main: m, clock: a.clock, cfg: []func(*configT){bucket}})
	body := payload(50000)
	a.site.artifacts[tgz] = body
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		h := a.harness
		if i%2 == 1 {
			h = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := h.get(tgz, "Authorization", bearer(a.full)); w.Code != 200 || w.Body.Len() != len(body) {
				t.Errorf("%d %d", w.Code, w.Body.Len())
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
		t.Fatalf("%d forwards across two replicas, want 1", forwards)
	}
}
