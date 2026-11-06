package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/cache"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
)

const (
	tgz   = "/api/npm/web/npm-all/@acme/sdk/-/sdk-1.0.0.tgz"
	other = "/api/npm/web/npm-all/lodash/-/lodash-4.0.0.tgz"
	doc   = "/api/npm/web/npm-all/lodash"
)

func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%23)
	}
	return b
}

// ---- R-BAS-021 --------------------------------------------------------------------------------------------

func TestR_BAS_021_AMissIsForwardedWithTheCallersCredentialAndTheListedHeaders(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.artifacts[tgz] = []byte("tarball")
	w := s.get(tgz+"?x=1", "Authorization", bearer(s.full), "Cookie", "a=b", "Range", "bytes=0-1", "If-None-Match", `"x"`, "If-Modified-Since", "Mon, 02 Jan 2006 15:04:05 GMT",
		"Forwarded", "for=192.0.2.1", "X-Forwarded-For", "192.0.2.1", "X-Forwarded-Host", "evil.example", "Trove-Bastion-Class", "artifact", "Trove-Anything", "1",
		"Connection", "keep-alive, X-Hop", "X-Hop", "1", "X-NuGet-ApiKey", "secret", "Accept", "application/octet-stream", "User-Agent", "npm/10", "Te", "trailers")
	if w.Code != http.StatusPartialContent || w.Body.String() != "ta" {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
	reqs := s.main.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	r := reqs[0]
	if r.Kind != mainclient.KindForward || r.Method != http.MethodGet || r.Target != tgz+"?x=1" || r.Addr != "203.0.113.7" || r.INM != "" {
		t.Fatalf("%+v", r)
	}
	if r.Header.Get("Authorization") != bearer(s.full) || r.Header.Get("Accept") != "application/octet-stream" || r.Header.Get("User-Agent") != "npm/10" {
		t.Fatalf("end-to-end headers missing: %v", r.Header)
	}
	for _, name := range []string{"Cookie", "Range", "If-None-Match", "If-Modified-Since", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "Trove-Bastion-Class", "Trove-Anything", "X-Hop", "Connection", "X-Nuget-Apikey", "Te"} {
		if r.Header.Get(name) != "" {
			t.Errorf("the request header %s was forwarded: %q", name, r.Header.Get(name))
		}
	}
	if r.Class != mainclient.Authenticated {
		t.Fatalf("class %q", r.Class)
	}
}

func TestR_BAS_021_TheCallerReceivesTheMainInstallationsAnswerWithoutTheInternalHeaders(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.artifacts[tgz] = []byte("tarball")
	s.site.docs[doc] = "document"
	for _, p := range []string{tgz, doc} {
		w := s.get(p, "Authorization", bearer(s.full))
		for k := range w.Header() {
			if strings.HasPrefix(k, "Trove-Bastion-") || k == "X-Trove-Replica" || k == "Cache-Status" || k == "Set-Cookie" || k == "Connection" {
				t.Errorf("%s: the response header %s reached the caller", p, k)
			}
		}
		if w.Header().Get("Etag") == "" || w.Header().Get("Content-Type") == "" {
			t.Errorf("%s: %v", p, w.Header())
		}
	}
}

func TestR_BAS_021_AnArtifactIsSpooledKeptAndServedWithoutARequestTheSecondTime(t *testing.T) {
	s := newSiteHarness(t, options{})
	body := payload(300 << 10)
	s.site.artifacts[tgz] = body
	for i := 0; i < 2; i++ {
		w := s.get(tgz, "Authorization", bearer(s.full))
		if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), body) {
			t.Fatalf("round %d: %d, %d bytes", i, w.Code, w.Body.Len())
		}
	}
	if s.main.count() != 1 {
		t.Fatalf("%d requests, want 1", s.main.count())
	}
	if s.cache.objectCount() != 1 || s.cache.entryCount() != 1 {
		t.Fatalf("objects %d entries %d", s.cache.objectCount(), s.cache.entryCount())
	}
	if got, _ := s.cache.object(sha(body)); !bytes.Equal(got, body) {
		t.Fatal("the object is not stored under its content address")
	}
	ent, ok := s.e.loadEntry(context.Background(), tgz)
	if !ok || ent.Kind != cache.EntryArtifact || ent.SHA256 != sha(body) || ent.Size != int64(len(body)) || ent.Repository != "web/npm-all" || ent.Header.Get("Etag") != `"`+sha(body)+`"` {
		t.Fatalf("%+v", ent)
	}
	for k := range ent.Header {
		if strings.HasPrefix(k, "Trove-Bastion-") || k == "Set-Cookie" || k == "X-Trove-Replica" {
			t.Errorf("the entry keeps %s", k)
		}
	}
	if counterValue(t, s.harness, "requests", metrics.ClassArtifact, metrics.ResultMiss) != 1 || counterValue(t, s.harness, "requests", metrics.ClassArtifact, metrics.ResultHit) != 1 {
		t.Fatal("miss and hit are not counted")
	}
	if counterValue(t, s.harness, "bytes", metrics.SourceMain) != float64(len(body)) || counterValue(t, s.harness, "bytes", metrics.SourceCache) != float64(len(body)) {
		t.Fatalf("bytes main %v cache %v", counterValue(t, s.harness, "bytes", metrics.SourceMain), counterValue(t, s.harness, "bytes", metrics.SourceCache))
	}
}

