package cluster

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"

	"github.com/guided-traffic/trove/internal/config"
)

// tlsConfig builds the client TLS configuration of valkey.tls (R-HA-056). It
// returns nil for mode disable. The client certificate, if any, is read again
// for every handshake, so a rotated Secret applies to new connections without
// a restart.
func tlsConfig(cfg config.ValkeyTLS) (*tls.Config, error) {
	if cfg.Mode == "disable" {
		return nil, nil
	}
	roots, err := rootPool(cfg.CAFile)
	if err != nil {
		return nil, err
	}
	c := &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    roots,
		ServerName: cfg.ServerName,
	}
	if cfg.CertFile != "" {
		if _, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile); err != nil {
			return nil, fmt.Errorf("valkey.tls client certificate: %w", err)
		}
		certFile, keyFile := cfg.CertFile, cfg.KeyFile
		c.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			cert, err := tls.LoadX509KeyPair(certFile, keyFile)
			if err != nil {
				return nil, fmt.Errorf("valkey.tls client certificate: %w", err)
			}
			return &cert, nil
		}
	}
	switch cfg.Mode {
	case "verify-full":
		return c, nil
	case "verify-ca":
		// The chain is verified, the host name is not: the standard library
		// only skips both together, so verification is done by hand.
		c.InsecureSkipVerify = true //nolint:gosec // VerifyConnection below verifies the chain
		c.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("valkey server presented no certificate")
			}
			inter := x509.NewCertPool()
			for _, ic := range cs.PeerCertificates[1:] {
				inter.AddCert(ic)
			}
			_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter})
			return err
		}
		return c, nil
	default:
		return nil, fmt.Errorf("valkey.tls.mode %q is not supported", cfg.Mode)
	}
}

// rootPool returns the CA pool: the file if set, the system roots otherwise.
func rootPool(caFile string) (*x509.CertPool, error) {
	if caFile == "" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("valkey.tls: system roots: %w", err)
		}
		return pool, nil
	}
	pem, err := os.ReadFile(caFile) //nolint:gosec // operator-configured CA path
	if err != nil {
		return nil, fmt.Errorf("valkey.tls.ca_file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("valkey.tls.ca_file %s holds no certificate", caFile)
	}
	return pool, nil
}
