package httpserver

import (
	"context"
	"crypto/tls"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/tlsfiles"
)

// ListenerTLS is the TLS of one listener (R-ARCH-035): its certificates, picked
// by SNI, its client-certificate policy and the reloader that replaces both
// without a restart. Every listener takes its own block, so a further endpoint
// listener gets its own certificates the same way.
//
// The handshake goes through tls.Config.GetConfigForClient: one *tls.Config per
// generation of the material, built on the first handshake after a swap. A new
// certificate or client CA therefore serves the next handshake; connections
// already open keep what they negotiated (Q-ARCH-31).
type ListenerTLS struct {
	material *tlsfiles.Reloader
	interval time.Duration
	auth     tls.ClientAuthType
	min      uint16
	base     *tls.Config
	cached   atomic.Pointer[generationConfig]
	running  atomic.Bool
}

type generationConfig struct {
	gen uint64
	cfg *tls.Config
}

// NewListenerTLS builds the TLS of the listener from its block; nil without
// certificates (plain HTTP). The files are read once here: a pair that does not
// load is not an error of NewListenerTLS but of Loaded, so the listener can bind,
// keep the replica unready and serve as soon as the files are valid.
func NewListenerTLS(listener string, b config.ServerTLS, opts ...tlsfiles.Option) (*ListenerTLS, error) {
	if !b.Enabled() {
		return nil, nil
	}
	auth, err := clientAuthType(b.ClientAuth)
	if err != nil {
		return nil, fmt.Errorf("%s listener: %w", listener, err)
	}
	minVer, err := tlsVersion(b.MinVersion)
	if err != nil {
		return nil, fmt.Errorf("%s listener: %w", listener, err)
	}
	spec := tlsfiles.Spec{}
	for _, c := range b.Certificates {
		spec.Pairs = append(spec.Pairs, tlsfiles.Pair{CertFile: c.CertFile, KeyFile: c.KeyFile})
	}
	if b.ClientCAFile != "" {
		spec.CAFiles = []string{b.ClientCAFile}
	}
	l := &ListenerTLS{
		material: tlsfiles.New(spec, tlsfiles.Labels{Kind: tlsfiles.KindListener, Listener: listener}, opts...),
		interval: b.ReloadInterval, auth: auth, min: minVer,
	}
	if l.interval <= 0 {
		l.interval = config.DefaultTLSReloadInterval
	}
	l.base = &tls.Config{
		MinVersion:         minVer,
		CipherSuites:       cipherSuites(),
		NextProtos:         []string{"h2", "http/1.1"},
		GetCertificate:     l.material.Certificate,
		GetConfigForClient: l.configFor,
	}
	return l, nil
}

// Loaded returns nil once every pair and the client CA have loaded at least
// once. Until then the listener refuses every handshake; the composition root
// holds the first readiness on it.
func (l *ListenerTLS) Loaded() error { return l.material.Loaded() }

// Material returns the reloader, for the expiry and reload state.
func (l *ListenerTLS) Material() *tlsfiles.Reloader { return l.material }

// Run polls the files every reload_interval until ctx ends. Serve runs it; a
// second call while one runs returns at once.
func (l *ListenerTLS) Run(ctx context.Context) {
	if !l.running.CompareAndSwap(false, true) {
		return
	}
	defer l.running.Store(false)
	l.material.Run(ctx, l.interval)
}

func (l *ListenerTLS) configFor(*tls.ClientHelloInfo) (*tls.Config, error) {
	gen := l.material.Generation() // before the pool: the pool is at least as new
	if c := l.cached.Load(); c != nil && c.gen == gen {
		return c.cfg, nil
	}
	cfg := &tls.Config{
		MinVersion:     l.min,
		CipherSuites:   cipherSuites(),
		NextProtos:     []string{"h2", "http/1.1"},
		GetCertificate: l.material.Certificate,
		ClientAuth:     l.auth,
	}
	if l.auth != tls.NoClientCert {
		// Empty, never nil, when the CA did not load: nothing verifies.
		cfg.ClientCAs = l.material.Pool()
	}
	l.cached.Store(&generationConfig{gen: gen, cfg: cfg})
	return cfg, nil
}

func clientAuthType(mode string) (tls.ClientAuthType, error) {
	switch mode {
	case "", config.ClientAuthNone:
		return tls.NoClientCert, nil
	case config.ClientAuthVerifyIfGiven:
		return tls.VerifyClientCertIfGiven, nil
	case config.ClientAuthRequire:
		return tls.RequireAndVerifyClientCert, nil
	}
	return 0, fmt.Errorf("client_auth %q is not one of none, verify_if_given, require", mode)
}

func tlsVersion(v string) (uint16, error) {
	switch v {
	case "", "1.2":
		return tls.VersionTLS12, nil
	case "1.3":
		return tls.VersionTLS13, nil
	}
	return 0, fmt.Errorf("min_version %q is not 1.2 or 1.3", v)
}

// cipherSuites are the TLS 1.2 suites a listener offers: forward secret AEAD
// only. TLS 1.3 suites are not configurable and all qualify. The list contains
// the AES-128-GCM suites HTTP/2 requires (RFC 9113 section 9.2.2).
func cipherSuites() []uint16 {
	return []uint16{
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
		tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
	}
}