// serve starts the engine behind a real server, so an aborted transfer is seen by a real client.
func serve(t *testing.T, s *harness) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "packages.example.com"
		s.e.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestR_BAS_021_ABodyThatDoesNotMatchItsContentAddressIsAbortedBeforeItsLast64KiBAndNothingIsKept(t *testing.T) {
	s := newSiteHarness(t, options{})
	body := payload(400 << 10)
	s.site.artifacts[tgz] = body
	s.site.corrupt[tgz] = true
	srv := serve(t, s.harness)
	for round := 1; round <= 2; round++ {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+tgz, nil)
		req.Header.Set("Authorization", bearer(s.full))
		// The abort may come before the status line reached the client (the fill verified the bytes first) or
		// during the body: either way the transfer never completes.
		var got []byte
		resp, rerr := srv.Client().Do(req)
		if rerr == nil {
			got, rerr = io.ReadAll(resp.Body)
			resp.Body.Close()
		}
		if rerr == nil {
			t.Fatalf("round %d: the corrupt transfer ended as a complete one (%d bytes)", round, len(got))
		}
		if len(got) > len(body)-withheldTail {
			t.Fatalf("round %d: %d bytes were released, at most %d may be", round, len(got), len(body)-withheldTail)
		}
		if s.cache.objectCount() != 0 || s.cache.entryCount() != 0 {
			t.Fatalf("round %d: something was kept", round)
		}
		if s.main.count() != round {
			t.Fatalf("round %d: %d requests, want a fetch every time", round, s.main.count())
		}
	}
	// the same bytes, uncorrupted, are kept
	s.site.corrupt[tgz] = false
	if w := s.get(tgz, "Authorization", bearer(s.full)); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatalf("%d", w.Code)
	}
	if s.cache.objectCount() != 1 {
		t.Fatal("the good copy is not kept")
	}
}

func TestR_BAS_021_ASmallCorruptArtifactIsNotReleasedAtAll(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.artifacts[tgz] = payload(1000)
	s.site.corrupt[tgz] = true
	srv := serve(t, s.harness)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+tgz, nil)
	req.Header.Set("Authorization", bearer(s.full))
	resp, err := srv.Client().Do(req)
	if err != nil {
		return // the connection was cut before the headers: nothing was released
	}
	got, rerr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if rerr == nil || len(got) != 0 {
		t.Fatalf("%d bytes, %v", len(got), rerr)
	}
}

func TestR_BAS_021_ADocumentIsKeptOnceWholeAndAboveTheLimitPassedThrough(t *testing.T) {
	s := newSiteHarness(t, options{cfg: []func(*configT){func(c *configT) { c.Bastion.Cache.DocumentsMaxBytes = 1024 }}})
	s.site.docs[doc] = "small document"
	big := strings.Repeat("x", 5000)
	s.site.docs[doc+"-big"] = big
	for i := 0; i < 2; i++ {
		if w := s.get(doc, "Authorization", bearer(s.full)); w.Code != http.StatusOK || w.Body.String() != "small document" {
			t.Fatalf("%d %q", w.Code, w.Body)
		}
	}
	// the document's entry and the same entry at the plain key, which tells another replica the Vary (saveEntry)
	if s.main.count() != 1 || s.cache.entryCount() != 2 {
		t.Fatalf("requests %d entries %d", s.main.count(), s.cache.entryCount())
	}
	for i := 0; i < 2; i++ {
		if w := s.get(doc+"-big", "Authorization", bearer(s.full)); w.Code != http.StatusOK || w.Body.String() != big {
			t.Fatalf("%d %d bytes", w.Code, w.Body.Len())
		}
	}
	if s.cache.entryCount() != 2 {
		t.Fatal("a document above the limit was kept")
	}
	if s.main.count() != 3 {
		t.Fatalf("%d requests: the big document is passed through every time", s.main.count())
	}
}

