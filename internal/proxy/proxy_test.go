package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/repository"
)

func TestUpstreamURLBuilder(t *testing.T) {
	base := UpstreamBase{URL: must(url.Parse("https://registry.example/root/"))}
	t.Run("R-REPO-025 every segment is encoded again: %2e%2e becomes %252e%252e and a b becomes a%20b", func(t *testing.T) {
		for in, want := range map[string]string{
			"pkg/%2e%2e/-/x.tgz": "https://registry.example/root/pkg/%252e%252e/-/x.tgz",
			"a b/c":              "https://registry.example/root/a%20b/c",
			"100%/x":             "https://registry.example/root/100%25/x",
			"@scope/name":        "https://registry.example/root/@scope/name",
			"":                   "https://registry.example/root/",
		} {
			u, err := base.Path(in)
			if err != nil || u.String() != want {
				t.Errorf("Path(%q) = %v, %v; want %s", in, u, err, want)
			}
		}
	})
	t.Run("R-REPO-025 a slash inside a segment is %2F and no input decodes to a dot segment", func(t *testing.T) {
		u, err := base.Segments("@scope/name", "-", "name-1.tgz")
		if err != nil || u.String() != "https://registry.example/root/@scope%2Fname/-/name-1.tgz" {
			t.Fatalf("%v %v", u, err)
		}
		for _, bad := range [][]string{{".."}, {"."}, {""}, {"a", "..", "b"}, {"a\x00b"}} {
			if _, err := base.Segments(bad...); err == nil {
				t.Errorf("Segments(%q) was accepted", bad)
			}
		}
		for _, in := range []string{"a/../b", "./a", "a//b"} {
			if _, err := base.Path(in); err == nil {
				t.Errorf("Path(%q) was accepted", in)
			}
		}
		for _, in := range []string{"%2e%2e", "%252e%252e", "a%2fb", "..%2f.."} {
			u, err := base.Path("pkg/" + in)
			if err != nil {
				continue
			}
			for _, seg := range strings.Split(strings.TrimPrefix(u.EscapedPath(), "/root/"), "/") {
				if d, _ := url.PathUnescape(seg); d == ".." || d == "." {
					t.Errorf("Path(%q) = %s decodes to a dot segment", in, u)
				}
			}
		}
	})
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestCacheStatusHeader(t *testing.T) {
	for name, tc := range map[string]struct {
		cs   CacheStatus
		want string
	}{
		"fresh hit":      {hitStatus(300*time.Second + 900*time.Millisecond), "trove; hit; ttl=300"},
		"artifact hit":   {CacheStatus{Hit: true, TTL: -1}, "trove; hit"},
		"miss stored":    {CacheStatus{Forwarded: "uri-miss", FwdStatus: 200, Stored: true}, "trove; fwd=uri-miss; fwd-status=200; stored"},
		"revalidated":    {CacheStatus{Forwarded: "stale", FwdStatus: 304}, "trove; fwd=stale; fwd-status=304"},
		"refreshed":      {CacheStatus{Forwarded: "stale", FwdStatus: 200, Stored: true}, "trove; fwd=stale; fwd-status=200; stored"},
		"stale failure":  {staleAfterFailure(503), "trove; fwd=stale; fwd-status=503; detail=served-stale"},
		"stale slow":     {staleAfterFailure(0), "trove; fwd=stale; detail=served-stale"},
		"stale unasked":  {servedStale(), "trove; hit; detail=served-stale"},
		"negative":       {CacheStatus{Hit: true, Negative: true, Detail: detailNegative}, "trove; hit; detail=negative"},
		"collapsed hit":  {CacheStatus{Hit: true, TTL: 5 * time.Second, Collapsed: true}, "trove; hit; ttl=5; collapsed"},
		"collapsed miss": {CacheStatus{Hit: true, TTL: -1, Collapsed: true}, "trove; hit; collapsed"},
		"expired":        {hitStatus(-time.Second), "trove; hit; ttl=0"},
	} {
		t.Run("R-REPO-047 "+name, func(t *testing.T) {
			if got := tc.cs.Header(); got != tc.want {
				t.Fatalf("%q, want %q", got, tc.want)
			}
		})
	}
}

func rq(policy string, always bool, want Digests) Request {
	r := Request{Expected: want, AlwaysVerify: always}
	r.Remote.Settings.Integrity.ChecksumPolicy = policy
	return r
}

func TestVerify(t *testing.T) {
	sum := blob.Digests{}
	sum.SHA256[0], sum.SHA512[0], sum.SHA1[0] = 1, 2, 3
	same := Digests{SHA256: append([]byte(nil), sum.SHA256[:]...), SHA512: append([]byte(nil), sum.SHA512[:]...), SHA1: append([]byte(nil), sum.SHA1[:]...)}
	other := Digests{SHA512: bytes.Repeat([]byte{9}, 64)}
	t.Run("R-REPO-041 verify-if-available compares what is announced and accepts what is not", func(t *testing.T) {
		if verify(rq(repository.ChecksumVerifyIfAvailable, false, same), sum) != verdictOK || verify(rq(repository.ChecksumVerifyIfAvailable, false, Digests{}), sum) != verdictOK {
			t.Fatal("matching or absent digests were refused")
		}
		if verify(rq(repository.ChecksumVerifyIfAvailable, false, other), sum) != verdictMismatch {
			t.Fatal("a mismatch was accepted")
		}
	})
	t.Run("R-REPO-041 ignore and pass-through do not compare, a format that always verifies does", func(t *testing.T) {
		for _, p := range []string{repository.ChecksumIgnore, repository.ChecksumPassThrough} {
			if verify(rq(p, false, other), sum) != verdictOK {
				t.Errorf("%s compared", p)
			}
			if verify(rq(p, true, other), sum) != verdictMismatch {
				t.Errorf("%s with AlwaysVerify did not compare", p)
			}
		}
	})
	t.Run("R-REPO-041 require wants an announced SHA-256 or stronger", func(t *testing.T) {
		if admitsDigests(rq(repository.ChecksumRequire, false, Digests{SHA1: []byte{1}})) || !admitsDigests(rq(repository.ChecksumRequire, false, Digests{SHA512: []byte{1}})) {
			t.Fatal("the require rule is wrong")
		}
		if !admitsDigests(rq(repository.ChecksumVerifyIfAvailable, false, Digests{})) {
			t.Fatal("verify-if-available needs no digest")
		}
	})
}

func TestStreamWithholdsTheTail(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 3*withheldTail)
	t.Run("R-REPO-041 the last 64 KiB are released by finish and not before", func(t *testing.T) {
		rec := httptest.NewRecorder()
		s := newStream(rec)
		s.begin("application/octet-stream", int64(len(body)), "trove")
		for i := 0; i < len(body); i += 10_000 {
			_, _ = s.Write(body[i:min(i+10_000, len(body))])
		}
		if got := rec.Body.Len(); got != len(body)-withheldTail {
			t.Fatalf("%d bytes written before finish, want %d", got, len(body)-withheldTail)
		}
		if started, done := s.state(); !started || done {
			t.Fatalf("started %v done %v", started, done)
		}
		if !s.finish() || rec.Body.Len() != len(body) {
			t.Fatalf("finish: %d bytes", rec.Body.Len())
		}
		if _, done := s.state(); !done || rec.Header().Get("Cache-Status") != "trove" {
			t.Fatal("the response is not complete")
		}
	})
	t.Run("R-REPO-041 a body that ends inside the withheld tail has not started a response", func(t *testing.T) {
		rec := httptest.NewRecorder()
		s := newStream(rec)
		s.begin("", -1, "trove")
		_, _ = s.Write(body[:1000])
		if started, _ := s.state(); started || rec.Code != 200 && rec.Body.Len() != 0 {
			t.Fatalf("started %v", started)
		}
	})
	t.Run("R-REPO-026 a client that goes away does not stop the fetch, and nothing is written after detach", func(t *testing.T) {
		rec := httptest.NewRecorder()
		s := newStream(rec)
		s.begin("", -1, "trove")
		_, _ = s.Write(body[:2*withheldTail])
		s.detach()
		n := rec.Body.Len()
		if m, err := s.Write(body); m != len(body) || err != nil || rec.Body.Len() != n || s.finish() {
			t.Fatalf("write after detach: %d %v, %d -> %d bytes", m, err, n, rec.Body.Len())
		}
	})
}

