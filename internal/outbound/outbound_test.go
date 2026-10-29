package outbound

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/observability"
)

// defaultCfg is proxy.outbound with the defaults of internal/config, so the
// tests check the policy an installation gets without configuring anything.
func defaultCfg() config.Outbound {
	c := config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true, MinVersion: "1.2"}}
	for _, k := range config.MustDescribe().Keys {
		if k.Path == "proxy.outbound.address_policy.deny_cidrs" {
			if err := json.Unmarshal([]byte(k.Default), &c.AddressPolicy.DenyCIDRs); err != nil {
				panic(err)
			}
		}
	}
	if len(c.AddressPolicy.DenyCIDRs) == 0 {
		panic("proxy.outbound.address_policy.deny_cidrs has no default")
	}
	return c
}

func allowLoopback() config.Outbound {
	c := defaultCfg()
	c.AddressPolicy.AllowCIDRs = []string{"127.0.0.0/8", "::1/128"}
	return c
}

// transportOf returns the http.Transport a client uses now: the one of the
// current generation of its trust store.
func transportOf(t *testing.T, c *http.Client) *http.Transport {
	t.Helper()
	tt, ok := c.Transport.(*roundTripper).base.(*trustedTransport)
	if !ok {
		t.Fatalf("client transport is %T, not the trust-aware transport", c.Transport.(*roundTripper).base)
	}
	tr, _ := tt.current()
	return tr
}

func get(t *testing.T, c *http.Client, u string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c.Do(req)
}

// getErr performs a GET and returns only the error, closing any response.
func getErr(t *testing.T, c *http.Client, u string) error {
	t.Helper()
	resp, err := get(t, c, u)
	if resp != nil {
		resp.Body.Close()
	}
	return err
}

func TestFactoryTimeouts(t *testing.T) {
	f := New(defaultCfg())

	t.Run("R-ARCH-022 purposes get timeouts and the transport carries them", func(t *testing.T) {
		for purpose, wantOverall := range map[string]time.Duration{
			PurposeJWKS: 30 * time.Second, PurposeOIDCDiscovery: 30 * time.Second, PurposeWebhook: 30 * time.Second,
			PurposeRemote: 0, PurposeObjectStore: 0,
		} {
			c := f.Client(purpose)
			if c.Timeout != wantOverall {
				t.Errorf("%s: overall timeout %s, want %s", purpose, c.Timeout, wantOverall)
			}
			tr := transportOf(t, c)
			if tr.TLSHandshakeTimeout == 0 || tr.ResponseHeaderTimeout == 0 || tr.IdleConnTimeout == 0 {
				t.Errorf("%s: transport without timeouts: %+v", purpose, tr)
			}
			if tr.TLSClientConfig.MinVersion != tls.VersionTLS12 {
				t.Errorf("%s: min TLS version %x", purpose, tr.TLSClientConfig.MinVersion)
			}
			if tr.TLSClientConfig.InsecureSkipVerify {
				t.Errorf("%s: verification disabled", purpose)
			}
		}
	})

	t.Run("R-ARCH-022 a remote's socket timeout maps to connect and response-header timeouts", func(t *testing.T) {
		c := f.Client(PurposeRemote, WithSocketTimeout(7*time.Second), WithTimeout(time.Minute))
		tr := transportOf(t, c)
		if tr.ResponseHeaderTimeout != 7*time.Second || c.Timeout != time.Minute {
			t.Errorf("header %s overall %s", tr.ResponseHeaderTimeout, c.Timeout)
		}
	})

	t.Run("R-ARCH-022 the response-header timeout is enforced", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(2 * time.Second):
			case <-r.Context().Done():
			}
		}))
		defer srv.Close()
		c := New(allowLoopback()).Client(PurposeRemote, WithResponseHeaderTimeout(100*time.Millisecond))
		start := time.Now()
		err := getErr(t, c, srv.URL)
		if err == nil || time.Since(start) > time.Second {
			t.Errorf("err = %v after %s", err, time.Since(start))
		}
	})

	t.Run("R-ARCH-022 the overall timeout is enforced", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			select {
			case <-time.After(2 * time.Second):
			case <-r.Context().Done():
			}
		}))
		defer srv.Close()
		c := New(allowLoopback()).Client(PurposeJWKS, WithTimeout(150*time.Millisecond))
		resp, err := get(t, c, srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if _, err := io.ReadAll(resp.Body); err == nil {
			t.Error("body read did not time out")
		}
	})

	t.Run("R-ARCH-022 every client sends User-Agent trove/<version>", func(t *testing.T) {
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.Header.Get("User-Agent") }))
		defer srv.Close()
		resp, err := get(t, New(allowLoopback(), WithVersion("v0.3.0")).Client(PurposeRemote), srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got != "trove/0.3.0" {
			t.Errorf("User-Agent = %q", got)
		}
	})

	t.Run("R-ARCH-022 responses are not decompressed behind the caller's back", func(t *testing.T) {
		var enc string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { enc = r.Header.Get("Accept-Encoding") }))
		defer srv.Close()
		resp, _ := get(t, New(allowLoopback()).Client(PurposeRemote), srv.URL)
		resp.Body.Close()
		if enc != "" {
			t.Errorf("transport added Accept-Encoding %q", enc)
		}
	})
}