func TestR_BAS_021_AnArtifactAboveTheLimitOrWithoutALengthIsPassedThrough(t *testing.T) {
	s := newSiteHarness(t, options{cfg: []func(*configT){func(c *configT) { c.Bastion.Cache.MaxObjectBytes = 2048 }}})
	big := payload(5000)
	s.site.artifacts[tgz] = big
	for i := 0; i < 2; i++ {
		if w := s.get(tgz, "Authorization", bearer(s.full)); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), big) {
			t.Fatalf("%d", w.Code)
		}
	}
	if s.cache.objectCount() != 0 || s.main.count() != 2 {
		t.Fatalf("objects %d requests %d", s.cache.objectCount(), s.main.count())
	}
	// without Content-Length
	small := payload(100)
	inner := s.main.doFn
	s.main.doFn = func(f mainclient.Forward) (*mainclient.Response, error) {
		r, err := inner(f)
		if err == nil && r.Status == http.StatusOK && strings.HasSuffix(f.Request.URL.Path, "nolen.tgz") {
			r.Header.Del("Content-Length")
		}
		return r, err
	}
	s.site.artifacts["/api/npm/web/npm-all/nolen.tgz"] = small
	for i := 0; i < 2; i++ {
		if w := s.get("/api/npm/web/npm-all/nolen.tgz", "Authorization", bearer(s.full)); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), small) {
			t.Fatalf("%d", w.Code)
		}
	}
	if s.cache.objectCount() != 0 {
		t.Fatal("an artifact without Content-Length was kept")
	}
}

func TestR_BAS_021_AHeadMissIsForwardedAsHeadAndKeepsAnswersOnly(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.artifacts[tgz] = []byte("tarball")
	w := s.do(http.MethodHead, tgz, nil, "Authorization", bearer(s.full))
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("%d %d", w.Code, w.Body.Len())
	}
	if r := s.main.requests()[0]; r.Method != http.MethodHead {
		t.Fatalf("forwarded as %s", r.Method)
	}
	if s.cache.objectCount() != 0 || s.cache.entryCount() != 0 {
		t.Fatal("a HEAD kept content")
	}
	if s.e.answers.shape(context.Background(), credentialKey(s.full), "web/npm-all") == nil || s.e.answers.credential(context.Background(), credentialKey(s.full)) == nil {
		t.Fatal("a HEAD kept no answers")
	}
	// the answers allow the GET that follows to be a miss for the bytes only: one request
	if w := s.get(tgz, "Authorization", bearer(s.full)); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	if s.main.count() != 2 {
		t.Fatalf("%d requests", s.main.count())
	}
}

func TestR_BAS_021_ErrorsOfTheMainInstallationAnswer502And504ButAMarkedAnswerIsPassedOn(t *testing.T) {
	cases := []struct {
		name   string
		do     func(mainclient.Forward) (*mainclient.Response, error)
		status int
		code   string
	}{
		{"an unmarked 5xx", func(mainclient.Forward) (*mainclient.Response, error) {
			return respUnmarked(503, http.Header{"Retry-After": {"5"}}, "ingress"), nil
		}, 502, "main_failed"},
		{"an unmarked 502", func(mainclient.Forward) (*mainclient.Response, error) {
			return respUnmarked(502, nil, "bad gateway"), nil
		}, 502, "main_failed"},
		{"a connect error", func(mainclient.Forward) (*mainclient.Response, error) {
			return nil, fmt.Errorf("%w: dial", mainclient.ErrUnreachable)
		}, 502, "main_failed"},
		{"a timeout", func(mainclient.Forward) (*mainclient.Response, error) {
			return nil, fmt.Errorf("%w: %w", mainclient.ErrUnreachable, context.DeadlineExceeded)
		}, 504, "main_timeout"},
		{"a net timeout", func(mainclient.Forward) (*mainclient.Response, error) {
			return nil, fmt.Errorf("%w: %w", mainclient.ErrUnreachable, &net.DNSError{IsTimeout: true})
		}, 504, "main_timeout"},
		{"a marked 500", func(mainclient.Forward) (*mainclient.Response, error) {
			return resp(500, marking(iface.ClassPass, "", "", iface.Subject{Kind: iface.SubjectAnonymous}), nil, `{"code":"internal"}`), nil
		}, 500, "internal"},
		{"an unmarked 404", func(mainclient.Forward) (*mainclient.Response, error) { return respUnmarked(404, nil, "nope"), nil }, 404, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSiteHarness(t, options{})
			s.main.doFn = c.do
			w := s.get(tgz, "Authorization", bearer(s.full))
			if w.Code != c.status || (c.code != "" && problemCode(w) != c.code) {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			if c.status >= 502 && w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%v", w.Header())
			}
			if s.cache.entryCount() != 0 || s.e.answers.mem.len() != 0 {
				t.Fatal("an error left an answer or an entry")
			}
		})
	}
}

