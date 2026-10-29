//go:build integration

package tls

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/tlsfiles"
	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
)

// newConnection drops every pooled connection and runs one statement, so the
// statement runs on a connection opened now.
func newConnection(db *postgres.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db.Main().Reset()
	_, err := db.Main().Exec(ctx, "SELECT 1")
	return err
}

func TestPostgresTLS(t *testing.T) {
	dir := t.TempDir()
	caFile := copyCA(t, dir, "database-ca.crt")
	cfg := loadConfig(t, config.CommandMigrate, databaseEnv(t, dir, newDatabase(t), caFile)).Database
	reg := prometheus.NewRegistry()
	m, err := tlsfiles.NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, cfg, "it-tls", meta.RoleWeb, quiet, postgres.WithTLSMetrics(m))
	if err != nil {
		t.Fatalf("Open with verify-full: %v", err)
	}
	t.Cleanup(db.Close)
	stranger := tlstest.NewCA(t, "unrelated ca")

	t.Run("R-HA-015 a verify-full connection to the stack's PostgreSQL works", func(t *testing.T) {
		if cfg.SSLMode != "verify-full" {
			t.Fatalf("ssl_mode %q", cfg.SSLMode)
		}
		if err := db.Ready(ctx); err != nil {
			t.Fatal(err)
		}
		if err := newConnection(db); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("R-HA-015 R-ARCH-035 a root CA rewritten in place is used by the next new connection without a restart", func(t *testing.T) {
		tlstest.WriteFile(t, caFile, stranger.PEM)
		if err := newConnection(db); err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Fatalf("a connection with an unrelated CA: %v", err)
		}
		copyCA(t, dir, "database-ca.crt") // the stack's CA again, in place
		if err := newConnection(db); err != nil {
			t.Fatalf("the restored CA was not used by the next connection: %v", err)
		}
		if v := seriesValue(t, reg, tlsfiles.FamilyReloads, caFile); v < 2 {
			t.Errorf("trove_tls_reloads_total{kind=database} = %v, want at least 2", v)
		}
	})

	t.Run("R-HA-015 R-ARCH-035 a CA file that does not load keeps the last good CA, counted and with no failed connection", func(t *testing.T) {
		before := seriesValue(t, reg, tlsfiles.FamilyReloadErrors, caFile)
		tlstest.WriteFile(t, caFile, []byte("-----BEGIN CERTIFICATE-----\ncut off"))
		if err := newConnection(db); err != nil {
			t.Fatalf("a broken CA file failed the connection: %v", err)
		}
		if after := seriesValue(t, reg, tlsfiles.FamilyReloadErrors, caFile); after != before+1 {
			t.Errorf("reload errors %v -> %v, want one more", before, after)
		}
	})
}
