package ansiblestub

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fixture is a running stub with a client that trusts its CA and follows no redirect: a test sees the 302 itself.
type fixture struct {
	t      *testing.T
	stub   *Server
	run    *Running
	client *http.Client
}

const testToken = "control-token-for-tests"

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
	client := &http.Client{
		Transport:     tr,
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &fixture{t: t, stub: stub, run: run, client: client}
}

// resp is an answer with its body read.
type resp struct {
	status int
	header http.Header
	body   []byte
}

func (r resp) json(t *testing.T) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(r.body, &v); err != nil {
		t.Fatalf("status %d, body %q: %v", r.status, r.body, err)
	}
	return v
}

// get requests a full URL with optional headers.
func (f *fixture) get(rawURL string, hdr ...string) resp {
	f.t.Helper()
	return f.do(http.MethodGet, rawURL, nil, hdr...)
}

func (f *fixture) do(method, rawURL string, body io.Reader, hdr ...string) resp {
	f.t.Helper()
	req, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		f.t.Fatal(err)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := f.client.Do(req)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, rawURL, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return resp{status: res.StatusCode, header: res.Header, body: b}
}

// api is a URL below the API root of the Galaxy host.
func (f *fixture) api(path string) string { return f.run.URL + "/api/" + strings.TrimPrefix(path, "/") }

// at resolves a link the stub answered, relative to the Galaxy host.
func (f *fixture) at(link string) string {
	f.t.Helper()
	u, err := url.Parse(f.run.URL)
	if err != nil {
		f.t.Fatal(err)
	}
	ref, err := url.Parse(link)
	if err != nil {
		f.t.Fatal(err)
	}
	return u.ResolveReference(ref).String()
}

// control sends a request to the control API.
func (f *fixture) control(method, path, body string) resp {
	f.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	return f.do(method, f.run.URL+"/-/stub/"+path, r, "Authorization", "Bearer "+testToken)
}

func (f *fixture) counts() Counts { return f.stub.Counts() }

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
