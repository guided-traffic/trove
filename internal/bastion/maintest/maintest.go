// Package maintest is an in-process main installation for the tests of the Bastion (R-BAS-048): an httptest TLS
// server that speaks the four operations of the interface with the types of internal/bastion/iface, answers
// forwarded requests with a marking, and can be switched, delayed, revoked and cut off from the test. It is a
// test helper and nothing in the role imports it.
//
// What it models of the main installation: the Bastion token check (401 bastion_token_invalid), the interface
// version check (400 bastion_interface_mismatch), the handshake with the response base URL derived from the
// forwarded headers of R-BAS-017, the feed with its lookback, entry IDs, paging and reset (R-BAS-015), the
// downloads intake, and the recording of every request the Bastion sent. What it does not model: users,
// policies, repositories, formats. The check answer and the forwarded answer are functions the test sets.
package maintest

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"hash/crc32"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/iface"
)

// Release is the release the fake reports in the handshake.
const Release = "0.0.0-maintest"

// Lookback is the feed lookback of the fake (bastions.feed.lookback).
const Lookback = 5 * time.Minute

// Main is the fake main installation.
type Main struct {
	srv *httptest.Server
	t   testing.TB

	mu            sync.Mutex
	version       int
	now           func() time.Time
	tokens        map[string]*regToken
	byID          map[string]*regToken
	entries       []iface.FeedEntry
	seq           int
	honoured      bool
	anonymous     bool
	record        bool
	delay         time.Duration
	down          bool
	forced        map[string]int
	forward       http.HandlerFunc
	check         func(iface.CheckRequest) iface.CheckAnswer
	forwards      []Forwarded
	checks        []iface.CheckRequest
	downloads     []iface.DownloadsRequest
	feedReads     []string
	replicas      map[string]time.Time
	calls         map[string]int
	resetNext     bool
	downloadsFail int

	headers map[string]http.Header

	inFlight    atomic.Int64
	maxInFlight atomic.Int64
	anonFlight  atomic.Int64
	maxAnon     atomic.Int64
}

type regToken struct {
	id      string
	raw     string
	expires time.Time
	revoked bool
}

// Forwarded is a request the Bastion forwarded.
type Forwarded struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   []byte
	At     time.Time
}

// New starts the fake. It is closed with the test.
func New(t testing.TB) *Main {
	t.Helper()
	m := &Main{
		t: t, version: iface.InterfaceVersion, now: time.Now, tokens: map[string]*regToken{}, byID: map[string]*regToken{},
		honoured: true, record: true, forced: map[string]int{}, replicas: map[string]time.Time{}, calls: map[string]int{}, headers: map[string]http.Header{},
	}
	m.forward = m.defaultForward
	m.check = func(iface.CheckRequest) iface.CheckAnswer { return iface.CheckAnswer{} }
	m.srv = httptest.NewTLSServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	return m
}

// URL is the base URL of the fake (https, 127.0.0.1).
func (m *Main) URL() string { return m.srv.URL }

// Transport returns a transport that trusts the fake's certificate.
func (m *Main) Transport() http.RoundTripper { return m.srv.Client().Transport }

// CAFile writes the fake's certificate as a PEM bundle into dir and returns its path.
func (m *Main) CAFile(dir string) string {
	m.t.Helper()
	path := filepath.Join(dir, "main-ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: m.srv.Certificate().Raw})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		m.t.Fatal(err)
	}
	return path
}

// Certificate returns the fake's certificate.
func (m *Main) Certificate() *x509.Certificate { return m.srv.Certificate() }

// Pool returns a pool that trusts the fake.
func (m *Main) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(m.srv.Certificate())
	return p
}

// TLSConfig returns a client configuration that trusts the fake.
func (m *Main) TLSConfig() *tls.Config {
	return &tls.Config{RootCAs: m.Pool(), MinVersion: tls.VersionTLS12}
}

// SetClock replaces the fake's clock (the timestamps of entries and serverTime).
func (m *Main) SetClock(now func() time.Time) { m.lock(func() { m.now = now }) }

func (m *Main) lock(f func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f()
}

// NewToken issues a valid Bastion token that expires in a year and returns it.
func (m *Main) NewToken() string { return m.NewTokenExpiring(m.nowTime().Add(365 * 24 * time.Hour)) }

