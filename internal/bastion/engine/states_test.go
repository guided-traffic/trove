package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/guided-traffic/trove/internal/bastion/answers"
	"github.com/guided-traffic/trove/internal/bastion/guard"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/observability"
)

// ---- R-BAS-027 --------------------------------------------------------------------------------------------

func TestR_BAS_027_AnswersHeldServeCachedContentForThePeriodAndTheGraceThenNothing(t *testing.T) {
	s := newSiteHarness(t, options{})
	var paths []string
	for i := 0; i < 5; i++ {
		p := fmt.Sprintf("/api/npm/web/npm-all/pkg%d.tgz", i)
		s.site.artifacts[p] = payload(300 + i)
		paths = append(paths, p)
		s.get(p, "Authorization", bearer(s.full))
	}
	s.site.docs[doc] = "all packages"
	s.get(doc, "Authorization", bearer(s.full))
	s.clock.advance(2 * time.Minute)
	s.setUnreachable(true)
	before := s.main.count()

	// seen two minutes before: the warm run completes, also past the revalidation periods
	s.clock.advance(6 * time.Minute) // 8 minutes: past the period (5 m), inside period + grace (20 m)
	for _, p := range paths {
		if w := s.get(p, "Authorization", bearer(s.full)); w.Code != 200 {
			t.Fatalf("%s: %d %s", p, w.Code, w.Body)
		}
	}
	if w := s.get(doc, "Authorization", bearer(s.full)); w.Code != 200 || w.Body.String() != "all packages" {
		t.Fatalf("%d", w.Code)
	}
	// a token never seen, and a miss, get 503 main_unreachable with Retry-After: 30
	for name, c := range map[string][]string{
		"an unseen token": {"/api/npm/web/npm-all/pkg0.tgz", bearer(s.part)},
		"a cold package":  {"/api/npm/web/npm-all/cold.tgz", bearer(s.full)},
		"anonymous":       {"/api/npm/web/npm-all/pkg0.tgz", ""},
	} {
		hdr := []string{}
		if c[1] != "" {
			hdr = []string{"Authorization", c[1]}
		}
		w := s.get(c[0], hdr...)
		if w.Code != 503 || problemCode(w) != "main_unreachable" || w.Header().Get("Retry-After") != "30" || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %d %s %v", name, w.Code, w.Body, w.Header())
		}
	}
	if s.main.count() != before {
		t.Fatalf("%d requests went to the main installation while it was unreachable", s.main.count()-before)
	}
	if counterValue(t, s.harness, "refused", metrics.RefusedUnreachable) != 3 {
		t.Fatal("the refusals are not counted")
	}

	// twenty minutes after the answers were obtained: nothing
	s.clock.advance(13 * time.Minute)
	for _, p := range append(paths, doc) {
		if w := s.get(p, "Authorization", bearer(s.full)); w.Code != 503 {
			t.Fatalf("%s: %d after the grace period", p, w.Code)
		}
	}

	// back: the replica serves again without a restart
	s.setUnreachable(false)
	if w := s.get(paths[0], "Authorization", bearer(s.full)); w.Code != 200 {
		t.Fatalf("%d", w.Code)
	}
}

func TestR_BAS_027_TheGraceExtendsOnlyWhileTheMainInstallationIsUnreachable(t *testing.T) {
	s := newSiteHarness(t, options{cfg: []func(*configT){func(c *configT) { c.Bastion.Cache.ArtifactRevalidateAfter = time.Hour }}})
	warm(t, s)
	s.clock.advance(8 * time.Minute)
	before := s.main.count()
	s.get(tgz, "Authorization", bearer(s.full))
	if s.main.count() == before {
		t.Fatal("an answer past its period was used while the main installation is reachable")
	}
}