func TestR_BAS_021_ABusyBudgetAnswers503WithRetryAfter1(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.main.doFn = func(mainclient.Forward) (*mainclient.Response, error) { return nil, mainclient.ErrBusy }
	w := s.get(tgz, "Authorization", bearer(s.full))
	if w.Code != 503 || problemCode(w) != "bastion_busy" || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("%d %s %v", w.Code, w.Body, w.Header())
	}
	if counterValue(t, s.harness, "refused", metrics.RefusedBusy) != 1 {
		t.Fatal("not counted")
	}
}

func TestR_BAS_021_APrivateDocumentIsNeverWrittenToTheBackend(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.docs[doc] = "all packages"
	for i := 0; i < 3; i++ {
		w := s.get(doc, "Authorization", bearer(s.part))
		if w.Code != http.StatusOK || w.Body.String() != "filtered:all packages" {
			t.Fatalf("%d %q", w.Code, w.Body)
		}
	}
	if s.cache.objectCount() != 0 || s.cache.entryCount() != 0 {
		t.Fatal("a private document reached the cache backend")
	}
}

// ---- R-BAS-022 --------------------------------------------------------------------------------------------

func fillTarballs(t *testing.T, s *siteHarness, n int, token string) []string {
	t.Helper()
	var paths []string
	for i := 0; i < n; i++ {
		p := fmt.Sprintf("/api/npm/web/npm-all/@acme/sdk%d/-/sdk%d-1.0.0.tgz", i, i)
		s.site.artifacts[p] = payload(500 + i)
		paths = append(paths, p)
		if w := s.get(p, "Authorization", bearer(token)); w.Code != http.StatusOK {
			t.Fatalf("%s: %d", p, w.Code)
		}
	}
	return paths
}

func TestR_BAS_022_AWarmCacheServesAFullReaderWithoutARequestAndReportsTheDownloads(t *testing.T) {
	s := newSiteHarness(t, options{})
	paths := fillTarballs(t, s, 20, s.full)
	before := s.main.count()
	for _, p := range paths {
		if w := s.get(p, "Authorization", bearer(s.full), "User-Agent", "npm/10"); w.Code != http.StatusOK {
			t.Fatalf("%s: %d", p, w.Code)
		}
	}
	if s.main.count() != before {
		t.Fatalf("%d requests to the main installation on a warm run", s.main.count()-before)
	}
	if got := counterValue(t, s.harness, "requests", metrics.ClassArtifact, metrics.ResultHit); got != 20 {
		t.Fatalf("hits %v", got)
	}
	ev := s.dl.list()
	if len(ev) != 20 {
		t.Fatalf("%d download events", len(ev))
	}
	e := ev[0]
	if e.TokenID != "full" || e.PrincipalID != "p-full" || e.ClientAddress != "203.0.113.7" || e.Repository != "web/npm-all" || e.Path != paths[0] ||
		e.SHA256 != sha(s.site.artifacts[paths[0]]) || e.UserAgent != "npm/10" || e.Replica != "bastion-0" || !e.At.Equal(epoch) {
		t.Fatalf("%+v", e)
	}
	// a HEAD and a 304 are no download
	s.do(http.MethodHead, paths[0], nil, "Authorization", bearer(s.full))
	s.get(paths[0], "Authorization", bearer(s.full), "If-None-Match", `"`+sha(s.site.artifacts[paths[0]])+`"`)
	if len(s.dl.list()) != 20 {
		t.Fatal("a HEAD or a 304 was reported as a download")
	}
}

