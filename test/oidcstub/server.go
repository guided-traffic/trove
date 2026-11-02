// Package oidcstub is a test double of a GitLab instance as an OpenID Connect issuer (spec
// docs/spec/gitlab-ci-oidc.md, R-CI-003, R-CI-004, R-CI-006): the discovery document and the key
// set over TLS, ID tokens in GitLab's shape (and the malformed ones the validation must refuse), keys that
// rotate, faults that switch on at run time and counters of the requests it received.
//
// A Go test uses it in process (New, Start or StartSelfSigned, Mint, AddKey, SetFault, Counts); the
// end-to-end tests on kind run it as the container of cmd/oidcstub and drive it through the
// control API (ControlHandler). It depends on the standard library only.
package oidcstub

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The paths GitLab serves. Self-managed instances serve the same ones below their base URL.
const (
	DiscoveryPath = "/.well-known/openid-configuration"
	JWKSPath      = "/oauth/discovery/keys"
)

// Config configures a Server. The zero value works.
type Config struct {
	// PublicURL is the issuer the double announces, "https://oidcstub.e2e.svc" for example.
	// Empty means the address it was started on (in process) or the Host of the request.
	PublicURL string
	// PlainURL is the base of the plain-HTTP listener as a client reaches it; empty means the
	// address it was started on.
	PlainURL string
	// Audience is the default "aud" of a minted token (DefaultAudience when empty).
	Audience string
	// NoDefaultKey starts without a key: the JWKS is empty until AddKey.
	NoDefaultKey bool
	// ControlToken is the bearer token of the control API; empty switches the API off.
	ControlToken string
	// Now is the clock of minted tokens (time.Now when nil).
	Now func() time.Time
	// Logger receives the listener's errors (discarded when nil).
	Logger *slog.Logger
}

// Server is the issuer double. All methods are safe for concurrent use.
type Server struct {
	cfg Config

	mu      sync.Mutex
	keys    []*Key // published and unpublished, oldest first
	retired []*Key // removed from the JWKS, still able to sign
	fault   Fault
	cache   Cache
	counts  Counts
	base    string // the address it was started on
	plain   string
}

// New returns a Server with one RSA-2048 key (unless NoDefaultKey), no faults and
// "Cache-Control: max-age=600".
func New(cfg Config) (*Server, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Server{cfg: cfg, cache: Cache{MaxAge: Duration(10 * time.Minute)}}
	if !cfg.NoDefaultKey {
		if _, err := s.AddKey(KeyOptions{}); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Server) now() time.Time {
	if s.cfg.Now != nil {
		return s.cfg.Now()
	}
	return time.Now()
}

// Issuer is the base URL the double announces as "issuer": what a test configures as the CI
// issuer's issuer. It is known once the server has been started, or PublicURL was set.
func (s *Server) Issuer() string {
	if s.cfg.PublicURL != "" {
		return strings.TrimSuffix(s.cfg.PublicURL, "/")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.base
}

// DiscoveryURL is the CI issuer's discovery_url.
func (s *Server) DiscoveryURL() string { return s.Issuer() + DiscoveryPath }

// JWKSURL is the key set's address as the discovery document names it.
func (s *Server) JWKSURL() string { return s.Issuer() + JWKSPath }

// AddKey adds a key; the existing ones stay. It is the rotation: the new key is the newest, and
// Mint signs with it from now on.
func (s *Server) AddKey(o KeyOptions) (*Key, error) {
	k, err := newKey(o)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.keys = append(s.keys, k)
	s.mu.Unlock()
	return k, nil
}

// Rotate adds a new RSA-2048 key and keeps the old ones.
func (s *Server) Rotate() (*Key, error) { return s.AddKey(KeyOptions{}) }

// RemoveKey takes the keys with this kid out of the JWKS (the way an issuer revokes a key) and
// reports whether there were any. An empty kid removes the keys published without one. A removed
// key can still sign: Mint with its KID.
func (s *Server) RemoveKey(kid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	s.keys = slices.DeleteFunc(s.keys, func(k *Key) bool {
		if k.KID != kid {
			return false
		}
		s.retired = append(s.retired, k)
		found = true
		return true
	})
	return found
}

// Keys lists the keys the double holds, oldest first.
func (s *Server) Keys() []Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Info, 0, len(s.keys))
	for _, k := range s.keys {
		out = append(out, k.info())
	}
	return out
}

