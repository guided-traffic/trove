package mainclient_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	"github.com/guided-traffic/trove/internal/bastion/maintest"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
)

const baseURL = "https://packages.example.com"

type rig struct {
	main   *maintest.Main
	m      *metrics.Metrics
	holder *mainclient.Holder
	token  string
	c      *mainclient.HTTPClient
	budget *mainclient.SlotBudget
}

func newRig(t *testing.T, mut func(*mainclient.Options)) *rig {
	t.Helper()
	m, err := metrics.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	main := maintest.New(t)
	raw := main.NewToken()
	tok, err := mainclient.ParseToken(raw)
	if err != nil {
		t.Fatalf("the fake issued a malformed token: %v", err)
	}
	holder := mainclient.NewHolder(tok)
	budget := mainclient.NewBudget(mainclient.BudgetOptions{MaxConcurrent: 64, Replicas: 1, AnonymousShare: 0.25, QueueTimeout: time.Second, Metrics: m})
	o := mainclient.Options{
		URL: main.URL(), BaseURL: baseURL, Replica: "replica-a", Release: "1.2.3", Tokens: holder, Budget: budget,
		ConnectTimeout: 2 * time.Second, ResponseHeaderTimeout: 2 * time.Second, CAFile: main.CAFile(t.TempDir()), Metrics: m,
		TrustCheckEvery: 20 * time.Millisecond,
	}
	if mut != nil {
		mut(&o)
	}
	c, err := mainclient.New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return &rig{main: main, m: m, holder: holder, token: raw, c: c, budget: budget}
}

func forward(method, target string, hdr ...string) mainclient.Forward {
	u, _ := url.Parse(target)
	r := &http.Request{Method: method, URL: u, Header: http.Header{}}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Add(hdr[i], hdr[i+1])
	}
	return mainclient.Forward{Request: r, Class: mainclient.Authenticated, Kind: mainclient.KindForward}
}

func closeBody(t *testing.T, r *mainclient.Response) string {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Body.Close()
	return string(b)
}

