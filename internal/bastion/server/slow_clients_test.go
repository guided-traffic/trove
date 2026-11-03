package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/maintest"
)

// slowClientsConfig is linkedConfig with a budget of 4 slots, of which anonymous callers may hold 1.
func slowClientsConfig(main *maintest.Main, dir, tokenFile string) string {
	return fmt.Sprintf(`
server:
  listen: 127.0.0.1:0
  ops_listen: localhost:0
  base_url: https://packages.example.com
bastion:
  main:
    url: %s
    token_file: %s
    ca_file: %s
    token_reload_interval: 1s
    handshake_interval: 5s
    unreachable_after: 3s
    connect_timeout: 1s
    response_header_timeout: 2s
    max_concurrent: 4
    anonymous_share: 0.25
    queue_timeout: 500ms
  feed:
    poll_interval: 1s
  audit:
    flush_interval: 1h
  cache:
    volume:
      dir: %s
`, main.URL(), tokenFile, main.CAFile(dir), filepath.Join(dir, "cache"))
}

// bigArtifactMain is a main installation that serves one artifact of 32 MiB for every path, marked artifact
// for the anonymous caller, with its SHA-256 as the ETag (R-STOR-012).
func bigArtifactMain(t *testing.T) (*maintest.Main, []byte, string) {
	t.Helper()
	main := maintest.New(t)
	main.SetAnonymous(true)
	big := make([]byte, 32<<20)
	for i := range big {
		big[i] = byte(i * 7)
	}
	sum := sha256.Sum256(big)
	sha := hex.EncodeToString(sum[:])
	main.SetForward(func(w http.ResponseWriter, r *http.Request) {
		main.Mark(w, iface.Marking{Class: iface.ClassArtifact, Repository: "npm-all", Read: iface.ReadFull, Subject: iface.Subject{Kind: iface.SubjectAnonymous}})
		w.Header().Set("Etag", `"`+sha+`"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(big)))
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(big)
	})
	return main, big, sha
}

// trickleGet opens a connection with a small receive buffer, sends a GET and reads nothing.
func trickleGet(t *testing.T, addr, path string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(4096)
	}
	fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: packages.example.com\r\n\r\n", path)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// fetchAll does a GET with the given limit and returns the status, the SHA-256 of the body and the time taken.
func fetchAll(addr, path string, limit time.Duration) (int, string, time.Duration) {
	start := time.Now()
	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+path, nil)
	req.Host = "packages.example.com"
	resp, err := (&http.Client{Timeout: limit}).Do(req)
	if err != nil {
		return 0, "error: " + err.Error(), time.Since(start)
	}
	defer resp.Body.Close()
	h := sha256.New()
	_, err = io.Copy(h, resp.Body)
	if err != nil {
		return resp.StatusCode, "error: " + err.Error(), time.Since(start)
	}
	return resp.StatusCode, hex.EncodeToString(h.Sum(nil)), time.Since(start)
}

// TestR_BAS_025_ClientsThatReadSlowlyDoNotHoldTheBudgetSlots: a fill reads the main installation's body at the
// main installation's speed and frees its slot when the body ends; the client is served from the spool at its
// own speed. Two connections that take their artifact one socket buffer at a time hold no slot, so an unrelated
// anonymous request, which finds exactly one anonymous slot, is served at once and not answered 503
// bastion_busy (Q-BAS-123, security finding P3).
func TestR_BAS_025_ClientsThatReadSlowlyDoNotHoldTheBudgetSlots(t *testing.T) {
	main, big, sha := bigArtifactMain(t)
	dir := t.TempDir()
	srv, logs := newLinked(t, slowClientsConfig(main, dir, tokenFileFor(t, dir, main.NewToken())), nil)
	run(t, srv, logs)
	eventually(t, 10*time.Second, "reachable", func() bool { st := srv.link.State(); return st.Reachable && st.Handshaken })
	addr := srv.ClientAddr().String()

	for i := 0; i < 3; i++ {
		trickleGet(t, addr, "/api/npm/npm-all/pkg/-/pkg-1.0.0.tgz?v="+strconv.Itoa(i))
		time.Sleep(100 * time.Millisecond)
	}
	// The spools are complete although nobody reads them.
	eventually(t, 20*time.Second, "the three fills to end", func() bool {
		return strings.Contains(grepLines(metricsBody(t, srv), "trove_bastion_main_in_flight"), `anonymous"} 0`)
	})
	code, got, took := fetchAll(addr, "/api/npm/npm-all/other/-/other-2.0.0.tgz", 20*time.Second)
	if code != http.StatusOK || got != sha {
		t.Fatalf("an unrelated anonymous request behind three slow readers: %d %.80s after %v (want 200, %d bytes)", code, got, took, len(big))
	}
}

// TestR_BAS_025_AWaiterOfACollapsedFetchIsNotHeldByTheLeadersClient: the second caller of a cold key does not
// wait for the first caller's client. The flight ends when the fill is committed, and the waiter is served
// from the entry at its own speed (Q-BAS-123, security finding P3).
func TestR_BAS_025_AWaiterOfACollapsedFetchIsNotHeldByTheLeadersClient(t *testing.T) {
	main, big, sha := bigArtifactMain(t)
	dir := t.TempDir()
	srv, logs := newLinked(t, slowClientsConfig(main, dir, tokenFileFor(t, dir, main.NewToken())), nil)
	run(t, srv, logs)
	eventually(t, 10*time.Second, "reachable", func() bool { st := srv.link.State(); return st.Reachable && st.Handshaken })
	addr := srv.ClientAddr().String()

	const path = "/api/npm/npm-all/pkg/-/pkg-1.0.0.tgz"
	trickleGet(t, addr, path)
	time.Sleep(300 * time.Millisecond)
	code, got, took := fetchAll(addr, path, 20*time.Second)
	if code != http.StatusOK || got != sha {
		t.Fatalf("the second caller of the key behind a slow leader: %d %.80s after %v (want 200, %d bytes)", code, got, took, len(big))
	}
	if n := len(main.Forwards()); n != 1 {
		t.Fatalf("%d requests to the main installation for two callers of one key, want 1", n)
	}
}