func (m *Main) nowTime() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now()
}

// NewTokenExpiring issues a valid Bastion token with the given expiry.
func (m *Main) NewTokenExpiring(expires time.Time) string {
	raw := GenerateToken(m.t)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	rt := &regToken{id: fmt.Sprintf("tok-%03d", m.seq), raw: raw, expires: expires}
	m.tokens[raw] = rt
	m.byID[rt.id] = rt
	return raw
}

// TokenID returns the ID the fake gave a token it issued.
func (m *Main) TokenID(raw string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rt := m.tokens[raw]; rt != nil {
		return rt.id
	}
	return ""
}

// Revoke revokes a token and adds the feed entry of the kind token (R-BAS-015), as one commit.
func (m *Main) Revoke(raw string) iface.FeedEntry {
	m.mu.Lock()
	rt := m.tokens[raw]
	if rt == nil {
		m.mu.Unlock()
		m.t.Fatalf("maintest: unknown token")
	}
	rt.revoked = true
	m.mu.Unlock()
	return m.AddEntry(iface.FeedEntry{Kind: iface.FeedToken, Tokens: []string{rt.id}})
}

// AddEntry appends a feed entry; the ID and the time are the fake's unless set. It returns the entry as stored.
func (m *Main) AddEntry(e iface.FeedEntry) iface.FeedEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	if e.ID == "" {
		e.ID = fmt.Sprintf("evt-%05d", m.seq)
	}
	if e.At.IsZero() {
		e.At = m.now().UTC()
	}
	m.entries = append(m.entries, e)
	return e
}

// Reset makes the next feed read answer reset: true.
func (m *Main) Reset() { m.lock(func() { m.resetNext = true }) }

// SetVersion changes the interface version the fake speaks.
func (m *Main) SetVersion(v int) { m.lock(func() { m.version = v }) }

// SetBaseURLHonoured selects whether the handshake reports the base URL the Bastion sent (true, the default) or
// the fake's own URL (the main installation lacks the Bastion's host in server.additional_base_urls).
func (m *Main) SetBaseURLHonoured(on bool) { m.lock(func() { m.honoured = on }) }

// SetAnonymous sets anonymousAccess of the handshake.
func (m *Main) SetAnonymous(on bool) { m.lock(func() { m.anonymous = on }) }

// SetRecordDownloads sets recordDownloads of the handshake.
func (m *Main) SetRecordDownloads(on bool) { m.lock(func() { m.record = on }) }

// SetDelay makes every request wait before it is answered.
func (m *Main) SetDelay(d time.Duration) { m.lock(func() { m.delay = d }) }

// SetDown cuts the fake off: every connection is closed without an answer.
func (m *Main) SetDown(down bool) { m.lock(func() { m.down = down }) }

// Force makes an operation ("handshake", "check", "feed", "downloads", "forward") answer a status with no
// body; zero removes it.
func (m *Main) Force(op string, status int) {
	m.lock(func() {
		if status == 0 {
			delete(m.forced, op)
		} else {
			m.forced[op] = status
		}
	})
}

// FailDownloads makes the next n downloads batches answer 503.
func (m *Main) FailDownloads(n int) { m.lock(func() { m.downloadsFail = n }) }

// SetForward sets the handler that answers forwarded requests after the Bastion token was checked. The default
// answers 200 with the marking class pass.
func (m *Main) SetForward(h http.HandlerFunc) { m.lock(func() { m.forward = h }) }

// SetCheck sets the function that answers the check.
func (m *Main) SetCheck(f func(iface.CheckRequest) iface.CheckAnswer) { m.lock(func() { m.check = f }) }

// Calls returns how many requests of an operation arrived: "handshake", "check", "feed", "downloads", "forward".
func (m *Main) Calls(op string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[op]
}

// Forwards returns the forwarded requests received so far.
func (m *Main) Forwards() []Forwarded {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Forwarded(nil), m.forwards...)
}

// Checks returns the check requests received so far.
func (m *Main) Checks() []iface.CheckRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]iface.CheckRequest(nil), m.checks...)
}

// Downloads returns the accepted batches in arrival order.
func (m *Main) Downloads() []iface.DownloadsRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]iface.DownloadsRequest(nil), m.downloads...)
}

