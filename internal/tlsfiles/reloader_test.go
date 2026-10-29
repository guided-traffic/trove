package tlsfiles_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/guided-traffic/trove/internal/tlsfiles"
	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
)

func listener(t *testing.T, spec tlsfiles.Spec) (*tlsfiles.Reloader, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	m, err := tlsfiles.NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	return tlsfiles.New(spec, tlsfiles.Labels{Kind: tlsfiles.KindListener, Listener: "client"}, tlsfiles.WithMetrics(m)), reg
}

func metric(t *testing.T, reg *prometheus.Registry, family, name string) (float64, bool) {
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
					return value(m), true
				}
			}
		}
	}
	return 0, false
}

func value(m *dto.Metric) float64 {
	if m.Gauge != nil {
		return m.GetGauge().GetValue()
	}
	return m.GetCounter().GetValue()
}

func serialOf(t *testing.T, r *tlsfiles.Reloader, sni string) *big.Int {
	t.Helper()
	c, err := r.Certificate(&tls.ClientHelloInfo{
		ServerName: sni, SignatureSchemes: []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256, tls.PSSWithSHA256, tls.PKCS1WithSHA256},
		SupportedVersions: []uint16{tls.VersionTLS13, tls.VersionTLS12}, SupportedCurves: []tls.CurveID{tls.CurveP256, tls.X25519},
		CipherSuites:    []uint16{tls.TLS_AES_128_GCM_SHA256, tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256, tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256},
		SupportedPoints: []uint8{0},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c.Leaf.SerialNumber
}

func TestReloader(t *testing.T) {
	ca := tlstest.NewCA(t, "test ca")

	t.Run("R-ARCH-035 a changed pair is served within the reload interval", func(t *testing.T) {
		dir := t.TempDir()
		first := ca.Issue(t, tlstest.Options{Serial: big.NewInt(101)})
		certFile, keyFile := first.Files(t, dir, "tls")
		r, reg := listener(t, tlsfiles.Spec{Pairs: []tlsfiles.Pair{{CertFile: certFile, KeyFile: keyFile}}})
		if err := r.Loaded(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		const interval = 100 * time.Millisecond
		go r.Run(ctx, interval)

		ca.Issue(t, tlstest.Options{Serial: big.NewInt(102)}).Write(t, certFile, keyFile)
		start := time.Now()
		for serialOf(t, r, "localhost").Int64() != 102 {
			if time.Since(start) > interval+time.Second {
				t.Fatalf("new serial not served after %s", time.Since(start))
			}
			time.Sleep(10 * time.Millisecond)
		}
		if v, _ := metric(t, reg, tlsfiles.FamilyReloads, certFile); v != 1 {
			t.Errorf("trove_tls_reloads_total = %v, want 1", v)
		}
	})

	t.Run("R-ARCH-035 a Secret volume's symlink swap is picked up", func(t *testing.T) {
		// The kubelet writes a new timestamped directory and swaps the ..data
		// symlink; tls.crt and tls.key are symlinks into ..data.
		vol := t.TempDir()
		v1, v2 := filepath.Join(vol, "..2026_10_29_v1"), filepath.Join(vol, "..2026_10_29_v2")
		for _, d := range []string{v1, v2} {
			if err := os.Mkdir(d, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		ca.Issue(t, tlstest.Options{Serial: big.NewInt(151)}).Write(t, filepath.Join(v1, "tls.crt"), filepath.Join(v1, "tls.key"))
		ca.Issue(t, tlstest.Options{Serial: big.NewInt(152)}).Write(t, filepath.Join(v2, "tls.crt"), filepath.Join(v2, "tls.key"))
		data := filepath.Join(vol, "..data")
		if err := os.Symlink(filepath.Base(v1), data); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"tls.crt", "tls.key"} {
			if err := os.Symlink(filepath.Join("..data", f), filepath.Join(vol, f)); err != nil {
				t.Fatal(err)
			}
		}
		certFile, keyFile := filepath.Join(vol, "tls.crt"), filepath.Join(vol, "tls.key")
		r, _ := listener(t, tlsfiles.Spec{Pairs: []tlsfiles.Pair{{CertFile: certFile, KeyFile: keyFile}}})
		if got := serialOf(t, r, "localhost").Int64(); got != 151 {
			t.Fatalf("serving %d, want 151", got)
		}
		tmp := filepath.Join(vol, "..data_tmp")
		if err := os.Symlink(filepath.Base(v2), tmp); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, data); err != nil { // the atomic swap
			t.Fatal(err)
		}
		if err := r.Reload(); err != nil {
			t.Fatal(err)
		}
		if got := serialOf(t, r, "localhost").Int64(); got != 152 {
			t.Fatalf("serving %d after the symlink swap, want 152", got)
		}
	})

	t.Run("R-ARCH-035 the content is compared, not the file time", func(t *testing.T) {
		dir := t.TempDir()
		leaf := ca.Issue(t, tlstest.Options{Serial: big.NewInt(201)})
		certFile, keyFile := leaf.Files(t, dir, "tls")
		r, reg := listener(t, tlsfiles.Spec{Pairs: []tlsfiles.Pair{{CertFile: certFile, KeyFile: keyFile}}})
		gen := r.Generation()
		future := time.Now().Add(time.Hour)
		_ = os.Chtimes(certFile, future, future)
		leaf.Write(t, certFile, keyFile) // same bytes, new mtime
		if err := r.Reload(); err != nil {
			t.Fatal(err)
		}
		if r.Generation() != gen {
			t.Error("unchanged content swapped the material")
		}
		if v, _ := metric(t, reg, tlsfiles.FamilyReloads, certFile); v != 0 {
			t.Errorf("reloads = %v for unchanged content", v)
		}
	})

	t.Run("R-ARCH-035 a key that does not match keeps the old pair and counts one error", func(t *testing.T) {
		dir := t.TempDir()
		good := ca.Issue(t, tlstest.Options{Serial: big.NewInt(301)})
		certFile, keyFile := good.Files(t, dir, "tls")
		r, reg := listener(t, tlsfiles.Spec{Pairs: []tlsfiles.Pair{{CertFile: certFile, KeyFile: keyFile}}})
		other := ca.Issue(t, tlstest.Options{Serial: big.NewInt(302)})
		tlstest.WriteFile(t, certFile, other.CertPEM) // the certificate of another key
		for range 3 {
			if err := r.Reload(); err == nil || !strings.Contains(err.Error(), "does not match") {
				t.Fatalf("Reload = %v", err)
			}
		}
		if got := serialOf(t, r, "localhost").Int64(); got != 301 {
			t.Errorf("serving %d, want the old 301", got)
		}
		if v, _ := metric(t, reg, tlsfiles.FamilyReloadErrors, certFile); v != 1 {
			t.Errorf("trove_tls_reload_errors_total = %v, want 1 (one distinct content)", v)
		}
		if r.Loaded() != nil {
			t.Error("Loaded fails although a pair has been served")
		}
		// the next valid pair is served
		tlstest.WriteFile(t, keyFile, other.KeyPEM)
		if err := r.Reload(); err != nil {
			t.Fatal(err)
		}
		if got := serialOf(t, r, "localhost").Int64(); got != 302 {
			t.Errorf("serving %d after a valid pair, want 302", got)
		}
	})

	t.Run("R-ARCH-035 a half-written or garbage file keeps the old pair", func(t *testing.T) {
		dir := t.TempDir()
		good := ca.Issue(t, tlstest.Options{Serial: big.NewInt(401)})
		certFile, keyFile := good.Files(t, dir, "tls")
		r, _ := listener(t, tlsfiles.Spec{Pairs: []tlsfiles.Pair{{CertFile: certFile, KeyFile: keyFile}}})
		next := ca.Issue(t, tlstest.Options{Serial: big.NewInt(402)})
		tlstest.WriteFile(t, certFile, next.CertPEM[:len(next.CertPEM)/2])
		if err := r.Reload(); err == nil {
			t.Fatal("half a certificate loaded")
		}
		tlstest.WriteFile(t, certFile, []byte("not a certificate"))
		_ = r.Reload()
		if err := os.Remove(keyFile); err != nil {
			t.Fatal(err)
		}
		_ = r.Reload()
		if got := serialOf(t, r, "localhost").Int64(); got != 401 {
			t.Errorf("serving %d, want 401", got)
		}
	})

	t.Run("R-ARCH-035 an empty file read during an in-place rewrite is not counted; one that stays empty is", func(t *testing.T) {
		dir := t.TempDir()
		good := ca.Issue(t, tlstest.Options{Serial: big.NewInt(451)})
		certFile, keyFile := good.Files(t, dir, "tls")
		r, reg := listener(t, tlsfiles.Spec{Pairs: []tlsfiles.Pair{{CertFile: certFile, KeyFile: keyFile}}})
		// A writer that truncates and then writes is read in between.
		tlstest.WriteFile(t, certFile, nil)
		_ = r.Reload()
		if v, _ := metric(t, reg, tlsfiles.FamilyReloadErrors, certFile); v != 0 {
			t.Fatalf("trove_tls_reload_errors_total = %v after one empty read, want 0", v)
		}
		tlstest.WriteFile(t, certFile, []byte("not a certificate"))
		_ = r.Reload()
		if v, _ := metric(t, reg, tlsfiles.FamilyReloadErrors, certFile); v != 1 {
			t.Fatalf("trove_tls_reload_errors_total = %v for the broken content, want 1", v)
		}
		// An empty file that stays empty is refused on the second pass and counted once.
		tlstest.WriteFile(t, certFile, nil)
		for range 3 {
			_ = r.Reload()
		}
		if v, _ := metric(t, reg, tlsfiles.FamilyReloadErrors, certFile); v != 2 {
			t.Errorf("trove_tls_reload_errors_total = %v with a file that stays empty, want 2", v)
		}
		if got := serialOf(t, r, "localhost").Int64(); got != 451 {
			t.Errorf("serving %d, want the old 451", got)
		}
	})

	t.Run("R-ARCH-035 an expired certificate is refused", func(t *testing.T) {
		dir := t.TempDir()
		good := ca.Issue(t, tlstest.Options{Serial: big.NewInt(501)})
		certFile, keyFile := good.Files(t, dir, "tls")
		r, _ := listener(t, tlsfiles.Spec{Pairs: []tlsfiles.Pair{{CertFile: certFile, KeyFile: keyFile}}})
		ca.Issue(t, tlstest.Options{Serial: big.NewInt(502), NotAfter: time.Now().Add(-time.Minute)}).Write(t, certFile, keyFile)
		if err := r.Reload(); err == nil || !strings.Contains(err.Error(), "expired") {
			t.Fatalf("Reload = %v", err)
		}
		if got := serialOf(t, r, "localhost").Int64(); got != 501 {
			t.Errorf("serving %d, want 501", got)
		}
	})

	t.Run("R-ARCH-035 a pair that never loaded serves nothing and Loaded says why", func(t *testing.T) {
		dir := t.TempDir()
		certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
		r, reg := listener(t, tlsfiles.Spec{Pairs: []tlsfiles.Pair{{CertFile: certFile, KeyFile: keyFile}}})
		if err := r.Loaded(); err == nil || !strings.Contains(err.Error(), "tls.crt") {
			t.Fatalf("Loaded = %v", err)
		}
		if _, err := r.Certificate(&tls.ClientHelloInfo{}); err == nil {
			t.Fatal("a certificate was served before any loaded")
		}
		if v, _ := metric(t, reg, tlsfiles.FamilyReloadErrors, certFile); v != 1 {
			t.Errorf("errors = %v, want 1", v)
		}
		_ = r.Reload() // the same missing file counts once
		if v, _ := metric(t, reg, tlsfiles.FamilyReloadErrors, certFile); v != 1 {
			t.Errorf("errors = %v after a second pass, want 1", v)
		}
		ca.Issue(t, tlstest.Options{Serial: big.NewInt(601)}).Write(t, certFile, keyFile)
		if err := r.Reload(); err != nil {
			t.Fatal(err)
		}
		if r.Loaded() != nil || serialOf(t, r, "").Int64() != 601 {
			t.Error("the pair that appeared is not served")
		}
	})

	t.Run("R-ARCH-035 the expiry gauge holds the notAfter of the served certificate", func(t *testing.T) {
		dir := t.TempDir()
		notAfter := time.Now().Add(90 * time.Minute).Truncate(time.Second)
		certFile, keyFile := ca.Issue(t, tlstest.Options{NotAfter: notAfter}).Files(t, dir, "tls")
		caFile := ca.WriteCA(t, dir, "ca.crt")
		_, reg := listener(t, tlsfiles.Spec{Pairs: []tlsfiles.Pair{{CertFile: certFile, KeyFile: keyFile}}, CAFiles: []string{caFile}})
		if v, ok := metric(t, reg, tlsfiles.FamilyExpiry, certFile); !ok || int64(v) != notAfter.Unix() {
			t.Errorf("expiry = %v (present %v), want %d", v, ok, notAfter.Unix())
		}
		if v, ok := metric(t, reg, tlsfiles.FamilyExpiry, caFile); !ok || int64(v) != ca.Cert.NotAfter.Unix() {
			t.Errorf("CA expiry = %v, want %d", v, ca.Cert.NotAfter.Unix())
		}
	})
}

func TestSNI(t *testing.T) {
	ca := tlstest.NewCA(t, "test ca")
	dir := t.TempDir()
	def := ca.Issue(t, tlstest.Options{Serial: big.NewInt(1), DNSNames: []string{"trove.example"}, IPs: []net.IP{net.IPv4(127, 0, 0, 1)}})
	wild := ca.Issue(t, tlstest.Options{Serial: big.NewInt(2), DNSNames: []string{"*.registry.example"}})
	exact := ca.Issue(t, tlstest.Options{Serial: big.NewInt(3), DNSNames: []string{"docker.registry.example"}})
	rsaLeaf := ca.Issue(t, tlstest.Options{Serial: big.NewInt(4), DNSNames: []string{"rsa.example"}, RSA: true})
	var pairs []tlsfiles.Pair
	for i, l := range []*tlstest.Leaf{def, wild, exact, rsaLeaf} {
		c, k := l.Files(t, dir, "p"+string(rune('0'+i)))
		pairs = append(pairs, tlsfiles.Pair{CertFile: c, KeyFile: k})
	}
	r, _ := listener(t, tlsfiles.Spec{Pairs: pairs})
	if err := r.Loaded(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, sni string
		want      int64
	}{
		{"R-ARCH-035 SNI picks the pair whose name matches", "trove.example", 1},
		{"R-ARCH-035 SNI matches a wildcard certificate", "npm.registry.example", 2},
		{"R-ARCH-035 SNI prefers the exact name over a wildcard listed first", "docker.registry.example", 3},
		{"R-ARCH-035 SNI is case-insensitive and ignores a trailing dot", "NPM.Registry.Example.", 2},
		{"R-ARCH-035 a wildcard covers one label only", "a.b.registry.example", 1},
		{"R-ARCH-035 an unknown name gets the first pair", "other.example", 1},
		{"R-ARCH-035 no SNI gets the first pair", "", 1},
		{"R-ARCH-035 an RSA pair is served by name too", "rsa.example", 4},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := serialOf(t, r, c.sni).Int64(); got != c.want {
				t.Errorf("SNI %q served serial %d, want %d", c.sni, got, c.want)
			}
		})
	}

	t.Run("R-ARCH-035 SNI selection through a real handshake", func(t *testing.T) {
		ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{GetCertificate: r.Certificate, MinVersion: tls.VersionTLS12})
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				_ = c.(*tls.Conn).Handshake()
				_ = c.Close()
			}
		}()
		for sni, want := range map[string]int64{"npm.registry.example": 2, "docker.registry.example": 3} {
			conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{ServerName: sni, RootCAs: ca.Pool(), MinVersion: tls.VersionTLS12})
			if err != nil {
				t.Fatalf("%s: %v", sni, err)
			}
			if got := conn.ConnectionState().PeerCertificates[0].SerialNumber.Int64(); got != want {
				t.Errorf("%s: serial %d, want %d", sni, got, want)
			}
			_ = conn.Close()
		}
	})
}

