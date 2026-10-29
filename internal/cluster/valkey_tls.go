package cluster

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/tlsfiles"
)

// WithTLSMetrics records the expiry, reload and reload-error families of the
// Valkey CA bundle and client certificate (kind valkey).
func WithTLSMetrics(m *tlsfiles.Metrics) ValkeyOption {
	return func(o *valkeyOptions) { o.tlsMetrics = m }
}

// valkeyTLS is the TLS of the data-node and Sentinel connections (R-HA-056): the
// CA bundle (or the system roots) and the optional client pair. Both are read
// again for every handshake, so a rotated file applies to the next connection
// without a restart, and a file that does not load keeps the last good material
// and is logged and counted (R-ARCH-035).
type valkeyTLS struct {
	// base is what valkey-go carries as TLSConfig: it marks TLS as on and holds
	// the fixed settings. The dialer never uses it directly; handshakeConfig
	// derives the configuration of each handshake from it.
	base   *tls.Config
	mode   string
	roots  *tlsfiles.Reloader
	client *tlsfiles.Reloader // nil without a client pair
}

// newValkeyTLS reads the files of valkey.tls once. It returns nil for mode
// disable. A CA or a client pair that does not load at start is a configuration
// error.
func newValkeyTLS(cfg config.ValkeyTLS, opts ...tlsfiles.Option) (*valkeyTLS, error) {
	if cfg.Mode == "disable" {
		return nil, nil
	}
	if cfg.Mode != "verify-full" && cfg.Mode != "verify-ca" {
		return nil, fmt.Errorf("valkey.tls.mode %q is not supported", cfg.Mode)
	}
	if cfg.Mode == "verify-ca" && cfg.CAFile == "" {
		// The configuration check refuses this too; the client does not rely on it.
		return nil, errors.New("valkey.tls.mode verify-ca needs valkey.tls.ca_file: with the system roots and no host name check any publicly issued certificate would pass")
	}
	labels := tlsfiles.Labels{Kind: tlsfiles.KindValkey}
	t := &valkeyTLS{mode: cfg.Mode, base: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.ServerName}}
	spec := tlsfiles.Spec{SystemRoots: true} // verify-full without a file
	if cfg.CAFile != "" {
		spec = tlsfiles.Spec{CAFiles: []string{cfg.CAFile}}
	}
	t.roots = tlsfiles.New(spec, labels, opts...)
	if err := t.roots.Loaded(); err != nil {
		return nil, fmt.Errorf("valkey.tls.ca_file: %w", err)
	}
	if cfg.CertFile != "" {
		t.client = tlsfiles.New(tlsfiles.Spec{Pairs: []tlsfiles.Pair{{CertFile: cfg.CertFile, KeyFile: cfg.KeyFile}}}, labels, opts...)
		if err := t.client.Loaded(); err != nil {
			return nil, fmt.Errorf("valkey.tls client certificate: %w", err)
		}
	}
	return t, nil
}

// handshakeConfig is the configuration of one handshake: base with the CA pool
// read now and the client certificate read when the server asks for it.
func (t *valkeyTLS) handshakeConfig(base *tls.Config) *tls.Config {
	t.roots.Refresh(0)
	c := base.Clone()
	pool := t.roots.Pool()
	c.RootCAs = pool
	if t.client != nil {
		c.GetClientCertificate = func(req *tls.CertificateRequestInfo) (*tls.Certificate, error) {
			t.client.Refresh(0)
			return t.client.ClientCertificate(req)
		}
	}
	if t.mode == "verify-ca" {
		// The chain is verified, the host name is not: the standard library
		// only skips both together, so the chain is verified by hand against
		// this handshake's pool.
		c.InsecureSkipVerify = true //nolint:gosec // G402: VerifyConnection below verifies the chain
		c.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("valkey server presented no certificate")
			}
			inter := x509.NewCertPool()
			for _, ic := range cs.PeerCertificates[1:] {
				inter.AddCert(ic)
			}
			_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: pool, Intermediates: inter})
			return err
		}
	}
	return c
}

// baseConfig is nil-safe: nil (TLS off) for a nil valkeyTLS.
func (t *valkeyTLS) baseConfig() *tls.Config {
	if t == nil {
		return nil
	}
	return t.base
}

// prepare is the dialer's hook: the handshake configuration for a connection
// that valkey-go wants with TLS (cfg non-nil), nil for a plaintext one.
func (t *valkeyTLS) prepare(cfg *tls.Config) *tls.Config {
	if cfg == nil || t == nil {
		return cfg
	}
	return t.handshakeConfig(cfg)
}