func TestR_BAS_027_AWriteWhileUnreachableAnswers503(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.setUnreachable(true)
	w := s.do(http.MethodPut, "/api/npm/web/npm-all/pkg", strings.NewReader("x"), "Authorization", bearer(s.full))
	if w.Code != 503 || problemCode(w) != "main_unreachable" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

// ---- R-BAS-028 --------------------------------------------------------------------------------------------

func TestR_BAS_028_AMismatchOrARefusedTokenOnARequestMakesTheReplicaUnreachable(t *testing.T) {
	mismatch := fmt.Errorf("%w", &mainclient.MismatchError{Main: iface.InterfaceVersion + 1})
	for _, row := range []struct {
		name     string
		cause    error
		viaCheck bool
	}{
		{"a mismatch on a forward", mismatch, false},
		{"a mismatch on a check", mismatch, true},
		// A refused token counts on the Bastion's own calls only; on a forward it does not (Q-BAS-121).
		{"a refused token on a check", mainclient.ErrTokenRefused, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			s := newSiteHarness(t, options{})
			who := s.full
			if row.viaCheck {
				s.site.artifacts[tgz] = []byte("tarball")
				// The entry exists and the reader has no shape answer yet: the check asks the main installation.
				if w := s.get(tgz, "Authorization", bearer(s.full)); w.Code != 200 {
					t.Fatalf("prime: %d %s", w.Code, w.Body)
				}
				who = s.part
				s.main.checkFn = func(iface.CheckRequest) (iface.CheckAnswer, error) { return iface.CheckAnswer{}, row.cause }
			}
			s.main.doFn = func(mainclient.Forward) (*mainclient.Response, error) { return nil, row.cause }
			w := s.get(tgz, "Authorization", bearer(who))
			if w.Code != 503 || problemCode(w) != "main_unreachable" {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			if s.link.Reachable() {
				t.Fatal("the link was not told: Link.Guard makes the replica unreachable at once")
			}
			if st := s.link.State(); errors.Is(row.cause, mainclient.ErrInterfaceMismatch) && !st.InterfaceMismatch {
				t.Fatalf("the mismatch is not recorded: %+v", st)
			}
			before := s.main.count()
			s.get(tgz, "Authorization", bearer(who))
			if s.main.count() != before {
				t.Fatal("the replica kept sending to the main installation")
			}
		})
	}
}

// TestR_BAS_006_R_BAS_021_ARefusalOfTheTokenOnAForwardDoesNotMakeTheReplicaUnreachable: a forward is a path the
// caller chose, so an answer that refuses the Bastion's token to it (the bad answer the real client makes of it, 502, or
// the typed refusal, which the guard does not note either) leaves the link reachable (Q-BAS-121).
func TestR_BAS_006_R_BAS_021_ARefusalOfTheTokenOnAForwardDoesNotMakeTheReplicaUnreachable(t *testing.T) {
	for name, row := range map[string]struct {
		cause  error
		status int
		code   string
	}{
		"the typed refusal": {mainclient.ErrTokenRefused, 503, "main_unreachable"},
		"the bad answer":    {&mainclient.AnswerError{Status: 401, Code: iface.CodeTokenInvalid}, 502, "main_failed"},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSiteHarness(t, options{})
			s.main.doFn = func(mainclient.Forward) (*mainclient.Response, error) { return nil, row.cause }
			w := s.get(tgz, "Authorization", bearer(s.full))
			if w.Code != row.status || problemCode(w) != row.code {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			if !s.link.Reachable() {
				t.Fatal("a forward made the replica unreachable")
			}
		})
	}
}

// ---- R-BAS-029 --------------------------------------------------------------------------------------------

