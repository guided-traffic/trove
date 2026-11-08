package oidcstub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testToken = "control-token-for-tests"

// control is a client of the control API on its own TLS listener.
type control struct {
	*fixture
	url string
}

func newControl(t *testing.T) *control {
	t.Helper()
	f := newFixture(t, Config{ControlToken: testToken})
	run, err := f.s.StartControl("127.0.0.1:0", f.ca.ServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	return &control{fixture: f, url: run.URL}
}

func (c *control) do(method, path, token, body string) (int, []byte) {
	c.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, c.url+path, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (c *control) ok(method, path, body string, into any) {
	c.t.Helper()
	status, out := c.do(method, path, testToken, body)
	if status/100 != 2 {
		c.t.Fatalf("%s %s: %d %s", method, path, status, out)
	}
	if into != nil {
		if err := json.Unmarshal(out, into); err != nil {
			c.t.Fatalf("%s %s: %v in %q", method, path, err, out)
		}
	}
}

func TestControlAPIRequiresTheToken(t *testing.T) {
	c := newControl(t)
	if st, _ := c.do("GET", "/counts", "", ""); st != http.StatusUnauthorized {
		t.Fatalf("no token: %d", st)
	}
	if st, _ := c.do("GET", "/counts", "wrong", ""); st != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", st)
	}
	if st, _ := c.do("GET", "/nothing", testToken, ""); st != http.StatusNotFound {
		t.Fatalf("unknown path: %d", st)
	}
	if st, out := c.do("POST", "/fault", testToken, `{"colour":"red"}`); st != http.StatusBadRequest {
		t.Fatalf("unknown field: %d %s", st, out)
	}
	// Without a configured token the API is off.
	off, _ := New(Config{NoDefaultKey: true})
	req := httptest.NewRequest(http.MethodGet, "/counts", http.NoBody)
	req.Header.Set("Authorization", "Bearer ")
	rec := httptest.NewRecorder()
	off.ControlHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("API without a token: %d", rec.Code)
	}
}