func TestLimitReader(t *testing.T) {
	t.Run("R-REPO-040 a body that crosses the limit fails and one at the limit does not", func(t *testing.T) {
		r := &limitReader{r: bytes.NewReader(make([]byte, 100)), max: 100}
		if _, err := readAll(r); err != nil {
			t.Fatalf("at the limit: %v", err)
		}
		r = &limitReader{r: bytes.NewReader(make([]byte, 101)), max: 100}
		if _, err := readAll(r); !errors.Is(err, errTooLargeBody) {
			t.Fatalf("over the limit: %v", err)
		}
		r = &limitReader{r: bytes.NewReader(make([]byte, 5000)), max: 0}
		if _, err := readAll(r); err != nil {
			t.Fatalf("no limit: %v", err)
		}
	})
}

func readAll(r *limitReader) (int, error) {
	n := 0
	buf := make([]byte, 64)
	for {
		m, err := r.Read(buf)
		n += m
		if err != nil {
			if errors.Is(err, io.EOF) {
				return n, nil
			}
			return n, err
		}
	}
}

func TestIdleBody(t *testing.T) {
	t.Run("R-REPO-024 a body that stops sending ends the transfer after readTimeoutMs, one that keeps sending does not", func(t *testing.T) {
		pr, pw := io.Pipe()
		var fired atomic.Bool
		ctx, cancel := context.WithCancel(context.Background())
		b := newIdleBody(pr, 80*time.Millisecond, cancel, &fired, func() {})
		go func() {
			for range 5 {
				time.Sleep(40 * time.Millisecond)
				_, _ = pw.Write([]byte("x"))
			}
		}()
		buf := make([]byte, 1)
		for range 5 {
			if _, err := b.Read(buf); err != nil {
				t.Fatalf("a steady body failed: %v", err)
			}
		}
		if fired.Load() || ctx.Err() != nil {
			t.Fatal("the timer fired on a steady body")
		}
		go func() { _, _ = b.Read(buf) }()
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("a silent body was not cut")
		}
		if !fired.Load() {
			t.Fatal("the idle flag is not set")
		}
		_ = pw.Close()
	})
}

