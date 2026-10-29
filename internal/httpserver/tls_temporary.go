package httpserver

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"log/slog"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/tlsfiles"
)

// Lifetime of a temporary certificate and the remaining time at which a handshake
// replaces it.
const (
	temporaryLifetime = time.Hour
	temporaryRenewAt  = 10 * time.Minute
)

// temporaryCert is the self-generated certificate a listener answers with while
// its configured pair has not loaded (R-ARCH-035, H-68). It lives in memory only.
type temporaryCert struct {
	cn      string
	metrics *tlsfiles.Metrics
	log     *slog.Logger
	active  atomic.Bool

	mu   sync.Mutex
	cert *tls.Certificate
}

// UseTemporaryCertificate lets the listener answer handshakes with a temporary
// certificate for as long as its configured pair has not loaded. It is for the
// ops listener: under HTTPS probes the kubelet does not verify the certificate,
// and a listener that refuses every handshake until the pair loads would fail the
// startup and liveness probes and get the container restarted in a loop. With the
// temporary certificate /healthz answers 200 and /readyz answers 503 with the
// failing tls check, and the replica serves its real certificate once the files
// are valid. The key is generated in memory (ECDSA P-256), valid for an hour, named
// by cn (the instance) and never written to disk. A listener that carries user
// traffic must not use it. Call it before Serve.
func (l *ListenerTLS) UseTemporaryCertificate(cn string, m *tlsfiles.Metrics, log *slog.Logger) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	l.temp = &temporaryCert{cn: cn, metrics: m, log: log}
	m.SetTemporary(l.listener, false)
}

// Temporary reports whether the last handshake was answered with the temporary
// certificate.
func (l *ListenerTLS) Temporary() bool { return l.temp != nil && l.temp.active.Load() }

// certificate is tls.Config.GetCertificate of the listener.
func (l *ListenerTLS) certificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	c, err := l.material.Certificate(hello)
	if l.temp == nil {
		return c, err
	}
	if err == nil {
		if l.temp.active.CompareAndSwap(true, false) {
			l.temp.metrics.SetTemporary(l.listener, false)
			l.temp.log.Info("the configured certificate serves now; the temporary one is retired", "listener", l.listener)
		}
		return c, nil
	}
	tc, terr := l.temp.get()
	if terr != nil {
		return nil, fmt.Errorf("%w (temporary certificate: %w)", err, terr)
	}
	if l.temp.active.CompareAndSwap(false, true) {
		l.temp.metrics.SetTemporary(l.listener, true)
		l.temp.log.Warn("the configured certificate has not loaded; the listener answers with a temporary self-generated certificate and the replica stays unready", "listener", l.listener, "error", err.Error())
	}
	return tc, nil
}

func (t *temporaryCert) get() (*tls.Certificate, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	if t.cert != nil && t.cert.Leaf.NotAfter.Sub(now) > temporaryRenewAt {
		return t.cert, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: t.cn},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(temporaryLifetime),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	t.cert = &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
	return t.cert, nil
}
