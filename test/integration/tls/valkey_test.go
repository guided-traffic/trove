//go:build integration

package tls

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/tlsfiles"
	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// valkeyConfig is a replica's Valkey configuration against the stack with the
// CA from caFile. Connections live one second, so new handshakes happen all the
// time without disturbing the connections of other suites.
func valkeyConfig(t *testing.T, caFile string) config.Valkey {
	t.Helper()
	v := testenv.ValkeyConfig(t)
	dir := t.TempDir()
	return config.Valkey{
		Mode:         "sentinel",
		KeyPrefix:    v.KeyPrefix,
		UsernameFile: write(t, dir, "username", v.Username+"\n"),
		PasswordFile: write(t, dir, "password", v.Password+"\n"),
		Sentinel: config.ValkeySentinel{
			Addresses: v.Sentinels, PrimaryName: v.PrimaryName,
			PasswordFile: write(t, dir, "sentinel-password", v.SentinelPassword+"\n"),
			TLS:          true, RefreshInterval: time.Second,
		},
		TLS:                  config.ValkeyTLS{Mode: "verify-full", CAFile: caFile},
		ConnectTimeout:       2 * time.Second,
		CommandTimeout:       500 * time.Millisecond,
		SentinelQueryTimeout: 300 * time.Millisecond,
		MaxConnLifetime:      time.Second,
		HealthCheckInterval:  200 * time.Millisecond,
		Reconnect:            config.ValkeyReconnect{InitialBackoff: 50 * time.Millisecond, MaxBackoff: 500 * time.Millisecond},
		CircuitBreaker:       config.ValkeyBreaker{FailureThreshold: 1000, OpenDuration: 100 * time.Millisecond},
	}
}

// pingWithin polls PING until it gives the wanted outcome and returns the last
// error.
func pingWithin(t *testing.T, raw cluster.IntegrationRaw, wantOK bool, within time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(within)
	var err error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err = raw.Ping(ctx)
		cancel()
		if (err == nil) == wantOK {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("PING ok=%v not reached within %s (last error %v)", wantOK, within, err)
	return err
}

func TestValkeyTLS(t *testing.T) {
	dir := t.TempDir()
	caFile := copyCA(t, dir, "valkey-ca.crt")
	reg := prometheus.NewRegistry()
	m, err := tlsfiles.NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	v, err := cluster.NewValkey(t.Context(), valkeyConfig(t, caFile), "it-tls-"+randHex(3), "all", quiet, prometheus.NewRegistry(),
		cluster.WithKeyspaceID(randHex(6)), cluster.WithTLSMetrics(m))
	if err != nil {
		t.Fatalf("NewValkey: %v", err)
	}
	t.Cleanup(v.Close)
	raw := cluster.Raw(v)
	stack, err := os.ReadFile(testenv.CAFile(t))
	if err != nil {
		t.Fatal(err)
	}
	// Whatever happens below, the CA file ends as the stack's CA.
	t.Cleanup(func() { tlstest.WriteFile(t, caFile, stack) })

	t.Run("R-HA-056 Valkey and the Sentinels are reached over TLS with verify-full", func(t *testing.T) {
		if err := v.StartupError(); err != nil {
			t.Fatal(err)
		}
		pingWithin(t, raw, true, 10*time.Second)
	})

	t.Run("R-HA-056 R-ARCH-035 a CA file rewritten in place is used by the next handshake without a restart", func(t *testing.T) {
		tlstest.WriteFile(t, caFile, tlstest.NewCA(t, "unrelated ca").PEM)
		// the recycled connections no longer verify the server
		if err := pingWithin(t, raw, false, 10*time.Second); !strings.Contains(err.Error(), "certificate") {
			t.Fatalf("PING failed, but not on the certificate: %v", err)
		}
		tlstest.WriteFile(t, caFile, stack)
		pingWithin(t, raw, true, 15*time.Second)
		if n := seriesValue(t, reg, tlsfiles.FamilyReloads, caFile); n < 2 {
			t.Errorf("trove_tls_reloads_total{kind=valkey} = %v, want at least 2", n)
		}
	})

	t.Run("R-HA-056 R-ARCH-035 a CA file that does not load keeps the last good CA across new handshakes", func(t *testing.T) {
		before := seriesValue(t, reg, tlsfiles.FamilyReloadErrors, caFile)
		tlstest.WriteFile(t, caFile, []byte("not a certificate"))
		end := time.Now().Add(3 * time.Second) // three connection lifetimes
		for time.Now().Before(end) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			err := raw.Ping(ctx)
			cancel()
			if err != nil {
				t.Fatalf("PING failed with a broken CA file: %v", err)
			}
			time.Sleep(100 * time.Millisecond)
		}
		if after := seriesValue(t, reg, tlsfiles.FamilyReloadErrors, caFile); after != before+1 {
			t.Errorf("reload errors %v -> %v, want one more", before, after)
		}
	})
}
