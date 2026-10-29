package cluster

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
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

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
)

type testPKI struct {
	caFile, certFile, keyFile string
	serverCert                tls.Certificate
}

// newPKI writes a CA and a server certificate for serverName (and 127.0.0.1).
func newPKI(t *testing.T, serverName string) testPKI {
	t.Helper()
	dir := t.TempDir()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: serverName},
		DNSNames: []string{serverName}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(leafKey)
	p := testPKI{
		caFile: filepath.Join(dir, "ca.pem"), certFile: filepath.Join(dir, "tls.crt"), keyFile: filepath.Join(dir, "tls.key"),
		serverCert: tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey},
	}
	for file, block := range map[string]*pem.Block{
		p.caFile: {Type: "CERTIFICATE", Bytes: caDER}, p.certFile: {Type: "CERTIFICATE", Bytes: leafDER},
		p.keyFile: {Type: "EC PRIVATE KEY", Bytes: keyDER},
	} {
		if err := os.WriteFile(file, pem.EncodeToMemory(block), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// handshake dials a TLS server with cert and reports the client's verdict.
func handshake(t *testing.T, cert tls.Certificate, client *tls.Config) error {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.(*tls.Conn).Handshake()
	}()
	conn, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return tls.Client(conn, client).Handshake()
}

// tlsConfig is the configuration of one handshake as the dialer builds it:
// newValkeyTLS, then handshakeConfig on its base.
func tlsConfig(cfg config.ValkeyTLS) (*tls.Config, error) {
	vt, err := newValkeyTLS(cfg)
	if err != nil || vt == nil {
		return nil, err
	}
	return vt.handshakeConfig(vt.base), nil
}

// tlsAcceptor accepts TLS connections with cert, asking for a client
// certificate if given, and reports the client's certificate serial per
// connection (0: none).
func tlsAcceptor(t *testing.T, cert tls.Certificate) (string, <-chan int64) {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, ClientAuth: tls.RequestClientCert})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	serials := make(chan int64, 16)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c *tls.Conn) {
				defer c.Close()
				if c.Handshake() != nil {
					return
				}
				_, _ = c.Read(make([]byte, 1))
				var s int64
				if pcs := c.ConnectionState().PeerCertificates; len(pcs) > 0 {
					s = pcs[0].SerialNumber.Int64()
				}
				serials <- s
			}(c.(*tls.Conn))
		}
	}()
	return ln.Addr().String(), serials
}

func TestTLSReload(t *testing.T) {
	serverCA := tlstest.NewCA(t, "valkey ca")
	otherCA := tlstest.NewCA(t, "unrelated ca")
	addr, serials := tlsAcceptor(t, serverCA.Issue(t, tlstest.Options{}).TLS)
	dir := t.TempDir()
	caFile := otherCA.WriteCA(t, dir, "ca.crt")
	certFile, keyFile := serverCA.Issue(t, tlstest.Options{Serial: big.NewInt(51)}).Files(t, dir, "client")
	vt, err := newValkeyTLS(config.ValkeyTLS{Mode: "verify-full", CAFile: caFile, CertFile: certFile, KeyFile: keyFile, ServerName: "localhost"})
	if err != nil {
		t.Fatal(err)
	}
	dial := dialFn(nil, vt.prepare)
	connect := func() error {
		d := outbound.NewDialer(time.Second, nil)
		c, err := dial(context.Background(), addr, &d, vt.baseConfig())
		if err != nil {
			return err
		}
		_, _ = c.Write([]byte("x"))
		return c.Close()
	}

	t.Run("R-HA-056 R-ARCH-035 a CA bundle rewritten in place is used by the next handshake", func(t *testing.T) {
		if err := connect(); err == nil {
			t.Fatal("an unrelated CA verified the server")
		}
		tlstest.WriteFile(t, caFile, serverCA.PEM)
		if err := connect(); err != nil {
			t.Fatalf("the rewritten CA was not used: %v", err)
		}
		if s := <-serials; s != 51 {
			t.Fatalf("client certificate serial %d, want 51", s)
		}
	})

	t.Run("R-HA-056 R-ARCH-035 a client certificate rotated in place is presented by the next handshake", func(t *testing.T) {
		serverCA.Issue(t, tlstest.Options{Serial: big.NewInt(52)}).Write(t, certFile, keyFile)
		if err := connect(); err != nil {
			t.Fatal(err)
		}
		if s := <-serials; s != 52 {
			t.Fatalf("client certificate serial %d, want 52", s)
		}
	})

	t.Run("R-HA-056 R-ARCH-035 broken files keep the last good material", func(t *testing.T) {
		tlstest.WriteFile(t, caFile, []byte("garbage"))
		tlstest.WriteFile(t, keyFile, []byte("garbage"))
		if err := connect(); err != nil {
			t.Fatalf("a broken file replaced the last good material: %v", err)
		}
		if s := <-serials; s != 52 {
			t.Fatalf("client certificate serial %d, want the last good 52", s)
		}
	})

	t.Run("R-HA-056 mode disable dials plaintext", func(t *testing.T) {
		none, err := newValkeyTLS(config.ValkeyTLS{Mode: "disable"})
		if err != nil || none.baseConfig() != nil || none.prepare(nil) != nil {
			t.Fatalf("disable: %v %v", none, err)
		}
	})
}

