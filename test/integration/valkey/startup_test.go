//go:build integration

package valkey

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func cfgTLSDisabled() config.ValkeyTLS { return config.ValkeyTLS{Mode: "disable"} }

func TestStartupErrors(t *testing.T) {
	v := testenv.ValkeyConfig(t)

	t.Run("R-HA-056 a wrong primary_name is a startup error that names it", func(t *testing.T) {
		cfg := stackConfig(t, 2*time.Second)
		cfg.Sentinel.PrimaryName = "trvoe"
		c := newClient(t, cfg)
		err := c.StartupError()
		if err == nil || !strings.Contains(err.Error(), `"trvoe"`) {
			t.Fatalf("StartupError = %v", err)
		}
		if c.Available() {
			t.Fatal("available with an unknown primary name")
		}
		if !strings.Contains(c.Log.String(), `no Sentinel knows the primary name`) || !strings.Contains(c.Log.String(), "trvoe") {
			t.Fatalf("the cause is not logged:\n%s", c.Log.String())
		}
		if code := c.Status().ReasonCode; code != cluster.ReasonConfigError {
			t.Fatalf("R-OPS-001 reason code = %q, want config_error", code)
		}
	})
	t.Run("R-HA-056 a wrong password is a startup error and the log names WRONGPASS, not the password", func(t *testing.T) {
		cfg := stackConfig(t, 2*time.Second)
		cfg.PasswordFile = writeFile(t, "password", "not-the-password-0451\n")
		c := newClient(t, cfg)
		err := c.StartupError()
		if err == nil || !strings.Contains(err.Error(), "WRONGPASS") {
			t.Fatalf("StartupError = %v", err)
		}
		logs := c.Log.String()
		if !strings.Contains(logs, "WRONGPASS") {
			t.Fatalf("the cause is not logged:\n%s", logs)
		}
		for _, secret := range []string{"not-the-password-0451", v.Password, v.SentinelPassword} {
			if strings.Contains(logs, secret) {
				t.Fatalf("the log contains a password:\n%s", logs)
			}
		}
		if code := c.Status().ReasonCode; code != cluster.ReasonAuthFailed {
			t.Fatalf("R-OPS-001 reason code = %q, want auth_failed", code)
		}
	})
	t.Run("R-HA-056 a wrong Sentinel password is a startup error", func(t *testing.T) {
		cfg := stackConfig(t, 2*time.Second)
		cfg.Sentinel.PasswordFile = writeFile(t, "sentinel-password", "wrong\n")
		c := newClient(t, cfg)
		if err := c.StartupError(); err == nil {
			t.Fatalf("no startup error; status %+v", c.Status())
		}
	})
	t.Run("R-HA-056 a certificate of an unknown CA is a startup error", func(t *testing.T) {
		cfg := stackConfig(t, 2*time.Second)
		cfg.TLS.CAFile = otherCA(t)
		c := newClient(t, cfg)
		err := c.StartupError()
		if err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Fatalf("StartupError = %v (status %+v)", err, c.Status())
		}
	})
	t.Run("R-HA-056 every Sentinel unreachable at first start gives a degraded client, no error", func(t *testing.T) {
		cfg := stackConfig(t, 2*time.Second)
		cfg.Sentinel.Addresses = []string{"127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3"}
		start := time.Now()
		c := newClient(t, cfg) // NewValkey itself returned no error (newClient fails otherwise)
		if took := time.Since(start); took > 8*time.Second {
			t.Fatalf("start-up waited %s for Valkey", took)
		}
		if c.Available() {
			t.Fatal("Available() with every Sentinel unreachable")
		}
		if err := c.StartupError(); err != nil {
			t.Fatalf("an outage is not a startup error: %v", err)
		}
		if r := c.Status().DegradedReason; !strings.Contains(r, "no Sentinel reachable") {
			t.Fatalf("degraded reason = %q", r)
		}
		if code := c.Status().ReasonCode; code != cluster.ReasonNoSentinel {
			t.Fatalf("R-OPS-001 reason code = %q, want no_sentinel", code)
		}
		if got := metric(t, c.Reg, "trove_valkey_available", ""); got != 0 {
			t.Fatalf("trove_valkey_available = %v", got)
		}
	})
	t.Run("R-HA-057 a client that started degraded recovers when a Sentinel answers", func(t *testing.T) {
		cfg := stackConfig(t, 2*time.Second)
		cfg.Sentinel.Addresses = append([]string{"127.0.0.1:1"}, v.Sentinels...)
		c := newClient(t, cfg)
		waitAvailable(t, c) // the unreachable first address is skipped
		if err := c.StartupError(); err != nil {
			t.Fatal(err)
		}
	})
}

// otherCA writes a CA certificate that signed nothing of the stack and returns
// the path of its PEM file.
func otherCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "some other CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return writeFile(t, "other-ca.pem", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
}
