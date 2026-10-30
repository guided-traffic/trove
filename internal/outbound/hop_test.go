package outbound

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/observability"
)

// seen is what a stub recorded of the requests it got.
type seen struct {
	mu   sync.Mutex
	hdrs []http.Header
}

func (s *seen) add(h http.Header) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hdrs = append(s.hdrs, h.Clone())
}

func (s *seen) last(t *testing.T) http.Header {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.hdrs) == 0 {
		t.Fatal("the stub got no request")
	}
	return s.hdrs[len(s.hdrs)-1]
}

// redirectPair starts an origin that redirects every path to a second stub on
// another port, and returns both with what each recorded. Both listen on
// 127.0.0.1, so they are two origins (the port differs) on one host name.
func redirectPair(t *testing.T) (origin, other *httptest.Server, atOrigin, atOther *seen) {
	t.Helper()
	atOrigin, atOther = &seen{}, &seen{}
	other = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atOther.add(r.Header)
		_, _ = io.WriteString(w, "tarball")
	}))
	t.Cleanup(other.Close)
	origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atOrigin.add(r.Header)
		http.Redirect(w, r, other.URL+"/blob", http.StatusFound)
	}))
	t.Cleanup(origin.Close)
	return origin, other, atOrigin, atOther
}

func creds() http.Header {
	return http.Header{"Authorization": {"Bearer s3cret"}, "X-Api-Key": {"k-123"}}
}

func policyFor(t *testing.T, origin *httptest.Server, forward bool, sendTo ...string) *CredentialPolicy {
	t.Helper()
	u, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &CredentialPolicy{Origin: u, SendCredentials: sendTo, ForwardOnRedirect: forward, Headers: creds}
}