func TestR_BAS_029_EveryOwnAnswerIsProblemDetailsWithNoStoreAndTheRightFields(t *testing.T) {
	type row struct {
		name   string
		status int
		code   string
		setup  func(s *siteHarness)
		call   func(s *siteHarness) *httptest.ResponseRecorder
		retry  string
		auth   bool
	}
	get := func(path string, hdr ...string) func(s *siteHarness) *httptest.ResponseRecorder {
		return func(s *siteHarness) *httptest.ResponseRecorder { return s.get(path, hdr...) }
	}
	rows := []row{
		{name: "invalid_token", status: 401, code: "invalid_token", call: func(s *siteHarness) *httptest.ResponseRecorder {
			return s.get(tgz, "Authorization", "Bearer nonsense")
		}, auth: true},
		{name: "credentials_required", status: 401, code: "credentials_required", setup: func(s *siteHarness) { s.setAnonymous(false) }, call: get(tgz), auth: true},
		{name: "invalid_token kept", status: 401, code: "invalid_token", call: func(s *siteHarness) *httptest.ResponseRecorder {
			s.get(tgz, "Authorization", bearer(s.bad))
			return s.get(tgz, "Authorization", bearer(s.bad))
		}, auth: true},
		{name: "none 404", status: 404, code: "not_found", call: func(s *siteHarness) *httptest.ResponseRecorder {
			s.get(tgz, "Authorization", bearer(s.none))
			return s.get(tgz, "Authorization", bearer(s.none))
		}},
		{name: "none 403", status: 403, code: "forbidden", call: func(s *siteHarness) *httptest.ResponseRecorder {
			s.e.answers.put(context.Background(), answers.Shape, shapeKey(credentialKey(s.full), "web/npm-all"),
				&answerVal{Answer: answerBase(answers.Shape, s.clock.now(), tokenSubject("full", nil), "web/npm-all"), Read: iface.ReadNone, Status: 403})
			return s.get(tgz, "Authorization", bearer(s.full))
		}},
		{name: "none 401", status: 401, code: "credentials_required", call: func(s *siteHarness) *httptest.ResponseRecorder {
			s.e.answers.put(context.Background(), answers.Shape, shapeKey(credentialKey(s.full), "web/npm-all"),
				&answerVal{Answer: answerBase(answers.Shape, s.clock.now(), tokenSubject("full", nil), "web/npm-all"), Read: iface.ReadNone, Status: 401})
			return s.get(tgz, "Authorization", bearer(s.full))
		}, auth: true},
		{name: "bastion_not_served", status: 404, code: "bastion_not_served", call: get("/ui/")},
		{name: "method_not_allowed", status: 405, code: "method_not_allowed", call: func(s *siteHarness) *httptest.ResponseRecorder {
			return s.do(http.MethodOptions, tgz, nil)
		}},
		{name: "payload_too_large", status: 413, code: "payload_too_large", call: func(s *siteHarness) *httptest.ResponseRecorder {
			r := httptest.NewRequest(http.MethodPut, "https://packages.example.com"+tgz, strings.NewReader("x"))
			r.ContentLength = 100 << 20
			r.RemoteAddr = "203.0.113.7:1"
			w := httptest.NewRecorder()
			s.e.ServeHTTP(w, r)
			return w
		}},
		{name: "misdirected_request", status: 421, code: "misdirected_request", call: func(s *siteHarness) *httptest.ResponseRecorder {
			r := httptest.NewRequest(http.MethodGet, "https://trove.example.com"+tgz, nil)
			r.RemoteAddr = "203.0.113.7:1"
			w := httptest.NewRecorder()
			s.e.ServeHTTP(w, r)
			return w
		}},
		{name: "rate_limited", status: 429, code: "rate_limited", retry: "7", setup: func(s *siteHarness) {
			s.guard.request = guard.Verdict{Status: 429, Code: iface.CodeRateLimited, RetryAfter: 7 * time.Second, Reason: metrics.RefusedRateLimited}
		}, call: get(tgz)},
		{name: "rate_limited by the miss limit", status: 429, code: "rate_limited", retry: "10", setup: func(s *siteHarness) { s.guard.missLimit = 1; s.guard.misses = 1 }, call: get(tgz)},
		{name: "auth_timeout", status: 429, code: "auth_timeout", retry: "900", setup: func(s *siteHarness) {
			s.guard.timeout = guard.Verdict{Status: 429, Code: iface.CodeAuthTimeout, RetryAfter: 15 * time.Minute, Reason: metrics.RefusedAuthTimeout}
		}, call: func(s *siteHarness) *httptest.ResponseRecorder { return s.get(tgz, "Authorization", bearer(s.part)) }},
		{name: "address_blocked", status: 429, code: "address_blocked", retry: "60", setup: func(s *siteHarness) {
			s.guard.blocked = guard.Verdict{Status: 429, Code: iface.CodeAddressBlocked, RetryAfter: time.Minute, Reason: metrics.RefusedBlocked}
		}, call: get(tgz)},
		{name: "main_failed", status: 502, code: "main_failed", setup: func(s *siteHarness) {
			s.main.doFn = func(mainclient.Forward) (*mainclient.Response, error) { return nil, mainclient.ErrUnreachable }
		}, call: get(tgz, "Authorization", "")},
		{name: "main_unreachable", status: 503, code: "main_unreachable", retry: "30", setup: func(s *siteHarness) { s.setUnreachable(true) }, call: get(tgz)},
		{name: "bastion_busy", status: 503, code: "bastion_busy", retry: "1", setup: func(s *siteHarness) {
			s.main.doFn = func(mainclient.Forward) (*mainclient.Response, error) { return nil, mainclient.ErrBusy }
		}, call: get(tgz)},
		{name: "main_timeout", status: 504, code: "main_timeout", setup: func(s *siteHarness) {
			s.main.doFn = func(mainclient.Forward) (*mainclient.Response, error) { return nil, context.DeadlineExceeded }
		}, call: get(tgz)},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			s := newSiteHarness(t, options{})
			s.site.artifacts[tgz] = []byte("x")
			if r.setup != nil {
				r.setup(s)
			}
			w := r.call(s)
			if w.Code != r.status || problemCode(w) != r.code {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("Cache-Control %q", w.Header().Get("Cache-Control"))
			}
			if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
				t.Errorf("Content-Type %q", w.Header().Get("Content-Type"))
			}
			var p map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || p["status"] != float64(r.status) || p["code"] != r.code || p["title"] == "" || p["type"] == "" || p["instance"] == nil {
				t.Errorf("not RFC 9457 problem details with the members of R-ARCH-023: %s", w.Body)
			}
			if r.auth && w.Header().Get("WWW-Authenticate") != `Basic realm="trove"` {
				t.Errorf("WWW-Authenticate %q", w.Header().Get("WWW-Authenticate"))
			}
			if r.retry != "" && w.Header().Get("Retry-After") != r.retry {
				t.Errorf("Retry-After %q, want %q", w.Header().Get("Retry-After"), r.retry)
			}
			if r.status == 429 && (w.Header().Get("RateLimit-Policy") == "" || w.Header().Get("RateLimit") == "") {
				t.Errorf("the RateLimit fields are missing: %v", w.Header())
			}
		})
	}
}