func TestEgressPolicy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	t.Run("R-ARCH-022 loopback is refused at connect time with a typed error", func(t *testing.T) {
		err := getErr(t, New(defaultCfg()).Client(PurposeRemote), srv.URL)
		var ed *EgressDeniedError
		if !errors.Is(err, ErrEgressDenied) || !errors.As(err, &ed) || ed.Addr.String() != "127.0.0.1" {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("R-ARCH-022 the cloud metadata address is refused without a connection attempt", func(t *testing.T) {
		start := time.Now()
		err := getErr(t, New(defaultCfg()).Client(PurposeRemote, WithConnectTimeout(5*time.Second)), "http://169.254.169.254/latest/")
		if !errors.Is(err, ErrEgressDenied) || time.Since(start) > time.Second {
			t.Errorf("err = %v after %s", err, time.Since(start))
		}
	})

	t.Run("R-ARCH-022 a GET to an IPv6 loopback server through a zoned literal is refused", func(t *testing.T) {
		ln, err := net.Listen("tcp6", "[::1]:0")
		if err != nil {
			t.Fatalf("this test needs IPv6 loopback on the host: %v", err)
		}
		var seen atomic.Int32
		v6 := &httptest.Server{Listener: ln, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen.Add(1) }), ReadHeaderTimeout: time.Second}}
		v6.Start()
		defer v6.Close()
		_, p6, _ := net.SplitHostPort(ln.Addr().String())
		// The unzoned form is refused by ::1/128; the zoned form must be too.
		for _, u := range []string{"http://[::1]:" + p6 + "/", "http://[::1%251]:" + p6 + "/", "http://[::1%25lo0]:" + p6 + "/", "http://[::1%25lo]:" + p6 + "/"} {
			if err := getErr(t, New(defaultCfg()).Client(PurposeRemote), u); !errors.Is(err, ErrEgressDenied) {
				t.Errorf("%s: err = %v", u, err)
			}
		}
		// Behind an outbound proxy, trove's own check of the target refuses it.
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen.Add(1) }))
		defer proxy.Close()
		pu, _ := url.Parse(proxy.URL)
		cfg := defaultCfg()
		cfg.AddressPolicy.AllowCIDRs = []string{"127.0.0.0/8"} // the proxy itself listens on 127.0.0.1
		if err := getErr(t, New(cfg).Client(PurposeRemote, WithProxyURL(pu)), "http://[::1%251]:"+p6+"/"); !errors.Is(err, ErrEgressDenied) {
			t.Errorf("through the proxy: err = %v", err)
		}
		if seen.Load() != 0 {
			t.Errorf("%d requests reached a server", seen.Load())
		}
	})

	t.Run("R-ARCH-022 an IPv4-mapped IPv6 loopback is refused", func(t *testing.T) {
		err := getErr(t, New(defaultCfg()).Client(PurposeRemote), "http://[::ffff:127.0.0.1]:"+port+"/")
		if !errors.Is(err, ErrEgressDenied) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("R-ARCH-022 a name that resolves to a denied address is refused after resolution", func(t *testing.T) {
		err := getErr(t, New(defaultCfg()).Client(PurposeRemote), "http://localhost:"+port+"/")
		if !errors.Is(err, ErrEgressDenied) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("R-ARCH-022 allow_cidrs permits an address inside a denied range", func(t *testing.T) {
		resp, err := get(t, New(allowLoopback()).Client(PurposeRemote), srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	})

	t.Run("R-ARCH-022 private ranges are permitted by default", func(t *testing.T) {
		f := New(defaultCfg())
		for _, a := range []string{"10.20.0.5", "192.168.1.1", "172.16.0.1", "100.64.0.1", "fd12::1", "93.184.216.34", "2001:db8::1", "64:ff9b::5db8:d822"} {
			if err := f.CheckAddr(netip.MustParseAddr(a)); err != nil {
				t.Errorf("%s: %v", a, err)
			}
		}
		for _, a := range []string{"127.0.0.1", "::1", "0.0.0.0", "169.254.169.254", "fe80::1", "fd00:ec2::254", "224.0.0.1", "ff02::1", "255.255.255.255", "::ffff:127.0.0.1", "::ffff:169.254.169.254"} {
			if err := f.CheckAddr(netip.MustParseAddr(a)); !errors.Is(err, ErrEgressDenied) {
				t.Errorf("%s: %v", a, err)
			}
		}
	})

	t.Run("R-ARCH-022 zoned IPv6 addresses are refused, also where allow_cidrs holds the unzoned form", func(t *testing.T) {
		cfg := defaultCfg()
		cfg.AddressPolicy.AllowCIDRs = []string{"::/0"}
		for name, f := range map[string]*Factory{"default": New(defaultCfg()), "allow everything": New(cfg)} {
			for _, a := range []string{"::1%1", "::%1", "fd00:ec2::254%eth0", "fe80::1%eth0", "2001:db8::1%eth0"} {
				err := f.CheckAddr(netip.MustParseAddr(a))
				var ed *EgressDeniedError
				if !errors.Is(err, ErrEgressDenied) || !errors.As(err, &ed) || ed.Rule != ruleZoned {
					t.Errorf("%s: %s: %v", name, a, err)
				}
			}
		}
	})

	t.Run("R-ARCH-022 the Control hook refuses a zoned literal before connecting", func(t *testing.T) {
		p := New(defaultCfg()).policy
		for _, addr := range []string{"[::1%1]:443", "[::%1]:80", "[fd00:ec2::254%eth0]:80", "[fe80::1%eth0]:80"} {
			if err := p.control(nil)("tcp6", addr, nil); !errors.Is(err, ErrEgressDenied) {
				t.Errorf("%s: %v", addr, err)
			}
		}
	})

	t.Run("R-ARCH-022 the default deny list holds the further metadata and IPv4-compatible ranges", func(t *testing.T) {
		f := New(defaultCfg())
		for _, a := range []string{"100.100.100.200", "168.63.129.16", "::127.0.0.1", "::a9fe:a9fe", "::ffff:100.100.100.200"} {
			if err := f.CheckAddr(netip.MustParseAddr(a)); !errors.Is(err, ErrEgressDenied) {
				t.Errorf("%s: %v", a, err)
			}
		}
	})

	t.Run("R-ARCH-022 a NAT64 address is checked as the IPv4 address it reaches", func(t *testing.T) {
		f := New(defaultCfg())
		for _, a := range []string{"64:ff9b::a9fe:a9fe", "64:ff9b::7f00:1", "64:ff9b::6464:64c8", "64:ff9b::a83f:8110"} {
			err := f.CheckAddr(netip.MustParseAddr(a))
			var ed *EgressDeniedError
			if !errors.As(err, &ed) || !strings.Contains(ed.Rule, "NAT64") {
				t.Errorf("%s: %v", a, err)
			}
		}
		cfg := defaultCfg()
		cfg.AddressPolicy.AllowCIDRs = []string{"64:ff9b::a9fe:a9fe/128"}
		if err := New(cfg).CheckAddr(netip.MustParseAddr("64:ff9b::a9fe:a9fe")); err != nil {
			t.Errorf("an explicit allow of the NAT64 address is not honoured: %v", err)
		}
	})

	t.Run("R-ARCH-022 the object store purpose is not subject to the address policy", func(t *testing.T) {
		resp, err := get(t, New(defaultCfg()).Client(PurposeObjectStore), srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	})

	t.Run("R-ARCH-022 a denied connection counts in trove_outbound_egress_denied_total", func(t *testing.T) {
		m := observability.NewMetrics("web")
		_ = getErr(t, New(defaultCfg(), WithMetrics(m)).Client(PurposeRemote), srv.URL)
		body := scrape(t, m)
		if !strings.Contains(body, `trove_outbound_egress_denied_total{kind="remote"} 1`) {
			t.Errorf("metrics:\n%s", body)
		}
		if !strings.Contains(body, `trove_outbound_requests_total{code="error",kind="remote"} 1`) {
			t.Errorf("requests:\n%s", body)
		}
		if errs := m.Check(); len(errs) != 0 {
			t.Error(errs)
		}
	})

	t.Run("R-ARCH-022 behind an outbound proxy the target is checked before the request goes out", func(t *testing.T) {
		var seen atomic.Int32
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen.Add(1) }))
		defer proxy.Close()
		pu, _ := url.Parse(proxy.URL)
		c := New(allowLoopbackExceptMetadata()).Client(PurposeRemote, WithProxyURL(pu))
		err := getErr(t, c, "http://169.254.169.254/latest/")
		if !errors.Is(err, ErrEgressDenied) || seen.Load() != 0 {
			t.Errorf("err = %v, proxy saw %d", err, seen.Load())
		}
		resp, err := get(t, c, "http://127.0.0.1:1/via-proxy")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if seen.Load() != 1 {
			t.Errorf("proxy saw %d requests", seen.Load())
		}
	})

	t.Run("R-ARCH-022 bypass hosts skip the proxy", func(t *testing.T) {
		var proxied atomic.Int32
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxied.Add(1) }))
		defer proxy.Close()
		pu, _ := url.Parse(proxy.URL)
		c := New(allowLoopback()).Client(PurposeRemote, WithProxyURL(pu, "127.0.0.*"))
		resp, err := get(t, c, srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if proxied.Load() != 0 {
			t.Error("bypassed host went through the proxy")
		}
	})
}