// DownloadEvents returns the events of all accepted batches.
func (m *Main) DownloadEvents() []iface.DownloadEvent {
	var out []iface.DownloadEvent
	for _, b := range m.Downloads() {
		out = append(out, b.Events...)
	}
	return out
}

// FeedReads returns the raw `after` parameters of the feed reads.
func (m *Main) FeedReads() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.feedReads...)
}

// Replicas returns the Trove-Bastion-Replica values seen, with the time of the last request.
func (m *Main) Replicas() map[string]time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]time.Time, len(m.replicas))
	for k, v := range m.replicas {
		out[k] = v
	}
	return out
}

// MaxInFlight is the highest number of requests being handled at once.
func (m *Main) MaxInFlight() int { return int(m.maxInFlight.Load()) }

// MaxAnonymousInFlight is the highest number of forwarded requests without an Authorization header being
// handled at once.
func (m *Main) MaxAnonymousInFlight() int { return int(m.maxAnon.Load()) }

// Headers returns the headers of the last request of an operation, or nil.
func (m *Main) Headers(op string) http.Header {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.headers[op]
}

// operation classifies a request path.
func operation(r *http.Request) string {
	switch r.URL.Path {
	case iface.PathHandshake:
		return iface.OperationHandshake
	case iface.PathCheck:
		return iface.OperationCheck
	case iface.PathFeed:
		return iface.OperationFeed
	case iface.PathDownloads:
		return iface.OperationDownloads
	}
	return "forward"
}

// raise lifts a high-water mark to n.
func raise(hi *atomic.Int64, n int64) {
	for {
		cur := hi.Load()
		if n <= cur || hi.CompareAndSwap(cur, n) {
			return
		}
	}
}

func (m *Main) serve(w http.ResponseWriter, r *http.Request) {
	op := operation(r)
	n := m.inFlight.Add(1)
	defer m.inFlight.Add(-1)
	raise(&m.maxInFlight, n)
	if op == "forward" && r.Header.Get("Authorization") == "" {
		raise(&m.maxAnon, m.anonFlight.Add(1))
		defer m.anonFlight.Add(-1)
	}
	m.mu.Lock()
	down, delay, version, forced := m.down, m.delay, m.version, m.forced[op]
	m.calls[op]++
	m.headers[op] = r.Header.Clone()
	if rep := r.Header.Get(iface.HeaderReplica); rep != "" {
		m.replicas[rep] = m.now()
	}
	m.mu.Unlock()
	if down {
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				_ = c.Close()
			}
		}
		return
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set(iface.HeaderInterface, strconv.Itoa(version))
	if forced != 0 {
		w.WriteHeader(forced)
		return
	}
	// The interface version check comes first (R-BAS-009), then the token.
	if r.Header.Get(iface.HeaderInterface) != strconv.Itoa(version) {
		m.problem(w, http.StatusBadRequest, iface.CodeInterfaceMismatch, version)
		return
	}
	bearer := r.Header.Get("Authorization")
	if op == "forward" {
		bearer = r.Header.Get(iface.HeaderAuthorization)
	}
	rt := m.validToken(strings.TrimPrefix(bearer, iface.BearerScheme+" "))
	if rt == nil {
		if op == "forward" && bearer == "" {
			m.forwardUnrecognized(w, r)
			return
		}
		m.problem(w, http.StatusUnauthorized, iface.CodeTokenInvalid, 0)
		return
	}
	switch op {
	case iface.OperationHandshake:
		m.handshake(w, r, rt)
	case iface.OperationCheck:
		m.checkOp(w, r)
	case iface.OperationFeed:
		m.feed(w, r)
	case iface.OperationDownloads:
		m.downloadsOp(w, r)
	default:
		m.forwardOp(w, r)
	}
}

func (m *Main) validToken(raw string) *regToken {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt := m.tokens[raw]
	if rt == nil || rt.revoked || !m.now().Before(rt.expires) {
		return nil
	}
	return rt
}

func (m *Main) problem(w http.ResponseWriter, status int, code string, version int) {
	w.Header().Set("Content-Type", iface.ProblemMediaType)
	w.WriteHeader(status)
	body := map[string]any{"type": "about:blank", "title": http.StatusText(status), "status": status, "code": code}
	if code == iface.CodeInterfaceMismatch {
		body[iface.ProblemMemberInterfaceVersion] = version
	}
	_ = json.NewEncoder(w).Encode(body)
}