// ---- R-BAS-014 --------------------------------------------------------------------------------------------

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

func TestR_BAS_014_AWriteIsProbedWithoutBodyAndCredentialAndTheRefusalIsReplayed(t *testing.T) {
	s := newSiteHarness(t, options{})
	body := &countingReader{r: strings.NewReader(strings.Repeat("u", 1<<20))}
	w := s.do(http.MethodPut, "/api/npm/web/npm-all/acme-sdk", body, "Authorization", bearer(s.full), "Content-Type", "application/json", "Cookie", "a=b")
	if w.Code != 403 || !strings.Contains(w.Body.String(), "bastion_read_only") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if body.n != 0 {
		t.Fatalf("%d bytes of the upload were read", body.n)
	}
	reqs := s.main.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	r := reqs[0]
	if r.Kind != mainclient.KindProbe || !r.Probe || r.Method != http.MethodPut || r.Target != "/api/npm/web/npm-all/acme-sdk" || r.Class != mainclient.Authenticated {
		t.Fatalf("%+v", r)
	}
	if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || len(r.Body) != 0 {
		t.Fatalf("the probe carries a credential, a cookie or a body: %v", r.Header)
	}
	for k := range w.Header() {
		if strings.HasPrefix(k, "Trove-Bastion-") {
			t.Errorf("%s reached the caller", k)
		}
	}
	if counterValue(t, s.harness, "refused", metrics.RefusedReadOnly) != 1 {
		t.Fatal("the refusal is not counted")
	}
}

