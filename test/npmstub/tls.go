package npmstub

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

// Running is a stub listening on a TLS socket.
type Running struct {
	// URL is the base address clients use, "https://host:port".
	URL string
	// Addr is the listener's address.
	Addr string

	srv *http.Server
}

// Start serves the stub over TLS on addr ("127.0.0.1:0" picks a port). There is no plain-HTTP
// variant: the configuration must carry a certificate (Certificates or GetCertificate), and the
// lowest protocol version is TLS 1.2.
func (s *Server) Start(addr string, cfg *tls.Config) (*Running, error) {
	if cfg == nil || (len(cfg.Certificates) == 0 && cfg.GetCertificate == nil) {
		return nil, errors.New("npmstub: a TLS certificate is required, the stub does not serve plain HTTP")
	}
	cfg = cfg.Clone()
	if cfg.MinVersion < tls.VersionTLS12 {
		cfg.MinVersion = tls.VersionTLS12
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: s, TLSConfig: cfg, ReadHeaderTimeout: 10 * time.Second, ErrorLog: slog.NewLogLogger(s.cfg.Logger.Handler(), slog.LevelWarn)}
	go func() { _ = srv.ServeTLS(ln, "", "") }()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		host = "localhost"
	}
	return &Running{URL: "https://" + net.JoinHostPort(host, port), Addr: ln.Addr().String(), srv: srv}, nil
}

// Close stops the listener and drops open connections.
func (r *Running) Close() error { return r.srv.Close() }

// SelfSigned is a throwaway CA and a server certificate it signed, for tests and for the
// container's own use where no certificate is provided.
type SelfSigned struct {
	Cert  tls.Certificate
	CAPEM []byte
	Pool  *x509.CertPool
}

// NewSelfSigned creates a CA and a certificate for the given host names and addresses.
func NewSelfSigned(hosts ...string) (*SelfSigned, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "trove npmstub test CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(30 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "npmstub"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(30 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return &SelfSigned{
		Cert:  tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
		CAPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		Pool:  pool,
	}, nil
}

// ServerConfig is the TLS configuration for the stub's listener.
func (c *SelfSigned) ServerConfig() *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{c.Cert}, MinVersion: tls.VersionTLS12}
}

// ClientConfig is the TLS configuration of a client that trusts the CA.
func (c *SelfSigned) ClientConfig() *tls.Config {
	return &tls.Config{RootCAs: c.Pool, MinVersion: tls.VersionTLS12}
}

// ReloadingKeyPair returns a GetCertificate that serves the certificate in two files and reads
// them again whenever either changed on disk, so a rotated certificate takes effect on the next
// handshake without a restart.
func ReloadingKeyPair(certFile, keyFile string) (func(*tls.ClientHelloInfo) (*tls.Certificate, error), error) {
	r := &reloader{certFile: certFile, keyFile: keyFile}
	if _, err := r.get(nil); err != nil {
		return nil, err
	}
	return r.get, nil
}

type reloader struct {
	certFile, keyFile string

	mu         sync.Mutex
	cert       *tls.Certificate
	certTime   time.Time
	keyTime    time.Time
	certLoaded bool
}

func (r *reloader) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	cs, err := os.Stat(r.certFile)
	if err != nil {
		return nil, err
	}
	ks, err := os.Stat(r.keyFile)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.certLoaded && cs.ModTime().Equal(r.certTime) && ks.ModTime().Equal(r.keyTime) {
		return r.cert, nil
	}
	pair, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		if r.certLoaded {
			return r.cert, nil // a half-written rotation keeps serving the previous certificate
		}
		return nil, fmt.Errorf("npmstub: load %s: %w", r.certFile, err)
	}
	r.cert, r.certTime, r.keyTime, r.certLoaded = &pair, cs.ModTime(), ks.ModTime(), true
	return r.cert, nil
}
