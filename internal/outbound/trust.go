package outbound

import (
	"crypto/x509"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/tlsfiles"
)

// DefaultTrustCheckEvery is how often a client looks at its CA bundles again,
// at most: on the first request after this time has passed since the last look.
// It keeps the reload of R-REPO-035 and R-ARCH-022 (60 s) with margin, without
// reading a file on every request.
const DefaultTrustCheckEvery = 10 * time.Second

// WithLogger sets the logger of the factory's operational records (trust store
// reloads); the upstream request log is WithUpstreamLog.
func WithLogger(l *slog.Logger) Option { return func(f *Factory) { f.log = l } }

// WithTLSMetrics records the expiry, reload and reload-error families of every
// trust store the factory builds.
func WithTLSMetrics(m *tlsfiles.Metrics) Option { return func(f *Factory) { f.tlsMetrics = m } }

// WithTrustCheckEvery overrides DefaultTrustCheckEvery.
func WithTrustCheckEvery(d time.Duration) Option { return func(f *Factory) { f.trustEvery = d } }

// TrustStore builds a reloadable trust store of exactly these CA bundles for
// clients of one purpose; the system roots and proxy.outbound.tls.ca_files are
// not part of it. The error reports a bundle that does not load now: a caller
// treats it as a configuration error. Hand the store to Client with
// WithRootCAs.
func (f *Factory) TrustStore(purpose string, files ...string) (*tlsfiles.Reloader, error) {
	r := tlsfiles.New(tlsfiles.Spec{CAFiles: files}, tlsfiles.Labels{Kind: purpose}, f.reloaderOptions()...)
	if err := r.Loaded(); err != nil {
		return nil, err
	}
	return r, nil
}

// WithRootCAs makes the client verify servers against this trust store instead
// of the factory's (system roots plus proxy.outbound.tls.ca_files). The store is
// re-read like the factory's.
func WithRootCAs(r *tlsfiles.Reloader) ClientOption { return func(s *clientSettings) { s.trust = r } }

func (f *Factory) reloaderOptions() []tlsfiles.Option {
	opts := []tlsfiles.Option{tlsfiles.WithMetrics(f.tlsMetrics)}
	if f.log != nil {
		opts = append(opts, tlsfiles.WithLogger(f.log.With("component", "outbound")))
	}
	return opts
}

// trustedTransport is the transport of one client. It builds an http.Transport
// for each generation of its trust store, so a rotated CA bundle applies to the
// next connection: the old transport keeps its open connections until they end
// and loses its idle ones. Verification itself stays crypto/tls's (RootCAs, host
// name), never a hand-written check.
type trustedTransport struct {
	trust *tlsfiles.Reloader
	every time.Duration
	what  string // the configuration that names the store, for errors
	build func(roots *x509.CertPool) *http.Transport

	mu  sync.Mutex
	cur atomic.Pointer[builtTransport]
}

type builtTransport struct {
	gen uint64
	tr  *http.Transport
	err error // the store is incomplete: a configured bundle never loaded
}

func (t *trustedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.trust.Refresh(t.every)
	tr, err := t.current()
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	return tr.RoundTrip(req)
}

// current returns the transport of the store's generation, building it on the
// first use after a swap.
func (t *trustedTransport) current() (*http.Transport, error) {
	gen := t.trust.Generation()
	if b := t.cur.Load(); b != nil && b.gen == gen {
		return b.tr, b.err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if b := t.cur.Load(); b != nil && b.gen == gen {
		return b.tr, b.err
	}
	nb := &builtTransport{gen: gen, tr: t.build(t.trust.Pool())}
	if err := t.trust.Loaded(); err != nil {
		nb.err = fmt.Errorf("outbound client unavailable: %s: %w", t.what, err)
	}
	if old := t.cur.Swap(nb); old != nil {
		old.tr.CloseIdleConnections()
	}
	return nb.tr, nb.err
}

// CloseIdleConnections lets http.Client.CloseIdleConnections reach the pool.
func (t *trustedTransport) CloseIdleConnections() {
	if b := t.cur.Load(); b != nil {
		b.tr.CloseIdleConnections()
	}
}
