package app

import (
	"crypto/tls"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
)

func httpsGet(t *testing.T, ca *tlstest.CA, url string) (int, string, *tls.ConnectionState, error) {
	t.Helper()
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{RootCAs: ca.Pool(), MinVersion: tls.VersionTLS12},
	}}
	resp, err := c.Get(url)
	if err != nil {
		return 0, "", nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.TLS, nil
}

func certificatesEnv(certFile, keyFile string) string {
	return fmt.Sprintf(`[{"cert_file":%q,"key_file":%q}]`, certFile, keyFile)
}

func TestListenerTLSWiring(t *testing.T) {
	ca := tlstest.NewCA(t, "app ca")

	t.Run("R-ARCH-035 both listeners serve TLS from server.tls, the ops listener by inheritance", func(t *testing.T) {
		dir := t.TempDir()
		certFile, keyFile := ca.Issue(t, tlstest.Options{}).Files(t, dir, "tls")
		h := start(t, "web", map[string]string{"TROVE_SERVER_TLS_CERTIFICATES": certificatesEnv(certFile, keyFile)})
		clientURL := "https://" + h.app.ClientAddr().String()
		opsURL := "https://" + h.app.OpsAddr().String()
		if code, body, _, err := httpsGet(t, ca, clientURL+"/api/v1/system/ping"); err != nil || code != 200 || body != "OK" {
			t.Fatalf("ping over TLS: %d %q %v", code, body, err)
		}
		if code, _, _, err := httpsGet(t, ca, opsURL+"/readyz"); err != nil || code != 200 {
			t.Fatalf("readyz over TLS: %d %v", code, err)
		}
		for _, u := range []string{"http://" + h.app.ClientAddr().String() + "/api/v1/system/ping", "http://" + h.app.OpsAddr().String() + "/readyz"} {
			if resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(u); err == nil {
				resp.Body.Close()
				if resp.StatusCode == 200 || resp.Header.Get("X-Request-Id") != "" {
					t.Errorf("plaintext %s reached trove: %d", u, resp.StatusCode)
				}
			}
		}
		_, metrics, _, err := httpsGet(t, ca, opsURL+"/metrics")
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range []string{"client", "ops"} {
			series := fmt.Sprintf(`trove_tls_certificate_expiry_timestamp_seconds{kind="listener",listener=%q,name=%q}`, l, certFile)
			if !strings.Contains(metrics, series) {
				t.Errorf("metrics lack %s", series)
			}
			if !strings.Contains(metrics, fmt.Sprintf(`trove_tls_reload_errors_total{kind="listener",listener=%q,name=%q} 0`, l, certFile)) {
				t.Errorf("reload error counter of the %s listener not exported at 0", l)
			}
		}
	})

	t.Run("R-ARCH-035 a pair missing at start holds the first readiness and is served without a restart once it appears", func(t *testing.T) {
		dir := t.TempDir()
		certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
		h := start(t, "web", map[string]string{
			"TROVE_SERVER_TLS_CERTIFICATES": certificatesEnv(certFile, keyFile), "TROVE_SERVER_TLS_RELOAD_INTERVAL": "1s",
		})
		time.Sleep(500 * time.Millisecond) // several check rounds
		if h.app.Health().Ready() {
			t.Fatal("ready without a certificate")
		}
		if _, _, _, err := httpsGet(t, ca, "https://"+h.app.OpsAddr().String()+"/readyz"); err == nil {
			t.Fatal("a handshake succeeded without a certificate")
		}
		if !strings.Contains(h.logs.String(), "TLS material did not load") {
			t.Errorf("no error logged:\n%s", h.logs.String())
		}
		ca.Issue(t, tlstest.Options{Serial: big.NewInt(77)}).Write(t, certFile, keyFile)
		deadline := time.Now().Add(5 * time.Second)
		for {
			code, _, cs, err := httpsGet(t, ca, "https://"+h.app.OpsAddr().String()+"/readyz")
			if err == nil && code == 200 && cs.PeerCertificates[0].SerialNumber.Int64() == 77 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("not ready after the pair appeared: %d %v", code, err)
			}
			time.Sleep(100 * time.Millisecond)
		}
	})

	t.Run("R-ARCH-035 a rotation that does not load keeps the old pair and the replica ready", func(t *testing.T) {
		dir := t.TempDir()
		certFile, keyFile := ca.Issue(t, tlstest.Options{Serial: big.NewInt(88)}).Files(t, dir, "tls")
		h := start(t, "web", map[string]string{
			"TROVE_SERVER_TLS_CERTIFICATES": certificatesEnv(certFile, keyFile), "TROVE_SERVER_TLS_RELOAD_INTERVAL": "1s",
		})
		opsURL := "https://" + h.app.OpsAddr().String()
		tlstest.WriteFile(t, keyFile, ca.Issue(t, tlstest.Options{}).KeyPEM) // a key of another certificate
		time.Sleep(1500 * time.Millisecond)
		code, _, cs, err := httpsGet(t, ca, opsURL+"/readyz")
		if err != nil || code != 200 || cs.PeerCertificates[0].SerialNumber.Int64() != 88 {
			t.Fatalf("after a broken rotation: %d %v", code, err)
		}
		_, metrics, _, _ := httpsGet(t, ca, opsURL+"/metrics")
		if !strings.Contains(metrics, fmt.Sprintf(`trove_tls_reload_errors_total{kind="listener",listener="client",name=%q} 1`, certFile)) {
			t.Errorf("the refused rotation is not counted:\n%s", grepLines(metrics, "trove_tls_"))
		}
	})
}

func grepLines(s, sub string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