func TestR_BAS_014_ThousandWritesToOnePathCauseOneProbe(t *testing.T) {
	m := &fakeMain{delay: 20 * time.Millisecond}
	s := newSiteHarness(t, options{main: m})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := s.do(http.MethodPut, "/api/npm/web/npm-all/acme-sdk", strings.NewReader("x")); w.Code != 403 {
				t.Errorf("%d %s", w.Code, w.Body)
			}
		}()
	}
	wg.Wait()
	for i := 0; i < 950; i++ {
		if w := s.do(http.MethodPut, "/api/npm/web/npm-all/acme-sdk", strings.NewReader("x")); w.Code != 403 || !strings.Contains(w.Body.String(), "bastion_read_only") {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	if n := m.count(); n != 1 {
		// Name every request the main installation saw, so a failure says which kind came twice (F-102).
		var seen []string
		for _, r := range m.requests() {
			seen = append(seen, fmt.Sprintf("%v %s %s probe=%v", r.Kind, r.Method, r.Target, r.Probe))
		}
		t.Fatalf("%d probes for 1000 writes, want 1: %s", n, strings.Join(seen, "; "))
	}
	// another method and another path are probes of their own
	s.do(http.MethodDelete, "/api/npm/web/npm-all/acme-sdk", nil)
	s.do(http.MethodPut, "/api/npm/web/npm-all/other", nil)
	if m.count() != 3 {
		t.Fatalf("%d probes", m.count())
	}
	// the answer is kept for metadata_revalidate_after
	s.clock.advance(s.cfg.Bastion.Cache.MetadataRevalidateAfter + time.Second)
	s.do(http.MethodPut, "/api/npm/web/npm-all/acme-sdk", nil)
	if m.count() != 4 {
		t.Fatalf("%d probes after the period", m.count())
	}
}

// TestR_BAS_014_AWriteThatMissedTheKeptAnswerBeforeTheProbeEndedDoesNotProbeAgain holds one write between its
// look into the kept answers and its flight until another write of the same key has been probed and kept: the
// held write must replay the kept answer and not send a second probe.
func TestR_BAS_014_AWriteThatMissedTheKeptAnswerBeforeTheProbeEndedDoesNotProbeAgain(t *testing.T) {
	g := newGuard()
	entered, release := make(chan struct{}), make(chan struct{})
	g.missHook = func(n int) {
		if n == 1 {
			close(entered)
			<-release
		}
	}
	m := &fakeMain{}
	s := newSiteHarness(t, options{main: m, guard: g})
	held := make(chan *httptest.ResponseRecorder, 1)
	go func() { held <- s.do(http.MethodPut, "/api/npm/web/npm-all/acme-sdk", strings.NewReader("x")) }()
	<-entered
	if w := s.do(http.MethodPut, "/api/npm/web/npm-all/acme-sdk", strings.NewReader("x")); w.Code != 403 {
		t.Fatalf("%d", w.Code)
	}
	close(release)
	if w := <-held; w.Code != 403 || !strings.Contains(w.Body.String(), "bastion_read_only") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if n := m.count(); n != 1 {
		t.Fatalf("%d probes for two writes of one key, want 1", n)
	}
	// the held write is a replay: counted as a hit served from the cache, and as a refusal
	if counterValue(t, s.harness, "requests", metrics.ClassPass, metrics.ResultHit) != 1 || counterValue(t, s.harness, "requests", metrics.ClassPass, metrics.ResultMiss) != 1 {
		t.Fatalf("requests: hit %v miss %v, want 1 and 1", counterValue(t, s.harness, "requests", metrics.ClassPass, metrics.ResultHit), counterValue(t, s.harness, "requests", metrics.ClassPass, metrics.ResultMiss))
	}
	if cache, main := counterValue(t, s.harness, "bytes", metrics.SourceCache), counterValue(t, s.harness, "bytes", metrics.SourceMain); cache == 0 || cache != main {
		t.Fatalf("bytes from the cache %v, from main %v: the replay must be served from the cache with the probe's body", cache, main)
	}
	if counterValue(t, s.harness, "refused", metrics.RefusedReadOnly) != 2 {
		t.Fatalf("refused %v, want 2", counterValue(t, s.harness, "refused", metrics.RefusedReadOnly))
	}
}

func TestR_BAS_014_ANuGetPushWithoutACredentialIsStillRefusedByTheProbe(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.setAnonymous(false)
	w := s.do(http.MethodPut, "/api/nuget/web/nuget-all/v2/package", strings.NewReader("nupkg"), "X-NuGet-ApiKey", "key")
	if w.Code != 403 || !strings.Contains(w.Body.String(), "bastion_read_only") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if r := s.main.requests()[0]; r.Header.Get("X-Nuget-Apikey") != "" {
		t.Fatal("the API key reached the main installation")
	}
}

func TestR_BAS_014_ARouteThatTakesABodyIsForwardedWithTheCallersCredentialAfter204(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.probe = func(f mainclient.Forward) (*mainclient.Response, error) {
		// The client removes the marking headers and hands the route over in its own field.
		r := resp(204, marking(iface.ClassPass, "", "web/npm-all", iface.Subject{Kind: iface.SubjectAnonymous}), nil, "")
		r.Route = iface.RouteReadWithBody
		return r, nil
	}
	inner := s.main.doFn
	s.main.doFn = func(f mainclient.Forward) (*mainclient.Response, error) {
		if !f.Probe && f.Request.Method == http.MethodPost {
			return resp(200, marking(iface.ClassPass, iface.ReadFull, "web/npm-all", tokenSubject("full", nil)), http.Header{"Content-Type": {"application/json"}}, `{"audit":"ok"}`), nil
		}
		return inner(f)
	}
	w := s.do(http.MethodPost, "/api/npm/web/npm-all/-/npm/v1/security/advisories/bulk", strings.NewReader(`{"lodash":["1.0.0"]}`), "Authorization", bearer(s.full))
	if w.Code != 200 || w.Body.String() != `{"audit":"ok"}` {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	reqs := s.main.requests()
	if len(reqs) != 2 || !reqs[0].Probe || reqs[1].Probe {
		t.Fatalf("%+v", reqs)
	}
	if reqs[1].Header.Get("Authorization") != bearer(s.full) || string(reqs[1].Body) != `{"lodash":["1.0.0"]}` || reqs[1].Method != http.MethodPost {
		t.Fatalf("the real request: %+v", reqs[1])
	}
	if s.cache.entryCount() != 0 {
		t.Fatal("the answer to a body request was kept")
	}
	// a chunked body is held up to the limit, and 413 above it
	s.cfg.Bastion.Main.MaxForwardedBody = 10
	r := httptest.NewRequest(http.MethodPost, "https://packages.example.com/api/npm/web/npm-all/-/npm/v1/security/advisories/bulk2", io.NopCloser(strings.NewReader(strings.Repeat("z", 100))))
	r.ContentLength = -1
	r.RemoteAddr = "203.0.113.7:1"
	r.Header.Set("Authorization", bearer(s.full))
	rec := httptest.NewRecorder()
	s.e.ServeHTTP(rec, r)
	if rec.Code != 413 || problemCode(rec) != "payload_too_large" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestR_BAS_014_ABodyAboveTheLimitIsRefusedBeforeTheProbe(t *testing.T) {
	s := newSiteHarness(t, options{})
	r := httptest.NewRequest(http.MethodPut, "https://packages.example.com/api/npm/web/npm-all/x", strings.NewReader("x"))
	r.ContentLength = int64(s.cfg.Bastion.Main.MaxForwardedBody) + 1
	r.RemoteAddr = "203.0.113.7:1"
	w := httptest.NewRecorder()
	s.e.ServeHTTP(w, r)
	if w.Code != 413 || s.main.count() != 0 {
		t.Fatalf("%d, %d requests", w.Code, s.main.count())
	}
}

func TestR_BAS_014_AProbeAnswerThatIsNotARefusalIsReplayedAndNotKept(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.probe = func(mainclient.Forward) (*mainclient.Response, error) {
		return resp(404, marking(iface.ClassPass, "", "", iface.Subject{Kind: iface.SubjectAnonymous}), nil, `{"code":"not_found"}`), nil
	}
	for i := 0; i < 3; i++ {
		if w := s.do(http.MethodPut, "/api/npm/web/npm-all/x", nil); w.Code != 404 {
			t.Fatalf("%d", w.Code)
		}
	}
	if s.main.count() != 1 {
		t.Fatalf("a 404 probe answer is kept: %d probes", s.main.count())
	}
	s.site.probe = func(mainclient.Forward) (*mainclient.Response, error) { return respUnmarked(502, nil, "gw"), nil }
	if w := s.do(http.MethodPost, "/api/npm/web/npm-all/y", nil); w.Code != 502 || problemCode(w) != "main_failed" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestR_BAS_014_TheMissLimitAppliesToProbes(t *testing.T) {
	g := newGuard()
	g.missLimit = 1
	s := newSiteHarness(t, options{guard: g})
	if w := s.do(http.MethodPut, "/api/npm/web/npm-all/a", nil); w.Code != 403 {
		t.Fatalf("%d", w.Code)
	}
	if w := s.do(http.MethodPut, "/api/npm/web/npm-all/b", nil); w.Code != 429 || problemCode(w) != "rate_limited" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

// ---- R-BAS-035 (the miss limit) ---------------------------------------------------------------------------

func TestR_BAS_035_AMissLimitedAddressKeepsGettingItsHits(t *testing.T) {
	g := newGuard()
	s := newSiteHarness(t, options{guard: g})
	warm(t, s)
	g.miss = guard.Verdict{Status: 429, Code: iface.CodeRateLimited, RetryAfter: 30 * time.Second, Reason: metrics.RefusedMissLimited}
	if w := s.get(tgz, "Authorization", bearer(s.full)); w.Code != 200 {
		t.Fatalf("a hit was refused: %d", w.Code)
	}
	w := s.get("/api/npm/web/npm-all/cold.tgz", "Authorization", bearer(s.full))
	if w.Code != 429 || problemCode(w) != "rate_limited" || !strings.Contains(w.Header().Get("RateLimit-Policy"), "bastion:misses") {
		t.Fatalf("%d %s %v", w.Code, w.Body, w.Header())
	}
	if counterValue(t, s.harness, "refused", metrics.RefusedMissLimited) != 1 {
		t.Fatal("not counted")
	}
}

// ---- R-BAS-042 --------------------------------------------------------------------------------------------

func TestR_BAS_042_TheRequestLogCarriesTheFieldsOfTheBastionAndNoToken(t *testing.T) {
	s := newSiteHarness(t, options{})
	warm(t, s)
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	h := observability.RequestLog(log, observability.NewMetrics("bastion"))(s.e)
	call := func(auth string) {
		r := httptest.NewRequest(http.MethodGet, "https://packages.example.com"+tgz, nil)
		r.RemoteAddr = "203.0.113.7:4711"
		r.Header.Set("Authorization", auth)
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	call(bearer(s.full))
	call("Bearer garbage")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d records: %s", len(lines), buf.String())
	}
	var hit, refused map[string]any
	_ = json.Unmarshal([]byte(lines[0]), &hit)
	_ = json.Unmarshal([]byte(lines[1]), &refused)
	if hit["remote_addr"] != "203.0.113.7" || hit["repo"] != "web/npm-all" || hit["format"] != "npm" || hit["cache"] != "hit" ||
		hit["bastion_class"] != "artifact" || hit["bastion_result"] != "hit" || hit["token_id"] != "full" || !strings.HasPrefix(fmt.Sprint(hit["token_hint"]), "trv_pat_") {
		t.Fatalf("%v", hit)
	}
	if refused["bastion_class"] != "refused" || refused["bastion_result"] != "refused" || refused["deny_reason"] != "invalid_token" || refused["cache"] != "refused" {
		t.Fatalf("%v", refused)
	}
	if strings.Contains(buf.String(), s.full) || strings.Contains(buf.String(), "garbage") {
		t.Fatal("a token is in the log")
	}
}

func TestR_BAS_042_TheRequestLogNamesTheMainInstallationsRequestIDOfAForward(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.artifacts[tgz] = payload(100)
	inner := s.main.doFn
	s.main.doFn = func(f mainclient.Forward) (*mainclient.Response, error) {
		r, err := inner(f)
		if r != nil {
			r.Header.Set("X-Request-Id", "main-trace-1")
		}
		return r, err
	}
	var buf bytes.Buffer
	h := observability.RequestLog(slog.New(slog.NewJSONHandler(&buf, nil)), observability.NewMetrics("bastion"))(s.e)
	r := httptest.NewRequest(http.MethodGet, "https://packages.example.com"+tgz, nil)
	r.RemoteAddr = "203.0.113.7:1"
	r.Header.Set("Authorization", bearer(s.full))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !strings.Contains(buf.String(), `"main_request_id":"main-trace-1"`) {
		t.Fatalf("%s", buf.String())
	}
	if w.Header().Get("X-Request-Id") == "main-trace-1" {
		t.Fatal("the main installation's request ID replaced the Bastion's")
	}
}

func TestR_BAS_042_TheSpanOfARequestCarriesTheClassAndTheResult(t *testing.T) {
	s := newSiteHarness(t, options{})
	warm(t, s)
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	ctx, span := tp.Tracer("test").Start(context.Background(), "request")
	r := httptest.NewRequest(http.MethodGet, "https://packages.example.com"+tgz, nil).WithContext(ctx)
	r.RemoteAddr = "203.0.113.7:1"
	r.Header.Set("Authorization", bearer(s.full))
	s.e.ServeHTTP(httptest.NewRecorder(), r)
	span.End()
	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("%d spans", len(spans))
	}
	got := map[string]string{}
	for _, a := range spans[0].Attributes() {
		if a.Value.Type() == attribute.STRING {
			got[string(a.Key)] = a.Value.AsString()
		}
	}
	if got["trove.bastion.class"] != "artifact" || got["trove.bastion.result"] != "hit" {
		t.Fatalf("%v", got)
	}
}

func TestR_BAS_042_TheContextOfTheCallerIsPassedToTheMainInstallation(t *testing.T) {
	type key struct{}
	s := newSiteHarness(t, options{})
	var seen any
	inner := s.main.doFn
	s.main.doFn = func(f mainclient.Forward) (*mainclient.Response, error) {
		seen = f.Request.Context().Value(key{})
		return inner(f)
	}
	r := httptest.NewRequest(http.MethodGet, "https://packages.example.com"+tgz, nil)
	r = r.WithContext(context.WithValue(r.Context(), key{}, "ctx"))
	r.RemoteAddr = "203.0.113.7:1"
	r.Header.Set("Authorization", bearer(s.full))
	s.e.ServeHTTP(httptest.NewRecorder(), r)
	if seen != "ctx" {
		t.Fatal("the forwarded request does not carry the request's context, so no trace is propagated")
	}
}

// ---- the lifecycle ----------------------------------------------------------------------------------------

func TestR_BAS_043_ReadinessWaitsForTheCacheBackendsFirstCheck(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.cache.fault.SetDown(true)
	if err := s.e.Ready(context.Background()); err == nil {
		t.Fatal("ready before the cache check passed")
	}
	s.cache.fault.SetDown(false)
	if err := s.e.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.cache.fault.SetDown(true)
	if err := s.e.Ready(context.Background()); err != nil {
		t.Fatal("the backend is gated at the first readiness only")
	}
}

func TestR_BAS_043_StartRunsTheJanitorAndDrainStopsItAndWaitsForTheBackgroundRefresh(t *testing.T) {
	s := newSiteHarness(t, options{})
	if err := s.e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.e.Start(context.Background()); err == nil {
		t.Fatal("started twice")
	}
	release := make(chan struct{})
	if !s.e.background(func() { <-release }) {
		t.Fatal("a background task was refused before the drain")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := s.e.Drain(ctx); err == nil {
		t.Fatal("the drain did not wait for the running background task")
	}
	if s.e.background(func() {}) {
		t.Fatal("a background task started after the drain began")
	}
	close(release)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if err := s.e.Drain(ctx2); err != nil {
		t.Fatal(err)
	}
}

// The sweep is the cache's (cache.Cache.RunSweeper, run by the server): its tests are TestR_BAS_031_* in
// internal/bastion/cache, and the server's wiring is TestR_BAS_031_TheServerRunsTheCacheSweeper.

// TestR_BAS_019_AnAnonymousRequestBeforeTheFirstHandshakeIs503: until a handshake succeeded the replica does not
// know whether the main installation admits anonymous requests, so it neither admits one nor claims that
// anonymous access is off (Q-BAS-99); an unseen token gets the same 503, and a replica that has handshaken and
// is told anonymous access is off answers 401.
func TestR_BAS_019_AnAnonymousRequestBeforeTheFirstHandshakeIs503(t *testing.T) {
	s := newSiteHarness(t, options{down: true})
	s.site.artifacts[tgz] = []byte("tarball")
	for name, hdr := range map[string][]string{"anonymous": nil, "an unseen token": {"Authorization", bearer(s.full)}} {
		w := s.get(tgz, hdr...)
		if w.Code != http.StatusServiceUnavailable || problemCode(w) != "main_unreachable" || w.Header().Get("Retry-After") != "30" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if s.main.count() != 0 {
		t.Fatalf("%d requests went to the main installation", s.main.count())
	}
	s.main.mu.Lock()
	s.main.hsErr = nil
	s.main.mu.Unlock()
	s.setAnonymous(false)
	if w := s.get(tgz); w.Code != http.StatusUnauthorized || problemCode(w) != "credentials_required" {
		t.Fatalf("anonymous access off after the handshake: %d %s", w.Code, w.Body)
	}
}

func TestR_BAS_040_AFreshAndAReplayedProbeAnswerRaiseTheSameReadOnlyCounter(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   float64
	}{
		{name: "403 is a refusal of a write", status: 403, want: 1},
		{name: "404 is the main installation's own answer", status: 404, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSiteHarness(t, options{})
			s.site.probe = func(mainclient.Forward) (*mainclient.Response, error) {
				return resp(tc.status, marking(iface.ClassPass, "", "", iface.Subject{Kind: iface.SubjectAnonymous}), nil, `{"code":"x"}`), nil
			}
			const path = "/api/npm/web/npm-all/x"
			// The fresh answer, then the replay of the kept answer: each raises the counter by tc.want.
			for i, calls := range []float64{1, 2} {
				if w := s.do(http.MethodPut, path, nil); w.Code != tc.status {
					t.Fatalf("call %d: %d", i, w.Code)
				}
				if got := counterValue(t, s.harness, "refused", metrics.RefusedReadOnly); got != tc.want*calls {
					t.Fatalf("after call %d (%s): refused read_only %v, want %v", i, map[int]string{0: "fresh", 1: "replayed"}[i], got, tc.want*calls)
				}
			}
			if s.main.count() != 1 {
				t.Fatalf("%d probes, want 1: the second answer must be a replay", s.main.count())
			}
		})
	}
}