func TestR_BAS_022_ACallerWithoutAnAnswerCostsOneCollapsedCheckAndNoBytes(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.artifacts[tgz] = payload(2000)
	if w := s.get(tgz, "Authorization", bearer(s.full)); w.Code != 200 {
		t.Fatal(w.Code)
	}
	other := newToken(t, "pat")
	s.site.addReader(other, &reader{id: "full2", read: iface.ReadFull})
	s.main.delay = 50 * time.Millisecond
	before := s.main.count()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := s.get(tgz, "Authorization", bearer(other)); w.Code != 200 || w.Body.Len() != 2000 {
				t.Errorf("%d", w.Code)
			}
		}()
	}
	wg.Wait()
	reqs := s.main.requests()[before:]
	if len(reqs) != 1 || reqs[0].Kind != mainclient.KindCheck {
		t.Fatalf("%+v", reqs)
	}
	if reqs[0].Check.Credential == nil || *reqs[0].Check.Credential != other || len(reqs[0].Check.Repositories) != 1 || reqs[0].Check.Repositories[0] != "web/npm-all" || reqs[0].Check.ClientAddress != "203.0.113.7" {
		t.Fatalf("%+v", reqs[0].Check)
	}
	if counterValue(t, s.harness, "requests", metrics.ClassArtifact, metrics.ResultHit) != 10 {
		t.Log("hits:", counterValue(t, s.harness, "requests", metrics.ClassArtifact, metrics.ResultHit), "checked:", counterValue(t, s.harness, "requests", metrics.ClassArtifact, metrics.ResultChecked))
	}
}

// A caller that read its answers before the check of an earlier leader stored its own reaches the collapse after that
// check left it: it uses what the check stored and sends none.
func TestR_BAS_022_ACallerReachingTheCheckAfterAnEarlierCheckEndedSendsNoSecond(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.artifacts[tgz] = payload(2000)
	tok := newToken(t, "pat")
	s.site.addReader(tok, &reader{id: "full2", read: iface.ReadFull})
	if w := s.get(tgz, "Authorization", bearer(tok)); w.Code != 200 {
		t.Fatal(w.Code)
	}
	before := s.main.count()
	q := &rq{e: s.e, r: httptest.NewRequest(http.MethodGet, tgz, nil), cred: credential{token: tok, key: credentialKey(tok)}, repo: "web/npm-all", addr: "203.0.113.7", clientText: "203.0.113.7"}
	shape, done := s.e.checkShape(q)
	if done || shape == nil || shape.Read != iface.ReadFull {
		t.Fatalf("the stored answers did not answer the late caller: done=%v shape=%+v", done, shape)
	}
	if q.subject.Kind != iface.SubjectToken {
		t.Fatalf("the late caller has the subject %+v", q.subject)
	}
	if got := s.main.count(); got != before {
		t.Fatalf("the late caller sent %d more requests", got-before)
	}
}

func TestR_BAS_022_APartialReaderCostsOneConditionalRequestPerTarballAnsweredByA304(t *testing.T) {
	s := newSiteHarness(t, options{})
	paths := fillTarballs(t, s, 5, s.full)
	s.site.artifacts[other] = payload(300)
	if w := s.get(other, "Authorization", bearer(s.full)); w.Code != 200 {
		t.Fatal(w.Code)
	}
	before := s.main.count()
	for _, p := range paths {
		w := s.get(p, "Authorization", bearer(s.part))
		if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), s.site.artifacts[p]) {
			t.Fatalf("%s: %d", p, w.Code)
		}
	}
	reqs := s.main.requests()[before:]
	var checks, cond int
	for _, r := range reqs {
		switch r.Kind {
		case mainclient.KindCheck:
			checks++
		case mainclient.KindConditional:
			cond++
			if r.INM == "" || r.Header.Get("Authorization") != bearer(s.part) || r.Header.Get("If-None-Match") != "" {
				t.Fatalf("%+v", r)
			}
		}
	}
	if checks != 1 || cond != len(paths) {
		t.Fatalf("checks %d conditional %d, want 1 and %d", checks, cond, len(paths))
	}
	// the tarball outside @acme/*: the main installation's 404 is replayed and not kept, as the 404 of a plain
	// fetch is not kept (Q-BAS-124)
	before = s.main.count()
	for i := 0; i < 3; i++ {
		if w := s.get(other, "Authorization", bearer(s.part)); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "not_found") {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	if n := s.main.count() - before; n != 3 {
		t.Fatalf("%d requests for three reads of a name the reader may not see, want one each (Q-BAS-124)", n)
	}
	// the entry stays for the others
	if w := s.get(other, "Authorization", bearer(s.full)); w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
	// within the period the partial reader's own answers serve the next round without a request
	before = s.main.count()
	for _, p := range paths {
		if w := s.get(p, "Authorization", bearer(s.part)); w.Code != http.StatusOK {
			t.Fatalf("%d", w.Code)
		}
	}
	if s.main.count() != before {
		t.Fatalf("the URL answers were not used: %d requests", s.main.count()-before)
	}
}

