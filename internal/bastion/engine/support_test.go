package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/bastion/answers"
	"github.com/guided-traffic/trove/internal/bastion/cache"
	"github.com/guided-traffic/trove/internal/bastion/guard"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/link"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/bastion/shared"
	"github.com/guided-traffic/trove/internal/config"
)

// ---- configuration ----------------------------------------------------------------------------------------

type configT = config.BastionConfig

func testConfig(t *testing.T, mods ...func(*config.BastionConfig)) *config.BastionConfig {
	t.Helper()
	l, err := config.LoadBastion(config.Options{
		File: "/etc/trove/trove.yaml", Command: config.CommandServe,
		ReadFile: func(name string) ([]byte, error) {
			if name == "/etc/trove/trove.yaml" {
				return []byte("server:\n  base_url: https://packages.example.com\nbastion:\n  main:\n    url: https://trove.example.com\n    token_file: /nonexistent\n"), nil
			}
			return nil, os.ErrNotExist
		},
	})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	l.Config.Bastion.Cache.Volume.Dir = t.TempDir()
	for _, m := range mods {
		m(l.Config)
	}
	return l.Config
}

// ---- tokens -----------------------------------------------------------------------------------------------

func newToken(t *testing.T, typ token.Type) string {
	t.Helper()
	raw, err := token.Generate(typ)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// ---- the main installation's double -----------------------------------------------------------------------

// recorded is one request the double of the main installation received.
type recorded struct {
	Kind   mainclient.Kind
	Class  mainclient.Class
	Method string
	Target string
	Header http.Header
	INM    string
	Probe  bool
	Body   []byte
	Addr   string
	Check  *iface.CheckRequest
}

// fakeMain is the small double of mainclient.Client the tests use: handlers answer, every request is recorded.
type fakeMain struct {
	mu       sync.Mutex
	reqs     []recorded
	doFn     func(f mainclient.Forward) (*mainclient.Response, error)
	checkFn  func(req iface.CheckRequest) (iface.CheckAnswer, error)
	delay    time.Duration
	inflight atomic.Int64
	peak     atomic.Int64
	anon     atomic.Int64
	peakAnon atomic.Int64
	// budget, when set, is taken like the real client takes it.
	budget mainclient.Budget

	// The handshake and the feed, which the real link under the engine reads.
	hs      iface.Handshake
	hsErr   error
	feedAns iface.FeedAnswer
	feedErr error
}

var _ mainclient.Client = (*fakeMain)(nil)

func (m *fakeMain) record(r recorded) {
	m.mu.Lock()
	m.reqs = append(m.reqs, r)
	m.mu.Unlock()
}

func (m *fakeMain) requests() []recorded {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]recorded(nil), m.reqs...)
}

func (m *fakeMain) count(kinds ...mainclient.Kind) int {
	n := 0
	for _, r := range m.requests() {
		if len(kinds) == 0 {
			n++
			continue
		}
		for _, k := range kinds {
			if r.Kind == k {
				n++
			}
		}
	}
	return n
}

func (m *fakeMain) Handshake(context.Context) (iface.Handshake, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hs, m.hsErr
}

func (m *fakeMain) Feed(context.Context, iface.Cursor, int) (iface.FeedAnswer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.feedErr != nil {
		return iface.FeedAnswer{}, m.feedErr
	}
	ans := m.feedAns
	if ans.Cursor == "" {
		ans.Cursor = iface.Cursor{At: epoch}.String()
	}
	return ans, nil
}

func (m *fakeMain) Downloads(context.Context, iface.DownloadsRequest) error { return nil }

func (m *fakeMain) enter(ctx context.Context, class mainclient.Class) (func(), error) {
	var release func()
	if m.budget != nil {
		var err error
		if release, err = m.budget.Acquire(ctx, class); err != nil {
			return nil, err
		}
	}
	bump := func(c *atomic.Int64, peak *atomic.Int64) {
		n := c.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
	}
	bump(&m.inflight, &m.peak)
	if class == mainclient.Anonymous {
		bump(&m.anon, &m.peakAnon)
	}
	return func() {
		m.inflight.Add(-1)
		if class == mainclient.Anonymous {
			m.anon.Add(-1)
		}
		if release != nil {
			release()
		}
	}, nil
}

