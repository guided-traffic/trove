package testenv

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/app"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// SyncBuffer is a log sink that is safe to read while a replica writes.
type SyncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

// Write implements io.Writer.
func (s *SyncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

// String returns everything written so far.
func (s *SyncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// ReplicaOptions describe one replica.
type ReplicaOptions struct {
	// Name is the replica's instance name (TROVE_POD_NAME); it must be unique in
	// the stack.
	Name string
	// Role is web (default), worker or all.
	Role string
	// Env is appended to the stack's environment for this replica only.
	Env []string
	// Endpoints is the JSON of server.endpoints (TROVE_SERVER_ENDPOINTS); empty
	// keeps the default endpoint only.
	Endpoints string
	// Extra registers more routes on every endpoint's router before the route
	// table is checked (app.Options.Extra). A route under /api/v1/ must be in the
	// document, so a test resource lives elsewhere.
	Extra func(*httpserver.Router)
}

// Replica is one in-process replica started through app.New.
type Replica struct {
	Name string
	Role string
	App  *app.App
	Logs *SyncBuffer

	stack  *Stack
	cancel context.CancelFunc
	stop   sync.Once
	err    error
}

// Start brings up one replica through app.New, waits until it has passed the
// start-up check (R-ARCH-024 step 10) and shuts it down gracefully when the test
// ends. The tail of its log is printed when the test failed.
func (s *Stack) Start(tb testing.TB, o ReplicaOptions) *Replica {
	tb.Helper()
	if o.Role == "" {
		o.Role = "web"
	}
	if o.Name == "" {
		tb.Fatal("a replica needs a name")
	}
	// The ops listener's address must differ from server.listen as written, so it
	// gets a free port of its own.
	environ := append(append([]string{}, s.base...), "TROVE_POD_NAME="+o.Name, "TROVE_SERVER_OPS_LISTEN="+FreeAddr(tb))
	if o.Endpoints != "" {
		environ = append(environ, "TROVE_SERVER_ENDPOINTS="+o.Endpoints)
	}
	environ = append(environ, o.Env...)
	loaded, err := config.Load(config.Options{Environ: environ, Flags: map[string]string{"process.role": o.Role}, Command: config.CommandServe})
	if err != nil {
		tb.Fatalf("config: %v", err)
	}
	logs := &SyncBuffer{}
	a, err := app.New(app.Options{Loaded: loaded, Role: o.Role, Environ: environ, LogWriter: logs, Extra: o.Extra})
	if err != nil {
		tb.Fatalf("new %s: %v", o.Name, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Replica{Name: o.Name, Role: o.Role, App: a, Logs: logs, stack: s, cancel: cancel}
	if err := a.Start(ctx); err != nil {
		cancel()
		tb.Fatalf("start %s: %v\n%s", o.Name, err, logs.String())
	}
	tb.Cleanup(func() {
		if err := r.Stop(); err != nil {
			tb.Errorf("stop %s: %v", o.Name, err)
		}
		if tb.Failed() {
			tb.Logf("log of %s:\n%s", o.Name, Tail(logs.String(), 40))
		}
	})
	select {
	case <-a.Started():
	case err := <-a.Failed():
		tb.Fatalf("%s failed to start: %v\n%s", o.Name, err, Tail(logs.String(), 40))
	case <-time.After(90 * time.Second):
		tb.Fatalf("%s did not start\n%s", o.Name, Tail(logs.String(), 40))
	}
	s.mu.Lock()
	s.reps = append(s.reps, r)
	s.mu.Unlock()
	return r
}

// Stop shuts the replica down with the full sequence of R-ARCH-024 and returns
// its error. It is idempotent, and the cleanup of the test calls it too.
func (r *Replica) Stop() error {
	r.stop.Do(func() {
		r.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		r.err = r.App.Shutdown(ctx)
	})
	return r.err
}

// Addr is the address of the listener of an endpoint ("default" or a configured
// name); empty if the replica serves none of that name (a worker serves none).
func (r *Replica) Addr(endpoint string) string { return r.App.EndpointAddr(endpoint) }

// OpsAddr is the address of the ops listener.
func (r *Replica) OpsAddr() string { return r.App.OpsAddr().String() }

// Metrics reads the replica's /metrics.
func (r *Replica) Metrics(tb testing.TB) string {
	tb.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+r.OpsAddr()+"/metrics", nil)
	if err != nil {
		tb.Fatal(err)
	}
	res, err := Client.Do(req)
	if err != nil {
		tb.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return string(b)
}

// MetricValue returns the value of a series line of a metrics text, -1 if the
// series is absent.
func MetricValue(text, series string) float64 {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, series+" ") {
			v, _ := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, series)), 64)
			return v
		}
	}
	return -1
}