func TestSlots(t *testing.T) {
	t.Run("R-REPO-040 a repository's share is ceil(budget / n), at least 1", func(t *testing.T) {
		n := 2
		s := newSlots(256, func() int { return n })
		for budget, want := range map[int]int{4: 2, 5: 3, 1: 1, 64: 32} {
			if got := s.repoLimit(budget); got != want {
				t.Errorf("n=2 budget %d: %d, want %d", budget, got, want)
			}
		}
		n = 3
		if got := s.repoLimit(4); got != 2 {
			t.Errorf("n=3 budget 4: %d, want 2", got)
		}
		n = 0
		if got := s.repoLimit(4); got != 4 {
			t.Errorf("n=0 counts as 1: %d", got)
		}
	})
	t.Run("R-REPO-040 a fetch waits for a free slot and answers 503 with Retry-After 5 after the queue timeout", func(t *testing.T) {
		s := newSlots(10, nil)
		rel, _, err := s.acquire(context.Background(), 1, 1, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = s.acquire(context.Background(), 1, 1, 100*time.Millisecond)
		ee, ok := errs.As(err)
		if !ok || ee.Code != errs.CodeFetchQueueTimeout || ee.RetryAfter != 5*time.Second || ee.Kind != errs.Unavailable {
			t.Fatalf("%v", err)
		}
		got := make(chan error, 1)
		go func() {
			r2, _, err := s.acquire(context.Background(), 1, 1, 2*time.Second)
			if err == nil {
				r2()
			}
			got <- err
		}()
		time.Sleep(50 * time.Millisecond)
		rel()
		if err := <-got; err != nil {
			t.Fatalf("a waiter was not served after the release: %v", err)
		}
		if s.held() != 0 {
			t.Fatalf("held = %d", s.held())
		}
	})
	t.Run("R-REPO-040 the replica's own bound holds across repositories", func(t *testing.T) {
		s := newSlots(2, nil)
		a, _, _ := s.acquire(context.Background(), 1, 100, time.Second)
		b, _, _ := s.acquire(context.Background(), 2, 100, time.Second)
		if _, _, err := s.acquire(context.Background(), 3, 100, 50*time.Millisecond); err == nil {
			t.Fatal("a third fetch got a slot")
		}
		a()
		b()
	})
}

func TestClassification(t *testing.T) {
	rem := Remote{Repo: repository.Repository{Key: "r"}}
	rem.Settings.Availability.CircuitOpenSeconds = 300
	resp := func(status int, h ...string) *http.Response {
		r := &http.Response{StatusCode: status, Header: http.Header{}, Body: http.NoBody}
		for i := 0; i+1 < len(h); i += 2 {
			r.Header.Set(h[i], h[i+1])
		}
		return r
	}
	classify := func(status int, h ...string) failure {
		r := resp(status, h...)
		defer func() { _ = r.Body.Close() }()
		return classifyStatus(rem, r)
	}
	for name, tc := range map[string]struct {
		status int
		code   string
		count  bool
	}{
		"401":   {401, errs.CodeUpstreamAuthFailed, false},
		"403":   {403, errs.CodeUpstreamAuthFailed, false},
		"429":   {429, errs.CodeUnavailable, true},
		"500":   {500, errs.CodeUpstreamError, true},
		"502":   {502, errs.CodeUpstreamError, true},
		"503":   {503, errs.CodeUpstreamError, true},
		"504":   {504, errs.CodeUpstreamError, true},
		"other": {418, errs.CodeUpstreamUnexpectedStatus, false},
		"3xx":   {300, errs.CodeUpstreamUnexpectedStatus, false},
	} {
		t.Run("R-REPO-029 "+name, func(t *testing.T) {
			f := classify(tc.status)
			if f.err.Code != tc.code || f.count != tc.count {
				t.Fatalf("%s count=%v, want %s %v", f.err.Code, f.count, tc.code, tc.count)
			}
		})
	}
	t.Run("R-REPO-029 a 429 opens the circuit for its Retry-After, capped at the period", func(t *testing.T) {
		if f := classify(429, "Retry-After", "7"); f.retryAfter != 7*time.Second {
			t.Fatalf("%s", f.retryAfter)
		}
		if f := classify(429, "Retry-After", "100000"); f.retryAfter != 300*time.Second {
			t.Fatalf("%s", f.retryAfter)
		}
		if f := classify(429); f.retryAfter != 300*time.Second {
			t.Fatalf("without the header: %s", f.retryAfter)
		}
	})
	e := &Engine{}
	target := must(url.Parse("https://up.example/x"))
	t.Run("R-REPO-029 egress, source network, certificate and redirect failures do not count; connection and timeout do", func(t *testing.T) {
		for name, tc := range map[string]struct {
			err   error
			code  string
			count bool
		}{
			"egress":      {&outbound.EgressDeniedError{Rule: "deny"}, errs.CodeEgressDenied, false},
			"source":      {outbound.ErrSourceNetworkUnavailable, errs.CodeSourceNetworkUnavailable, false},
			"redirects":   {outbound.ErrTooManyRedirects, errs.CodeTooManyRedirects, false},
			"insecure":    {outbound.ErrInsecureRedirect, errs.CodeInsecureRedirect, false},
			"refused":     {errors.New("dial tcp: connection refused"), errs.CodeUpstreamUnreachable, true},
			"deadline":    {context.DeadlineExceeded, errs.CodeUpstreamTimeout, true},
			"secret":      {errs.E(errs.UpstreamFailed, errs.CodeUpstreamSecretUnavailable, "x"), errs.CodeUpstreamSecretUnavailable, false},
			"cancelled":   {context.Canceled, errs.CodeShuttingDown, false},
			"unreachable": {errors.New("boom"), errs.CodeUpstreamUnreachable, true},
		} {
			f := e.classifyError(rem, target, tc.err, false)
			if f.err.Code != tc.code || f.count != tc.count {
				t.Errorf("%s: %s count=%v, want %s %v", name, f.err.Code, f.count, tc.code, tc.count)
			}
		}
		if f := e.classifyError(rem, target, errors.New("x"), true); f.err.Code != errs.CodeUpstreamTimeout || !f.count {
			t.Errorf("a silent body: %+v", f)
		}
	})
}

type fakeNegatives struct {
	entry cluster.NegativeEntry
	found bool
	err   error
	puts  int
}

func (f *fakeNegatives) Lookup(context.Context, int64, cluster.FlightKey) (cluster.NegativeEntry, bool, error) {
	return f.entry, f.found, f.err
}

func (f *fakeNegatives) Record(context.Context, int64, cluster.FlightKey, cluster.NegativeEntry, time.Duration) error {
	f.puts++
	return f.err
}

type fakeInvalid struct{ validAfter time.Time }

func (f fakeInvalid) MetadataValid(int64, string, time.Time) bool { return true }
func (f fakeInvalid) NegativeValid(_ int64, _ string, sentAt time.Time) bool {
	return !sentAt.Add(-5 * time.Second).Before(f.validAfter)
}

type countedLookups struct{ rejected int }

func (c *countedLookups) CountCacheLookup(_, result string) {
	if result == "rejected" {
		c.rejected++
	}
}

func TestNegativeReaders(t *testing.T) {
	now := time.Now()
	rem := Remote{id: 7}
	rem.Settings.Cache.NotFoundTTLSeconds = 60
	build := func(store cluster.NegativeStore, inv fakeInvalid, c LookupCounter) *negatives {
		reg := must(cache.NewRegistry(nil, nil))
		return &negatives{
			l1:    cache.Register(reg, CacheNegativeL1, cache.Options[negKey, negEntry]{MaxEntries: 10, SafetyTTL: time.Hour}),
			store: store, inv: inv, counter: c, now: func() time.Time { return now },
		}
	}
	ctx := context.Background()
	t.Run("R-REPO-028 a Valkey hit is copied into the L1 and answers there afterwards", func(t *testing.T) {
		fs := &fakeNegatives{found: true, entry: cluster.NegativeEntry{Status: 404, SentAt: now.Add(-time.Second)}}
		n := build(fs, fakeInvalid{}, nil)
		if _, ok := n.lookup(ctx, rem, "p", ""); !ok {
			t.Fatal("no hit")
		}
		fs.found = false
		if _, ok := n.lookup(ctx, rem, "p", ""); !ok {
			t.Fatal("the L1 did not answer")
		}
	})
	t.Run("R-REPO-028 an entry older than the period, or from the future beyond the allowance, does not count", func(t *testing.T) {
		for name, sent := range map[string]time.Time{"old": now.Add(-2 * time.Minute), "future": now.Add(10 * time.Second)} {
			c := &countedLookups{}
			n := build(&fakeNegatives{found: true, entry: cluster.NegativeEntry{Status: 404, SentAt: sent}}, fakeInvalid{}, c)
			if _, ok := n.lookup(ctx, rem, "p", ""); ok || c.rejected != 1 {
				t.Errorf("%s: ok=%v rejected=%d", name, ok, c.rejected)
			}
		}
		n := build(&fakeNegatives{found: true, entry: cluster.NegativeEntry{Status: 404, SentAt: now.Add(4 * time.Second)}}, fakeInvalid{}, nil)
		if _, ok := n.lookup(ctx, rem, "p", ""); !ok {
			t.Error("an entry 4 s ahead is inside the 5 s allowance")
		}
	})
	t.Run("R-REPO-028 an invalidation newer than the entry minus 5 s refuses it, in the L1 as well", func(t *testing.T) {
		c := &countedLookups{}
		fs := &fakeNegatives{found: true, entry: cluster.NegativeEntry{Status: 404, SentAt: now.Add(-30 * time.Second)}}
		inv := fakeInvalid{validAfter: now.Add(-60 * time.Second)}
		n := build(fs, inv, c)
		if _, ok := n.lookup(ctx, rem, "p", ""); !ok {
			t.Fatal("no hit before the invalidation")
		}
		n.inv = fakeInvalid{validAfter: now} // an invalidation arrives
		if _, ok := n.lookup(ctx, rem, "p", ""); ok || c.rejected != 1 {
			t.Fatalf("ok=%v rejected=%d: the L1 entry must be refused and Valkey's too", ok, c.rejected)
		}
	})
	t.Run("R-REPO-028 with Valkey unavailable only the L1 answers, and notFoundTtlSeconds 0 records nothing", func(t *testing.T) {
		n := build(&fakeNegatives{err: cluster.ErrUnavailable}, fakeInvalid{}, nil)
		if _, ok := n.lookup(ctx, rem, "p", ""); ok {
			t.Fatal("hit from an unavailable Valkey")
		}
		if err := n.record(ctx, rem, "p", "", 404, now); err != nil {
			t.Fatalf("an unavailable Valkey must not fail the request: %v", err)
		}
		if _, ok := n.lookup(ctx, rem, "p", ""); !ok {
			t.Fatal("the L1 did not keep the entry")
		}
		off := rem
		off.Settings.Cache.NotFoundTTLSeconds = 0
		fs := &fakeNegatives{}
		n = build(fs, fakeInvalid{}, nil)
		_ = n.record(ctx, off, "p", "", 404, now)
		if fs.puts != 0 {
			t.Fatal("a period of 0 recorded an entry")
		}
	})
}

func TestPrefixEnd(t *testing.T) {
	for in, want := range map[string]string{"pkg/e/": "pkg/e0", "a": "b", "": "", "\xff": "", "a\xff": "b"} {
		if got := prefixEnd(in); got != want {
			t.Errorf("prefixEnd(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWithStatusKeepsTheError(t *testing.T) {
	t.Run("R-REPO-047 an error answer carries the Cache-Status header and keeps its code", func(t *testing.T) {
		orig := assumedOffline(time.Minute)
		err := withStatus(orig, CacheStatus{Forwarded: "uri-miss", FwdStatus: 503})
		rec := httptest.NewRecorder()
		errs.Write(rec, httptest.NewRequest("GET", "/", nil), err, "id", nil)
		if rec.Code != 503 || rec.Header().Get("Cache-Status") != "trove; fwd=uri-miss; fwd-status=503" || rec.Header().Get("Retry-After") != "60" {
			t.Fatalf("%d %v", rec.Code, rec.Header())
		}
		if orig.Headers != nil {
			t.Fatal("the original error was changed")
		}
	})
}
