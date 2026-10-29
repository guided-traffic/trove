package tlsfiles_test

import (
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/tlsfiles"
	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
)

func TestReloaderReview2(t *testing.T) {
	ca := tlstest.NewCA(t, "ca")

	t.Run("R-ARCH-035 CR2-7 one distinct bad content is counted once across a read error", func(t *testing.T) {
		dir := t.TempDir()
		caFile := ca.WriteCA(t, dir, "ca.crt")
		r, reg := listener(t, tlsfiles.Spec{CAFiles: []string{caFile}})
		bad := []byte("-----BEGIN CERTIFICATE-----\nhalf")
		tlstest.WriteFile(t, caFile, bad)
		_ = r.Reload()
		_ = r.Reload() // same content: not counted again
		n1, _ := metric(t, reg, tlsfiles.FamilyReloadErrors, caFile)
		_ = os.Remove(caFile)
		_ = r.Reload() // read error: a distinct error, counted
		_ = r.Reload() // the same read error: not counted again
		tlstest.WriteFile(t, caFile, bad)
		_ = r.Reload() // the same bad content as before the read error
		n2, _ := metric(t, reg, tlsfiles.FamilyReloadErrors, caFile)
		if n2 != n1+1 {
			t.Errorf("errors %v -> %v, want %v (the read error counts, the same bad content does not count again)", n1, n2, n1+1)
		}
	})

	t.Run("R-ARCH-035 SA10 a certificate that is not valid yet is refused and the old pair stays", func(t *testing.T) {
		dir := t.TempDir()
		certFile, keyFile := ca.Issue(t, tlstest.Options{Serial: big.NewInt(701)}).Files(t, dir, "tls")
		r, _ := listener(t, tlsfiles.Spec{Pairs: []tlsfiles.Pair{{CertFile: certFile, KeyFile: keyFile}}})
		ca.Issue(t, tlstest.Options{Serial: big.NewInt(702), NotBefore: time.Now().Add(10 * time.Minute), NotAfter: time.Now().Add(time.Hour)}).Write(t, certFile, keyFile)
		if err := r.Reload(); err == nil || !strings.Contains(err.Error(), "not valid") {
			t.Fatalf("Reload = %v", err)
		}
		if got := serialOf(t, r, "localhost").Int64(); got != 701 {
			t.Errorf("serving %d, want 701", got)
		}
	})

	t.Run("R-ARCH-035 SA10 the same not-yet-valid content loads once its time has come, and counts once before", func(t *testing.T) {
		dir := t.TempDir()
		certFile, keyFile := ca.Issue(t, tlstest.Options{Serial: big.NewInt(801)}).Files(t, dir, "tls")
		now := time.Now()
		clock := func() time.Time { return now }
		reg, m := metricsFor(t)
		r := tlsfiles.New(tlsfiles.Spec{Pairs: []tlsfiles.Pair{{CertFile: certFile, KeyFile: keyFile}}},
			tlsfiles.Labels{Kind: tlsfiles.KindListener, Listener: "client"}, tlsfiles.WithMetrics(m), tlsfiles.WithClock(clock))
		ca.Issue(t, tlstest.Options{Serial: big.NewInt(802), NotBefore: now.Add(10 * time.Minute), NotAfter: now.Add(time.Hour)}).Write(t, certFile, keyFile)
		_ = r.Reload()
		_ = r.Reload()
		if v, _ := metric(t, reg, tlsfiles.FamilyReloadErrors, certFile); v != 1 {
			t.Errorf("errors = %v after two passes over the same content, want 1", v)
		}
		now = now.Add(11 * time.Minute)
		if err := r.Reload(); err != nil {
			t.Fatalf("Reload after NotBefore = %v", err)
		}
		if got := serialOf(t, r, "localhost").Int64(); got != 802 {
			t.Errorf("serving %d, want 802", got)
		}
	})
}

func metricsFor(t *testing.T) (*prometheus.Registry, *tlsfiles.Metrics) {
	t.Helper()
	reg := prometheus.NewRegistry()
	m, err := tlsfiles.NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	return reg, m
}