// FreeAddr returns a loopback address nothing listens on at the moment.
func FreeAddr(tb testing.TB) string {
	tb.Helper()
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// Tail returns the last n lines of s.
func Tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// Client is the HTTP client of the harness: no connection is reused, so every
// request is a new connection to the listener the test names and a replica that
// stops is noticed at once.
var Client = &http.Client{Transport: &http.Transport{DisableKeepAlives: true, MaxIdleConnsPerHost: -1}, Timeout: 30 * time.Second}

// Answer is one response, read.
type Answer struct {
	Status int
	Header http.Header
	Body   []byte
}

// JSON decodes the body as a JSON object.
func (a Answer) JSON(tb testing.TB) map[string]any {
	tb.Helper()
	var m map[string]any
	if err := json.Unmarshal(a.Body, &m); err != nil {
		tb.Fatalf("answer %d is not a JSON object: %v\n%s", a.Status, err, a.Body)
	}
	return m
}

// Code is the "code" member of a problem body.
func (a Answer) Code(tb testing.TB) string {
	tb.Helper()
	c, _ := a.JSON(tb)["code"].(string)
	return c
}

// Problem returns the problem body without its per-request members, so two
// answers compare equal when they say the same.
func (a Answer) Problem(tb testing.TB) string {
	tb.Helper()
	m := a.JSON(tb)
	delete(m, "requestId")
	delete(m, "instance")
	b, _ := json.Marshal(m)
	return string(b)
}

// DefaultHost is the Host of the default endpoint's base URL.
const DefaultHost = "trove.test"

// Do sends one request to the default endpoint of a replica, with the Host of
// its base URL and an optional bearer token. A JSON-marshalable body is sent as
// JSON, a []byte as it is; answers of the management API are validated against
// the document.
func (s *Stack) Do(r *Replica, method, path, token string, body any, hdr ...string) Answer {
	s.tb.Helper()
	return s.DoAt(r.Addr("default"), DefaultHost, method, path, token, body, hdr...)
}

// DoAt is Do against an address and a Host, for a further endpoint.
func (s *Stack) DoAt(addr, host, method, path, token string, body any, hdr ...string) Answer {
	tb := s.tb
	tb.Helper()
	var rd io.Reader
	var raw []byte
	switch b := body.(type) {
	case nil:
	case []byte:
		raw = b
	case string:
		raw = []byte(b)
	default:
		var err error
		if raw, err = json.Marshal(b); err != nil {
			tb.Fatal(err)
		}
	}
	if raw != nil {
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, "http://"+addr+path, rd)
	if err != nil {
		tb.Fatal(err)
	}
	req.Host = host
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if raw != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := Client.Do(req)
	if err != nil {
		tb.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	a := Answer{Status: res.StatusCode, Header: res.Header, Body: b}
	if strings.HasPrefix(path, "/api/v1/") && host == DefaultHost {
		check, _ := http.NewRequestWithContext(context.Background(), method, "http://"+DefaultHost+path, nil)
		if _, err := s.val.Check(check, a.Status, a.Header, a.Body); err != nil {
			tb.Errorf("response validation: %v\n%s", err, a.Body)
		}
	}
	return a
}