func TestTrustPool(t *testing.T) {
	ca := tlstest.NewCA(t, "first")
	other := tlstest.NewCA(t, "second")
	dir := t.TempDir()
	server := ca.Issue(t, tlstest.Options{})

	verifies := func(pool *x509.CertPool, leaf *tlstest.Leaf) bool {
		_, err := leaf.Cert.Verify(x509.VerifyOptions{Roots: pool, DNSName: "localhost"})
		return err == nil
	}

	t.Run("R-ARCH-035 a CA bundle rewritten in place is the pool of the next pass", func(t *testing.T) {
		caFile := other.WriteCA(t, dir, "rotating.crt")
		r := tlsfiles.New(tlsfiles.Spec{CAFiles: []string{caFile}}, tlsfiles.Labels{Kind: tlsfiles.KindDatabase})
		if verifies(r.Pool(), server) {
			t.Fatal("unrelated CA verified the server")
		}
		tlstest.WriteFile(t, caFile, append(append([]byte{}, other.PEM...), ca.PEM...))
		r.Refresh(0)
		if !verifies(r.Pool(), server) {
			t.Fatal("rotated bundle not used")
		}
		tlstest.WriteFile(t, caFile, []byte("garbage"))
		r.Refresh(0)
		if !verifies(r.Pool(), server) {
			t.Error("a broken bundle replaced the last good one")
		}
	})

	t.Run("R-ARCH-035 a configured bundle that never loaded trusts nothing, never the system roots", func(t *testing.T) {
		r := tlsfiles.New(tlsfiles.Spec{CAFiles: []string{filepath.Join(dir, "missing.crt")}}, tlsfiles.Labels{Kind: tlsfiles.KindValkey})
		if r.Pool() == nil {
			t.Fatal("nil pool: crypto/tls would fall back to the system roots")
		}
		if r.Loaded() == nil {
			t.Error("Loaded is nil for a missing bundle")
		}
	})

	t.Run("R-ARCH-035 without CA files and system roots the pool is nil", func(t *testing.T) {
		if tlsfiles.New(tlsfiles.Spec{}, tlsfiles.Labels{Kind: tlsfiles.KindListener}).Pool() != nil {
			t.Error("pool without trust configured")
		}
	})

	t.Run("R-ARCH-022 Refresh with a max age reads at most once per age", func(t *testing.T) {
		caFile := other.WriteCA(t, dir, "throttled.crt")
		now := time.Now()
		r := tlsfiles.New(tlsfiles.Spec{CAFiles: []string{caFile}}, tlsfiles.Labels{Kind: tlsfiles.KindOutbound},
			tlsfiles.WithClock(func() time.Time { return now }))
		tlstest.WriteFile(t, caFile, ca.PEM)
		r.Refresh(10 * time.Second)
		if verifies(r.Pool(), server) {
			t.Fatal("read again before the max age passed")
		}
		now = now.Add(11 * time.Second)
		r.Refresh(10 * time.Second)
		if !verifies(r.Pool(), server) {
			t.Fatal("not read after the max age")
		}
	})
}