func TestHopPolicy(t *testing.T) {
	t.Run("R-REPO-039 R-REPO-033 a redirect to another origin carries no credential and no custom header", func(t *testing.T) {
		origin, _, atOrigin, atOther := redirectPair(t)
		c := New(allowLoopback()).Client(PurposeRemote, WithHopPolicy(policyFor(t, origin, false)))
		if err := getErr(t, c, origin.URL); err != nil {
			t.Fatal(err)
		}
		if h := atOrigin.last(t); h.Get("Authorization") != "Bearer s3cret" || h.Get("X-Api-Key") != "k-123" {
			t.Errorf("origin got %v", h)
		}
		if h := atOther.last(t); h.Get("Authorization") != "" || h.Get("X-Api-Key") != "" {
			t.Errorf("the other origin got %v", h)
		}
	})

	t.Run("R-REPO-039 forwardAuthOnRedirect sends them to the other origin of the chain", func(t *testing.T) {
		origin, _, _, atOther := redirectPair(t)
		c := New(allowLoopback()).Client(PurposeRemote, WithHopPolicy(policyFor(t, origin, true)))
		if err := getErr(t, c, origin.URL); err != nil {
			t.Fatal(err)
		}
		if h := atOther.last(t); h.Get("Authorization") != "Bearer s3cret" || h.Get("X-Api-Key") != "k-123" {
			t.Errorf("the other origin got %v", h)
		}
	})

	t.Run("R-REPO-033 a host of extraHosts with sendCredentials receives them on a redirect", func(t *testing.T) {
		origin, _, _, atOther := redirectPair(t)
		c := New(allowLoopback()).Client(PurposeRemote, WithHopPolicy(policyFor(t, origin, false, "127.0.0.*")))
		if err := getErr(t, c, origin.URL); err != nil {
			t.Fatal(err)
		}
		if h := atOther.last(t); h.Get("Authorization") != "Bearer s3cret" {
			t.Errorf("the extra host got %v", h)
		}
	})

	t.Run("R-REPO-033 a host of extraHosts without sendCredentials receives none", func(t *testing.T) {
		origin, _, _, atOther := redirectPair(t)
		c := New(allowLoopback()).Client(PurposeRemote, WithHopPolicy(policyFor(t, origin, false, "*.registry.example")))
		if err := getErr(t, c, origin.URL); err != nil {
			t.Fatal(err)
		}
		if h := atOther.last(t); h.Get("Authorization") != "" {
			t.Errorf("the extra host got %v", h)
		}
	})

	t.Run("R-REPO-033 a first request to another host never carries them, whatever forwardAuthOnRedirect says", func(t *testing.T) {
		origin, other, _, atOther := redirectPair(t)
		c := New(allowLoopback()).Client(PurposeRemote, WithHopPolicy(policyFor(t, origin, true)))
		if err := getErr(t, c, other.URL); err != nil {
			t.Fatal(err)
		}
		if h := atOther.last(t); h.Get("Authorization") != "" || h.Get("X-Api-Key") != "" {
			t.Errorf("a direct request got %v", h)
		}
	})

	t.Run("R-REPO-033 a credential the caller set itself is removed where it does not belong", func(t *testing.T) {
		origin, other, _, atOther := redirectPair(t)
		c := New(allowLoopback()).Client(PurposeRemote, WithHopPolicy(policyFor(t, origin, false)))
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, other.URL, nil)
		req.Header.Set("Authorization", "Basic Zm9vOmJhcg==")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if h := atOther.last(t); h.Get("Authorization") != "" {
			t.Errorf("got %v", h)
		}
		if req.Header.Get("Authorization") == "" {
			t.Error("the caller's request was changed")
		}
	})

	t.Run("R-REPO-036 a rotated secret applies to the next request", func(t *testing.T) {
		origin, _, atOrigin, _ := redirectPair(t)
		p := policyFor(t, origin, false)
		cur := "one"
		p.Headers = func() http.Header { return http.Header{"X-Api-Key": {cur}} }
		c := New(allowLoopback()).Client(PurposeRemote, WithHopPolicy(p))
		for _, want := range []string{"one", "two"} {
			cur = want
			if err := getErr(t, c, origin.URL); err != nil {
				t.Fatal(err)
			}
			if got := atOrigin.last(t).Get("X-Api-Key"); got != want {
				t.Errorf("X-Api-Key = %q, want %q", got, want)
			}
		}
	})

	t.Run("R-REPO-039 the redirect limit holds with a policy", func(t *testing.T) {
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
			http.Redirect(w, r, srv.URL+"/"+strconv.Itoa(n+1), http.StatusFound)
		}))
		defer srv.Close()
		c := New(allowLoopback()).Client(PurposeRemote, WithHopPolicy(policyFor(t, srv, true)))
		if err := getErr(t, c, srv.URL+"/0"); !errors.Is(err, ErrTooManyRedirects) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("R-REPO-039 hops are counted from the redirect responses", func(t *testing.T) {
		var hops []int
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
			if n < 3 {
				http.Redirect(w, r, srv.URL+"/"+strconv.Itoa(n+1), http.StatusFound)
			}
		}))
		defer srv.Close()
		c := New(allowLoopback()).Client(PurposeRemote, WithHopPolicy(hopRecorder(func(h int) { hops = append(hops, h) })))
		if err := getErr(t, c, srv.URL+"/0"); err != nil {
			t.Fatal(err)
		}
		if len(hops) != 4 || hops[0] != 0 || hops[1] != 1 || hops[2] != 2 || hops[3] != 3 {
			t.Errorf("hops = %v", hops)
		}
	})
}

type hopRecorder func(hop int)

func (h hopRecorder) Decorate(_ *http.Request, hop int) { h(hop) }

// clientCert returns a self-signed client certificate.
func clientCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "trove-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestHopPolicyClientCertificate(t *testing.T) {
	var mu sync.Mutex
	certs := map[string]int{}
	handler := func(name string, redirectTo func() string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			certs[name] = len(r.TLS.PeerCertificates)
			mu.Unlock()
			if redirectTo != nil {
				http.Redirect(w, r, redirectTo(), http.StatusFound)
			}
		})
	}
	start := func(h http.Handler) *httptest.Server {
		s := httptest.NewUnstartedServer(h)
		s.TLS = &tls.Config{ClientAuth: tls.RequestClientCert, MinVersion: tls.VersionTLS12}
		s.StartTLS()
		t.Cleanup(s.Close)
		return s
	}
	other := start(handler("other", nil))
	origin := start(handler("origin", func() string { return other.URL }))
	cfg := allowLoopback()
	cfg.TLS.CAFiles = []string{writeCert(t, origin)} // the two stubs share httptest's certificate
	cert := clientCert(t)

	run := func(t *testing.T, p HopPolicy) {
		t.Helper()
		mu.Lock()
		clear(certs)
		mu.Unlock()
		c := New(cfg).Client(PurposeRemote, WithClientCertificate(cert), WithHopPolicy(p))
		if err := getErr(t, c, origin.URL); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("R-REPO-039 the client certificate goes to the origin and not to another origin of the chain", func(t *testing.T) {
		run(t, policyFor(t, origin, false))
		mu.Lock()
		defer mu.Unlock()
		if certs["origin"] != 1 || certs["other"] != 0 {
			t.Errorf("certificates presented: %v", certs)
		}
	})

	t.Run("R-REPO-039 with forwardAuthOnRedirect it goes to the other origin as well", func(t *testing.T) {
		run(t, policyFor(t, origin, true))
		mu.Lock()
		defer mu.Unlock()
		if certs["origin"] != 1 || certs["other"] != 1 {
			t.Errorf("certificates presented: %v", certs)
		}
	})

	t.Run("R-REPO-039 a policy that does not scope the certificate leaves it to every host", func(t *testing.T) {
		run(t, hopRecorder(func(int) {}))
		mu.Lock()
		defer mu.Unlock()
		if certs["origin"] != 1 || certs["other"] != 1 {
			t.Errorf("certificates presented: %v", certs)
		}
	})
}

