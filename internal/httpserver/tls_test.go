package httpserver

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
)

// tlsListener serves h over TLS with the block and returns the address.
func tlsListener(t *testing.T, b config.ServerTLS, h http.Handler) (*ListenerTLS, string) {
	t.Helper()
	if b.ReloadInterval == 0 {
		b.ReloadInterval = 50 * time.Millisecond
	}
	l, err := NewListenerTLS("client", b)
	if err != nil {
		t.Fatal(err)
	}
	if h == nil {
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, r.Proto) })
	}
	_, addr := start(t, ServerConfig{Handler: h, TLS: l})
	return l, addr
}

func pairOf(certFile, keyFile string) []config.TLSCertificate {
	return []config.TLSCertificate{{CertFile: certFile, KeyFile: keyFile}}
}

// dial performs a handshake and returns the server's leaf serial.
func dial(addr string, c *tls.Config) (int64, error) {
	d := &net.Dialer{Timeout: 2 * time.Second}
	conn, err := tls.DialWithDialer(d, "tcp", addr, c)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	// TLS 1.3 reports a refused client certificate after the handshake, on the
	// first read: read once so the server's verdict arrives.
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if _, err := fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"); err != nil {
		return 0, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return conn.ConnectionState().PeerCertificates[0].SerialNumber.Int64(), nil
}

