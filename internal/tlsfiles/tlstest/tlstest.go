// Package tlstest issues throwaway certificates for tests of TLS code: a CA,
// server and client leaves, PEM files written in place. Nothing outside _test.go
// files and test/ may import it; no command or library of trove does.
package tlstest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// CA is a certificate authority that signs leaves.
type CA struct {
	Cert *x509.Certificate
	Key  crypto.Signer
	PEM  []byte
}

// NewCA creates a self-signed CA valid for a day.
func NewCA(tb testing.TB, name string) *CA {
	tb.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		tb.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(tb), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		tb.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		tb.Fatal(err)
	}
	return &CA{Cert: cert, Key: key, PEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// Pool is a pool that holds only this CA.
func (ca *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.Cert)
	return p
}

// WriteCA writes the CA certificate to dir/name and returns the path.
func (ca *CA) WriteCA(tb testing.TB, dir, name string) string {
	tb.Helper()
	p := filepath.Join(dir, name)
	WriteFile(tb, p, ca.PEM)
	return p
}

// Options shape a leaf. The zero value is a server and client certificate for
// localhost and 127.0.0.1, valid for an hour, with an ECDSA key.
type Options struct {
	DNSNames []string
	IPs      []net.IP
	// Serial is the serial number; nil picks a random one.
	Serial *big.Int
	// NotAfter is the expiry; zero means an hour from now.
	NotAfter time.Time
	// RSA uses a 2048-bit RSA key instead of ECDSA P-256.
	RSA bool
}

// Leaf is an issued certificate with its key.
type Leaf struct {
	Cert    *x509.Certificate
	CertPEM []byte
	KeyPEM  []byte
	TLS     tls.Certificate
}

// Issue signs a leaf for the options.
func (ca *CA) Issue(tb testing.TB, o Options) *Leaf {
	tb.Helper()
	if o.DNSNames == nil && o.IPs == nil {
		o.DNSNames, o.IPs = []string{"localhost"}, []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	}
	if o.Serial == nil {
		o.Serial = serial(tb)
	}
	notBefore := time.Now().Add(-time.Hour)
	if o.NotAfter.IsZero() {
		o.NotAfter = time.Now().Add(time.Hour)
	}
	if o.NotAfter.Before(notBefore) {
		notBefore = o.NotAfter.Add(-time.Hour)
	}
	var (
		key    crypto.Signer
		keyDER []byte
		block  string
		err    error
	)
	if o.RSA {
		k, gerr := rsa.GenerateKey(rand.Reader, 2048)
		if gerr != nil {
			tb.Fatal(gerr)
		}
		key, keyDER, block = k, x509.MarshalPKCS1PrivateKey(k), "RSA PRIVATE KEY"
	} else {
		k, gerr := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if gerr != nil {
			tb.Fatal(gerr)
		}
		key, block = k, "EC PRIVATE KEY"
		if keyDER, err = x509.MarshalECPrivateKey(k); err != nil {
			tb.Fatal(err)
		}
	}
	cn := "leaf"
	if len(o.DNSNames) > 0 {
		cn = o.DNSNames[0]
	}
	tmpl := &x509.Certificate{
		SerialNumber: o.Serial, Subject: pkix.Name{CommonName: cn},
		DNSNames: o.DNSNames, IPAddresses: o.IPs, NotBefore: notBefore, NotAfter: o.NotAfter,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, key.Public(), ca.Key)
	if err != nil {
		tb.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		tb.Fatal(err)
	}
	l := &Leaf{
		Cert:    cert,
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: block, Bytes: keyDER}),
	}
	if l.TLS, err = tls.X509KeyPair(l.CertPEM, l.KeyPEM); err != nil {
		tb.Fatal(err)
	}
	return l
}

// Write writes the certificate and the key to the paths, in place: the files
// keep their names and are overwritten, the way an operator or a test rotates.
func (l *Leaf) Write(tb testing.TB, certFile, keyFile string) {
	tb.Helper()
	WriteFile(tb, certFile, l.CertPEM)
	WriteFile(tb, keyFile, l.KeyPEM)
}

// Files writes the pair into dir as name.crt and name.key and returns the paths.
func (l *Leaf) Files(tb testing.TB, dir, name string) (certFile, keyFile string) {
	tb.Helper()
	certFile, keyFile = filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key")
	l.Write(tb, certFile, keyFile)
	return certFile, keyFile
}

// WriteFile overwrites path with data (mode 0600), in place.
func WriteFile(tb testing.TB, path string, data []byte) {
	tb.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		tb.Fatal(err)
	}
}

func serial(tb testing.TB) *big.Int {
	tb.Helper()
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		tb.Fatal(err)
	}
	return n.Add(n, big.NewInt(1))
}
