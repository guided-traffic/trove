package outbound

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/buildinfo"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/observability"
)

// Purposes of outbound clients. They are the kind label of the outbound metrics
// and the purpose field of the upstream log (R-OPS-011).
const (
	PurposeRemote        = "remote"
	PurposeWebhook       = "webhook"
	PurposeOIDCDiscovery = "oidc_discovery"
	PurposeJWKS          = "jwks"
	PurposeObjectStore   = "objectstore"
)

// Errors of the redirect policy; the proxy engine maps them to 502
// too_many_redirects and insecure_redirect (R-REPO-039).
var (
	ErrTooManyRedirects = errors.New("too many redirects")
	ErrInsecureRedirect = errors.New("redirect from https to http refused")
)

// Option configures a Factory.
type Option func(*Factory)

// WithTracer propagates trace context to upstreams (when tracing.propagate_to_upstreams
// is set) and records a client span per request.
func WithTracer(t *observability.Tracer) Option { return func(f *Factory) { f.tracer = t } }

// WithUpstreamLog enables the log=upstream records of R-OPS-011 on the logger.
func WithUpstreamLog(l *slog.Logger) Option {
	return func(f *Factory) { f.upstreamLog = l.With("log", observability.StreamUpstream) }
}

// WithMetrics registers the outbound families on m.
func WithMetrics(m *observability.Metrics) Option { return func(f *Factory) { f.metrics = m } }

// WithVersion sets the version in the User-Agent; the default is the build version.
func WithVersion(v string) Option { return func(f *Factory) { f.version = v } }

// Factory builds the outbound clients of the process.
type Factory struct {
	cfg         config.Outbound
	roots       *x509.CertPool
	minVersion  uint16
	policy      addressPolicy
	setupErr    error
	tracer      *observability.Tracer
	upstreamLog *slog.Logger
	metrics     *observability.Metrics
	version     string
	scrub       *observability.Scrubber

	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	denied   *prometheus.CounterVec
}

// New builds the factory. Problems with the trust store or the address policy
// do not fail New; they are reported by Err, and every client of a failed
// factory fails each request with that error rather than connecting with a
// weaker setup. The composition root calls Err at start-up.
func New(cfg config.Outbound, opts ...Option) *Factory {
	f := &Factory{cfg: cfg, version: buildinfo.Get().Version, scrub: observability.NewScrubber(observability.DefaultQueryParams())}
	for _, o := range opts {
		o(f)
	}
	var errs []error
	f.minVersion = tls.VersionTLS12
	if cfg.TLS.MinVersion == "1.3" {
		f.minVersion = tls.VersionTLS13
	}
	f.roots, errs = loadRoots(cfg.TLS, errs)
	var err error
	if f.policy.deny, err = parsePrefixes(cfg.AddressPolicy.DenyCIDRs); err != nil {
		errs = append(errs, fmt.Errorf("proxy.outbound.address_policy.deny_cidrs: %w", err))
	}
	if f.policy.allow, err = parsePrefixes(cfg.AddressPolicy.AllowCIDRs); err != nil {
		errs = append(errs, fmt.Errorf("proxy.outbound.address_policy.allow_cidrs: %w", err))
	}
	f.setupErr = errors.Join(errs...)
	if f.metrics != nil {
		f.requests = f.metrics.NewCounterVec("trove_outbound_requests_total", "Requests sent to other systems by purpose.", "kind", "code")
		f.duration = f.metrics.NewHistogramVec("trove_outbound_request_duration_seconds",
			"Time of an outbound request including the body, by purpose.", observability.HTTPBuckets(), "kind")
		f.denied = f.metrics.NewCounterVec("trove_outbound_egress_denied_total", "Connections refused by the egress address policy, by purpose.", "kind")
	}
	return f
}

func loadRoots(c config.OutboundTLS, errs []error) (*x509.CertPool, []error) {
	var pool *x509.CertPool
	if c.UseSystemRoots {
		sys, err := x509.SystemCertPool()
		if err != nil {
			errs = append(errs, fmt.Errorf("proxy.outbound.tls: system roots: %w", err))
			sys = x509.NewCertPool()
		}
		pool = sys
	} else {
		pool = x509.NewCertPool()
	}
	for _, file := range c.CAFiles {
		pem, err := os.ReadFile(file) //nolint:gosec // operator-configured path
		if err != nil {
			errs = append(errs, fmt.Errorf("proxy.outbound.tls.ca_files: %w", err))
			continue
		}
		if !pool.AppendCertsFromPEM(pem) {
			errs = append(errs, fmt.Errorf("proxy.outbound.tls.ca_files: %s holds no PEM certificate", file))
		}
	}
	if !c.UseSystemRoots && len(c.CAFiles) == 0 {
		errs = append(errs, errors.New("proxy.outbound.tls: use_system_roots is false and ca_files is empty: no upstream certificate could verify"))
	}
	return pool, errs
}

