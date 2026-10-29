package outbound

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/tlsfiles"
	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
)

// tlsServer serves "ok" with a certificate of ca and counts new connections.
func tlsServer(t *testing.T, ca *tlstest.CA) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var conns atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{ca.Issue(t, tlstest.Options{}).TLS}, MinVersion: tls.VersionTLS12}
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, &conns
}

func okGet(t *testing.T, c *http.Client, u string) error {
	t.Helper()
	resp, err := get(t, c, u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// seriesValue reads the series of a TLS family whose name label is name.
func seriesValue(t *testing.T, reg *prometheus.Registry, family, name string) float64 {
	t.Helper()
	fams, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() != family {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "name" && l.GetValue() == name {
					return m.GetCounter().GetValue() + m.GetGauge().GetValue()
				}
			}
		}
	}
	t.Fatalf("no %s series for %s", family, name)
	return 0
}

// eventually retries fn until it returns nil or the bound passes.
func eventually(t *testing.T, bound time.Duration, fn func() error) {
	t.Helper()
	end := time.Now().Add(bound)
	for {
		err := fn()
		if err == nil {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("not within %s: %v", bound, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestTrustReload(t *testing.T) {
	serverCA := tlstest.NewCA(t, "upstream ca")
	otherCA := tlstest.NewCA(t, "unrelated ca")
	srv, conns := tlsServer(t, serverCA)
	const every = 50 * time.Millisecond

	t.Run("R-ARCH-022 a CA bundle rewritten in place is used by the next connection without a restart", func(t *testing.T) {
		caFile := otherCA.WriteCA(t, t.TempDir(), "ca.crt")
		cfg := allowLoopback()
		cfg.TLS.UseSystemRoots, cfg.TLS.CAFiles = false, []string{caFile}
		f := New(cfg, WithTrustCheckEvery(every))
		c := f.Client(PurposeRemote)
		if err := okGet(t, c, srv.URL); err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Fatalf("unrelated CA: err = %v", err)
		}
		tlstest.WriteFile(t, caFile, serverCA.PEM)
		eventually(t, every+time.Second, func() error { return okGet(t, c, srv.URL) })
	})

	t.Run("R-ARCH-022 a rotation closes the idle connections and the next request connects again", func(t *testing.T) {
		caFile := serverCA.WriteCA(t, t.TempDir(), "ca.crt")
		cfg := allowLoopback()
		cfg.TLS.UseSystemRoots, cfg.TLS.CAFiles = false, []string{caFile}
		c := New(cfg, WithTrustCheckEvery(every)).Client(PurposeRemote)
		if err := okGet(t, c, srv.URL); err != nil {
			t.Fatal(err)
		}
		before := conns.Load()
		if err := okGet(t, c, srv.URL); err != nil || conns.Load() != before {
			t.Fatalf("second request did not reuse the connection: %v, %d new", err, conns.Load()-before)
		}
		tlstest.WriteFile(t, caFile, append(append([]byte{}, otherCA.PEM...), serverCA.PEM...))
		time.Sleep(2 * every)
		if err := okGet(t, c, srv.URL); err != nil {
			t.Fatal(err)
		}
		if conns.Load() == before {
			t.Error("the request after the rotation reused a connection verified against the old bundle")
		}
	})

	t.Run("R-ARCH-022 a broken bundle keeps the last good one and counts a reload error", func(t *testing.T) {
		caFile := serverCA.WriteCA(t, t.TempDir(), "ca.crt")
		reg := prometheus.NewRegistry()
		m, err := tlsfiles.NewMetrics(reg)
		if err != nil {
			t.Fatal(err)
		}
		cfg := allowLoopback()
		cfg.TLS.UseSystemRoots, cfg.TLS.CAFiles = false, []string{caFile}
		f := New(cfg, WithTrustCheckEvery(every), WithTLSMetrics(m))
		c := f.Client(PurposeRemote)
		tlstest.WriteFile(t, caFile, []byte("-----BEGIN CERTIFICATE-----\ntruncated"))
		time.Sleep(2 * every)
		if err := okGet(t, c, srv.URL); err != nil {
			t.Fatalf("the broken bundle replaced the good one: %v", err)
		}
		if f.Err() != nil {
			t.Errorf("Err = %v: a bundle that loaded once keeps the factory usable", f.Err())
		}
		if v := seriesValue(t, reg, tlsfiles.FamilyReloadErrors, caFile); v != 1 {
			t.Errorf("trove_tls_reload_errors_total = %v, want 1", v)
		}
	})

	t.Run("R-ARCH-022 a CA file missing at start fails closed and recovers when it appears", func(t *testing.T) {
		caFile := filepath.Join(t.TempDir(), "later.crt")
		cfg := allowLoopback()
		cfg.TLS.UseSystemRoots, cfg.TLS.CAFiles = false, []string{caFile}
		f := New(cfg, WithTrustCheckEvery(every))
		if f.Err() == nil || !strings.Contains(f.Err().Error(), "ca_files") {
			t.Fatalf("Err = %v", f.Err())
		}
		c := f.Client(PurposeRemote)
		if err := okGet(t, c, srv.URL); err == nil || !strings.Contains(err.Error(), "outbound client unavailable") {
			t.Fatalf("err = %v", err)
		}
		serverCA.WriteCA(t, filepath.Dir(caFile), "later.crt")
		eventually(t, every+time.Second, func() error { return okGet(t, c, srv.URL) })
		if f.Err() != nil {
			t.Errorf("Err = %v after the file appeared", f.Err())
		}
	})

	t.Run("R-ARCH-022 Err looks at a missing CA file again, without a request, and clears once it loads", func(t *testing.T) {
		caFile := filepath.Join(t.TempDir(), "later.crt")
		cfg := allowLoopback()
		cfg.TLS.UseSystemRoots, cfg.TLS.CAFiles = false, []string{caFile}
		f := New(cfg, WithTrustCheckEvery(every))
		if f.Err() == nil {
			t.Fatal("Err is nil without the file")
		}
		serverCA.WriteCA(t, filepath.Dir(caFile), "later.crt")
		eventually(t, every+time.Second, func() error { return f.Err() })
	})

	t.Run("R-ARCH-022 WithRootCAs trusts its own bundle only, not the factory's", func(t *testing.T) {
		dir := t.TempDir()
		cfg := allowLoopback()
		cfg.TLS.CAFiles = []string{serverCA.WriteCA(t, dir, "factory.crt")}
		f := New(cfg, WithTrustCheckEvery(every))
		if err := okGet(t, f.Client(PurposeRemote), srv.URL); err != nil {
			t.Fatalf("factory trust: %v", err)
		}
		own, err := f.TrustStore(PurposeObjectStore, otherCA.WriteCA(t, dir, "own.crt"))
		if err != nil {
			t.Fatal(err)
		}
		if err := okGet(t, f.Client(PurposeObjectStore, WithRootCAs(own)), srv.URL); err == nil {
			t.Fatal("a client with its own bundle trusted the factory's CA")
		}
		if _, err := f.TrustStore(PurposeObjectStore, filepath.Join(dir, "missing.crt")); err == nil {
			t.Fatal("TrustStore accepted a missing file")
		}
	})
}