func TestListenerTLS(t *testing.T) {
	ca := tlstest.NewCA(t, "listener ca")
	trust := func() *tls.Config {
		return &tls.Config{RootCAs: ca.Pool(), ServerName: "localhost", MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}}
	}

	t.Run("R-ARCH-005 R-ARCH-035 HTTP/2 is negotiated over TLS with ALPN and HTTP/1.1 stays on", func(t *testing.T) {
		certFile, keyFile := ca.Issue(t, tlstest.Options{}).Files(t, t.TempDir(), "tls")
		_, addr := tlsListener(t, config.ServerTLS{Certificates: pairOf(certFile, keyFile), MinVersion: "1.2"}, nil)
		for _, proto := range []string{"h2", "http/1.1"} {
			tc := trust()
			tc.NextProtos = []string{proto}
			tr := &http.Transport{TLSClientConfig: tc, ForceAttemptHTTP2: proto == "h2"}
			resp, err := (&http.Client{Transport: tr, Timeout: 5 * time.Second}).Get("https://" + addr + "/")
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			want := map[string]string{"h2": "HTTP/2.0", "http/1.1": "HTTP/1.1"}[proto]
			if string(b) != want {
				t.Errorf("ALPN %s: served over %s", proto, b)
			}
		}
	})

	t.Run("R-ARCH-035 a plaintext request to a TLS listener never reaches the handler", func(t *testing.T) {
		certFile, keyFile := ca.Issue(t, tlstest.Options{}).Files(t, t.TempDir(), "tls")
		reached := false
		_, addr := tlsListener(t, config.ServerTLS{Certificates: pairOf(certFile, keyFile)},
			http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
		resp, err := (&http.Client{Timeout: 2 * time.Second}).Get("http://" + addr + "/")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				t.Fatalf("plaintext request answered %d", resp.StatusCode)
			}
		}
		if reached {
			t.Fatal("a plaintext request reached the handler")
		}
	})

	t.Run("R-ARCH-035 a rotated pair serves the next handshake and an open connection keeps its certificate", func(t *testing.T) {
		dir := t.TempDir()
		certFile, keyFile := ca.Issue(t, tlstest.Options{Serial: big.NewInt(11)}).Files(t, dir, "tls")
		_, addr := tlsListener(t, config.ServerTLS{Certificates: pairOf(certFile, keyFile), ReloadInterval: 100 * time.Millisecond}, nil)
		open, err := tls.Dial("tcp", addr, trust())
		if err != nil {
			t.Fatal(err)
		}
		defer open.Close()
		ca.Issue(t, tlstest.Options{Serial: big.NewInt(12)}).Write(t, certFile, keyFile)
		start := time.Now()
		for {
			serial, err := dial(addr, trust())
			if err != nil {
				t.Fatalf("handshake during the rotation failed: %v", err)
			}
			if serial == 12 {
				break
			}
			if time.Since(start) > 100*time.Millisecond+time.Second {
				t.Fatalf("new certificate not served after %s", time.Since(start))
			}
			time.Sleep(10 * time.Millisecond)
		}
		if got := open.ConnectionState().PeerCertificates[0].SerialNumber.Int64(); got != 11 {
			t.Errorf("the connection opened before the rotation shows serial %d", got)
		}
	})

	t.Run("R-ARCH-035 a pair missing at start binds, refuses handshakes and serves once the files appear", func(t *testing.T) {
		dir := t.TempDir()
		certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
		l, addr := tlsListener(t, config.ServerTLS{Certificates: pairOf(certFile, keyFile)}, nil)
		if l.Loaded() == nil {
			t.Fatal("Loaded is nil without files")
		}
		if _, err := dial(addr, trust()); err == nil {
			t.Fatal("handshake succeeded without a certificate")
		}
		ca.Issue(t, tlstest.Options{Serial: big.NewInt(21)}).Write(t, certFile, keyFile)
		deadline := time.Now().Add(2 * time.Second)
		for {
			if serial, err := dial(addr, trust()); err == nil && serial == 21 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the certificate that appeared is not served")
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err := l.Loaded(); err != nil {
			t.Errorf("Loaded = %v", err)
		}
	})

	t.Run("R-ARCH-035 SNI selects among several pairs, a wildcard included", func(t *testing.T) {
		dir := t.TempDir()
		c1, k1 := ca.Issue(t, tlstest.Options{Serial: big.NewInt(31)}).Files(t, dir, "default")
		c2, k2 := ca.Issue(t, tlstest.Options{Serial: big.NewInt(32), DNSNames: []string{"*.registry.example"}}).Files(t, dir, "wild")
		_, addr := tlsListener(t, config.ServerTLS{Certificates: []config.TLSCertificate{{CertFile: c1, KeyFile: k1}, {CertFile: c2, KeyFile: k2}}}, nil)
		for sni, want := range map[string]int64{"localhost": 31, "npm.registry.example": 32} {
			tc := trust()
			tc.ServerName = sni
			if got, err := dial(addr, tc); err != nil || got != want {
				t.Errorf("SNI %s: serial %d (%v), want %d", sni, got, err, want)
			}
		}
	})

	clientCA := tlstest.NewCA(t, "edge ca")
	strangerCA := tlstest.NewCA(t, "stranger ca")
	// withClient presents leaf whatever the server asks for: a client built from
	// tls.Config.Certificates would drop a certificate whose issuer the server
	// does not list, and the test would prove the no-certificate case instead.
	withClient := func(leaf *tlstest.Leaf) *tls.Config {
		c := trust()
		if leaf != nil {
			c.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &leaf.TLS, nil }
		}
		return c
	}
	mtls := func(t *testing.T, mode string) (*ListenerTLS, string, string) {
		t.Helper()
		dir := t.TempDir()
		certFile, keyFile := ca.Issue(t, tlstest.Options{}).Files(t, dir, "tls")
		caFile := clientCA.WriteCA(t, dir, "client-ca.crt")
		l, addr := tlsListener(t, config.ServerTLS{Certificates: pairOf(certFile, keyFile), ClientCAFile: caFile, ClientAuth: mode}, nil)
		return l, addr, caFile
	}

	t.Run("R-ARCH-035 client_auth require refuses a client without a certificate in the handshake", func(t *testing.T) {
		_, addr, _ := mtls(t, config.ClientAuthRequire)
		if _, err := dial(addr, withClient(nil)); err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-ARCH-035 client_auth require refuses a certificate of another CA and admits one of client_ca_file", func(t *testing.T) {
		_, addr, _ := mtls(t, config.ClientAuthRequire)
		if _, err := dial(addr, withClient(strangerCA.Issue(t, tlstest.Options{}))); err == nil {
			t.Fatal("a certificate of another CA was accepted")
		}
		if _, err := dial(addr, withClient(clientCA.Issue(t, tlstest.Options{}))); err != nil {
			t.Fatalf("a certificate of the client CA was refused: %v", err)
		}
	})

	t.Run("R-ARCH-035 client_auth verify_if_given admits a client without one and refuses a bad one", func(t *testing.T) {
		_, addr, _ := mtls(t, config.ClientAuthVerifyIfGiven)
		if _, err := dial(addr, withClient(nil)); err != nil {
			t.Fatalf("a client without a certificate was refused: %v", err)
		}
		if _, err := dial(addr, withClient(strangerCA.Issue(t, tlstest.Options{}))); err == nil {
			t.Fatal("a certificate of another CA was accepted")
		}
	})

	t.Run("R-ARCH-035 a client CA rotated in place applies to the next handshake", func(t *testing.T) {
		_, addr, caFile := mtls(t, config.ClientAuthRequire)
		stranger := withClient(strangerCA.Issue(t, tlstest.Options{}))
		tlstest.WriteFile(t, caFile, append(append([]byte{}, clientCA.PEM...), strangerCA.PEM...))
		deadline := time.Now().Add(2 * time.Second)
		for {
			if _, err := dial(addr, stranger); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the rotated client CA was not used")
			}
			time.Sleep(20 * time.Millisecond)
		}
	})

	t.Run("R-ARCH-035 min_version 1.3 refuses a TLS 1.2 client", func(t *testing.T) {
		certFile, keyFile := ca.Issue(t, tlstest.Options{}).Files(t, t.TempDir(), "tls")
		_, addr := tlsListener(t, config.ServerTLS{Certificates: pairOf(certFile, keyFile), MinVersion: "1.3"}, nil)
		old := trust()
		old.MaxVersion = tls.VersionTLS12
		if _, err := dial(addr, old); err == nil {
			t.Fatal("a TLS 1.2 client was admitted with min_version 1.3")
		}
		if _, err := dial(addr, trust()); err != nil {
			t.Fatalf("TLS 1.3 client refused: %v", err)
		}
	})

	t.Run("R-ARCH-035 TLS 1.2 offers forward-secret AEAD suites only", func(t *testing.T) {
		certFile, keyFile := ca.Issue(t, tlstest.Options{}).Files(t, t.TempDir(), "tls")
		_, addr := tlsListener(t, config.ServerTLS{Certificates: pairOf(certFile, keyFile)}, nil)
		cbc := trust()
		cbc.MaxVersion, cbc.CipherSuites = tls.VersionTLS12, []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA}
		if _, err := dial(addr, cbc); err == nil {
			t.Fatal("a CBC suite was negotiated")
		}
		gcm := trust()
		gcm.MaxVersion = tls.VersionTLS12
		if _, err := dial(addr, gcm); err != nil {
			t.Fatalf("TLS 1.2 with the default suites refused: %v", err)
		}
	})

	t.Run("R-ARCH-035 without certificates the listener is plain HTTP", func(t *testing.T) {
		l, err := NewListenerTLS("client", config.ServerTLS{})
		if err != nil || l != nil {
			t.Fatalf("%v %v", l, err)
		}
	})
}