func (m *Main) writeJSON(w http.ResponseWriter, v any) {
	b, err := iface.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", iface.MediaType)
	_, _ = w.Write(b)
}

func (m *Main) handshake(w http.ResponseWriter, r *http.Request, rt *regToken) {
	m.mu.Lock()
	base := m.srv.URL
	if m.honoured {
		base = forwardedOrigin(r, m.srv.URL)
	}
	hs := iface.Handshake{
		InterfaceVersion: m.version,
		Bastion:          iface.HandshakeBastion{Name: "partners", TokenID: rt.id, TokenExpiresAt: rt.expires.UTC()},
		Installation:     iface.HandshakeInstallation{BaseURL: m.srv.URL, Release: Release},
		ResponseBaseURL:  base,
		AnonymousAccess:  m.anonymous,
		RecordDownloads:  m.record,
		Feed:             iface.HandshakeFeed{Cursor: m.cursorLocked().String()},
		ServerTime:       m.now().UTC(),
	}
	m.mu.Unlock()
	m.writeJSON(w, hs)
}

// forwardedOrigin derives the origin from X-Forwarded-Proto, -Host and -Port as the main installation's
// resolver does (R-BAS-017), omitting a default port.
func forwardedOrigin(r *http.Request, fallback string) string {
	proto, host, port := r.Header.Get(iface.HeaderForwardedProto), r.Header.Get(iface.HeaderForwardedHost), r.Header.Get(iface.HeaderForwardedPort)
	if proto == "" || host == "" {
		return fallback
	}
	if port != "" && (proto != "https" || port != "443") && (proto != "http" || port != "80") {
		host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	return proto + "://" + host
}

// cursorLocked is the feed position now: the last entry, or a synthetic cursor at the clock.
func (m *Main) cursorLocked() iface.Cursor {
	if n := len(m.entries); n > 0 {
		return iface.Cursor{At: m.entries[n-1].At, ID: m.entries[n-1].ID}
	}
	return iface.Cursor{At: m.now().UTC()}
}

func (m *Main) checkOp(w http.ResponseWriter, r *http.Request) {
	var req iface.CheckRequest
	if err := iface.DecodeStrict(r.Body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	m.checks = append(m.checks, req)
	f := m.check
	m.mu.Unlock()
	m.writeJSON(w, f(req))
}

// feed answers like R-BAS-015: every entry after the cursor, plus every entry within the lookback behind it, in
// order, cut by limit.
func (m *Main) feed(w http.ResponseWriter, r *http.Request) {
	after := r.URL.Query().Get(iface.QueryAfter)
	limit, _ := strconv.Atoi(r.URL.Query().Get(iface.QueryLimit))
	cur, err := iface.ParseCursor(after)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.feedReads = append(m.feedReads, after)
	if m.resetNext {
		m.resetNext = false
		m.writeJSON(w, iface.FeedAnswer{Entries: []iface.FeedEntry{}, Cursor: iface.Cursor{At: m.now().UTC()}.String(), Reset: true})
		return
	}
	pos := -1
	if !cur.Synthetic() {
		for i, e := range m.entries {
			if e.ID == cur.ID {
				pos = i
			}
		}
		if pos < 0 {
			m.writeJSON(w, iface.FeedAnswer{Entries: []iface.FeedEntry{}, Cursor: iface.Cursor{At: m.now().UTC()}.String(), Reset: true})
			return
		}
	}
	// Two reads as R-BAS-015 says: every entry within the lookback up to and including the cursor, all of
	// them, and the entries after it, cut by the limit, so that a page always advances.
	out := []iface.FeedEntry{}
	more := false
	ahead := 0
	for i, e := range m.entries {
		within := e.At.After(cur.At.Add(-Lookback))
		if i <= pos {
			if within {
				out = append(out, e)
			}
			continue
		}
		if cur.Synthetic() && !within {
			continue
		}
		if limit > 0 && ahead == limit {
			more = true
			break
		}
		out = append(out, e)
		ahead++
	}
	next := after
	if n := len(out); n > 0 {
		next = iface.Cursor{At: out[n-1].At, ID: out[n-1].ID}.String()
	}
	m.writeJSON(w, iface.FeedAnswer{Entries: out, Cursor: next, More: more})
}

func (m *Main) downloadsOp(w http.ResponseWriter, r *http.Request) {
	var req iface.DownloadsRequest
	if err := iface.DecodeStrict(r.Body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	if m.downloadsFail > 0 {
		m.downloadsFail--
		m.mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	m.downloads = append(m.downloads, req)
	m.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// forwardUnrecognized answers a request without a Bastion token as an ordinary request, unmarked.
func (m *Main) forwardUnrecognized(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ordinary"))
}

func (m *Main) forwardOp(w http.ResponseWriter, r *http.Request) {
	body := make([]byte, 0, 64)
	if r.Body != nil {
		buf := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buf)
			body = append(body, buf[:n]...)
			if err != nil {
				break
			}
		}
	}
	m.mu.Lock()
	m.forwards = append(m.forwards, Forwarded{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: body, At: m.now()})
	h := m.forward
	m.mu.Unlock()
	h(w, r)
}

// defaultForward answers 200 with the marking class pass.
func (m *Main) defaultForward(w http.ResponseWriter, _ *http.Request) {
	iface.Marking{Class: iface.ClassPass, Interface: m.versionNow()}.Set(w.Header())
	_, _ = w.Write([]byte("forwarded"))
}

func (m *Main) versionNow() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.version
}

// Mark writes a marking onto a response header, for forward handlers set by tests.
func (m *Main) Mark(w http.ResponseWriter, mk iface.Marking) {
	mk.Interface = m.versionNow()
	mk.Set(w.Header())
}

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// GenerateToken returns a well-formed Bastion token (prefix, 43 random base62 digits, CRC-32 in 6 digits).
func GenerateToken(t testing.TB) string {
	t.Helper()
	return GenerateTokenWithPrefix(t, iface.TokenPrefix)
}

// GenerateTokenWithPrefix returns a well-formed token of the form of R-AUTH-022 with the given prefix, for example
// "trv_pat_": the credential of a caller of the Bastion in a test.
func GenerateTokenWithPrefix(t testing.TB, prefix string) string {
	t.Helper()
	random := make([]byte, 43)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	b := []byte(prefix)
	for _, x := range random {
		b = append(b, base62[int(x)%62])
	}
	sum := uint64(crc32.ChecksumIEEE(b))
	tail := make([]byte, 6)
	for i := 5; i >= 0; i-- {
		tail[i] = base62[sum%62]
		sum /= 62
	}
	return string(b) + string(tail)
}

// Faulty is a transport that fails requests on command, for tests of the grace period and of reconnection: a
// transport error as a network cut gives, without the fake being involved.
type Faulty struct {
	Next http.RoundTripper
	down atomic.Bool
	fail atomic.Int64
}

// SetDown cuts or restores the link.
func (f *Faulty) SetDown(down bool) { f.down.Store(down) }

// FailNext makes the next n requests fail.
func (f *Faulty) FailNext(n int) { f.fail.Store(int64(n)) }

// RoundTrip implements http.RoundTripper.
func (f *Faulty) RoundTrip(r *http.Request) (*http.Response, error) {
	if f.down.Load() || f.take() {
		if r.Body != nil {
			_ = r.Body.Close()
		}
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: fmt.Errorf("maintest: link cut")}
	}
	return f.Next.RoundTrip(r)
}

func (f *Faulty) take() bool {
	for {
		n := f.fail.Load()
		if n <= 0 {
			return false
		}
		if f.fail.CompareAndSwap(n, n-1) {
			return true
		}
	}
}

// MemSpool is an in-memory audit spool for the tests of the download buffer (the spool methods of
// cache.Backend). Files are named by a counter so that two writes never collide.
type MemSpool struct {
	mu    sync.Mutex
	n     int
	files map[string]memFile
	// FailWrites makes SpoolWrite fail, for the case of a spool that cannot be written.
	FailWrites bool
}

type memFile struct {
	replica string
	data    []byte
}

// NewMemSpool returns an empty spool.
func NewMemSpool() *MemSpool { return &MemSpool{files: map[string]memFile{}} }

// Files returns the contents of the spool, by file name.
func (s *MemSpool) Files() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for k, f := range s.files {
		out[f.replica+"/"+k] = string(f.data)
	}
	return out
}
