// Package roletest is the end-to-end scenario of the whole Bastion role in one process (R-BAS-048): the server
// is started from a configuration file exactly as `trove serve --role bastion` starts it (config.LoadBastion on
// the file, then the server of internal/bastion/server), against the in-process main installation of
// internal/bastion/maintest over TLS. The scenario is the same for every cache backend; a test supplies the
// backend's part of the configuration. It is a test helper and nothing in the role imports it.
//
// The scenario shows, in this order: a cold miss that is fetched from the main installation and kept; a warm hit
// that costs the main installation no request; a second reader that costs one check and no artifact request; a
// revocation that arrives through the feed and is refused within the bound of five seconds; and the main
// installation cut off, where the answers live on through the grace period, a token the replica has not seen
// gets 503, and nothing is served after the period and the grace period. The clock of the replica and of the
// main installation is one settable clock, so that the periods are crossed without waiting for them.
package roletest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/engine"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/maintest"
	"github.com/guided-traffic/trove/internal/bastion/server"
	"github.com/guided-traffic/trove/internal/config"
)

// Backend is the cache backend a run of the scenario uses.
type Backend struct {
	// Name labels the run.
	Name string
	// YAML returns the lines below `bastion:` that configure the cache and what it needs (indented by two
	// spaces); dir is a temporary directory of the run.
	YAML func(dir string) string
	// Client sends the scenario's requests to the role's listeners. The caller builds it in its _test.go file,
	// because a client is made only by internal/outbound or by a test (R-ARCH-022).
	Client *http.Client
}

// Volume is the volume backend on a directory of the run, without a signing key (Q-BAS-98).
func Volume() Backend {
	return Backend{Name: "volume", YAML: func(dir string) string {
		return "  cache:\n    backend: volume\n    volume:\n      dir: " + filepath.Join(dir, "cache") + "\n"
	}}
}

// clock is the one clock of the run.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

const (
	pkg      = "/api/npm/web/npm-all/lodash/-/lodash-1.0.0.tgz"
	repo     = "web/npm-all"
	period   = 30 * time.Second
	grace    = time.Minute
	bound    = 5 * time.Second
	hostName = "packages.example.com"
)

// world is the main installation's readers and its one artifact.
type world struct {
	mu      sync.Mutex
	readers map[string]string // bearer token -> token ID
	body    []byte
	sum     string
}

func newWorld() *world {
	b := make([]byte, 300<<10)
	for i := range b {
		b[i] = byte(i*7 + i>>8)
	}
	s := sha256.Sum256(b)
	return &world{readers: map[string]string{}, body: b, sum: hex.EncodeToString(s[:])}
}

func (w *world) add(tok, id string) { w.mu.Lock(); w.readers[tok] = id; w.mu.Unlock() }
func (w *world) remove(tok string)  { w.mu.Lock(); delete(w.readers, tok); w.mu.Unlock() }

func (w *world) subject(tok string) iface.Subject {
	w.mu.Lock()
	defer w.mu.Unlock()
	if id, ok := w.readers[tok]; ok {
		return iface.Subject{Kind: iface.SubjectToken, TokenID: id, PrincipalID: "p-" + id}
	}
	return iface.Subject{Kind: iface.SubjectInvalid}
}

// check answers the interface's check: a known token reads the repository in full.
func (w *world) check(req iface.CheckRequest) iface.CheckAnswer {
	tok := ""
	if req.Credential != nil {
		tok = *req.Credential
	}
	sub := w.subject(tok)
	out := iface.CheckAnswer{Subject: sub, Repositories: map[string]iface.RepositoryRead{}}
	if sub.Kind == iface.SubjectToken {
		for _, r := range req.Repositories {
			out.Repositories[r] = iface.RepositoryRead{Read: iface.ReadFull}
		}
	}
	return out
}

// forward serves the artifact to a known token, with the marking the Bastion needs, and 304 to its validator.
func (w *world) forward(m *maintest.Main) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		sub := w.subject(tok)
		if sub.Kind != iface.SubjectToken {
			m.Mark(rw, iface.Marking{Class: iface.ClassPass, Subject: sub})
			rw.WriteHeader(http.StatusUnauthorized)
			return
		}
		etag := `"` + w.sum + `"`
		rw.Header().Set("Etag", etag)
		rw.Header().Set("Content-Type", "application/octet-stream")
		m.Mark(rw, iface.Marking{Class: iface.ClassArtifact, Repository: repo, Read: iface.ReadFull, Subject: sub})
		if r.Header.Get("If-None-Match") == etag {
			rw.WriteHeader(http.StatusNotModified)
			return
		}
		rw.Header().Set("Content-Length", strconv.Itoa(len(w.body)))
		_, _ = rw.Write(w.body)
	}
}