// TestR_BAS_008_EveryRequestCarriesTheInterfaceHeaders: the four operations and a forward send the interface
// version, the user agent, the replica, the token in the header the route calls for, the forwarded host of
// R-BAS-017 and a W3C traceparent.
func TestR_BAS_008_EveryRequestCarriesTheInterfaceHeaders(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tracer, err := observability.Tracing(config.Tracing{Enabled: true, SampleRatio: 1, IncomingContext: "none", PropagateToUpstreams: true, QueueSize: 16},
		observability.WithSpanExporter(exp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tracer.Shutdown(context.Background()) })
	r := newRig(t, func(o *mainclient.Options) { o.Tracer = tracer })
	ctx := context.Background()

	if _, err := r.c.Handshake(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.c.Check(ctx, mainclient.Authenticated, iface.CheckRequest{ClientAddress: "203.0.113.7", Repositories: []string{"web/npm-all"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.c.Feed(ctx, iface.Cursor{At: time.Now()}, 50); err != nil {
		t.Fatal(err)
	}
	if err := r.c.Downloads(ctx, iface.DownloadsRequest{Events: []iface.DownloadEvent{}}); err != nil {
		t.Fatal(err)
	}
	resp, err := r.c.Do(ctx, forward(http.MethodGet, "/api/npm/web/npm-all/lodash", "Authorization", "Bearer caller"))
	if err != nil {
		t.Fatal(err)
	}
	closeBody(t, resp)

	traceparent := regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-0[01]$`)
	for _, op := range []string{"handshake", "check", "feed", "downloads", "forward"} {
		h := r.main.Headers(op)
		if h == nil {
			t.Fatalf("%s: no request arrived", op)
		}
		if got := h.Get(iface.HeaderInterface); got != "1" {
			t.Errorf("%s: %s = %q", op, iface.HeaderInterface, got)
		}
		if got := h.Get("User-Agent"); got != "trove-bastion/1.2.3" {
			t.Errorf("%s: User-Agent = %q", op, got)
		}
		if got := h.Get(iface.HeaderReplica); got != "replica-a" {
			t.Errorf("%s: replica = %q", op, got)
		}
		if got := h.Get("Traceparent"); !traceparent.MatchString(got) {
			t.Errorf("%s: traceparent = %q", op, got)
		}
		if h.Get(iface.HeaderForwardedProto) != "https" || h.Get(iface.HeaderForwardedHost) != "packages.example.com" || h.Get(iface.HeaderForwardedPort) != "443" {
			t.Errorf("%s: forwarded host = %q %q %q", op, h.Get(iface.HeaderForwardedProto), h.Get(iface.HeaderForwardedHost), h.Get(iface.HeaderForwardedPort))
		}
		if got := h.Get(iface.HeaderForwarded); !strings.Contains(got, "proto=https;host=packages.example.com") {
			t.Errorf("%s: Forwarded = %q", op, got)
		}
	}
	// The token is Authorization on an interface route and Trove-Bastion-Authorization on a forward, where
	// the caller's own credential stays where it was.
	for _, op := range []string{"handshake", "check", "feed", "downloads"} {
		if got := r.main.Headers(op).Get("Authorization"); got != "Bearer "+r.token {
			t.Errorf("%s: Authorization is not the Bastion token", op)
		}
	}
	fh := r.main.Headers("forward")
	if fh.Get(iface.HeaderAuthorization) != "Bearer "+r.token || fh.Get("Authorization") != "Bearer caller" {
		t.Errorf("forward credentials: %q / %q", fh.Get(iface.HeaderAuthorization), fh.Get("Authorization"))
	}
	if got := r.main.Checks()[0].ClientAddress; got != "203.0.113.7" {
		t.Errorf("check client address = %q", got)
	}
	if !strings.Contains(r.main.Headers("feed").Get("Accept"), "json") {
		t.Error("the feed read does not ask for JSON")
	}
	if n := maintest.Value(r.m.MainRequests.WithLabelValues("forward", "2xx")); n != 1 {
		t.Errorf("forward 2xx = %v", n)
	}
	if n := maintest.Value(r.m.MainRequests.WithLabelValues("handshake", "2xx")); n != 1 {
		t.Errorf("handshake 2xx = %v", n)
	}
	// With a client span per request, the exporter saw one span for each of the five requests.
	_ = tracer.ForceFlush(context.Background())
	if n := len(exp.GetSpans()); n != 5 {
		t.Errorf("client spans = %d, want 5", n)
	}
}

// TestR_BAS_008_TheBaseURLsPortIsTheForwardedPort: a non-default port is sent as the port and part of host=.
func TestR_BAS_008_TheBaseURLsPortIsTheForwardedPort(t *testing.T) {
	r := newRig(t, func(o *mainclient.Options) { o.BaseURL = "https://Packages.Example.com:8443" })
	if _, err := r.c.Handshake(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := r.main.Headers("handshake")
	if h.Get(iface.HeaderForwardedHost) != "packages.example.com" || h.Get(iface.HeaderForwardedPort) != "8443" {
		t.Errorf("host %q port %q", h.Get(iface.HeaderForwardedHost), h.Get(iface.HeaderForwardedPort))
	}
	if got := h.Get(iface.HeaderForwarded); got != `proto=https;host="packages.example.com:8443"` {
		t.Errorf("Forwarded = %q", got)
	}
}

// TestR_BAS_017_AForwardCarriesOneClientAddressInBothFamilies: whatever forwarded headers the caller sent are
// replaced by exactly one client address in X-Forwarded-For and Forwarded, in both address families, and the
// caller's conditions, range and cookies do not travel.
func TestR_BAS_017_AForwardCarriesOneClientAddressInBothFamilies(t *testing.T) {
	cases := []struct {
		name, addr, xff, forEntry string
	}{
		{"ipv4", "203.0.113.7", "203.0.113.7", "for=203.0.113.7;"},
		{"ipv4 with a port", "203.0.113.7:51234", "203.0.113.7", "for=203.0.113.7;"},
		{"ipv6", "2001:db8::7", "2001:db8::7", `for="[2001:db8::7]";`},
		{"ipv6 in brackets", "[2001:db8::7]", "2001:db8::7", `for="[2001:db8::7]";`},
		{"v4 mapped", "::ffff:203.0.113.7", "203.0.113.7", "for=203.0.113.7;"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, nil)
			f := forward(http.MethodGet, "/api/npm/web/npm-all/@acme%2fsdk?write=false",
				"Authorization", "Basic Zm9vOmJhcg==",
				"X-Forwarded-For", "192.0.2.1, 192.0.2.2", "X-Forwarded-Host", "evil.example", "X-Forwarded-Proto", "http", "X-Forwarded-Port", "80",
				"Forwarded", "for=192.0.2.1;host=evil.example", "Cookie", "sid=1", "Range", "bytes=0-9", "If-None-Match", `"callers"`,
				"If-Modified-Since", "Mon, 02 Jan 2006 15:04:05 GMT", "Trove-Bastion-Class", "artifact", "Trove-Bastion-Probe", "1",
				"Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", "Connection", "X-Hop", "X-Hop", "1",
				"Accept", "application/json", "User-Agent", "npm/11")
			f.ClientAddress = tc.addr
			f.IfNoneMatch = `"bastions-own"`
			resp, err := r.c.Do(context.Background(), f)
			if err != nil {
				t.Fatal(err)
			}
			closeBody(t, resp)
			got := r.main.Forwards()
			if len(got) != 1 {
				t.Fatalf("forwards = %d", len(got))
			}
			h := got[0].Header
			if v := h.Values("X-Forwarded-For"); len(v) != 1 || v[0] != tc.xff {
				t.Errorf("X-Forwarded-For = %q, want %q", v, tc.xff)
			}
			if v := h.Values("Forwarded"); len(v) != 1 || !strings.HasPrefix(v[0], tc.forEntry) || strings.Contains(v[0], "evil") || strings.Contains(v[0], ",") {
				t.Errorf("Forwarded = %q", v)
			}
			for _, k := range []string{"X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Port"} {
				if v := h.Values(k); len(v) != 1 || strings.Contains(v[0], "evil") || v[0] == "80" || v[0] == "http" {
					t.Errorf("%s = %q", k, v)
				}
			}
			for _, k := range []string{"Cookie", "Range", "If-Modified-Since", "X-Hop", iface.HeaderClass, iface.HeaderProbe} {
				if h.Get(k) != "" {
					t.Errorf("%s reached the main installation: %q", k, h.Get(k))
				}
			}
			if h.Get("If-None-Match") != `"bastions-own"` {
				t.Errorf("If-None-Match = %q", h.Get("If-None-Match"))
			}
			if h.Get("Authorization") != "Basic Zm9vOmJhcg==" || h.Get("Accept") != "application/json" {
				t.Errorf("the caller's end-to-end headers changed: %q %q", h.Get("Authorization"), h.Get("Accept"))
			}
			if h.Get("User-Agent") != "trove-bastion/1.2.3" {
				t.Errorf("User-Agent = %q", h.Get("User-Agent"))
			}
			if strings.Contains(h.Get("Traceparent"), "4bf92f3577b34da6a3ce929d0e0e4736") {
				t.Error("the caller's trace context was continued")
			}
			if got[0].Path != "/api/npm/web/npm-all/@acme%2fsdk" || got[0].Query != "write=false" {
				t.Errorf("path %q query %q", got[0].Path, got[0].Query)
			}
		})
	}
}

// TestR_BAS_014_AProbeIsBodilessAndCarriesNoCredential: the write probe has the caller's method, no body, no
// caller credential and the probe header, and still names the Bastion.
func TestR_BAS_014_AProbeIsBodilessAndCarriesNoCredential(t *testing.T) {
	r := newRig(t, nil)
	f := forward(http.MethodPut, "/api/npm/npm-local/x", "Authorization", "Bearer caller", "Content-Type", "application/json")
	f.Probe = true
	f.Body = strings.NewReader("should not be sent")
	f.Request.ContentLength = 18
	resp, err := r.c.Do(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	closeBody(t, resp)
	got := r.main.Forwards()[0]
	if got.Method != http.MethodPut || len(got.Body) != 0 {
		t.Errorf("method %s, body %q", got.Method, got.Body)
	}
	if got.Header.Get("Authorization") != "" || got.Header.Get(iface.HeaderProbe) != iface.ProbeValue || got.Header.Get(iface.HeaderAuthorization) == "" {
		t.Errorf("probe headers: %v", got.Header)
	}
	if n := maintest.Value(r.m.MainRequests.WithLabelValues("probe", "2xx")); n != 1 {
		t.Errorf("probe 2xx = %v", n)
	}
}

// TestR_BAS_021_AForwardedBodyIsStreamedAndTheMarkingIsStripped: a read route that takes a body is forwarded
// with it; the response is marked, the marking is parsed and no Trove-Bastion header remains.
func TestR_BAS_021_AForwardedBodyIsStreamedAndTheMarkingIsStripped(t *testing.T) {
	r := newRig(t, nil)
	r.main.SetForward(func(w http.ResponseWriter, _ *http.Request) {
		r.main.Mark(w, iface.Marking{
			Class: iface.ClassArtifact, Repository: "web/npm-all", Read: iface.ReadFull,
			Subject: iface.Subject{Kind: iface.SubjectToken, TokenID: "tok-1", PrincipalID: "p-1"},
		})
		w.Header().Set("ETag", `"abc"`)
		_, _ = w.Write([]byte("tarball"))
	})
	f := forward(http.MethodPost, "/api/npm/web/npm-all/-/npm/v1/security/audits/quick")
	f.Body = strings.NewReader(`{"a":1}`)
	f.Request.ContentLength = 7
	resp, err := r.c.Do(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Marked || resp.Marking.Class != iface.ClassArtifact || resp.Marking.Repository != "web/npm-all" || resp.Marking.Read != iface.ReadFull {
		t.Errorf("marking = %+v marked %v", resp.Marking, resp.Marked)
	}
	for k := range resp.Header {
		if strings.HasPrefix(k, iface.HeaderPrefix) {
			t.Errorf("%s was not stripped", k)
		}
	}
	if resp.Header.Get("ETag") != `"abc"` || closeBody(t, resp) != "tarball" {
		t.Error("the answer was altered")
	}
	if string(r.main.Forwards()[0].Body) != `{"a":1}` {
		t.Errorf("body = %q", r.main.Forwards()[0].Body)
	}
}

// TestR_BAS_014_TheRouteOfAProbeAnswerSurvivesTheStrippedMarking: the 204 of a probe to a read route that takes a
// body names Trove-Bastion-Route; the client removes every Trove-Bastion header from the response and hands the
// route over in Response.Route, because the engine decides on it.
func TestR_BAS_014_TheRouteOfAProbeAnswerSurvivesTheStrippedMarking(t *testing.T) {
	r := newRig(t, nil)
	r.main.SetForward(func(w http.ResponseWriter, _ *http.Request) {
		r.main.Mark(w, iface.Marking{Class: iface.ClassPass, Repository: "web/npm-all", Subject: iface.Subject{Kind: iface.SubjectAnonymous}})
		w.Header().Set(iface.HeaderRoute, iface.RouteReadWithBody)
		w.WriteHeader(http.StatusNoContent)
	})
	f := forward(http.MethodPost, "/api/npm/web/npm-all/-/npm/v1/security/advisories/bulk")
	f.Probe = true
	resp, err := r.c.Do(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Status != http.StatusNoContent || resp.Route != iface.RouteReadWithBody {
		t.Errorf("status %d, route %q", resp.Status, resp.Route)
	}
	if resp.Header.Get(iface.HeaderRoute) != "" {
		t.Error("the route header was not stripped")
	}
}

// TestR_BAS_006_ARefusedTokenAndAMismatchAreTypedErrors: 401 bastion_token_invalid is ErrTokenRefused and sets
// trove_bastion_main_token_refused; 400 bastion_interface_mismatch and an answer with another version are
// *MismatchError with the main installation's version; every operation maps them the same way (R-BAS-009).
func TestR_BAS_006_ARefusedTokenAndAMismatchAreTypedErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("token", func(t *testing.T) {
		r := newRig(t, nil)
		if _, err := r.c.Handshake(ctx); err != nil {
			t.Fatal(err)
		}
		if v := maintest.Value(r.m.MainTokenRefused); v != 0 {
			t.Fatalf("refused = %v before the revocation", v)
		}
		r.main.Revoke(r.token)
		calls := map[string]func() error{
			"handshake": func() error { _, err := r.c.Handshake(ctx); return err },
			"feed":      func() error { _, err := r.c.Feed(ctx, iface.Cursor{At: time.Now()}, 1); return err },
			"check": func() error {
				_, err := r.c.Check(ctx, mainclient.Authenticated, iface.CheckRequest{Repositories: []string{"a"}})
				return err
			},
			"downloads": func() error { return r.c.Downloads(ctx, iface.DownloadsRequest{Events: []iface.DownloadEvent{}}) },
		}
		for name, call := range calls {
			if err := call(); !errors.Is(err, mainclient.ErrTokenRefused) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if v := maintest.Value(r.m.MainTokenRefused); v != 1 {
			t.Errorf("trove_bastion_main_token_refused = %v", v)
		}
		// A token the main installation accepts again clears it.
		fresh, _ := mainclient.ParseToken(r.main.NewToken())
		r.holder.Set(fresh)
		if _, err := r.c.Handshake(ctx); err != nil {
			t.Fatal(err)
		}
		if v := maintest.Value(r.m.MainTokenRefused); v != 0 {
			t.Errorf("trove_bastion_main_token_refused = %v after a good answer", v)
		}
	})
	t.Run("mismatch", func(t *testing.T) {
		r := newRig(t, nil)
		r.main.SetVersion(2)
		calls := map[string]func() error{
			"handshake": func() error { _, err := r.c.Handshake(ctx); return err },
			"feed":      func() error { _, err := r.c.Feed(ctx, iface.Cursor{At: time.Now()}, 1); return err },
			"check": func() error {
				_, err := r.c.Check(ctx, mainclient.Anonymous, iface.CheckRequest{Repositories: []string{"a"}})
				return err
			},
			"downloads": func() error { return r.c.Downloads(ctx, iface.DownloadsRequest{Events: []iface.DownloadEvent{}}) },
			"forward": func() error {
				resp, err := r.c.Do(ctx, forward(http.MethodGet, "/api/npm/web/npm-all/x"))
				if resp != nil {
					closeBody(t, resp)
				}
				return err
			},
		}
		for name, call := range calls {
			err := call()
			var mm *mainclient.MismatchError
			if !errors.Is(err, mainclient.ErrInterfaceMismatch) || !errors.As(err, &mm) || mm.Main != 2 {
				t.Errorf("%s: %v", name, err)
			}
		}
		// An answer that only states its version in the header (a main installation that did not reject the
		// request) is a mismatch as well.
		r.main.Force("forward", http.StatusOK)
		if resp, err := r.c.Do(ctx, forward(http.MethodGet, "/api/npm/web/npm-all/x")); !errors.Is(err, mainclient.ErrInterfaceMismatch) {
			if resp != nil {
				closeBody(t, resp)
			}
			t.Errorf("header-only mismatch: %v", err)
		}
	})
}

// TestR_BAS_021_FailuresAreUnreachableOrBadAnswers: a cut connection, an unmarked 5xx and a timeout are
// ErrUnreachable (the timeout says so); an unexpected status on an interface route is ErrBadAnswer; a marked
// 5xx is the main installation's answer and passes. No error contains the token.
func TestR_BAS_021_FailuresAreUnreachableOrBadAnswers(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, func(o *mainclient.Options) { o.ResponseHeaderTimeout = 150 * time.Millisecond })
	do := func() (*mainclient.Response, error) {
		return r.c.Do(ctx, forward(http.MethodGet, "/api/npm/web/npm-all/x?token=secret-query"))
	}

	r.main.SetDown(true)
	_, err := r.c.Handshake(ctx)
	var ue *mainclient.UnreachableError
	if !errors.Is(err, mainclient.ErrUnreachable) || !errors.As(err, &ue) || ue.Timeout {
		t.Errorf("cut connection: %v", err)
	}
	_, err = do()
	if !errors.Is(err, mainclient.ErrUnreachable) || strings.Contains(err.Error(), "secret-query") || strings.Contains(err.Error(), r.token) {
		t.Errorf("forward on a cut connection: %v", err)
	}
	r.main.SetDown(false)

	r.main.Force("forward", http.StatusBadGateway)
	if _, err := do(); !errors.Is(err, mainclient.ErrUnreachable) || !strings.Contains(err.Error(), "502") {
		t.Errorf("unmarked 502: %v", err)
	}
	r.main.Force("forward", 0)
	r.main.SetForward(func(w http.ResponseWriter, _ *http.Request) {
		r.main.Mark(w, iface.Marking{Class: iface.ClassPass})
		w.WriteHeader(http.StatusInternalServerError)
	})
	if resp, err := do(); err != nil || resp.Status != 500 || !resp.Marked {
		t.Errorf("marked 500: %v %+v", err, resp)
	} else {
		closeBody(t, resp)
	}

	r.main.Force("feed", http.StatusTeapot)
	if _, err := r.c.Feed(ctx, iface.Cursor{At: time.Now()}, 1); !errors.Is(err, mainclient.ErrBadAnswer) {
		t.Errorf("unexpected status: %v", err)
	}
	r.main.Force("feed", http.StatusServiceUnavailable)
	if _, err := r.c.Feed(ctx, iface.Cursor{At: time.Now()}, 1); !errors.Is(err, mainclient.ErrUnreachable) {
		t.Errorf("feed 503: %v", err)
	}
	r.main.Force("feed", 0)

	r.main.SetDelay(2 * time.Second)
	start := time.Now()
	_, err = r.c.Handshake(ctx)
	if !errors.As(err, &ue) || !ue.Timeout || !errors.Is(err, mainclient.ErrUnreachable) {
		t.Errorf("slow main installation: %v", err)
	}
	if time.Since(start) > 1900*time.Millisecond {
		t.Errorf("the response-header timeout did not bound the wait: %v", time.Since(start))
	}
	if n := maintest.Value(r.m.MainRequests.WithLabelValues("handshake", "timeout")); n != 1 {
		t.Errorf("handshake timeout = %v", n)
	}
	if n := maintest.Value(r.m.MainRequests.WithLabelValues("handshake", "error")); n != 1 {
		t.Errorf("handshake error = %v", n)
	}
	// A caller that gives up is not the main installation's fault.
	r.main.SetDelay(time.Second)
	cctx, cancel := context.WithCancel(ctx)
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if _, err := r.c.Handshake(cctx); !errors.Is(err, context.Canceled) || errors.Is(err, mainclient.ErrUnreachable) {
		t.Errorf("cancelled call: %v", err)
	}
}

// TestR_BAS_021_ARedirectIsTheMainInstallationsAnswer: the client never follows a redirect, because the Bastion
// token travels in a header no redirect policy would strip.
func TestR_BAS_021_ARedirectIsTheMainInstallationsAnswer(t *testing.T) {
	r := newRig(t, nil)
	r.main.SetForward(func(w http.ResponseWriter, _ *http.Request) {
		r.main.Mark(w, iface.Marking{Class: iface.ClassPass})
		w.Header().Set("Location", "https://elsewhere.example/x")
		w.WriteHeader(http.StatusFound)
	})
	resp, err := r.c.Do(context.Background(), forward(http.MethodGet, "/api/npm/web/npm-all/x"))
	if err != nil || resp.Status != http.StatusFound || resp.Header.Get("Location") == "" {
		t.Fatalf("%v %+v", err, resp)
	}
	closeBody(t, resp)
}

// TestR_BAS_044_AnHTTPMainURLIsRefused: the connection to the main installation is https only.
func TestR_BAS_044_AnHTTPMainURLIsRefused(t *testing.T) {
	m, _ := metrics.New(nil)
	holder := mainclient.NewHolder(mainclient.Token{Value: "x"})
	for _, u := range []string{"http://trove.example.com", "trove.example.com", "ftp://trove.example.com", "https://"} {
		_, err := mainclient.New(mainclient.Options{URL: u, BaseURL: baseURL, Tokens: holder, Metrics: m})
		if err == nil {
			t.Errorf("%q was accepted", u)
		}
	}
	// A CA file that does not load stops the client at construction.
	_, err := mainclient.New(mainclient.Options{URL: "https://trove.example.com", BaseURL: baseURL, Tokens: holder, Metrics: m, CAFile: filepath.Join(t.TempDir(), "missing.pem")})
	if err == nil {
		t.Error("a missing CA file was accepted")
	}
}

// TestR_BAS_044_TheCABundleIsReadAgainWithoutARestart: a main installation that no root vouches for fails every
// request; the bundle replaced by one that holds its certificate makes the next request succeed.
func TestR_BAS_044_TheCABundleIsReadAgainWithoutARestart(t *testing.T) {
	dir := t.TempDir()
	// A CA of another party: nothing it vouches for is the main installation.
	ca := tlstest.NewCA(t, "another party").WriteCA(t, dir, "ca.pem")
	r := newRig(t, func(o *mainclient.Options) { o.CAFile = ca })
	ctx := context.Background()
	if _, err := r.c.Handshake(ctx); !errors.Is(err, mainclient.ErrUnreachable) {
		t.Fatalf("with the wrong bundle: %v", err)
	}
	good, err := os.ReadFile(r.main.CAFile(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ca, good, 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := r.c.Handshake(ctx)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the rotated bundle was not picked up: %v", err)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// TestR_BAS_025_TheBudgetIsTheCeilingOfTheShareAndAnonymousIsACapOfIt: ceil(max_concurrent / replicas) slots per
// replica, of which anonymous callers may hold the share (rounded down, at least one while the share is
// positive).
func TestR_BAS_025_TheBudgetIsTheCeilingOfTheShareAndAnonymousIsACapOfIt(t *testing.T) {
	for _, tc := range []struct {
		max, replicas int
		share         float64
		total, anon   int
	}{
		{64, 2, 0.25, 32, 8},
		{8, 2, 0.25, 4, 1},
		{10, 3, 0.25, 4, 1},
		{7, 2, 0.5, 4, 2},
		{1, 1, 0.25, 1, 1},
		{64, 1, 0, 64, 0},
		{64, 1, 1, 64, 64},
		{5, 0, 0.25, 5, 1},
	} {
		m, _ := metrics.New(nil)
		b := mainclient.NewBudget(mainclient.BudgetOptions{MaxConcurrent: tc.max, Replicas: tc.replicas, AnonymousShare: tc.share, QueueTimeout: time.Second, Metrics: m})
		total, anon := b.Slots()
		if total != tc.total || anon != tc.anon {
			t.Errorf("max %d replicas %d share %v: slots %d/%d, want %d/%d", tc.max, tc.replicas, tc.share, total, anon, tc.total, tc.anon)
		}
		if maintest.Value(m.MainBudget.WithLabelValues("authenticated")) != float64(tc.total) || maintest.Value(m.MainBudget.WithLabelValues("anonymous")) != float64(tc.anon) {
			t.Errorf("the budget gauges do not show %d/%d", tc.total, tc.anon)
		}
	}
}

// TestR_BAS_025_ASlotIsHeldUntilTheBodyEndsAndAnAnonymousFloodDoesNotStarveTheRest: the caps hold, a waiter gets
// a released slot, a request that finds none within the queue timeout is ErrBusy and counted, and requests of
// the other class are not blocked behind a waiter at its cap.
func TestR_BAS_025_ASlotIsHeldUntilTheBodyEndsAndAnAnonymousFloodDoesNotStarveTheRest(t *testing.T) {
	m, _ := metrics.New(nil)
	b := mainclient.NewBudget(mainclient.BudgetOptions{MaxConcurrent: 4, Replicas: 1, AnonymousShare: 0.25, QueueTimeout: 80 * time.Millisecond, Metrics: m})
	ctx := context.Background()

	anon1, err := b.Acquire(ctx, mainclient.Anonymous)
	if err != nil {
		t.Fatal(err)
	}
	// The second anonymous request waits, times out, and is counted as exhausted.
	if _, err := b.Acquire(ctx, mainclient.Anonymous); !errors.Is(err, mainclient.ErrBusy) {
		t.Fatalf("second anonymous slot: %v", err)
	}
	if n := maintest.Value(m.BudgetExhausted.WithLabelValues("anonymous")); n != 1 {
		t.Errorf("exhausted = %v", n)
	}
	// Meanwhile three authenticated requests fit: an anonymous waiter at its cap blocks nobody.
	var auth []func()
	for i := 0; i < 3; i++ {
		rel, err := b.Acquire(ctx, mainclient.Authenticated)
		if err != nil {
			t.Fatalf("authenticated slot %d: %v", i, err)
		}
		auth = append(auth, rel)
	}
	if n := maintest.Value(m.MainInFlight.WithLabelValues("anonymous")); n != 1 {
		t.Errorf("anonymous in flight = %v", n)
	}
	if n := maintest.Value(m.MainInFlight.WithLabelValues("authenticated")); n != 3 {
		t.Errorf("authenticated in flight = %v", n)
	}
	// All four are in use: a fifth waits, and gets the slot a release frees.
	got := make(chan error, 1)
	long := mainclient.NewBudget(mainclient.BudgetOptions{MaxConcurrent: 1, Replicas: 1, AnonymousShare: 0.25, QueueTimeout: 30 * time.Second, Metrics: m})
	first, _ := long.Acquire(ctx, mainclient.Authenticated)
	go func() {
		rel, err := long.Acquire(ctx, mainclient.Authenticated)
		if err == nil {
			rel()
		}
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("the waiter did not wait: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	first()
	first() // releasing twice frees one slot only
	if err := <-got; err != nil {
		t.Fatalf("the waiter: %v", err)
	}
	// A cancelled waiter returns the context's error and leaves no slot behind.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := b.Acquire(cctx, mainclient.Authenticated); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	anon1()
	for _, rel := range auth {
		rel()
	}
	if n := maintest.Value(m.MainInFlight.WithLabelValues("authenticated")) + maintest.Value(m.MainInFlight.WithLabelValues("anonymous")); n != 0 {
		t.Errorf("in flight after every release = %v", n)
	}
	// With no anonymous share, an anonymous request is refused at once.
	none := mainclient.NewBudget(mainclient.BudgetOptions{MaxConcurrent: 4, Replicas: 1, AnonymousShare: 0, QueueTimeout: time.Minute, Metrics: m})
	start := time.Now()
	if _, err := none.Acquire(ctx, mainclient.Anonymous); !errors.Is(err, mainclient.ErrBusy) || time.Since(start) > 5*time.Second {
		t.Errorf("anonymous without a share: %v after %v", err, time.Since(start))
	}
}

// TestR_BAS_025_TheBodyHoldsTheSlot: the slot of a forward is released when its body is closed, not when the
// headers arrive; a checked and a probed request take one as well; the handshake and the feed take none.
func TestR_BAS_025_TheBodyHoldsTheSlot(t *testing.T) {
	r := newRig(t, func(o *mainclient.Options) {
		o.Budget = mainclient.NewBudget(mainclient.BudgetOptions{MaxConcurrent: 1, Replicas: 1, AnonymousShare: 0.5, QueueTimeout: 50 * time.Millisecond, Metrics: nil})
	})
	ctx := context.Background()
	resp, err := r.c.Do(ctx, forward(http.MethodGet, "/api/npm/web/npm-all/x"))
	if err != nil {
		t.Fatal(err)
	}
	// The only slot is held by the open body: a check finds none, a handshake and a feed read need none.
	if _, err := r.c.Check(ctx, mainclient.Authenticated, iface.CheckRequest{Repositories: []string{"a"}}); !errors.Is(err, mainclient.ErrBusy) {
		t.Errorf("check while the body is open: %v", err)
	}
	if _, err := r.c.Do(ctx, forward(http.MethodGet, "/api/npm/web/npm-all/y")); !errors.Is(err, mainclient.ErrBusy) {
		t.Errorf("forward while the body is open: %v", err)
	}
	if n := maintest.Value(r.m.MainRequests.WithLabelValues("check", "busy")); n != 1 {
		t.Errorf("check busy = %v", n)
	}
	if _, err := r.c.Handshake(ctx); err != nil {
		t.Errorf("handshake needs no slot: %v", err)
	}
	if _, err := r.c.Feed(ctx, iface.Cursor{At: time.Now()}, 1); err != nil {
		t.Errorf("feed needs no slot: %v", err)
	}
	closeBody(t, resp)
	if _, err := r.c.Check(ctx, mainclient.Authenticated, iface.CheckRequest{Repositories: []string{"a"}}); err != nil {
		t.Errorf("check after the body was closed: %v", err)
	}
	// A body that is read to its end releases as well.
	resp, err = r.c.Do(ctx, forward(http.MethodGet, "/api/npm/web/npm-all/x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	if err := r.c.Downloads(ctx, iface.DownloadsRequest{Events: []iface.DownloadEvent{}}); err != nil {
		t.Errorf("downloads after the body ended: %v", err)
	}
	_ = resp.Body.Close()
}

// TestR_BAS_025_TheMainInstallationNeverSeesMoreThanTheBudget: max_concurrent 8 across two replicas and a main
// installation that delays every answer: 500 concurrent cold requests never show more than 8 in flight at the
// main installation, and anonymous ones never more than 2 (the answer delay is scaled down from the
// acceptance's 2 s).
func TestR_BAS_025_TheMainInstallationNeverSeesMoreThanTheBudget(t *testing.T) {
	main := maintest.New(t)
	main.SetDelay(15 * time.Millisecond)
	var clients []*mainclient.HTTPClient
	for i := 0; i < 2; i++ {
		m, _ := metrics.New(nil)
		tok, _ := mainclient.ParseToken(main.NewToken())
		b := mainclient.NewBudget(mainclient.BudgetOptions{MaxConcurrent: 8, Replicas: 2, AnonymousShare: 0.25, QueueTimeout: time.Minute, Metrics: m})
		c, err := mainclient.New(mainclient.Options{
			URL: main.URL(), BaseURL: baseURL, Replica: "r", Release: "1", Tokens: mainclient.NewHolder(tok), Budget: b,
			ConnectTimeout: 5 * time.Second, ResponseHeaderTimeout: 30 * time.Second, CAFile: main.CAFile(t.TempDir()), Metrics: m,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(c.Close)
		clients = append(clients, c)
	}
	var wg sync.WaitGroup
	var failed atomic.Int64
	for i := 0; i < 500; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f := forward(http.MethodGet, "/api/npm/web/npm-all/pkg")
			f.Class = mainclient.Authenticated
			if i%3 == 0 {
				f.Class = mainclient.Anonymous
			} else {
				f.Request.Header.Set("Authorization", "Bearer caller")
			}
			resp, err := clients[i%2].Do(context.Background(), f)
			if err != nil {
				failed.Add(1)
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}()
	}
	wg.Wait()
	if failed.Load() != 0 {
		t.Errorf("%d requests failed", failed.Load())
	}
	if got := main.MaxInFlight(); got > 8 || got < 1 {
		t.Errorf("most in flight at the main installation = %d, budget 8", got)
	}
	if got := main.MaxAnonymousInFlight(); got > 2 {
		t.Errorf("most anonymous in flight at the main installation = %d, budget 2", got)
	}
}

// TestR_BAS_039_ADownloadBatchIsOneRequestWithItsBody: the client posts the batch and the dropped members as the
// interface defines them, and a 4xx other than a problem code is a bad answer.
func TestR_BAS_039_ADownloadBatchIsOneRequestWithItsBody(t *testing.T) {
	r := newRig(t, nil)
	at := time.Date(2026, 11, 2, 10, 0, 3, 100_000_000, time.UTC)
	since := at.Add(-time.Hour)
	req := iface.DownloadsRequest{
		Events:  []iface.DownloadEvent{{At: at, TokenID: "t", ClientAddress: "203.0.113.7", Repository: "web/npm-all", Path: "/api/npm/web/npm-all/x.tgz", SHA256: strings.Repeat("a", 64), Replica: "replica-a"}},
		Dropped: 7, DroppedSince: &since,
	}
	if err := r.c.Downloads(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	got := r.main.Downloads()
	if len(got) != 1 || got[0].Dropped != 7 || got[0].DroppedSince == nil || !got[0].DroppedSince.Equal(since) || !got[0].Events[0].At.Equal(at) {
		t.Errorf("received %+v", got)
	}
}

// TestR_BAS_044_ForwardsGoToThePackagesURLAndTheInterfaceToTheMainURL: with bastion.main.packages_url set (phase 6a, Q-HA-74),
// the forwards and the probes go to the main installation's packages Service and the four interface operations to the main
// URL; without it both go to the main URL; a packages_url that is not https is refused.
func TestR_BAS_044_ForwardsGoToThePackagesURLAndTheInterfaceToTheMainURL(t *testing.T) {
	ctx := context.Background()
	packages := maintest.New(t)
	packages.Force("forward", http.StatusBadGateway) // the call is what is counted; no token of this fake is needed
	r := newRig(t, func(o *mainclient.Options) { o.PackagesURL = packages.URL() })

	_, _ = r.c.Do(ctx, forward(http.MethodGet, "/api/npm/web/npm-all/lodash"))
	probe := forward(http.MethodGet, "/api/npm/web/npm-all/lodash")
	probe.Probe, probe.Kind = true, mainclient.KindProbe
	_, _ = r.c.Do(ctx, probe)
	if got := packages.Calls("forward"); got != 2 {
		t.Errorf("the packages Service received %d forwards and probes, want 2", got)
	}
	if got := r.main.Calls("forward"); got != 0 {
		t.Errorf("the main URL received %d forwards, want 0", got)
	}

	if _, err := r.c.Handshake(ctx); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if got, other := r.main.Calls(iface.OperationHandshake), packages.Calls(iface.OperationHandshake); got != 1 || other != 0 {
		t.Errorf("handshake: main %d, packages %d, want 1 and 0", got, other)
	}

	t.Run("an empty packages_url means the main URL for both", func(t *testing.T) {
		r := newRig(t, nil)
		r.main.Force("forward", http.StatusBadGateway)
		_, _ = r.c.Do(ctx, forward(http.MethodGet, "/api/npm/web/npm-all/lodash"))
		if got := r.main.Calls("forward"); got != 1 {
			t.Errorf("the main URL received %d forwards, want 1", got)
		}
	})

	t.Run("a packages_url that is not https is refused", func(t *testing.T) {
		m, err := metrics.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		main := maintest.New(t)
		tok, _ := mainclient.ParseToken(main.NewToken())
		for _, u := range []string{"http://trove-packages.trove.svc:8080", "trove-packages", "https://"} {
			_, err := mainclient.New(mainclient.Options{
				URL: main.URL(), PackagesURL: u, BaseURL: baseURL, Tokens: mainclient.NewHolder(tok), Metrics: m,
				ConnectTimeout: time.Second, ResponseHeaderTimeout: time.Second,
			})
			if err == nil || !strings.Contains(err.Error(), "packages_url") {
				t.Errorf("%q: %v", u, err)
			}
		}
	})
}