func allowLoopbackExceptMetadata() config.Outbound { return allowLoopback() }

func scrape(t *testing.T, m *observability.Metrics) string {
	t.Helper()
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return w.Body.String()
}

func writeCert(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ca.pem")
	b := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTrustStore(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "secure") }))
	defer srv.Close()

	t.Run("R-ARCH-022 a certificate from an unknown CA is refused", func(t *testing.T) {
		err := getErr(t, New(allowLoopback()).Client(PurposeRemote), srv.URL)
		var ua interface{ Error() string }
		if err == nil || !errors.As(err, &ua) || !strings.Contains(err.Error(), "certificate") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("R-ARCH-022 a configured CA bundle makes the certificate verify", func(t *testing.T) {
		cfg := allowLoopback()
		cfg.TLS.CAFiles = []string{writeCert(t, srv)}
		f := New(cfg)
		if f.Err() != nil {
			t.Fatal(f.Err())
		}
		resp, err := get(t, f.Client(PurposeRemote), srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	})

	t.Run("R-ARCH-022 an unreadable CA file fails closed with a clear message", func(t *testing.T) {
		cfg := allowLoopback()
		cfg.TLS.CAFiles = []string{"/nonexistent/ca.pem"}
		f := New(cfg)
		if f.Err() == nil || !strings.Contains(f.Err().Error(), "ca_files") {
			t.Fatalf("Err = %v", f.Err())
		}
		err := getErr(t, f.Client(PurposeRemote), srv.URL)
		if err == nil || !strings.Contains(err.Error(), "outbound client unavailable") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("R-ARCH-022 no system roots and no CA file is an error", func(t *testing.T) {
		cfg := allowLoopback()
		cfg.TLS.UseSystemRoots = false
		if New(cfg).Err() == nil {
			t.Error("empty trust store accepted")
		}
	})

	t.Run("R-ARCH-022 a bad CIDR in the policy is an error", func(t *testing.T) {
		cfg := defaultCfg()
		cfg.AddressPolicy.AllowCIDRs = []string{"not-a-cidr"}
		if New(cfg).Err() == nil {
			t.Error("bad CIDR accepted")
		}
	})

	t.Run("R-ARCH-022 min_version 1.3 raises the floor", func(t *testing.T) {
		cfg := allowLoopback()
		cfg.TLS.MinVersion = "1.3"
		tr := transportOf(t, New(cfg).Client(PurposeRemote))
		if tr.TLSClientConfig.MinVersion != tls.VersionTLS13 {
			t.Error("floor not raised")
		}
	})
}

func TestRedirects(t *testing.T) {
	t.Run("R-ARCH-022 redirects stop after the limit", func(t *testing.T) {
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, srv.URL+"/again", http.StatusFound)
		}))
		defer srv.Close()
		err := getErr(t, New(allowLoopback()).Client(PurposeRemote, WithMaxRedirects(3)), srv.URL)
		if !errors.Is(err, ErrTooManyRedirects) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("R-ARCH-022 an https to http redirect is refused", func(t *testing.T) {
		plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		defer plain.Close()
		tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, plain.URL, http.StatusFound)
		}))
		defer tlsSrv.Close()
		cfg := allowLoopback()
		cfg.TLS.CAFiles = []string{writeCert(t, tlsSrv)}
		err := getErr(t, New(cfg).Client(PurposeRemote), tlsSrv.URL)
		if !errors.Is(err, ErrInsecureRedirect) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("R-ARCH-022 every redirect hop passes the egress policy", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://169.254.169.254/latest/", http.StatusFound)
		}))
		defer srv.Close()
		err := getErr(t, New(allowLoopback()).Client(PurposeRemote), srv.URL)
		if !errors.Is(err, ErrEgressDenied) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("R-ARCH-022 a format can restrict redirect targets", func(t *testing.T) {
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				http.Redirect(w, r, srv.URL+"/x", http.StatusFound)
			}
		}))
		defer srv.Close()
		errNo := errors.New("host not accepted")
		c := New(allowLoopback()).Client(PurposeRemote, WithRedirectCheck(func(*http.Request, []*http.Request) error { return errNo }))
		if err := getErr(t, c, srv.URL); !errors.Is(err, errNo) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestTracePropagation(t *testing.T) {
	var gotParent atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotParent.Store(r.Header.Get("Traceparent"))
	}))
	defer srv.Close()

	run := func(t *testing.T, propagate bool) (parent, traceID string, exp *tracetest.InMemoryExporter) {
		t.Helper()
		exp = tracetest.NewInMemoryExporter()
		tr, err := observability.Tracing(config.Tracing{Enabled: true, SampleRatio: 1, PropagateToUpstreams: propagate, QueueSize: 8},
			observability.WithSpanExporter(exp))
		if err != nil {
			t.Fatal(err)
		}
		ctx, span := tr.Start(context.Background(), "request", trace.SpanKindServer)
		defer span.End()
		gotParent.Store("")
		c := New(allowLoopback(), WithTracer(tr)).Client(PurposeRemote)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/pkg?token=abc", nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		_ = tr.ForceFlush(context.Background())
		return gotParent.Load().(string), observability.RequestID(ctx), exp
	}

	t.Run("R-OPS-018 propagate_to_upstreams sends traceparent with the trace ID of the request", func(t *testing.T) {
		parent, traceID, exp := run(t, true)
		if !regexp.MustCompile(`^00-` + traceID + `-[0-9a-f]{16}-0[01]$`).MatchString(parent) {
			t.Errorf("traceparent = %q, trace %s", parent, traceID)
		}
		var found bool
		for _, s := range exp.GetSpans() {
			if s.SpanKind == trace.SpanKindClient {
				found = true
				if !strings.HasSuffix(parent[:len(parent)-3], s.SpanContext.SpanID().String()) {
					t.Errorf("traceparent span %q is not the client span %s", parent, s.SpanContext.SpanID())
				}
				for _, a := range s.Attributes {
					if strings.Contains(a.Value.String(), "token=abc") {
						t.Errorf("query in client span: %s=%s", a.Key, a.Value.String())
					}
				}
			}
		}
		if !found {
			t.Error("no client span")
		}
	})

	t.Run("R-OPS-018 without propagate_to_upstreams the upstream sees no trace context", func(t *testing.T) {
		parent, _, exp := run(t, false)
		if parent != "" {
			t.Errorf("traceparent leaked to upstream: %q", parent)
		}
		var client bool
		for _, s := range exp.GetSpans() {
			client = client || s.SpanKind == trace.SpanKindClient
		}
		if !client {
			t.Error("client span should still be recorded")
		}
	})
}