func (m *fakeMain) Check(ctx context.Context, class mainclient.Class, req iface.CheckRequest) (iface.CheckAnswer, error) {
	done, err := m.enter(ctx, class)
	if err != nil {
		return iface.CheckAnswer{}, err
	}
	defer done()
	cp := req
	m.record(recorded{Kind: mainclient.KindCheck, Class: class, Check: &cp, Addr: req.ClientAddress})
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return iface.CheckAnswer{}, ctx.Err()
		}
	}
	if m.checkFn == nil {
		return iface.CheckAnswer{}, errors.New("no check handler")
	}
	return m.checkFn(req)
}

func (m *fakeMain) Do(ctx context.Context, f mainclient.Forward) (*mainclient.Response, error) {
	done, err := m.enter(ctx, f.Class)
	if err != nil {
		return nil, err
	}
	defer done()
	r := recorded{Kind: f.Kind, Class: f.Class, Method: f.Request.Method, Target: f.Request.URL.RequestURI(), Header: f.Request.Header.Clone(), INM: f.IfNoneMatch, Probe: f.Probe, Addr: f.ClientAddress}
	if f.Body != nil {
		r.Body, _ = io.ReadAll(f.Body)
	}
	m.record(r)
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if m.doFn == nil {
		return nil, errors.New("no do handler")
	}
	return m.doFn(f)
}

// resp builds a marked response.
func resp(status int, m iface.Marking, hdr http.Header, body string) *mainclient.Response {
	if hdr == nil {
		hdr = http.Header{}
	}
	m.Set(hdr)
	hdr.Set("Content-Length", fmt.Sprint(len(body)))
	return &mainclient.Response{Status: status, Header: hdr, Body: io.NopCloser(strings.NewReader(body)), Marking: m, Marked: true}
}

// resp2 builds an unmarked response (a proxy in front of the main installation).
func respUnmarked(status int, hdr http.Header, body string) *mainclient.Response {
	if hdr == nil {
		hdr = http.Header{}
	}
	return &mainclient.Response{Status: status, Header: hdr, Body: io.NopCloser(strings.NewReader(body))}
}

func marking(class iface.Class, read iface.Read, repo string, sub iface.Subject) iface.Marking {
	return iface.Marking{Class: class, Repository: repo, Read: read, Subject: sub, Interface: iface.InterfaceVersion}
}

func tokenSubject(id string, exp *time.Time) iface.Subject {
	return iface.Subject{Kind: iface.SubjectToken, TokenID: id, PrincipalID: "p-" + id, ExpiresAt: exp}
}

// ---- the cache -------------------------------------------------------------------------------------------

// testCache is the real cache of package 5 on a volume in a temporary directory, with the helpers the tests
// use to see and to change what it holds.
type testCache struct {
	*cache.Cache
	dir   string
	fault *cache.Fault
}