func TestUpstreamLogCall(t *testing.T) {
	record := func(t *testing.T, purpose string, call Call, opts ...ClientOption) map[string]any {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "x") }))
		defer srv.Close()
		var buf bytes.Buffer
		lg := observability.NewLogger(config.Log{Level: "info", Format: "json"}, &buf)
		f := New(allowLoopback(), WithUpstreamLog(lg))
		req, _ := http.NewRequestWithContext(WithCall(context.Background(), call), http.MethodGet, srv.URL, nil)
		resp, err := f.Client(purpose, opts...).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		var rec map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
			t.Fatalf("%v: %s", err, buf.String())
		}
		return rec
	}

	t.Run("R-OPS-011 R-REPO-047 a remote record has the flight role, cache status, waiters and repository", func(t *testing.T) {
		rec := record(t, PurposeRemote, Call{
			Repo: "npm-remote", FlightRole: "leader",
			CollapsedWaiters: func() int { return 0 },
			CacheStatus:      func() string { return "trove; fwd=miss" },
		}, WithLogAttrs(slog.String("repo", "ignored")))
		for k, want := range map[string]any{"repo": "npm-remote", "flight_role": "leader", "cache_status": "trove; fwd=miss", "collapsed_waiters": float64(0)} {
			if rec[k] != want {
				t.Errorf("%s = %#v, want %#v", k, rec[k], want)
			}
		}
	})

	t.Run("R-OPS-011 collapsed_waiters is read when the request completes", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "x") }))
		defer srv.Close()
		var buf bytes.Buffer
		lg := observability.NewLogger(config.Log{Level: "info", Format: "json"}, &buf)
		var waiters int
		ctx := WithCall(context.Background(), Call{CollapsedWaiters: func() int { return waiters }})
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
		resp, err := New(allowLoopback(), WithUpstreamLog(lg)).Client(PurposeRemote).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		waiters = 7 // joined while the body streamed
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		var rec map[string]any
		_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec)
		if rec["collapsed_waiters"] != float64(7) {
			t.Errorf("collapsed_waiters = %#v", rec["collapsed_waiters"])
		}
	})

	t.Run("R-OPS-011 absent facts are null and the remote-only fields stay off other purposes", func(t *testing.T) {
		rec := record(t, PurposeRemote, Call{})
		for _, k := range []string{"flight_role", "cache_status", "collapsed_waiters"} {
			if v, ok := rec[k]; !ok || v != nil {
				t.Errorf("remote %s = %#v, %v", k, v, ok)
			}
		}
		rec = record(t, PurposeWebhook, Call{FlightRole: "leader", CacheStatus: func() string { return "x" }})
		for _, k := range []string{"flight_role", "cache_status"} {
			if _, ok := rec[k]; ok {
				t.Errorf("webhook record has %s", k)
			}
		}
		if v, ok := rec["collapsed_waiters"]; !ok || v != nil {
			t.Errorf("webhook collapsed_waiters = %#v, %v", v, ok)
		}
	})

	t.Run("R-REPO-047 a shadowing probe is logged with flight_role probe", func(t *testing.T) {
		if rec := record(t, PurposeRemote, Call{FlightRole: "probe"}); rec["flight_role"] != "probe" {
			t.Errorf("flight_role = %#v", rec["flight_role"])
		}
	})
}
