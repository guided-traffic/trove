// Self-tests of the counting stub of R-OCI-035; they check the test tool, not trove, and carry no requirement ID (Q-ROAD-5).

package ocistub

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fixture is a running stub with a client that trusts its CA and follows no redirect. reg, realm and cdn are the three hosts: the
// same listener under registry.localhost, auth.localhost and cdn.localhost, which the transport dials through Running.DialContext.
type fixture struct {
	t      *testing.T
	stub   *Server
	run    *Running
	client *http.Client
	reg    string
	realm  string
	cdn    string
}

func newFixture(t *testing.T, cfg Config) *fixture {
	t.Helper()
	stub, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ss, err := NewSelfSigned(LoopbackHosts...)
	if err != nil {
		t.Fatal(err)
	}
	run, err := stub.Start("127.0.0.1:0", ss.ServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	tr := &http.Transport{TLSClientConfig: ss.ClientConfig(), DisableCompression: true, DialContext: run.DialContext}
	t.Cleanup(tr.CloseIdleConnections)
	return &fixture{
		t: t, stub: stub, run: run,
		client: &http.Client{Transport: tr, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		reg:    run.URL, realm: run.RealmURL, cdn: run.CDNURL,
	}
}

type reply struct {
	status int
	header http.Header
	body   []byte
}

func (r reply) json(t *testing.T, into any) {
	t.Helper()
	if err := json.Unmarshal(r.body, into); err != nil {
		t.Fatalf("body %q is no JSON: %v", r.body, err)
	}
}

// errorCode returns the code of the first error of an error document.
func (r reply) errorCode(t *testing.T) string {
	t.Helper()
	var doc struct {
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	r.json(t, &doc)
	if len(doc.Errors) == 0 {
		t.Fatalf("no error in %q", r.body)
	}
	return doc.Errors[0].Code
}

func (f *fixture) do(method, rawURL string, header map[string]string) reply {
	f.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, rawURL, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	for k, v := range header {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, rawURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatalf("%s %s: read body: %v", method, rawURL, err)
	}
	return reply{status: resp.StatusCode, header: resp.Header, body: body}
}

func (f *fixture) get(rawURL string, header map[string]string) reply {
	f.t.Helper()
	return f.do(http.MethodGet, rawURL, header)
}

// bearer runs the handshake for scopes and returns the Authorization header value.
func (f *fixture) bearer(scopes ...string) map[string]string {
	f.t.Helper()
	q := url.Values{"service": {DefaultService}}
	for _, s := range scopes {
		q.Add("scope", s)
	}
	r := f.get(f.realm+"/token?"+q.Encode(), nil)
	if r.status != http.StatusOK {
		f.t.Fatalf("token: %d %s", r.status, r.body)
	}
	var tok struct {
		Token string `json:"token"`
	}
	r.json(f.t, &tok)
	return map[string]string{"Authorization": "Bearer " + tok.Token}
}

// open is a mode with no challenge, for tests that are not about the handshake.
func (f *fixture) open() {
	f.t.Helper()
	if err := f.stub.SetMode(Mode{Auth: AuthNone}); err != nil {
		f.t.Fatal(err)
	}
}

const acceptAll = MediaOCIIndex + "," + MediaDockerList + "," + MediaOCIManifest + "," + MediaDockerManifest

// manifestOf fetches a manifest of the registry with every type accepted and returns the reply.
func (f *fixture) manifestOf(name, ref string) reply {
	f.t.Helper()
	return f.get(f.reg+"/v2/"+name+"/manifests/"+ref, map[string]string{"Accept": acceptAll})
}

// clock is a settable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func hostPort(t *testing.T, base string) string {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

func itoa(n int) string { return strconv.Itoa(n) }

// firstLayer is the first layer of an image: the base layer every image of the platform shares.
func firstLayer(t *testing.T, s *Server, name, tag string) string {
	t.Helper()
	info, err := s.Image(name, tag)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Layers) == 0 {
		t.Fatalf("%s:%s has no layers", name, tag)
	}
	return info.Layers[0].Digest
}

// appLayer is the last layer of an image: the one only that image has.
func appLayer(t *testing.T, s *Server, name, tag string) string {
	t.Helper()
	info, err := s.Image(name, tag)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Layers) == 0 {
		t.Fatalf("%s:%s has no layers", name, tag)
	}
	return info.Layers[len(info.Layers)-1].Digest
}

func portOf(t *testing.T, base string) string {
	t.Helper()
	_, p, err := net.SplitHostPort(hostPort(t, base))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