// Run is the scenario for one backend.
func Run(t *testing.T, b Backend) {
	t.Helper()
	if b.Client == nil {
		t.Fatal("roletest: Backend.Client is nil")
	}
	dir := t.TempDir()
	ck := &clock{t: time.Now().UTC().Truncate(time.Second)}
	main := maintest.New(t)
	main.SetClock(ck.Now)
	w := newWorld()
	main.SetCheck(w.check)
	main.SetForward(w.forward(main))
	main.SetAnonymous(false)

	tokenFile := filepath.Join(dir, "token")
	write(t, tokenFile, main.NewToken()+"\n")
	cfgFile := filepath.Join(dir, "trove.yaml")
	write(t, cfgFile, fmt.Sprintf(`server:
  listen: 127.0.0.1:0
  ops_listen: localhost:0
  base_url: https://%s
bastion:
  replicas: 1
  main:
    url: %s
    token_file: %s
    ca_file: %s
    handshake_interval: 5s
    unreachable_after: 3s
    connect_timeout: 1s
    response_header_timeout: 2s
  feed:
    poll_interval: 1s
  answers:
    period: %s
    grace_period: %s
  audit:
    flush_interval: 1h
%s`, hostName, main.URL(), tokenFile, main.CAFile(dir), period, grace, b.YAML(dir)))

	// What `trove serve --role bastion` does with the file, then the server of the role.
	loaded, err := config.LoadBastion(config.Options{File: cfgFile, Command: config.CommandServe})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	logs := &syncBuffer{}
	srv, err := server.New(server.Options{
		Loaded: loaded, Environ: []string{"TROVE_POD_NAME=bastion-e2e"}, LogWriter: logs, DrainDelay: 10 * time.Millisecond,
		Deps: func(d *engine.Deps) { d.Now = ck.Now },
	})
	if err != nil {
		t.Fatalf("server: %v\n%s", err, logs)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v\n%s", err, logs)
	}
	t.Cleanup(func() {
		if err := srv.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	client, ops := "http://"+srv.ClientAddr().String(), "http://"+srv.OpsAddr().String()
	eventually(t, 20*time.Second, "ready and connected", func() bool {
		return srv.Health().Ready() && strings.Contains(metrics(t, b.Client, ops), "\ntrove_bastion_main_reachable 1\n")
	})

	reader1, reader2, reader3 := maintest.GenerateTokenWithPrefix(t, "trv_pat_"), maintest.GenerateTokenWithPrefix(t, "trv_pat_"), maintest.GenerateTokenWithPrefix(t, "trv_pat_")
	w.add(reader1, "tok-1")
	w.add(reader2, "tok-2")
	get := func(tok string) (int, []byte, http.Header) { return fetch(t, b.Client, client+pkg, tok) }
	forwards, checks := func() int { return len(main.Forwards()) }, func() int { return len(main.Checks()) }

	// A cold miss is fetched from the main installation and kept.
	code, body, _ := get(reader1)
	if code != 200 || !bytes.Equal(body, w.body) {
		t.Fatalf("%s: cold miss: %d, %d bytes\n%s", b.Name, code, len(body), logs)
	}
	if forwards() != 1 {
		t.Fatalf("%s: a cold miss cost %d forwarded requests, want 1", b.Name, forwards())
	}

	// A warm hit costs the main installation no request at all.
	f0, c0 := forwards(), checks()
	for i := 0; i < 3; i++ {
		if code, body, _ := get(reader1); code != 200 || !bytes.Equal(body, w.body) {
			t.Fatalf("%s: warm hit %d: %d, %d bytes", b.Name, i, code, len(body))
		}
	}
	if forwards() != f0 || checks() != c0 {
		t.Fatalf("%s: a warm hit reached the main installation: %d forwards, %d checks", b.Name, forwards()-f0, checks()-c0)
	}

	// A second reader costs one check and no request for the bytes.
	if code, body, _ := get(reader2); code != 200 || !bytes.Equal(body, w.body) {
		t.Fatalf("%s: second reader: %d, %d bytes", b.Name, code, len(body))
	}
	if forwards() != f0 || checks() != c0+1 {
		t.Fatalf("%s: a second reader cost %d forwards and %d checks, want 0 and 1", b.Name, forwards()-f0, checks()-c0)
	}

	// A revocation reaches the replica through the feed and is refused within the bound.
	w.remove(reader1)
	main.AddEntry(iface.FeedEntry{Kind: iface.FeedToken, Tokens: []string{"tok-1"}})
	start := time.Now()
	for {
		if code, _, _ := get(reader1); code == http.StatusUnauthorized {
			break
		}
		if time.Since(start) > bound {
			t.Fatalf("%s: the revoked token was still served %v after the revocation\n%s", b.Name, bound, logs)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("%s: the revocation was refused after %v", b.Name, time.Since(start).Round(time.Millisecond))
	if code, body, _ := get(reader2); code != 200 || !bytes.Equal(body, w.body) {
		t.Fatalf("%s: another reader after the revocation: %d", b.Name, code)
	}

	// The main installation is cut off: the replica notices at its next handshake.
	main.SetDown(true)
	ck.Advance(6 * time.Second)
	eventually(t, 20*time.Second, "unreachable", func() bool { return strings.Contains(metrics(t, b.Client, ops), "\ntrove_bastion_main_reachable 0\n") })
	f0, c0 = forwards(), checks()
	// Past the period, inside the grace period: a reader the replica knows is still served from the cache.
	ck.Advance(period)
	if code, body, _ := get(reader2); code != 200 || !bytes.Equal(body, w.body) {
		t.Fatalf("%s: inside the grace period: %d, %d bytes", b.Name, code, len(body))
	}
	// A token the replica has not seen gets 503 main_unreachable with Retry-After: 30.
	code, _, h := get(reader3)
	if code != http.StatusServiceUnavailable || h.Get("Retry-After") != "30" {
		t.Fatalf("%s: an unseen token while the main installation is down: %d, Retry-After %q", b.Name, code, h.Get("Retry-After"))
	}
	// Past the period and the grace period nothing is served from the answers any more.
	ck.Advance(grace)
	if code, _, _ := get(reader2); code != http.StatusServiceUnavailable {
		t.Fatalf("%s: past period and grace: %d, want 503", b.Name, code)
	}
	if forwards() != f0 || checks() != c0 {
		t.Fatalf("%s: a request reached the main installation while it was cut off", b.Name)
	}
}

func fetch(t *testing.T, c *http.Client, url, tok string) (int, []byte, http.Header) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = hostName
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s: reading the body: %v", url, err)
	}
	return resp.StatusCode, b, resp.Header
}

func metrics(t *testing.T, c *http.Client, ops string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ops+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func eventually(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("not within %v: %s", within, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// syncBuffer is the log sink of the run.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