func TestUpstreamLog(t *testing.T) {
	t.Run("R-OPS-009 R-OPS-013 the upstream record has its fields and a scrubbed URL", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "12345") }))
		defer srv.Close()
		var buf bytes.Buffer
		lg := observability.NewLogger(config.Log{Level: "info", Format: "json", Request: config.LogRequest{RedactQueryParams: observability.DefaultQueryParams()}}, &buf)
		f := New(allowLoopback(), WithUpstreamLog(lg))
		u := strings.Replace(srv.URL, "http://", "http://svc:topsecret@", 1) + "/p?access_token=xyz&v=1"
		ctx := WithCall(context.Background(), Call{OnBehalfOf: "alice", Attempt: 2})
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		resp, err := f.Client(PurposeRemote, WithLogAttrs(slog.String("repo", "npm-remote"))).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		var rec map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
			t.Fatalf("%v: %s", err, buf.String())
		}
		want := map[string]any{
			"log": "upstream", "purpose": "remote", "repo": "npm-remote", "subscription": nil, "method": "GET",
			"status": float64(200), "response_bytes": float64(5), "attempt": float64(2), "on_behalf_of": "alice", "error": nil,
		}
		for k, v := range want {
			if rec[k] != v {
				t.Errorf("%s = %#v, want %#v", k, rec[k], v)
			}
		}
		raw := buf.String()
		if strings.Contains(raw, "topsecret") || strings.Contains(raw, "xyz") || !strings.Contains(raw, "v=1") {
			t.Errorf("URL not scrubbed: %s", raw)
		}
		for _, k := range []string{"url", "request_bytes", "duration_ms"} {
			if _, ok := rec[k]; !ok {
				t.Errorf("%s missing", k)
			}
		}
	})

	t.Run("R-OPS-009 a transport error is logged with status 0 and a scrubbed message", func(t *testing.T) {
		var buf bytes.Buffer
		lg := observability.NewLogger(config.Log{Level: "info", Format: "json"}, &buf)
		err := getErr(t, New(defaultCfg(), WithUpstreamLog(lg)).Client(PurposeWebhook), "http://127.0.0.1:9/")
		if !errors.Is(err, ErrEgressDenied) {
			t.Fatal(err)
		}
		var rec map[string]any
		_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec)
		if rec["status"] != float64(0) || rec["level"] != "WARN" || !strings.Contains(rec["error"].(string), "egress denied") {
			t.Errorf("record = %v", rec)
		}
	})
}

func TestNoOtherClients(t *testing.T) {
	t.Run("R-ARCH-022 no package outside internal/outbound uses the default client or builds a transport", func(t *testing.T) {
		root := filepath.Join("..")
		banned := regexp.MustCompile(`http\.DefaultClient|http\.DefaultTransport|http\.Transport\{|\bhttp\.(Get|Post|Head|PostForm)\(`)
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			if d.IsDir() {
				if rel == "outbound" || rel == "archtest" || rel == "tools" || d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if m := banned.Find(b); m != nil {
				t.Errorf("%s uses %q", rel, m)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestNewDialer(t *testing.T) {
	t.Run("R-ARCH-022 NewDialer builds a working infrastructure dialer", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		defer srv.Close()
		d := NewDialer(time.Second, nil)
		conn, err := d.DialContext(context.Background(), "tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
		if d.Timeout != time.Second || d.KeepAlive == 0 {
			t.Errorf("dialer = %+v", d)
		}
	})
}