func TestTLSModes(t *testing.T) {
	pki := newPKI(t, "valkey.example")
	other := newPKI(t, "valkey.example")

	t.Run("R-HA-056 disable gives no TLS", func(t *testing.T) {
		c, err := tlsConfig(config.ValkeyTLS{Mode: "disable"})
		if err != nil || c != nil {
			t.Fatalf("got %v, %v", c, err)
		}
	})
	t.Run("R-HA-056 verify-full checks chain and host name", func(t *testing.T) {
		c, err := tlsConfig(config.ValkeyTLS{Mode: "verify-full", CAFile: pki.caFile})
		if err != nil {
			t.Fatal(err)
		}
		c.ServerName = "other.example"
		if err := handshake(t, pki.serverCert, c); err == nil {
			t.Fatal("verify-full accepted a certificate for another host name")
		}
		named, _ := tlsConfig(config.ValkeyTLS{Mode: "verify-full", CAFile: pki.caFile, ServerName: "valkey.example"})
		if err := handshake(t, pki.serverCert, named); err != nil {
			t.Fatalf("verify-full with server_name: %v", err)
		}
	})
	t.Run("R-HA-056 verify-full and verify-ca refuse a certificate of another CA", func(t *testing.T) {
		for _, mode := range []string{"verify-full", "verify-ca"} {
			c, err := tlsConfig(config.ValkeyTLS{Mode: mode, CAFile: other.caFile, ServerName: "valkey.example"})
			if err != nil {
				t.Fatal(err)
			}
			if err := handshake(t, pki.serverCert, c); err == nil {
				t.Errorf("%s accepted a certificate of an unknown CA", mode)
			}
		}
	})
	t.Run("R-HA-056 verify-ca checks the chain but not the host name", func(t *testing.T) {
		c, err := tlsConfig(config.ValkeyTLS{Mode: "verify-ca", CAFile: pki.caFile, ServerName: "wrong.example"})
		if err != nil {
			t.Fatal(err)
		}
		if err := handshake(t, pki.serverCert, c); err != nil {
			t.Fatalf("verify-ca: %v", err)
		}
	})
	t.Run("R-HA-056 a client certificate is read again for every handshake", func(t *testing.T) {
		c, err := tlsConfig(config.ValkeyTLS{Mode: "verify-full", CAFile: pki.caFile, CertFile: pki.certFile, KeyFile: pki.keyFile})
		if err != nil {
			t.Fatal(err)
		}
		first, err := c.GetClientCertificate(nil)
		if err != nil {
			t.Fatal(err)
		}
		// rotate: overwrite the files with the other pair
		for src, dst := range map[string]string{other.certFile: pki.certFile, other.keyFile: pki.keyFile} {
			b, _ := os.ReadFile(src)
			if err := os.WriteFile(dst, b, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		second, err := c.GetClientCertificate(nil)
		if err != nil {
			t.Fatal(err)
		}
		if string(first.Certificate[0]) == string(second.Certificate[0]) {
			t.Fatal("the rotated client certificate was not picked up")
		}
	})
	t.Run("R-HA-056 an unreadable CA or certificate is an error", func(t *testing.T) {
		if _, err := tlsConfig(config.ValkeyTLS{Mode: "verify-full", CAFile: filepath.Join(t.TempDir(), "none")}); err == nil {
			t.Error("missing CA accepted")
		}
		if _, err := tlsConfig(config.ValkeyTLS{Mode: "verify-full", CAFile: pki.caFile, CertFile: "/none", KeyFile: "/none"}); err == nil {
			t.Error("missing client certificate accepted")
		}
	})
}
