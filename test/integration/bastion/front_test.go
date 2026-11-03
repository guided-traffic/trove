//go:build integration

package bastion

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
)

// front is the ingress of the main installation as the Bastion meets it: a TLS listener (the Bastion talks to
// the main endpoint over TLS only, R-BAS-044) in front of the web replica's plain listener. It is also the
// test's eye on the wire: every request the Bastion sends to the main installation is recorded with its
// answer, and the test can cut the link, delay or hold requests, and alter the interface version of a request
// or an answer, which is how a main installation of another InterfaceVersion is simulated (Q-BAS-105).
//
// The front keeps the forwarded headers as the Bastion wrote them (the main installation's
// server.trusted_proxies lists the front's address, so the forwarded-for entry is resolved to the caller's
// address) and sets the Host of the main endpoint, as the ingress rule of a deployment does.
type front struct {
	t      *testing.T
	srv    *httptest.Server
	ca     *tlstest.CA
	caFile string

	target     atomic.Pointer[string]
	down       atomic.Bool
	reqVersion atomic.Int64 // when non-zero, the interface version the request carries to the main installation
	resVersion atomic.Int64 // when non-zero, the interface version the answer claims

	mu    sync.Mutex
	recs  []rec
	delay time.Duration
	gate  func(*http.Request) // called before the request is passed on; may block
	hold  func(*http.Request, *http.Response)

	cur, peak atomic.Int64 // forwarded reads in flight at the main installation, and the most there were
}

// rec is one request the Bastion sent to the main installation, with what came back.
type rec struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Status int
	Bytes  int64
	Kind   string // handshake, check, feed, downloads or forward
	Probe  bool
	At     time.Time
}

func newFront(t *testing.T, dir, target string) *front {
	t.Helper()
	f := &front{t: t}
	f.ca = tlstest.NewCA(t, "bastion-it-front")
	f.caFile = f.ca.WriteCA(t, dir, "front-ca.pem")
	leaf := f.ca.Issue(t, tlstest.Options{DNSNames: []string{"localhost"}, IPs: []net.IP{net.IPv4(127, 0, 0, 1)}, NotAfter: time.Now().Add(6 * time.Hour)})
	f.target.Store(&target)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			u, err := url.Parse("http://" + *f.target.Load())
			if err != nil {
				t.Error(err)
				return
			}
			pr.SetURL(u)
			pr.Out.Host = mainHost
			for _, k := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Port", "Forwarded"} {
				if v, ok := pr.In.Header[k]; ok {
					pr.Out.Header[k] = v
				}
			}
			if v := f.reqVersion.Load(); v != 0 && pr.Out.Header.Get(iface.HeaderInterface) != "" {
				pr.Out.Header.Set(iface.HeaderInterface, itoa(v))
			}
		},
		ModifyResponse: func(res *http.Response) error {
			f.mu.Lock()
			hold := f.hold
			f.mu.Unlock()
			if hold != nil {
				hold(res.Request, res)
			}
			if v := f.resVersion.Load(); v != 0 && res.Header.Get(iface.HeaderInterface) != "" {
				res.Header.Set(iface.HeaderInterface, itoa(v))
			}
			return nil
		},
		FlushInterval: -1,
		Transport:     &http.Transport{DisableCompression: true, MaxIdleConnsPerHost: 64},
	}
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.down.Load() {
			f.cut(w)
			return
		}
		f.mu.Lock()
		delay, gate := f.delay, f.gate
		f.mu.Unlock()
		if gate != nil {
			gate(r)
		}
		if delay > 0 {
			time.Sleep(delay)
		}
		// The record is made when the answer's status line is about to go to the Bastion, so that a test that has seen
		// the Bastion's answer finds the request in Since: a record made after the proxy returned would race with
		// the Bastion answering its caller as soon as it has read the last byte.
		rw := &recorder{ResponseWriter: w, status: http.StatusOK}
		rw.record = func() {
			f.mu.Lock()
			f.recs = append(f.recs, rec{
				Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Status: rw.status,
				Kind: kindOf(r), Probe: r.Header.Get(iface.HeaderProbe) != "", At: time.Now(),
			})
			rw.idx = len(f.recs) - 1
			f.mu.Unlock()
		}
		if kindOf(r) == "forward" {
			n := f.cur.Add(1)
			for {
				p := f.peak.Load()
				if n <= p || f.peak.CompareAndSwap(p, n) {
					break
				}
			}
			defer f.cur.Add(-1)
		}
		proxy.ServeHTTP(rw, r)
		rw.once.Do(rw.record)
		f.mu.Lock()
		f.recs[rw.idx].Bytes = rw.n
		f.mu.Unlock()
	}))
	f.srv.TLS = nil
	f.srv.TLS = tlsServerConfig(leaf)
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	return f
}