func newTestCache(t *testing.T, cfg *config.BastionConfig, signer *cache.Signer, m *metrics.Metrics, now func() time.Time) *testCache {
	t.Helper()
	f := &cache.Fault{}
	cc := cfg.Bastion.Cache
	cc.Backend = "volume" // a test that sets the bucket backend in the configuration (for the cross-replica lock) still runs on a volume
	c, err := cache.New(context.Background(), cc, signer, cache.Options{Metrics: m, Fault: f, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return &testCache{Cache: c, dir: cfg.Bastion.Cache.Volume.Dir, fault: f}
}

// files lists the regular files below one top-level directory of the volume.
func (c *testCache) files(top string) []string {
	var out []string
	_ = filepath.WalkDir(filepath.Join(c.dir, top), func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func (c *testCache) objectCount() int { return len(c.files("sha256")) + len(c.files("documents")) }
func (c *testCache) entryCount() int  { return len(c.files("entries")) }

// objectPath is where the volume keeps the bytes of a content address.
func (c *testCache) objectPath(top, sum string) string {
	return filepath.Join(c.dir, top, sum[0:2], sum[2:4], sum)
}

// object reads the bytes of a content address from either area.
func (c *testCache) object(sum string) ([]byte, bool) {
	for _, top := range []string{"sha256", "documents"} {
		if b, err := os.ReadFile(c.objectPath(top, sum)); err == nil {
			return b, true
		}
	}
	return nil, false
}

// deleteObject removes the bytes behind an entry without touching the entry.
func (c *testCache) deleteObject(sum string) {
	for _, top := range []string{"sha256", "documents"} {
		_ = os.Remove(c.objectPath(top, sum))
	}
}

// putRaw replaces the bytes of an artifact's content address by other bytes: a replaced object.
func (c *testCache) putRaw(sum string, b []byte) {
	p := c.objectPath("sha256", sum)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		panic(err)
	}
	if err := os.WriteFile(p, b, 0o640); err != nil {
		panic(err)
	}
}

// entryRecords returns the stored entry records by file.
func (c *testCache) entryRecords() map[string][]byte {
	out := map[string][]byte{}
	for _, p := range c.files("entries") {
		b, _ := os.ReadFile(p)
		out[p] = b
	}
	return out
}

// ---- the link, the guard and the download buffer ----------------------------------------------------------

// The link is the real one (internal/bastion/link) over the double of the main installation, on the test clock:
// the tests move it through its states the way the main installation would, with the handshake and the feed.

// handshake is the answer of a main installation that admits anonymous requests and records downloads.
func handshake() iface.Handshake {
	return iface.Handshake{
		InterfaceVersion: iface.InterfaceVersion, ResponseBaseURL: "https://packages.example.com",
		AnonymousAccess: true, RecordDownloads: true, ServerTime: epoch,
	}
}

// fakeGuard is a guard double: a verdict per step, and a record of what it was asked.
type fakeGuard struct {
	mu      sync.Mutex
	blocked guard.Verdict
	request guard.Verdict
	miss    guard.Verdict
	timeout guard.Verdict
	addrs   []string
	failed  []string
	misses  int
	reqs    int
	// missLimit, when positive, refuses every miss from the missLimit+1st.
	missLimit int
	// missHook, when set, is called with the number of the miss before the verdict is given, outside the lock.
	missHook func(n int)
}

func newGuard() *fakeGuard {
	return &fakeGuard{blocked: guard.Allow(), request: guard.Allow(), miss: guard.Allow(), timeout: guard.Allow()}
}

func (g *fakeGuard) Blocked(_ context.Context, addr string) guard.Verdict {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.addrs = append(g.addrs, addr)
	return g.blocked
}

func (g *fakeGuard) Request(_ context.Context, _ string, _ guard.Credentialed) guard.Verdict {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reqs++
	return g.request
}

func (g *fakeGuard) Miss(context.Context, string, guard.Credentialed) guard.Verdict {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.misses++
	n := g.misses
	if g.missHook != nil {
		g.mu.Unlock()
		g.missHook(n)
		g.mu.Lock()
	}
	if g.missLimit > 0 && g.misses > g.missLimit {
		return guard.Verdict{Status: 429, Code: iface.CodeRateLimited, RetryAfter: 10 * time.Second, Reason: metrics.RefusedMissLimited}
	}
	return g.miss
}

func (g *fakeGuard) AuthTimeout(context.Context, string) guard.Verdict {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.timeout
}

func (g *fakeGuard) FailedAuth(_ context.Context, addr string) {
	g.mu.Lock()
	g.failed = append(g.failed, addr)
	g.mu.Unlock()
}

func (g *fakeGuard) failedCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.failed)
}

type fakeDownloads struct {
	mu     sync.Mutex
	events []iface.DownloadEvent
}

func (d *fakeDownloads) Add(e iface.DownloadEvent) {
	d.mu.Lock()
	d.events = append(d.events, e)
	d.mu.Unlock()
}
func (d *fakeDownloads) SetEnabled(bool)               {}
func (d *fakeDownloads) Run(ctx context.Context) error { <-ctx.Done(); return nil }
func (d *fakeDownloads) Drain(context.Context) error   { return nil }
func (d *fakeDownloads) list() []iface.DownloadEvent {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]iface.DownloadEvent(nil), d.events...)
}

// ---- the harness ------------------------------------------------------------------------------------------

type harness struct {
	t     *testing.T
	e     *engine
	cfg   *config.BastionConfig
	main  *fakeMain
	cache *testCache
	link  *link.Link
	guard *fakeGuard
	dl    *fakeDownloads
	feed  *answers.FeedLog
	m     *metrics.Metrics
	state *shared.State
	clock *testClock
}

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var epoch = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

// options tweak a harness before the engine is built.
type options struct {
	cfg    []func(*config.BastionConfig)
	signer bool
	state  *shared.State
	cache  *testCache
	main   *fakeMain
	guard  *fakeGuard
	clock  *testClock
	m      *metrics.Metrics
	// down makes the handshake fail from the start: the link is unreachable and has never handshaken.
	down bool
	// noFeed makes the feed fail from the start, so that the link has no history and is not reachable.
	noFeed bool
	// wrapMain wraps the client the engine is given (below the link's guard), for a test that counts bodies.
	wrapMain func(mainclient.Client) mainclient.Client
}