// Err reports a problem with the trust store or the address policy. Nil means
// every client verifies certificates against the configured roots and enforces
// the policy.
func (f *Factory) Err() error { return f.setupErr }

// CheckAddr applies the egress address policy to one address.
func (f *Factory) CheckAddr(a netip.Addr) error { return f.policy.check(a) }

// ClientOption adjusts one client.
type ClientOption func(*clientSettings)

type clientSettings struct {
	overall        time.Duration
	connect        time.Duration
	tlsHandshake   time.Duration
	responseHeader time.Duration
	idleConn       time.Duration
	maxIdlePerHost int
	maxRedirects   int
	redirectCheck  func(req *http.Request, via []*http.Request) error
	proxy          func(*http.Request) (*url.URL, error)
	direct         bool
	clientCert     []tls.Certificate
	egress         bool
	logAttrs       []slog.Attr
}

// WithTimeout sets the client's overall timeout including the body. The default
// is 30 s for oidc_discovery, jwks and webhook and none for remote and
// objectstore, whose transfers are bounded by their own stall detection.
func WithTimeout(d time.Duration) ClientOption { return func(s *clientSettings) { s.overall = d } }

// WithConnectTimeout bounds dialing.
func WithConnectTimeout(d time.Duration) ClientOption {
	return func(s *clientSettings) { s.connect = d }
}

// WithResponseHeaderTimeout bounds the wait for response headers.
func WithResponseHeaderTimeout(d time.Duration) ClientOption {
	return func(s *clientSettings) { s.responseHeader = d }
}

// WithSocketTimeout maps a remote repository's socket timeout (RMT-015) to the
// connect and the response-header timeout.
func WithSocketTimeout(d time.Duration) ClientOption {
	return func(s *clientSettings) { s.connect, s.responseHeader = d, d }
}

// WithMaxRedirects sets the redirect limit (default 5).
func WithMaxRedirects(n int) ClientOption { return func(s *clientSettings) { s.maxRedirects = n } }

// WithRedirectCheck adds a check that every redirect target must pass, for
// example the host restriction of a format.
func WithRedirectCheck(f func(req *http.Request, via []*http.Request) error) ClientOption {
	return func(s *clientSettings) { s.redirectCheck = f }
}

// WithProxyURL sends requests through an outbound proxy, except for target
// hosts that match a bypass pattern (path.Match syntax, e.g. "*.corp.example").
// User information in proxyURL becomes Proxy-Authorization toward the proxy
// only.
func WithProxyURL(proxyURL *url.URL, bypassHosts ...string) ClientOption {
	return func(s *clientSettings) {
		s.proxy = func(r *http.Request) (*url.URL, error) {
			h := r.URL.Hostname()
			for _, p := range bypassHosts {
				if ok, _ := path.Match(p, h); ok {
					return nil, nil
				}
			}
			return proxyURL, nil
		}
	}
}

// WithDirect forbids any proxy for this client, also proxy_from_environment.
func WithDirect() ClientOption { return func(s *clientSettings) { s.direct = true } }

// WithClientCertificate presents a client certificate to upstreams.
func WithClientCertificate(c tls.Certificate) ClientOption {
	return func(s *clientSettings) { s.clientCert = append(s.clientCert, c) }
}

// WithLogAttrs adds fixed fields to the upstream log records of this client,
// for example slog.String("repo", key).
func WithLogAttrs(attrs ...slog.Attr) ClientOption {
	return func(s *clientSettings) { s.logAttrs = append(s.logAttrs, attrs...) }
}

func (f *Factory) defaults(purpose string) clientSettings {
	s := clientSettings{
		connect: 10 * time.Second, tlsHandshake: 10 * time.Second, responseHeader: 30 * time.Second,
		idleConn: 90 * time.Second, maxIdlePerHost: 16, maxRedirects: 5, egress: true,
	}
	switch purpose {
	case PurposeOIDCDiscovery, PurposeJWKS, PurposeWebhook:
		s.overall = 30 * time.Second
	case PurposeObjectStore:
		// The object store endpoint is operator configuration, not input that a
		// repository administrator or an upstream document can choose, and in
		// development it is a local service. The address policy guards the
		// destinations that other people can name.
		s.egress = false
	}
	return s
}