// TestControlAPICarriesConcurrentMints is the load of the 50-job e2e tests (R-CI-029): many clients mint at the same moment, each over
// a new TLS connection, and every one is answered. All 200 requests are issued at once, but at most maxInFlight of them hold a
// connection at a time: a host's listen backlog (128 on macOS) is not what is under test, and a connect beyond it is reset by the
// kernel before the stub ever sees it.
func TestControlAPICarriesConcurrentMints(t *testing.T) {
	c := newControl(t)
	tr, ok := c.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("the fixture's transport is %T", c.client.Transport)
	}
	tr = tr.Clone()
	tr.DisableKeepAlives = true
	cl := &http.Client{Transport: tr, Timeout: 30 * time.Second}
	const clients, maxInFlight = 200, 64
	slots := make(chan struct{}, maxInFlight)
	errs := make(chan error, clients)
	for range clients {
		go func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, c.url+"/mint", strings.NewReader(`{"claims":{}}`))
			if err != nil {
				errs <- err
				return
			}
			req.Header.Set("Authorization", "Bearer "+testToken)
			resp, err := cl.Do(req)
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"token"`)) {
				errs <- fmt.Errorf("mint answered %d: %s", resp.StatusCode, body)
				return
			}
			errs <- nil
		}()
	}
	for range clients {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

func TestControlAPIMintRotateRemoveFaultsAndCounters(t *testing.T) {
	c := newControl(t)

	var tok struct{ Token string }
	c.ok("POST", "/mint", `{"claims":{"project_path":"acme/billing/other","jti":null},"remove":["nbf"],"alg":"RS256"}`, &tok)
	_, p := verifyJWS(t, tok.Token, c.jwks())
	if p["project_path"] != "acme/billing/other" || p["iss"] != c.run.URL {
		t.Fatalf("payload %v", p)
	}
	if _, ok := p["nbf"]; ok {
		t.Fatal("nbf not removed")
	}
	if v, ok := p["jti"]; !ok || v != nil {
		t.Fatal("a null in the control body must be a JSON null in the token")
	}

	var added Info
	c.ok("POST", "/keys", `{"type":"es256"}`, &added)
	c.ok("POST", "/mint", `{"alg":"ES256"}`, &tok)
	if h, _ := verifyJWS(t, tok.Token, c.jwks()); h["kid"] != added.KID {
		t.Fatalf("header %v", h)
	}
	var keys []Info
	c.ok("GET", "/keys", "", &keys)
	if len(keys) != 3 {
		t.Fatalf("keys %+v", keys)
	}
	c.ok("POST", "/keys/remove", `{"kid":"`+added.KID+`"}`, nil)
	if st, _ := c.do("POST", "/keys/remove", testToken, `{"kid":"`+added.KID+`"}`); st != http.StatusNotFound {
		t.Fatalf("removing twice: %d", st)
	}
	if len(c.jwks().Keys) != 2 {
		t.Fatal("key still published")
	}

	// A mint that the double cannot build is a 400 with the reason.
	if st, out := c.do("POST", "/mint", testToken, `{"alg":"ES256"}`); st != http.StatusBadRequest || !bytes.Contains(out, []byte("no es256 key")) {
		t.Fatalf("mint without a key: %d %s", st, out)
	}

	// Counters: the control API itself is not counted.
	c.ok("POST", "/reset", "", nil)
	_, _, _ = c.get(c.s.JWKSURL())
	var counts Counts
	c.ok("GET", "/counts", "", &counts)
	if counts.JWKS != 1 || counts.Discovery != 0 {
		t.Fatalf("counts %+v", counts)
	}
	c.ok("POST", "/reset", "", nil)
	c.ok("GET", "/counts", "", &counts)
	if counts != (Counts{}) {
		t.Fatalf("after reset %+v", counts)
	}

	// Faults and caching; the control port works while the issuer is unreachable.
	var fault Fault
	c.ok("POST", "/fault", `{"delay":"250ms","status":503,"target":"jwks"}`, &fault)
	if time.Duration(fault.Delay) != 250*time.Millisecond || fault.Status != 503 {
		t.Fatalf("fault %+v", fault)
	}
	c.ok("POST", "/fault", `{"unreachable":true}`, nil)
	if _, _, err := c.get(c.s.JWKSURL()); err == nil {
		t.Fatal("issuer reachable")
	}
	c.ok("GET", "/fault", "", &fault)
	if !fault.Unreachable {
		t.Fatalf("fault %+v", fault)
	}
	c.ok("POST", "/cache", `{"max_age":"30s","only_max_age":true}`, nil)
	c.ok("POST", "/fault", `{}`, nil)
	if resp, _, err := c.get(c.s.JWKSURL()); err != nil || resp.Header.Get("Cache-Control") != "max-age=30" || resp.Header.Get("ETag") == "" {
		t.Fatalf("cache: %v %v", resp, err)
	}
	c.ok("POST", "/cache", `{"max_age":"30s","no_etag":true}`, nil)
	if resp, _, err := c.get(c.s.JWKSURL()); err != nil || resp.Header.Get("Cache-Control") != "max-age=30, public, must-revalidate, no-transform=true" || resp.Header.Get("ETag") != "" {
		t.Fatalf("cache: %v %v", resp, err)
	}
	// A job token through the control API.
	c.ok("POST", "/mint", `{"job_token":true}`, &tok)
	if !strings.HasPrefix(tok.Token, JobTokenPrefix) {
		t.Fatalf("job token %.10q...", tok.Token)
	}
	if st, _ := c.do("POST", "/fault", testToken, `{"status":42}`); st != http.StatusBadRequest {
		t.Fatalf("invalid fault: %d", st)
	}

	// reset?all=true heals everything and replaces the keys.
	c.ok("POST", "/fault", `{"status":500}`, nil)
	c.ok("POST", "/reset?all=true", "", nil)
	var info struct {
		Issuer       string `json:"issuer"`
		DiscoveryURL string `json:"discovery_url"`
		JWKSURL      string `json:"jwks_url"`
		Keys         []Info `json:"keys"`
	}
	c.ok("GET", "/info", "", &info)
	if info.Issuer != c.run.URL || info.JWKSURL != c.run.URL+JWKSPath || info.DiscoveryURL != c.run.URL+DiscoveryPath || len(info.Keys) != 2 {
		t.Fatalf("info %+v", info)
	}
	if resp, _, err := c.get(c.s.JWKSURL()); err != nil || resp.StatusCode != 200 {
		t.Fatalf("after reset all: %v %v", resp, err)
	}
}
