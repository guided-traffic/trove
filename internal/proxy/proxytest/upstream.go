package proxytest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
)

// Route is how the upstream answers one path.
type Route struct {
	// Status is the status; 0 is 200.
	Status int
	Body   []byte
	Type   string
	// ETag and Modified make the route answer a matching conditional request with 304.
	ETag     string
	Modified time.Time
	Header   map[string]string
	// Delay is the wait before the response; Chunk and ChunkDelay stream the body
	// slowly, ChunkDelay after each Chunk bytes.
	Delay      time.Duration
	Chunk      int
	ChunkDelay time.Duration
	// Failures is how many requests fail with 503 before the route answers as set.
	Failures int
	// Handler replaces the route's answer.
	Handler http.HandlerFunc
}

// Seen is one request the upstream got.
type Seen struct {
	Method, Path, RawPath string
	Conditional           bool
	IfNoneMatch           string
	Authorization         string
	Host                  string
	At                    time.Time
}

// Upstream is a counting TLS upstream. Requests for a path without a route are 404.
type Upstream struct {
	*httptest.Server

	mu     sync.Mutex
	routes map[string]*Route
	seen   []Seen
	failed map[string]int
	total  atomic.Int64
	// aliases are the host names the upstream is also reached under ([Upstream.As]).
	aliases []string
}

// NewUpstream starts an upstream that is closed with the test.
func NewUpstream(t testing.TB) *Upstream {
	t.Helper()
	u := &Upstream{routes: map[string]*Route{}, failed: map[string]int{}}
	u.Server = httptest.NewUnstartedServer(http.HandlerFunc(u.serve))
	u.TLS = &tls.Config{Certificates: []tls.Certificate{certificate(t)}, MinVersion: tls.VersionTLS12}
	u.StartTLS()
	t.Cleanup(u.Close)
	return u
}

// certificate is a self-signed certificate for the loopback addresses and for every name below example.com and example.test,
// which a test reaches the upstream under ([Upstream.As]).
func certificate(t testing.TB) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "trove proxytest upstream"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
		DNSNames:    []string{"localhost", "example.com", "*.example.com", "example.test", "*.example.test"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// As returns the base URL under which the upstream is reached as the host name host, and makes every factory built with it
// ([Factory]) connect that name to the upstream's listener. The name is a different host than the upstream's own URL for every
// rule that reads URLs: a token service or a blob host of a test is a second upstream reached as "auth.example.com" or
// "cdn.example.com", and the credential rules of R-REPO-033 are exercised on host names and not on ports. The certificate of the
// test server is valid for every name below example.com and example.test.
func (u *Upstream) As(host string) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.aliases = append(u.aliases, host)
	_, port, _ := net.SplitHostPort(u.Listener.Addr().String())
	return "https://" + host + ":" + port
}

// Set installs the route of a path ("/pkg/lodash"); it replaces the previous one.
func (u *Upstream) Set(path string, r Route) {
	u.mu.Lock()
	defer u.mu.Unlock()
	rr := r
	u.routes[path] = &rr
	delete(u.failed, path)
}

// Bytes sets a 200 route with a body.
func (u *Upstream) Bytes(path string, body []byte, ctype string) {
	u.Set(path, Route{Body: body, Type: ctype})
}

// Remove drops the route of a path: it answers 404 again.
func (u *Upstream) Remove(path string) {
	u.mu.Lock()
	delete(u.routes, path)
	u.mu.Unlock()
}

// Count is the number of requests of a method for a path ("" counts every method).
func (u *Upstream) Count(method, path string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	for _, s := range u.seen {
		if s.Path == path && (method == "" || s.Method == method) {
			n++
		}
	}
	return n
}

// Conditional is the number of conditional requests for a path.
func (u *Upstream) Conditional(path string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	for _, s := range u.seen {
		if s.Path == path && s.Conditional {
			n++
		}
	}
	return n
}

// Total is the number of requests the upstream has seen.
func (u *Upstream) Total() int { return int(u.total.Load()) }

// Seen returns the requests in arrival order.
func (u *Upstream) Seen() []Seen {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]Seen(nil), u.seen...)
}

// Reset forgets the requests seen.
func (u *Upstream) Reset() {
	u.mu.Lock()
	u.seen = nil
	u.mu.Unlock()
	u.total.Store(0)
}