func TestClientCertificate(t *testing.T) {
	ca := tlstest.NewCA(t, "client ca")
	dir := t.TempDir()

	t.Run("R-HA-056 without a pair no client certificate is presented", func(t *testing.T) {
		c, err := tlsfiles.New(tlsfiles.Spec{}, tlsfiles.Labels{Kind: tlsfiles.KindValkey}).ClientCertificate(nil)
		if err != nil || len(c.Certificate) != 0 {
			t.Fatalf("%v %v", c, err)
		}
	})

	t.Run("R-HA-056 a configured pair that never loaded fails the handshake", func(t *testing.T) {
		r := tlsfiles.New(tlsfiles.Spec{Pairs: []tlsfiles.Pair{{CertFile: filepath.Join(dir, "none.crt"), KeyFile: filepath.Join(dir, "none.key")}}},
			tlsfiles.Labels{Kind: tlsfiles.KindValkey})
		if _, err := r.ClientCertificate(nil); err == nil {
			t.Fatal("connected without the configured client certificate")
		}
	})

	t.Run("R-HA-015 a rotated client pair is presented after the next pass", func(t *testing.T) {
		certFile, keyFile := ca.Issue(t, tlstest.Options{Serial: big.NewInt(7)}).Files(t, dir, "client")
		r := tlsfiles.New(tlsfiles.Spec{Pairs: []tlsfiles.Pair{{CertFile: certFile, KeyFile: keyFile}}}, tlsfiles.Labels{Kind: tlsfiles.KindDatabase})
		ca.Issue(t, tlstest.Options{Serial: big.NewInt(8)}).Write(t, certFile, keyFile)
		r.Refresh(0)
		c, err := r.ClientCertificate(nil)
		if err != nil || c.Leaf.SerialNumber.Int64() != 8 {
			t.Fatalf("%v %v", c, err)
		}
	})
}

func TestMetricsRegistration(t *testing.T) {
	t.Run("R-OPS-007 a second registration on the same registry shares the families", func(t *testing.T) {
		reg := prometheus.NewRegistry()
		if _, err := tlsfiles.NewMetrics(reg); err != nil {
			t.Fatal(err)
		}
		if _, err := tlsfiles.NewMetrics(reg); err != nil {
			t.Fatalf("second NewMetrics: %v", err)
		}
	})
}