func TestR_BAS_022_ANoneAnswerRefusesWithoutARequest(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.artifacts[tgz] = []byte("x")
	if w := s.get(tgz, "Authorization", bearer(s.full)); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := s.get(tgz, "Authorization", bearer(s.none)); w.Code != http.StatusNotFound {
		t.Fatalf("%d", w.Code)
	}
	before := s.main.count()
	for i := 0; i < 5; i++ {
		w := s.get(tgz, "Authorization", bearer(s.none))
		if w.Code != http.StatusNotFound || problemCode(w) != "not_found" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	if w := s.get(other, "Authorization", bearer(s.none)); w.Code != http.StatusNotFound {
		t.Fatalf("another path of the repository: %d", w.Code)
	}
	if s.main.count() != before {
		t.Fatalf("%d requests for a none reader", s.main.count()-before)
	}
}

func TestR_BAS_022_AfterTheRevalidationPeriodTheNextRequestSendsOneConditionalRequest(t *testing.T) {
	s := newSiteHarness(t, options{cfg: []func(*configT){func(c *configT) { c.Bastion.Answers.Period = time.Hour }}})
	s.site.artifacts[tgz] = payload(1000)
	s.get(tgz, "Authorization", bearer(s.full))
	s.clock.advance(s.cfg.Bastion.Cache.ArtifactRevalidateAfter - time.Second)
	before := s.main.count()
	s.get(tgz, "Authorization", bearer(s.full))
	if s.main.count() != before {
		t.Fatal("a request before the period")
	}
	// the answers have a period of their own (5 m); keep them usable by moving only a little past the revalidation
	s.cfg.Bastion.Answers.Period = time.Hour
	s.clock.advance(2 * time.Second)
	w := s.get(tgz, "Authorization", bearer(s.full))
	reqs := s.main.requests()[before:]
	if w.Code != 200 || len(reqs) != 1 || reqs[0].Kind != mainclient.KindConditional || reqs[0].INM != `"`+sha(s.site.artifacts[tgz])+`"` {
		t.Fatalf("%d %+v", w.Code, reqs)
	}
	// the 304 renewed the entry: the next request is a hit again
	s.get(tgz, "Authorization", bearer(s.full))
	if s.main.count() != before+1 {
		t.Fatal("the entry was not renewed by the 304")
	}
	if counterValue(t, s.harness, "requests", metrics.ClassArtifact, metrics.ResultChecked) != 1 {
		t.Fatal("the checked result is not counted")
	}
}

func TestR_BAS_022_ARangeAnIfRangeAnIfNoneMatchAndAHeadAreAnsweredFromTheEntry(t *testing.T) {
	s := newSiteHarness(t, options{})
	body := payload(1000)
	s.site.artifacts[tgz] = body
	s.get(tgz, "Authorization", bearer(s.full))
	etag := `"` + sha(body) + `"`
	auth := bearer(s.full)
	before := s.main.count()
	cases := []struct {
		name   string
		method string
		hdr    []string
		status int
		want   []byte
		cr     string
	}{
		{"a range", "GET", []string{"Range", "bytes=10-19"}, 206, body[10:20], "bytes 10-19/1000"},
		{"an open range", "GET", []string{"Range", "bytes=990-"}, 206, body[990:], "bytes 990-999/1000"},
		{"a suffix range", "GET", []string{"Range", "bytes=-5"}, 206, body[995:], "bytes 995-999/1000"},
		{"a range past the end is cut", "GET", []string{"Range", "bytes=990-5000"}, 206, body[990:], "bytes 990-999/1000"},
		{"If-Range with the content address", "GET", []string{"Range", "bytes=0-9", "If-Range", etag}, 206, body[:10], "bytes 0-9/1000"},
		{"If-Range with another validator", "GET", []string{"Range", "bytes=0-9", "If-Range", `"other"`}, 200, body, ""},
		{"several ranges are ignored", "GET", []string{"Range", "bytes=0-1,5-6"}, 200, body, ""},
		{"another unit is ignored", "GET", []string{"Range", "items=0-1"}, 200, body, ""},
		{"a range beyond the end", "GET", []string{"Range", "bytes=1000-"}, 416, nil, "bytes */1000"},
		{"If-None-Match", "GET", []string{"If-None-Match", etag}, 304, nil, ""},
		{"If-None-Match with a list and a weak tag", "GET", []string{"If-None-Match", `"a", W/` + etag}, 304, nil, ""},
		{"If-None-Match with another tag", "GET", []string{"If-None-Match", `"a"`}, 200, body, ""},
		{"HEAD", "HEAD", nil, 200, nil, ""},
		{"HEAD with a range", "HEAD", []string{"Range", "bytes=0-9"}, 206, nil, "bytes 0-9/1000"},
	}
	for _, c := range cases {
		w := s.do(c.method, tgz, nil, append([]string{"Authorization", auth}, c.hdr...)...)
		if w.Code != c.status {
			t.Errorf("%s: status %d, want %d", c.name, w.Code, c.status)
			continue
		}
		if c.want != nil && !bytes.Equal(w.Body.Bytes(), c.want) {
			t.Errorf("%s: %d bytes", c.name, w.Body.Len())
		}
		if c.method == "HEAD" && w.Body.Len() != 0 {
			t.Errorf("%s: a body", c.name)
		}
		if w.Header().Get("Content-Range") != c.cr {
			t.Errorf("%s: Content-Range %q, want %q", c.name, w.Header().Get("Content-Range"), c.cr)
		}
		if c.status == 206 && c.method == "GET" && w.Header().Get("Content-Length") != fmt.Sprint(len(c.want)) {
			t.Errorf("%s: Content-Length %q", c.name, w.Header().Get("Content-Length"))
		}
		if c.status != 304 && c.status != 416 && w.Header().Get("Accept-Ranges") != "bytes" {
			t.Errorf("%s: no Accept-Ranges", c.name)
		}
	}
	if s.main.count() != before {
		t.Fatal("the entry did not answer")
	}
}

func TestR_BAS_022_ARangeOnAMissIsAnsweredFromTheVerifiedFill(t *testing.T) {
	s := newSiteHarness(t, options{})
	body := payload(300 << 10)
	s.site.artifacts[tgz] = body
	w := s.get(tgz, "Authorization", bearer(s.full), "Range", "bytes=100-199")
	if w.Code != 206 || !bytes.Equal(w.Body.Bytes(), body[100:200]) || w.Header().Get("Content-Range") != fmt.Sprintf("bytes 100-199/%d", len(body)) {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	if r := s.main.requests()[0]; r.Header.Get("Range") != "" {
		t.Fatal("the caller's Range was forwarded")
	}
	if s.cache.objectCount() != 1 {
		t.Fatal("the fill was not kept")
	}
	if w := s.get(tgz, "Authorization", bearer(s.full), "If-None-Match", `"`+sha(body)+`"`); w.Code != 304 || s.main.count() != 1 {
		t.Fatalf("%d", w.Code)
	}
}

func TestR_BAS_022_AConditionalThatAnswers200ReplacesTheEntryAndAnAbsent404RemovesIt(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.artifacts[tgz] = payload(1000)
	s.get(tgz, "Authorization", bearer(s.part))
	s.get(tgz, "Authorization", bearer(s.full))
	// the artifact changes at the main installation
	newBody := payload(1500)
	s.site.mu.Lock()
	s.site.artifacts[tgz] = newBody
	s.site.mu.Unlock()
	s.clock.advance(s.cfg.Bastion.Cache.ArtifactRevalidateAfter + time.Second)
	s.cfg.Bastion.Answers.Period = time.Hour
	w := s.get(tgz, "Authorization", bearer(s.part))
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), newBody) {
		t.Fatalf("%d %d bytes", w.Code, w.Body.Len())
	}
	ent, _ := s.e.loadEntry(context.Background(), tgz)
	if ent.SHA256 != sha(newBody) {
		t.Fatal("the entry still points at the old bytes")
	}
	// gone at the main installation: absent for a full reader
	s.site.mu.Lock()
	delete(s.site.artifacts, tgz)
	s.site.mu.Unlock()
	s.clock.advance(s.cfg.Bastion.Cache.ArtifactRevalidateAfter + time.Second)
	if w := s.get(tgz, "Authorization", bearer(s.full)); w.Code != 404 {
		t.Fatalf("%d", w.Code)
	}
	if _, ok := s.e.loadEntry(context.Background(), tgz); ok {
		t.Fatal("an absent 404 did not remove the entry")
	}
}