func (u *Upstream) serve(w http.ResponseWriter, r *http.Request) {
	u.total.Add(1)
	u.mu.Lock()
	u.seen = append(u.seen, Seen{
		Method: r.Method, Path: r.URL.Path, RawPath: r.URL.EscapedPath(),
		Conditional: r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "",
		IfNoneMatch: r.Header.Get("If-None-Match"), Authorization: r.Header.Get("Authorization"), Host: r.Host, At: time.Now(),
	})
	rt := u.routes[r.URL.Path]
	if rt == nil {
		u.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	r2 := *rt
	if r2.Failures > 0 && u.failed[r.URL.Path] < r2.Failures {
		u.failed[r.URL.Path]++
		u.mu.Unlock()
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	u.mu.Unlock()
	if r2.Handler != nil {
		r2.Handler(w, r)
		return
	}
	if r2.Delay > 0 {
		select {
		case <-time.After(r2.Delay):
		case <-r.Context().Done():
			return
		}
	}
	h := w.Header()
	if r2.Type != "" {
		h.Set("Content-Type", r2.Type)
	}
	if r2.ETag != "" {
		h.Set("ETag", r2.ETag)
	}
	if !r2.Modified.IsZero() {
		h.Set("Last-Modified", r2.Modified.UTC().Format(http.TimeFormat))
	}
	for k, v := range r2.Header {
		h.Set(k, v)
	}
	if r2.ETag != "" && r.Header.Get("If-None-Match") == r2.ETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if ims, err := http.ParseTime(r.Header.Get("If-Modified-Since")); err == nil && !r2.Modified.IsZero() && !r2.Modified.After(ims) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	status := r2.Status
	if status == 0 {
		status = http.StatusOK
	}
	h.Set("Content-Length", strconv.Itoa(len(r2.Body)))
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	if r2.Chunk <= 0 {
		_, _ = w.Write(r2.Body)
		return
	}
	for i := 0; i < len(r2.Body); i += r2.Chunk {
		end := min(i+r2.Chunk, len(r2.Body))
		if _, err := w.Write(r2.Body[i:end]); err != nil {
			return
		}
		w.(http.Flusher).Flush()
		select {
		case <-time.After(r2.ChunkDelay):
		case <-r.Context().Done():
			return
		}
	}
}

// Factory returns an outbound factory that trusts the certificates of the upstreams
// and allows loopback, as the integration environment does.
func Factory(t testing.TB, ups ...*Upstream) *outbound.Factory {
	t.Helper()
	return FactoryWithLog(t, nil, ups...)
}

// FactoryWithLog is Factory with the upstream log (R-OPS-011) written to log.
func FactoryWithLog(t testing.TB, log *slog.Logger, ups ...*Upstream) *outbound.Factory {
	t.Helper()
	cfg := config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true, MinVersion: "1.2"}}
	for _, k := range config.MustDescribe().Keys {
		if k.Path == "proxy.outbound.address_policy.deny_cidrs" {
			if err := json.Unmarshal([]byte(k.Default), &cfg.AddressPolicy.DenyCIDRs); err != nil {
				t.Fatal(err)
			}
		}
	}
	cfg.AddressPolicy.AllowCIDRs = []string{"127.0.0.0/8", "::1/128"}
	// The names the upstreams are reached under (Upstream.As) connect to their listeners.
	dial := map[string]string{}
	for _, u := range ups {
		u.mu.Lock()
		_, port, _ := net.SplitHostPort(u.Listener.Addr().String())
		for _, h := range u.aliases {
			dial[h+":"+port] = u.Listener.Addr().String()
		}
		u.mu.Unlock()
	}
	dir := t.TempDir()
	for i, u := range ups {
		p := filepath.Join(dir, "ca"+strconv.Itoa(i)+".pem")
		b := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: u.Certificate().Raw})
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg.TLS.CAFiles = append(cfg.TLS.CAFiles, p)
	}
	opts := []outbound.Option{outbound.WithDialOverride(dial)}
	if log != nil {
		opts = append(opts, outbound.WithUpstreamLog(log))
	}
	return outbound.New(cfg, opts...)
}

// SHA256 is the SHA-256 of a body, for the digests a test announces.
func SHA256(b []byte) []byte { s := sha256.Sum256(b); return s[:] }

// SHA512 is the SHA-512 of a body.
func SHA512(b []byte) []byte { s := sha512.Sum512(b); return s[:] }

// Body returns n bytes that depend on seed and do not compress.
func Body(seed string, n int) []byte {
	out := make([]byte, 0, n)
	for i := 0; len(out) < n; i++ {
		s := sha256.Sum256([]byte(seed + strconv.Itoa(i)))
		out = append(out, s[:]...)
	}
	return out[:n]
}
