package httpserver

import (
	"crypto/tls"
	"fmt"
	"os"
	"sync"
	"time"
)

// TLSFiles are the certificate and key files of server.tls. Both empty means
// plain HTTP; TLS normally ends at the Ingress.
type TLSFiles struct {
	CertFile string
	KeyFile  string
}

// certReloadEvery bounds how often a handshake looks at the files' modification
// times. The key file is a mounted Secret that rotates without a restart.
const certReloadEvery = 5 * time.Second

// certReloader serves the certificate and re-reads the pair when either file
// changed.
type certReloader struct {
	files TLSFiles

	mu      sync.Mutex
	cert    *tls.Certificate
	certMod time.Time
	keyMod  time.Time
	checked time.Time
}

func newCertReloader(f TLSFiles) (*certReloader, error) {
	c := &certReloader{files: f}
	if _, err := c.get(nil); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *certReloader) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cert != nil && time.Since(c.checked) < certReloadEvery {
		return c.cert, nil
	}
	c.checked = time.Now()
	cm, err1 := modTime(c.files.CertFile)
	km, err2 := modTime(c.files.KeyFile)
	if c.cert != nil && err1 == nil && err2 == nil && cm.Equal(c.certMod) && km.Equal(c.keyMod) {
		return c.cert, nil
	}
	pair, err := tls.LoadX509KeyPair(c.files.CertFile, c.files.KeyFile)
	if err != nil {
		if c.cert != nil {
			return c.cert, nil // keep serving the last good pair
		}
		return nil, fmt.Errorf("load server.tls: %w", err)
	}
	c.cert, c.certMod, c.keyMod = &pair, cm, km
	return c.cert, nil
}

func modTime(path string) (time.Time, error) {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return st.ModTime(), nil
}

// tlsConfig returns the server configuration for the files: TLS 1.2 or later,
// HTTP/2 and HTTP/1.1.
func tlsConfig(f TLSFiles) (*tls.Config, error) {
	r, err := newCertReloader(f)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		NextProtos:     []string{"h2", "http/1.1"},
		GetCertificate: r.get,
	}, nil
}