func TestR_BAS_022_AContentEntryOfTheFeedMakesTheEntryStale(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.artifacts[tgz] = payload(1000)
	s.get(tgz, "Authorization", bearer(s.full))
	before := s.main.count()
	s.get(tgz, "Authorization", bearer(s.full))
	if s.main.count() != before {
		t.Fatal("not a hit")
	}
	s.clock.advance(time.Second)
	s.feed.Record(iface.FeedEntry{ID: "e1", Kind: iface.FeedContent, Repositories: []string{"web/npm-all"}}, s.clock.now())
	s.clock.advance(time.Second)
	s.get(tgz, "Authorization", bearer(s.full))
	reqs := s.main.requests()[before:]
	if len(reqs) == 0 {
		t.Fatal("the entry was served although its repository's content was withdrawn")
	}
	// another repository's content entry changes nothing
	s2 := newSiteHarness(t, options{})
	s2.site.artifacts[tgz] = payload(1000)
	s2.get(tgz, "Authorization", bearer(s2.full))
	s2.clock.advance(time.Second)
	s2.feed.Record(iface.FeedEntry{ID: "e1", Kind: iface.FeedContent, Repositories: []string{"npm-other"}}, s2.clock.now())
	before = s2.main.count()
	s2.get(tgz, "Authorization", bearer(s2.full))
	if s2.main.count() != before {
		t.Fatal("a content entry of another repository made the entry stale")
	}
}