// cut drops the connection without an answer: what a network cut gives the Bastion's transport.
func (f *front) cut(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	c, _, err := hj.Hijack()
	if err == nil {
		_ = c.Close()
	}
}

// SetTarget points the front at another replica of the main installation.
func (f *front) SetTarget(addr string) { f.target.Store(&addr) }

// URL is the main endpoint as the Bastion's bastion.main.url names it.
func (f *front) URL() string { return f.srv.URL }

// SetDown cuts the link or restores it.
func (f *front) SetDown(down bool) {
	f.down.Store(down)
	if down {
		f.srv.CloseClientConnections()
	}
}

// SetDelay delays every request by d before it is passed on.
func (f *front) SetDelay(d time.Duration) { f.mu.Lock(); f.delay = d; f.mu.Unlock() }

// SetHold installs a function called when the main installation's answer has arrived and before it is passed
// on to the Bastion (nil removes it): the decision of the main installation is made, its delivery waits.
func (f *front) SetHold(h func(*http.Request, *http.Response)) {
	f.mu.Lock()
	f.hold = h
	f.mu.Unlock()
}

// Peak returns the most forwarded reads that were in flight at the main installation at once since ResetPeak.
func (f *front) Peak() int { return int(f.peak.Load()) }

// ResetPeak starts the count of Peak again.
func (f *front) ResetPeak() { f.peak.Store(f.cur.Load()) }

// SetGate installs a function called for every request before it is passed on (nil removes it).
func (f *front) SetGate(g func(*http.Request)) { f.mu.Lock(); f.gate = g; f.mu.Unlock() }

// Records returns a copy of what was recorded so far.
func (f *front) Records() []rec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rec(nil), f.recs...)
}

// Mark returns a position for Since.
func (f *front) Mark() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.recs) }

// Since returns the records after a mark.
func (f *front) Since(mark int) []rec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rec(nil), f.recs[mark:]...)
}

// Forwards returns the forwarded reads (not the interface operations, not probes) after a mark whose path
// has the prefix.
func (f *front) Forwards(mark int, prefix string) []rec {
	var out []rec
	for _, r := range f.Since(mark) {
		if r.Kind == "forward" && !r.Probe && strings.HasPrefix(r.Path, prefix) {
			out = append(out, r)
		}
	}
	return out
}

// Cost counts what a client request costs the main installation after a mark: forwarded reads, probes and
// checks. The Bastion's own polling (the feed, the handshake, the downloads) is not a cost of a request.
func (f *front) Cost(mark int) int {
	n := 0
	for _, r := range f.Since(mark) {
		if r.Kind == "forward" || r.Kind == "check" {
			n++
		}
	}
	return n
}

// Kind returns the records of an operation after a mark.
func (f *front) Kind(mark int, kind string) []rec {
	var out []rec
	for _, r := range f.Since(mark) {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// Artifacts returns the forwarded reads after a mark that transferred an artifact: a 200 with a body of a
// tarball or a package file.
func (f *front) Artifacts(mark int) []rec {
	var out []rec
	for _, r := range f.Since(mark) {
		if r.Kind == "forward" && r.Status == http.StatusOK && isArtifactPath(r.Path) {
			out = append(out, r)
		}
	}
	return out
}

func isArtifactPath(p string) bool {
	return strings.HasSuffix(p, ".tgz") || strings.HasSuffix(p, ".nupkg")
}

func kindOf(r *http.Request) string {
	switch r.URL.Path {
	case iface.PathHandshake:
		return "handshake"
	case iface.PathCheck:
		return "check"
	case iface.PathFeed:
		return "feed"
	case iface.PathDownloads:
		return "downloads"
	}
	return "forward"
}

// recorder notes the status and the size of an answer; it passes Flush on, which the proxy uses to stream.
type recorder struct {
	http.ResponseWriter
	status int
	n      int64
	idx    int
	once   sync.Once
	record func()
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.once.Do(r.record)
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	r.once.Do(r.record)
	n, err := r.ResponseWriter.Write(b)
	r.n += int64(n)
	return n, err
}

func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