// Client returns an HTTP client for the purpose. The client owns a connection
// pool: build one per purpose (or per remote repository) at start-up or when
// the remote's settings change, and keep it. The client sends
// User-Agent: trove/<version>, does not decompress responses (Accept-Encoding
// is the caller's choice), and verifies TLS against the factory's trust store.
func (f *Factory) Client(purpose string, opts ...ClientOption) *http.Client {
	s := f.defaults(purpose)
	for _, o := range opts {
		o(&s)
	}
	rt := &roundTripper{f: f, purpose: purpose, attrs: s.logAttrs}
	if f.setupErr != nil {
		rt.base = failing{err: fmt.Errorf("outbound client unavailable: %w", f.setupErr)}
	} else {
		rt.base = f.transport(purpose, s)
	}
	c := &http.Client{Transport: rt, Timeout: s.overall}
	maxHops, extra := s.maxRedirects, s.redirectCheck
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > maxHops {
			return fmt.Errorf("%w: more than %d hops", ErrTooManyRedirects, maxHops)
		}
		if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme == "http" {
			return ErrInsecureRedirect
		}
		if extra != nil {
			return extra(req, via)
		}
		return nil
	}
	return c
}

func (f *Factory) transport(purpose string, s clientSettings) *http.Transport {
	var onDeny func()
	if f.denied != nil {
		c := f.denied.WithLabelValues(purpose)
		onDeny = c.Inc
	}
	dialer := &net.Dialer{Timeout: s.connect, KeepAlive: 30 * time.Second}
	if s.egress {
		dialer.Control = f.policy.control(onDeny)
	}
	tr := &http.Transport{
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   s.tlsHandshake,
		ResponseHeaderTimeout: s.responseHeader,
		IdleConnTimeout:       s.idleConn,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   s.maxIdlePerHost,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
		DisableCompression:    true,
		TLSClientConfig: &tls.Config{
			RootCAs:      f.roots,
			MinVersion:   f.minVersion,
			Certificates: s.clientCert,
		},
	}
	proxy := s.proxy
	if proxy == nil && !s.direct && f.cfg.ProxyFromEnvironment {
		proxy = http.ProxyFromEnvironment
	}
	if proxy != nil {
		tr.Proxy = f.checkedProxy(proxy, s.egress, onDeny)
	}
	return tr
}

// checkedProxy wraps a proxy selector so that the target behind a proxy is
// resolved and checked by trove itself before the request goes out; the proxy
// connects on its own and trove cannot see that connection (H-6).
func (f *Factory) checkedProxy(sel func(*http.Request) (*url.URL, error), egress bool, onDeny func()) func(*http.Request) (*url.URL, error) {
	return func(r *http.Request) (*url.URL, error) {
		p, err := sel(r)
		if err != nil || p == nil || !egress {
			return p, err
		}
		host := r.URL.Hostname()
		if a, perr := netip.ParseAddr(host); perr == nil {
			return p, f.denyWrap(f.policy.check(a), onDeny)
		}
		ips, rerr := net.DefaultResolver.LookupNetIP(r.Context(), "ip", host)
		if rerr != nil {
			return nil, fmt.Errorf("resolve %s for the egress check: %w", host, rerr)
		}
		for _, a := range ips {
			if err := f.policy.check(a); err != nil {
				return nil, f.denyWrap(err, onDeny)
			}
		}
		return p, nil
	}
}

func (f *Factory) denyWrap(err error, onDeny func()) error {
	if err != nil && onDeny != nil {
		onDeny()
	}
	return err
}

// failing is the transport of a factory whose setup failed.
type failing struct{ err error }

func (f failing) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

// Call carries facts about the caller into the upstream log of the requests made
// with the returned context.
type Call struct {
	OnBehalfOf string // principal whose request caused the call; "" means system
	Attempt    int    // attempt number of a retried call; 0 means 1
}

type callKey struct{}

// WithCall attaches the call facts to ctx.
func WithCall(ctx context.Context, c Call) context.Context {
	return context.WithValue(ctx, callKey{}, c)
}

func callFrom(ctx context.Context) Call {
	c, _ := ctx.Value(callKey{}).(Call)
	if c.OnBehalfOf == "" {
		c.OnBehalfOf = "system"
	}
	if c.Attempt < 1 {
		c.Attempt = 1
	}
	return c
}

// userAgent returns the User-Agent every client sends.
func (f *Factory) userAgent() string {
	return "trove/" + strings.TrimPrefix(f.version, "v")
}