func TestR_BAS_022_AnEntryWhoseBytesAreGoneIsAMissAndIsRemoved(t *testing.T) {
	s := newSiteHarness(t, options{})
	body := payload(1000)
	s.site.artifacts[tgz] = body
	s.get(tgz, "Authorization", bearer(s.full))
	s.cache.deleteObject(sha(body))
	w := s.get(tgz, "Authorization", bearer(s.full))
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatalf("%d", w.Code)
	}
	if s.main.count() != 2 || s.cache.objectCount() != 1 {
		t.Fatalf("requests %d objects %d", s.main.count(), s.cache.objectCount())
	}
}

func TestR_BAS_032_AnObjectThatChangedInTheBackendAbortsTheTransferAndTheNextOneFetches(t *testing.T) {
	s := newSiteHarness(t, options{})
	body := payload(300 << 10)
	s.site.artifacts[tgz] = body
	s.get(tgz, "Authorization", bearer(s.full))
	tampered := append([]byte(nil), body...)
	tampered[10] ^= 0xff
	s.cache.putRaw(sha(body), tampered)
	srv := serve(t, s.harness)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+tgz, nil)
	req.Header.Set("Authorization", bearer(s.full))
	// The abort may come before the status line reached the client or during the body (R-BAS-032).
	var got []byte
	resp, rerr := srv.Client().Do(req)
	if rerr == nil {
		got, rerr = io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	if rerr == nil || len(got) > len(body)-withheldTail {
		t.Fatalf("the tampered object was served: %d bytes, %v", len(got), rerr)
	}
	if counterValue(t, s.harness, "requests", "artifact", "hit") != 1 {
		t.Log("hit counter")
	}
	if s.cache.objectCount() != 0 || s.cache.entryCount() != 0 {
		t.Fatal("the corrupt object and its entry were not removed")
	}
	if w := s.get(tgz, "Authorization", bearer(s.full)); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatalf("%d", w.Code)
	}
	if s.main.count() != 2 {
		t.Fatalf("%d requests, want the refill", s.main.count())
	}
}

func TestR_BAS_032_AnEntryRewrittenToPointAtOtherBytesIsIgnored(t *testing.T) {
	s := newSiteHarness(t, options{signer: true})
	body := payload(2000)
	s.site.artifacts[tgz] = body
	s.get(tgz, "Authorization", bearer(s.full))
	if s.cache.entryCount() != 1 {
		t.Fatal("no entry")
	}
	// rewrite the record in place: the signature no longer matches
	for p, rec := range s.cache.entryRecords() {
		if err := os.WriteFile(p, bytes.Replace(rec, []byte(sha(body)), []byte(strings.Repeat("0", 64)), 1), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	before := s.main.count()
	if w := s.get(tgz, "Authorization", bearer(s.full)); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatalf("%d", w.Code)
	}
	if s.main.count() != before+1 {
		t.Fatal("the forged entry was used")
	}
	if counterValue(t, s.harness, "requests", "artifact", "miss") < 2 {
		t.Fatal("not a miss")
	}
}

func TestR_BAS_021_TheCacheBackendFailingTurnsIntoPassThroughNeverIntoAFailedRequest(t *testing.T) {
	s := newSiteHarness(t, options{})
	body := payload(2000)
	s.site.artifacts[tgz] = body
	s.cache.fault.SetFailPut(true)
	w := s.get(tgz, "Authorization", bearer(s.full))
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatalf("%d", w.Code)
	}
	if readCounter(s.m.CacheErrors.WithLabelValues(metrics.OpPut)) == 0 {
		t.Fatal("the error is not counted")
	}
}