// SetFault replaces the fault set; Fault{} heals the issuer.
func (s *Server) SetFault(f Fault) error {
	if err := f.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	s.fault = f
	s.mu.Unlock()
	return nil
}

// Fault returns the current fault set.
func (s *Server) Fault() Fault {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fault
}

// SetCache replaces the caching of the JWKS answer.
func (s *Server) SetCache(c Cache) {
	s.mu.Lock()
	s.cache = c
	s.mu.Unlock()
}

// Counts returns the request counters.
func (s *Server) Counts() Counts {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts
}

// Reset zeroes the counters.
func (s *Server) Reset() {
	s.mu.Lock()
	s.counts = Counts{}
	s.mu.Unlock()
}

// ResetAll zeroes the counters, heals every fault, restores the default caching and replaces the
// keys by one fresh default key (unless NoDefaultKey).
func (s *Server) ResetAll() error {
	var fresh *Key
	if !s.cfg.NoDefaultKey {
		var err error
		if fresh, err = newKey(KeyOptions{}); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts, s.fault = Counts{}, Fault{}
	s.cache = Cache{MaxAge: Duration(10 * time.Minute)}
	s.retired = append(s.retired, s.keys...)
	s.keys = nil
	if fresh != nil {
		s.keys = []*Key{fresh}
	}
	return nil
}

// ServeHTTP serves the issuer endpoints (TLS listener).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.serve(w, r, false) }

type plainHandler struct{ s *Server }

func (h plainHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.s.serve(w, r, true) }

func (s *Server) hostBase(r *http.Request, plain bool) string {
	if s.cfg.PublicURL != "" {
		return s.Issuer()
	}
	if plain {
		return s.Issuer()
	}
	return "https://" + r.Host
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request, plain bool) {
	target := ""
	switch r.URL.Path {
	case DiscoveryPath:
		target = TargetDiscovery
	case JWKSPath:
		target = TargetJWKS
	}
	f, cache := s.count(r, target, plain)

	if f.Unreachable {
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				_ = c.Close()
			}
		}
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if target == "" {
		http.NotFound(w, r)
		return
	}
	faulted := f.applies(target)
	if faulted && s.faultAnswer(w, r, f, plain) {
		return
	}

	var doc map[string]any
	if target == TargetDiscovery {
		doc = s.discovery(f, s.hostBase(r, plain))
	} else {
		doc = s.jwks()
	}
	if faulted && f.OversizeBytes > 0 {
		doc["padding"] = strings.Repeat("a", f.OversizeBytes)
	}
	body, _ := json.Marshal(doc)
	h := w.Header()
	h.Set("Content-Type", "application/json")
	if target == TargetJWKS && s.cacheHeaders(w, r, body, cache) {
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// count books the request and returns the fault set and the caching that apply to it.
func (s *Server) count(r *http.Request, target string, plain bool) (Fault, Cache) {
	_, viaRedirect := r.URL.Query()["hop"]
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.fault.Unreachable:
		s.counts.Refused++
	case plain:
		s.counts.Plain++
	case target == "":
		s.counts.Other++
	case viaRedirect:
		s.counts.Hops++
	case target == TargetDiscovery:
		s.counts.Discovery++
	default:
		s.counts.JWKS++
	}
	return s.fault, s.cache
}

// faultAnswer applies the faults that replace the document and reports whether it answered.
func (s *Server) faultAnswer(w http.ResponseWriter, r *http.Request, f Fault, plain bool) bool {
	if f.Delay > 0 {
		t := time.NewTimer(time.Duration(f.Delay))
		select {
		case <-t.C:
		case <-r.Context().Done():
			t.Stop()
			return true
		}
	}
	switch {
	case !plain && f.Redirect != nil && s.redirect(w, r, f.Redirect, plain):
		return true
	case f.Status != 0:
		http.Error(w, "oidcstub fault: status "+strconv.Itoa(f.Status), f.Status)
		return true
	case f.NotJSON:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html><body>"+NonJSONMarker+"</body></html>")
		return true
	}
	return false
}

// cacheHeaders sets the caching of a key set answer and reports whether it answered 304.
func (s *Server) cacheHeaders(w http.ResponseWriter, r *http.Request, body []byte, cache Cache) bool {
	h := w.Header()
	if !cache.NoHeader {
		h.Set("Cache-Control", "max-age="+strconv.Itoa(int(time.Duration(cache.MaxAge).Seconds())))
	}
	if !cache.ETag {
		return false
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	h.Set("ETag", etag)
	if r.Header.Get("If-None-Match") != etag {
		return false
	}
	s.mu.Lock()
	s.counts.NotModified++
	s.mu.Unlock()
	w.WriteHeader(http.StatusNotModified)
	return true
}

// redirect answers one hop of the chain and reports whether it did.
func (s *Server) redirect(w http.ResponseWriter, r *http.Request, rd *Redirect, plain bool) bool {
	hop := 0
	if v := r.URL.Query().Get("hop"); v != "" {
		hop, _ = strconv.Atoi(v)
	}
	switch {
	case hop < rd.Hops:
		http.Redirect(w, r, s.hostBase(r, plain)+r.URL.Path+"?hop="+strconv.Itoa(hop+1), http.StatusFound) //nolint:gosec // a test double: the target is its own base and one of two fixed paths
		return true
	case rd.ToHTTP:
		s.mu.Lock()
		plainBase := s.plain
		s.mu.Unlock()
		if s.cfg.PlainURL != "" {
			plainBase = strings.TrimSuffix(s.cfg.PlainURL, "/")
		}
		http.Redirect(w, r, plainBase+r.URL.Path, http.StatusFound) //nolint:gosec // a test double: the target is its own plain listener and one of two fixed paths
		return true
	}
	return false
}

func (s *Server) discovery(f Fault, base string) map[string]any {
	issuer := base
	if f.IssuerMismatch != "" {
		issuer = f.IssuerMismatch
	}
	jwksURI := base + JWKSPath
	if f.JWKSURI != "" {
		jwksURI = f.JWKSURI
	}
	var algs []string
	switch {
	case len(f.Algorithms) > 0:
		algs = f.Algorithms
	case f.NoCommonAlgorithm:
		algs = []string{"HS256"}
	default:
		s.mu.Lock()
		for _, k := range s.keys {
			if k.Published() && !slices.Contains(algs, k.Type.DefaultAlg()) {
				algs = append(algs, k.Type.DefaultAlg())
			}
		}
		s.mu.Unlock()
		if len(algs) == 0 {
			algs = []string{"RS256"}
		}
		slices.Sort(algs)
	}
	return map[string]any{
		"issuer":                                issuer,
		"jwks_uri":                              jwksURI,
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": algs,
		"scopes_supported":                      []string{"openid"},
		"claims_supported":                      []string{"iss", "sub", "aud", "exp", "iat", "nbf", "jti"},
	}
}

func (s *Server) jwks() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := []map[string]any{}
	for _, k := range s.keys {
		if k.Published() {
			keys = append(keys, k.JWK())
		}
	}
	return map[string]any{"keys": keys}
}

// Running is a double listening on TLS and, for the redirect out of TLS, on plain HTTP.
type Running struct {
	// URL is the base address of the issuer, "https://host:port".
	URL string
	// Addr is the TLS listener's address.
	Addr string
	// PlainURL is the plain-HTTP listener's base address.
	PlainURL string

	servers []*http.Server
}

// Close stops the listeners and drops open connections.
func (r *Running) Close() error {
	var err error
	for _, s := range r.servers {
		err = errors.Join(err, s.Close())
	}
	return err
}

// gate closes connections while the Unreachable fault is on, before the TLS handshake.
type gate struct {
	net.Listener
	s *Server
}

func (g gate) Accept() (net.Conn, error) {
	for {
		c, err := g.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if g.s.Fault().Unreachable {
			g.s.mu.Lock()
			g.s.counts.Refused++
			g.s.mu.Unlock()
			_ = c.Close()
			continue
		}
		return c, nil
	}
}

func hostURL(scheme string, ln net.Listener) string {
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		host = "localhost"
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

func (s *Server) newHTTPServer(h http.Handler, cfg *tls.Config) *http.Server {
	srv := &http.Server{
		Handler: h, TLSConfig: cfg, ReadHeaderTimeout: 10 * time.Second,
		ErrorLog: slog.NewLogLogger(s.cfg.Logger.Handler(), slog.LevelWarn),
	}
	// HTTP/1.1 only: a kept-alive connection can then be dropped by the Unreachable fault.
	srv.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){}
	return srv
}

func serverTLS(cfg *tls.Config) (*tls.Config, error) {
	if cfg == nil || (len(cfg.Certificates) == 0 && cfg.GetCertificate == nil) {
		return nil, errors.New("oidcstub: a TLS certificate is required, the issuer does not serve plain HTTP")
	}
	cfg = cfg.Clone()
	if cfg.MinVersion < tls.VersionTLS12 {
		cfg.MinVersion = tls.VersionTLS12
	}
	cfg.NextProtos = []string{"http/1.1"}
	return cfg, nil
}

// Start serves the issuer over TLS on addr ("127.0.0.1:0" picks a port) and, for the redirect
// out of TLS, on plain HTTP on plainAddr ("" means "127.0.0.1:0"). The configuration must carry a
// certificate; the lowest protocol version is TLS 1.2.
func (s *Server) Start(addr string, cfg *tls.Config, plainAddr string) (*Running, error) {
	cfg, err := serverTLS(cfg)
	if err != nil {
		return nil, err
	}
	if plainAddr == "" {
		plainAddr = "127.0.0.1:0"
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return nil, err
	}
	pln, err := lc.Listen(context.Background(), "tcp", plainAddr)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	run := &Running{URL: hostURL("https", ln), Addr: ln.Addr().String(), PlainURL: hostURL("http", pln)}
	s.mu.Lock()
	s.base, s.plain = run.URL, run.PlainURL
	s.mu.Unlock()

	tlsSrv := s.newHTTPServer(s, cfg)
	plainSrv := s.newHTTPServer(plainHandler{s}, nil)
	run.servers = []*http.Server{tlsSrv, plainSrv}
	go func() { _ = tlsSrv.ServeTLS(gate{ln, s}, "", "") }()
	go func() { _ = plainSrv.Serve(pln) }()
	return run, nil
}

// StartSelfSigned starts the double on a throwaway CA and certificate for localhost and
// 127.0.0.1 (and hosts) and returns the CA, whose PEM a test hands to trove as ca_bundle_ref.
func (s *Server) StartSelfSigned(hosts ...string) (*Running, *SelfSigned, error) {
	ss, err := NewSelfSigned(append([]string{"localhost", "127.0.0.1"}, hosts...)...)
	if err != nil {
		return nil, nil, err
	}
	run, err := s.Start("127.0.0.1:0", ss.ServerConfig(), "")
	if err != nil {
		return nil, nil, err
	}
	return run, ss, nil
}

// StartControl serves the control API (ControlHandler) over TLS on its own address, so that the
// Unreachable fault of the issuer listener does not cut the test off from it.
func (s *Server) StartControl(addr string, cfg *tls.Config) (*Running, error) {
	cfg, err := serverTLS(cfg)
	if err != nil {
		return nil, err
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := s.newHTTPServer(s.ControlHandler(), cfg)
	go func() { _ = srv.ServeTLS(ln, "", "") }()
	return &Running{URL: hostURL("https", ln), Addr: ln.Addr().String(), servers: []*http.Server{srv}}, nil
}