func newHarness(t *testing.T, o options) *harness {
	t.Helper()
	cfg := testConfig(t, o.cfg...)
	h := &harness{t: t, cfg: cfg, main: o.main, cache: o.cache, guard: o.guard, clock: o.clock, m: o.m, state: o.state}
	if h.main == nil {
		h.main = &fakeMain{}
	}
	if h.guard == nil {
		h.guard = newGuard()
	}
	if h.clock == nil {
		h.clock = &testClock{t: epoch}
	}
	if h.m == nil {
		m, err := metrics.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		h.m = m
	}
	if h.state == nil {
		h.state = shared.NewLocal(1, h.clock.now)
	}
	var signer *cache.Signer
	if o.signer {
		var err error
		if signer, err = cache.NewSigner(bytes.Repeat([]byte{7}, 32)); err != nil {
			t.Fatal(err)
		}
	}
	if h.cache == nil {
		h.cache = newTestCache(t, cfg, signer, h.m, h.clock.now)
	}
	h.main.mu.Lock()
	if h.main.hs.InterfaceVersion == 0 {
		h.main.hs = handshake()
	}
	if o.down {
		h.main.hsErr = errors.New("the main installation is down")
	}
	if o.noFeed {
		h.main.feedErr = errors.New("the feed is not available")
	}
	h.main.mu.Unlock()
	h.link = link.New(link.Options{Client: h.main, Config: link.ConfigFrom(cfg), Metrics: h.m, Now: h.clock.now})
	h.feed = h.link.Feed()
	h.dl = &fakeDownloads{}
	var client mainclient.Client = h.main
	if o.wrapMain != nil {
		client = o.wrapMain(client)
	}
	d := Deps{
		Config: cfg, Metrics: h.m, Replica: "bastion-0", Release: "test", Now: h.clock.now, Main: h.link.Guard(client),
		Cache: h.cache.Cache, Shared: h.state, Guard: h.guard, Downloads: h.dl, Link: h.link,
	}
	e, err := newFlow(d)
	if err != nil {
		t.Fatal(err)
	}
	d.Link.SetSink(e.answers)
	h.e = e
	h.tick()
	return h
}

// tick runs one round of the link: a handshake when due and a feed poll.
func (h *harness) tick() {
	h.t.Helper()
	h.link.Tick(context.Background())
}

// setUnreachable makes the main installation fail its handshake, or answer it again, and lets the link notice.
func (h *harness) setUnreachable(v bool) {
	h.t.Helper()
	h.main.mu.Lock()
	h.main.hsErr = nil
	if v {
		h.main.hsErr = errors.New("the main installation is down")
	}
	h.main.mu.Unlock()
	h.link.RequestHandshake()
	h.tick()
	if got := !h.link.Reachable(); got != v {
		h.t.Fatalf("unreachable = %v, want %v", got, v)
	}
}

// setAnonymous changes anonymousAccess of the handshake and lets the link read it.
func (h *harness) setAnonymous(v bool) {
	h.t.Helper()
	h.main.mu.Lock()
	h.main.hs.AnonymousAccess = v
	h.main.mu.Unlock()
	h.link.RequestHandshake()
	h.tick()
}

// get performs a request through the engine; header pairs are name, value.
func (h *harness) get(path string, hdr ...string) *httptest.ResponseRecorder {
	return h.do(http.MethodGet, path, nil, hdr...)
}

func (h *harness) do(method, path string, body io.Reader, hdr ...string) *httptest.ResponseRecorder {
	h.t.Helper()
	r := httptest.NewRequest(method, "https://packages.example.com"+path, body)
	r.RemoteAddr = "203.0.113.7:4711"
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.e.ServeHTTP(w, r)
	return w
}

func bearer(tok string) string { return "Bearer " + tok }

// problemCode reads the code of a problem details body.
func problemCode(w *httptest.ResponseRecorder) string {
	body := w.Body.String()
	i := strings.Index(body, `"code":"`)
	if i < 0 {
		return ""
	}
	rest := body[i+len(`"code":"`):]
	return rest[:strings.Index(rest, `"`)]
}

func counterValue(t *testing.T, h *harness, name string, labels ...string) float64 {
	t.Helper()
	var v float64
	switch name {
	case "requests":
		v = readCounter(h.m.Requests.WithLabelValues(labels...))
	case "refused":
		v = readCounter(h.m.Refused.WithLabelValues(labels...))
	case "bytes":
		v = readCounter(h.m.BytesServed.WithLabelValues(labels...))
	case "answers":
		v = readCounter(h.m.AnswersTotal.WithLabelValues(labels...))
	default:
		t.Fatalf("unknown counter %s", name)
	}
	return v
}
