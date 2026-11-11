package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/cache"
	"github.com/guided-traffic/trove/internal/bastion/downloads"
	"github.com/guided-traffic/trove/internal/bastion/engine"
	"github.com/guided-traffic/trove/internal/bastion/guard"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	"github.com/guided-traffic/trove/internal/bastion/maintest"
	bmetrics "github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/config"
)

// linkedConfig is a configuration whose main installation is the fake: its certificate as ca_file, short
// intervals so that a test does not wait, a download buffer that does not flush on its own.
func linkedConfig(main *maintest.Main, dir, tokenFile, extra string) string {
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
  feed:
    poll_interval: 1s
  audit:
    flush_interval: 1h
  cache:
    volume:
      dir: %s
%s`, main.URL(), tokenFile, main.CAFile(dir), filepath.Join(dir, "cache"), extra)
}

func tokenFileFor(t *testing.T, dir, token string) string {
	t.Helper()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// newLinked builds a replica against the fake without starting it.
func newLinked(t *testing.T, cfgYAML string, deps func(*engine.Deps)) (*Server, *syncBuffer) {
	t.Helper()
	loaded, err := load(t, cfgYAML)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	logs := &syncBuffer{}
	srv, err := New(Options{Loaded: loaded, Environ: []string{"TROVE_POD_NAME=replica-a"}, LogWriter: logs, DrainDelay: 10 * time.Millisecond, Deps: deps})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv, logs
}

// run starts a replica that the test shuts down itself.
func run(t *testing.T, srv *Server, logs *syncBuffer) {
	t.Helper()
	if err := srv.Start(context.Background()); err != nil {
		srv.abort()
		t.Fatalf("Start: %v\n%s", err, logs.String())
	}
	var once atomic.Bool
	t.Cleanup(func() {
		if once.CompareAndSwap(false, true) {
			_ = srv.Shutdown(context.Background())
		}
	})
}

func metricsBody(t *testing.T, srv *Server) string {
	t.Helper()
	_, _, body := get(t, "http://"+srv.OpsAddr().String()+"/metrics")
	return body
}

func eventually(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("not within %v: %s", within, what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestR_BAS_043_AReplicaThatCannotReachTheMainInstallationBecomesReadyAndConnectsLater: the readiness does not
// depend on the main installation (Q-BAS-44); the replica is unreachable and ready, and becomes reachable
// without a restart when the main installation is back.
func TestR_BAS_043_AReplicaThatCannotReachTheMainInstallationBecomesReadyAndConnectsLater(t *testing.T) {
	main := maintest.New(t)
	main.SetDown(true)
	dir := t.TempDir()
	srv, logs := newLinked(t, linkedConfig(main, dir, tokenFileFor(t, dir, main.NewToken()), ""), nil)
	run(t, srv, logs)
	eventually(t, 5*time.Second, "ready", srv.Health().Ready)
	if code, _, _ := get(t, "http://"+srv.OpsAddr().String()+"/readyz"); code != http.StatusOK {
		t.Fatalf("/readyz = %d", code)
	}
	time.Sleep(300 * time.Millisecond)
	if body := metricsBody(t, srv); !strings.Contains(body, "trove_bastion_main_reachable 0") || strings.Contains(body, "trove_bastion_main_unreachable_since_timestamp_seconds 0\n") {
		t.Errorf("an unreachable replica reports:\n%s", grepLines(body, "trove_bastion_main_"))
	}
	if st := srv.link.State(); st.Reachable || st.Handshaken {
		t.Errorf("link state while the main installation is down: %+v", st)
	}
	// The client listener answers: there is no hit to serve, and a miss is a 503 without a request to the main
	// installation (R-BAS-027).
	if code, _, _ := get(t, "http://"+srv.ClientAddr().String()+"/api/npm/web/npm-all/lodash"); code != http.StatusServiceUnavailable {
		t.Errorf("client request = %d", code)
	}

	main.SetDown(false)
	eventually(t, 20*time.Second, "reachable without a restart", func() bool { return srv.link.State().Reachable })
	body := metricsBody(t, srv)
	for _, want := range []string{"trove_bastion_main_reachable 1", "trove_bastion_main_unreachable_since_timestamp_seconds 0", fmt.Sprintf(`trove_bastion_interface_version{side="main"} %d`, iface.InterfaceVersion), "trove_bastion_main_token_refused 0"} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
	if !srv.link.State().Handshaken || !srv.Health().Ready() {
		t.Error("handshake or readiness lost")
	}
}

// faultyCache replaces the cache the server built by one whose storage the test can switch off (the volume
// backend of the configuration, wrapped with cache.Fault).
func faultyCache(f *cache.Fault) func(*engine.Deps) {
	return func(d *engine.Deps) {
		c, err := cache.New(context.Background(), d.Config.Bastion.Cache, nil, cache.Options{Metrics: d.Metrics, Fault: f})
		if err != nil {
			panic(err)
		}
		d.Cache = c
	}
}

// spoolFiles lists the audit spool of the cache backend.
func spoolFiles(t *testing.T, srv *Server) []cache.SpoolFile {
	t.Helper()
	files, err := srv.deps.Cache.Backend().SpoolList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// TestR_BAS_043_ReadinessWaitsForTheCacheBackendsFirstCheckAndFailsWhileDraining: not ready before the first
// check of the cache backend passed, ready after, still ready when the backend fails later (reported, never
// unready), and not ready while draining.
func TestR_BAS_043_ReadinessWaitsForTheCacheBackendsFirstCheckAndFailsWhileDraining(t *testing.T) {
	main := maintest.New(t)
	dir := t.TempDir()
	fault := &cache.Fault{}
	fault.SetDown(true)
	srv, logs := newLinked(t, linkedConfig(main, dir, tokenFileFor(t, dir, main.NewToken()), ""), faultyCache(fault))
	run(t, srv, logs)
	ctx := context.Background()
	srv.Health().Refresh(ctx)
	if srv.Health().Ready() {
		t.Fatal("ready before the cache backend's first check passed")
	}
	if code, _, body := get(t, "http://"+srv.OpsAddr().String()+"/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz = %d %s", code, body)
	}
	fault.SetDown(false)
	srv.Health().Refresh(ctx)
	if !srv.Health().Ready() {
		t.Fatal("not ready after the first check passed")
	}
	fault.SetDown(true)
	srv.Health().Refresh(ctx)
	if !srv.Health().Ready() {
		t.Error("a later failure of the cache backend made the replica unready: the cache degrades to misses (R-BAS-030)")
	}
	srv.Health().SetDraining()
	if srv.Health().Ready() {
		t.Error("ready while draining")
	}
	if code, _, _ := get(t, "http://"+srv.OpsAddr().String()+"/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("/readyz while draining = %d", code)
	}
}

// TestR_BAS_043_TheDrainFlipsReadinessFirstAndTheClientListenerServesThroughTheDelay: readiness is off while the
// client listener still answers; then the listeners stop, in the order of R-ARCH-024.
func TestR_BAS_043_TheDrainFlipsReadinessFirstAndTheClientListenerServesThroughTheDelay(t *testing.T) {
	main := maintest.New(t)
	dir := t.TempDir()
	loaded, err := load(t, linkedConfig(main, dir, tokenFileFor(t, dir, main.NewToken()), ""))
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	srv, err := New(Options{Loaded: loaded, LogWriter: logs, DrainDelay: 600 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	run(t, srv, logs)
	eventually(t, 5*time.Second, "ready", srv.Health().Ready)
	clientAddr, opsAddr := srv.ClientAddr().String(), srv.OpsAddr().String()
	done := make(chan error, 1)
	go func() { done <- srv.Shutdown(context.Background()) }()
	eventually(t, 5*time.Second, "readiness off", func() bool { return !srv.Health().Ready() })
	// During the drain delay the client listener still serves, so a request that was routed to this replica
	// before the endpoints updated is not cut.
	if code, _, _ := get(t, "http://"+clientAddr+"/api/v1/x"); code != http.StatusNotFound {
		t.Errorf("client request during the drain delay = %d", code)
	}
	if code, _, _ := get(t, "http://"+opsAddr+"/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("/readyz during the drain delay = %d", code)
	}
	if err := <-done; err != nil {
		t.Errorf("shutdown: %v", err)
	}
	if resp, err := (&http.Client{Timeout: time.Second}).Get("http://" + clientAddr + "/"); err == nil {
		resp.Body.Close()
		t.Error("the client listener still accepts after the shutdown")
	}
	for _, want := range []string{"shutting down", "draining", "stopped"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the log lacks %q", want)
		}
	}
}

// TestR_BAS_039_TheDrainSpoolsTheBufferDuringAnOutageAndAnotherReplicaDeliversIt: a replica that is drained while
// the main installation is down writes its events to the cache backend's audit spool; a second replica of the
// same Bastion, with the main installation back, delivers them and deletes the file. With the main installation
// up, the drain sends them and spools nothing.
func TestR_BAS_039_TheDrainSpoolsTheBufferDuringAnOutageAndAnotherReplicaDeliversIt(t *testing.T) {
	main := maintest.New(t)
	dir := t.TempDir()
	srv, logs := newLinked(t, linkedConfig(main, dir, tokenFileFor(t, dir, main.NewToken()), ""), nil)
	run(t, srv, logs)
	eventually(t, 20*time.Second, "the handshake, which says recordDownloads", func() bool { return srv.link.State().Handshaken })
	// The outage begins before the events are added, so that no flush can deliver them before the drain: the
	// reporter keeps what it cannot send (R-BAS-039), whenever its flush runs.
	main.SetDown(true)
	at := time.Date(2026, 11, 2, 10, 0, 3, 0, time.UTC)
	for i := 0; i < 3; i++ {
		srv.deps.Downloads.Add(iface.DownloadEvent{At: at.Add(time.Duration(i) * time.Second), Repository: "web/npm-all", Path: fmt.Sprintf("/api/npm/web/npm-all/p%d.tgz", i), ClientAddress: "203.0.113.7", Replica: "replica-a"})
	}
	// A flush that runs now holds the batch while it fails against the outage and then puts it back.
	eventually(t, 20*time.Second, "the three events in the buffer", func() bool { return maintest.Value(srv.bm.AuditBuffer) == 3 })
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if n := len(spoolFiles(t, srv)); n != 1 {
		t.Fatalf("spool files = %d\n%s", n, logs.String())
	}
	if v := maintest.Value(srv.bm.AuditEvents.WithLabelValues(bmetrics.AuditSpooled)); v != 3 {
		t.Errorf("spooled = %v", v)
	}

	main.SetDown(false)
	m2, _ := bmetrics.New(nil)
	tok, _ := mainclient.ParseToken(main.NewToken())
	client, err := mainclient.New(mainclient.Options{
		URL: main.URL(), BaseURL: "https://packages.example.com", Replica: "replica-b", Release: "1", Tokens: mainclient.NewHolder(tok),
		ConnectTimeout: time.Second, ResponseHeaderTimeout: 2 * time.Second, CAFile: main.CAFile(t.TempDir()), Metrics: m2,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	other := downloads.NewReporter(downloads.ReporterOptions{
		Config:  config.BastionAudit{BufferEvents: 10, BatchSize: 5, FlushInterval: time.Second, SpoolMaxEvents: 10},
		Replica: "replica-b", Sender: client, Spool: srv.deps.Cache.Backend(), Metrics: m2,
	})
	if err := other.Adopt(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := main.DownloadEvents()
	if len(got) != 3 || !got[0].At.Equal(at) || got[0].Replica != "replica-a" || len(spoolFiles(t, srv)) != 0 {
		t.Errorf("delivered %+v, spool files %d", got, len(spoolFiles(t, srv)))
	}
}

// TestR_BAS_039_TheDrainSendsTheBufferWhenTheMainInstallationIsUp: nothing is spooled.
func TestR_BAS_039_TheDrainSendsTheBufferWhenTheMainInstallationIsUp(t *testing.T) {
	main := maintest.New(t)
	dir := t.TempDir()
	srv, logs := newLinked(t, linkedConfig(main, dir, tokenFileFor(t, dir, main.NewToken()), ""), nil)
	run(t, srv, logs)
	eventually(t, 20*time.Second, "reachable", func() bool { return srv.link.State().Reachable })
	for i := 0; i < 4; i++ {
		srv.deps.Downloads.Add(iface.DownloadEvent{At: time.Now(), Repository: "web/npm-all", Path: "/x", ClientAddress: "203.0.113.7", Replica: "replica-a"})
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(main.DownloadEvents()); n != 4 || len(spoolFiles(t, srv)) != 0 {
		t.Errorf("sent %d, spooled files %d", n, len(spoolFiles(t, srv)))
	}
}

// TestR_BAS_039_RecordDownloadsOffAtTheMainInstallationStopsTheBuffer: the handshake says recordDownloads false;
// events are discarded and nothing is counted.
func TestR_BAS_039_RecordDownloadsOffAtTheMainInstallationStopsTheBuffer(t *testing.T) {
	main := maintest.New(t)
	main.SetRecordDownloads(false)
	dir := t.TempDir()
	srv, logs := newLinked(t, linkedConfig(main, dir, tokenFileFor(t, dir, main.NewToken()), ""), nil)
	run(t, srv, logs)
	eventually(t, 20*time.Second, "reachable", func() bool { return srv.link.State().Reachable })
	srv.deps.Downloads.Add(iface.DownloadEvent{At: time.Now()})
	if v := maintest.Value(srv.bm.AuditBuffer); v != 0 {
		t.Errorf("buffer = %v with recordDownloads false", v)
	}
}

// TestR_BAS_006_ARotatedTokenFileIsUsedByARunningReplica: the server's reloader replaces the token in use and the
// next handshake carries it; the log shows hints, never a token.
func TestR_BAS_006_ARotatedTokenFileIsUsedByARunningReplica(t *testing.T) {
	main := maintest.New(t)
	dir := t.TempDir()
	first := main.NewToken()
	path := tokenFileFor(t, dir, first)
	srv, logs := newLinked(t, linkedConfig(main, dir, path, ""), nil)
	run(t, srv, logs)
	eventually(t, 20*time.Second, "reachable", func() bool { return srv.link.State().Reachable })
	second := main.NewToken()
	tokenFileFor(t, dir, second)
	eventually(t, 10*time.Second, "the new token in use", func() bool { return srv.holder.Token().Value == second })
	eventually(t, 10*time.Second, "a request with the new token", func() bool {
		h := main.Headers("handshake")
		return h != nil && h.Get("Authorization") == "Bearer "+second
	})
	main.Revoke(first)
	// The malformed file keeps the second token and is counted.
	malformed := "trv_bst_" + strings.Repeat("Q", 49)
	tokenFileFor(t, dir, malformed)
	eventually(t, 10*time.Second, "the refusal counted", func() bool {
		return strings.Contains(metricsBody(t, srv), `trove_bastion_token_reloads_total{outcome="refused"} 1`)
	})
	if srv.holder.Token().Value != second {
		t.Error("a malformed file replaced the token")
	}
	time.Sleep(1200 * time.Millisecond)
	if !srv.link.State().Reachable {
		t.Error("the replica lost the main installation with the old token revoked and the new one in use")
	}
	body := metricsBody(t, srv)
	for _, want := range []string{`trove_bastion_token_reloads_total{outcome="loaded"} 2`, "trove_bastion_token_remaining_seconds", "trove_bastion_main_token_refused 0"} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
	// R-BAS-042: no token in any log line, only hints.
	out := logs.String()
	for name, secret := range map[string]string{"first": first, "second": second, "malformed": malformed} {
		if strings.Contains(out, secret) {
			t.Errorf("the log shows the %s token", name)
		}
	}
	if !strings.Contains(out, "bastion token reloaded") {
		t.Errorf("the log does not show the reload:\n%s", out)
	}
}

// TestR_BAS_042_TheCallersCredentialsAndTheTraceContextAreNotLoggedOrContinued: a client request with an
// Authorization header, a Bastion token header and a traceparent leaves no secret in the log; the Bastion never
// continues a caller's trace.
func TestR_BAS_042_TheCallersCredentialsAndTheTraceContextAreNotLoggedOrContinued(t *testing.T) {
	main := maintest.New(t)
	dir := t.TempDir()
	srv, logs := newLinked(t, linkedConfig(main, dir, tokenFileFor(t, dir, main.NewToken()), ""), nil)
	run(t, srv, logs)
	eventually(t, 5*time.Second, "ready", srv.Health().Ready)
	callerToken := maintest.GenerateToken(t)
	const trace = "4bf92f3577b34da6a3ce929d0e0e4736"
	_, header, _ := get(t, "http://"+srv.ClientAddr().String()+"/api/npm/web/npm-all/lodash",
		"Authorization", "Bearer "+callerToken, iface.HeaderAuthorization, "Bearer "+callerToken,
		"Traceparent", "00-"+trace+"-00f067aa0ba902b7-01")
	if strings.Contains(header.Get("X-Request-Id"), trace) {
		t.Error("the caller's trace context was continued")
	}
	if strings.Contains(logs.String(), callerToken) {
		t.Error("a credential is in the log")
	}
	if got := (config.BastionTracing{}).Tracing().IncomingContext; got != "none" {
		t.Errorf("tracing.incoming_context = %q, fixed to none for the role", got)
	}
}

// TestR_BAS_044_TheCAFileOfTheMainInstallationStopsStartupWhenItDoesNotLoad: an unreadable bundle is a defect that
// stops startup and names the key; a bundle that loads makes the TLS connection work (the tests above).
func TestR_BAS_044_TheCAFileOfTheMainInstallationStopsStartupWhenItDoesNotLoad(t *testing.T) {
	main := maintest.New(t)
	dir := t.TempDir()
	cfg := linkedConfig(main, dir, tokenFileFor(t, dir, main.NewToken()), "")
	cfg = strings.Replace(cfg, main.CAFile(dir), filepath.Join(dir, "missing-ca.pem"), 1)
	loaded, err := load(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(Options{Loaded: loaded, LogWriter: &syncBuffer{}})
	if err == nil || !strings.Contains(err.Error(), "bastion.main") || !strings.Contains(err.Error(), "ca_file") {
		t.Fatalf("error = %v", err)
	}
	// http:// is refused by the configuration itself.
	if _, err := load(t, strings.Replace(linkedConfig(main, dir, "t", ""), main.URL(), "http://"+strings.TrimPrefix(main.URL(), "https://"), 1)); err == nil {
		t.Error("an http main URL was accepted")
	}
}

// quietGuard blocks the requests of the address while on is set, with a verdict that is quiet (R-BAS-037).
type quietGuard struct {
	guard.Guard
	on atomic.Bool
}

func (g *quietGuard) Blocked(ctx context.Context, addr string) guard.Verdict {
	if g.on.Load() {
		return guard.Verdict{Status: http.StatusTooManyRequests, Code: iface.CodeAddressBlocked, RetryAfter: time.Minute, Reason: bmetrics.RefusedBlocked, Quiet: true}
	}
	return g.Guard.Blocked(ctx, addr)
}

// TestR_BAS_037_ARequestFromABlockedAddressIsCountedButNotLoggedOneByOne: the guard's verdict is quiet, the
// engine marks the request and the request log of the client listener skips it, while the metrics count it.
func TestR_BAS_037_ARequestFromABlockedAddressIsCountedButNotLoggedOneByOne(t *testing.T) {
	main := maintest.New(t)
	dir := t.TempDir()
	qg := &quietGuard{}
	srv, logs := newLinked(t, linkedConfig(main, dir, tokenFileFor(t, dir, main.NewToken()), ""), func(d *engine.Deps) {
		qg.Guard = d.Guard
		d.Guard = qg
	})
	run(t, srv, logs)
	eventually(t, 5*time.Second, "ready", srv.Health().Ready)
	client := "http://" + srv.ClientAddr().String()
	if code, _, _ := get(t, client+"/api/npm/web/npm-all/logged-request"); code == http.StatusTooManyRequests {
		t.Fatal("blocked before the guard blocked")
	}
	qg.on.Store(true)
	for i := 0; i < 3; i++ {
		if code, _, body := get(t, client+"/api/npm/web/npm-all/quiet-request"); code != http.StatusTooManyRequests || !strings.Contains(body, iface.CodeAddressBlocked) {
			t.Fatalf("blocked request: %d %s", code, body)
		}
	}
	eventually(t, 5*time.Second, "the first request in the log", func() bool { return strings.Contains(logs.String(), "logged-request") })
	if strings.Contains(logs.String(), "quiet-request") {
		t.Errorf("a quiet request is in the request log:\n%s", logs.String())
	}
	if body := metricsBody(t, srv); !strings.Contains(body, `trove_bastion_refused_total{reason="blocked"} 3`) {
		t.Errorf("the refusals are not counted:\n%s", grepLines(body, "trove_bastion_refused_total"))
	}
}

// TestR_BAS_031_TheServerRunsTheCacheSweeper: the sweeper of the cache runs for the life of the process (the
// engine runs none): it publishes the bytes the cache holds as soon as it starts.
func TestR_BAS_031_TheServerRunsTheCacheSweeper(t *testing.T) {
	main := maintest.New(t)
	dir := t.TempDir()
	srv, logs := newLinked(t, linkedConfig(main, dir, tokenFileFor(t, dir, main.NewToken()), ""), nil)
	f, err := srv.deps.Cache.NewFill(cache.EntryArtifact)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte{'x'}, 1234)
	_, _ = f.Write(data)
	sum := sha256.Sum256(data)
	if err := f.Commit(context.Background(), hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	run(t, srv, logs)
	eventually(t, 5*time.Second, "the sweeper's first measurement", func() bool {
		return strings.Contains(metricsBody(t, srv), `trove_bastion_cache_bytes{area="artifacts"} 1234`)
	})
}
