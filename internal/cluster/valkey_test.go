package cluster

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/config"
)

func standaloneCfg(addr string) config.Valkey {
	return config.Valkey{
		Mode: "standalone", Address: addr, KeyPrefix: "trove:", AllowInsecure: true,
		TLS:            config.ValkeyTLS{Mode: "disable"},
		ConnectTimeout: time.Second, CommandTimeout: 300 * time.Millisecond, SentinelQueryTimeout: 200 * time.Millisecond,
		MaxConnLifetime: 30 * time.Minute, HealthCheckInterval: 200 * time.Millisecond,
		Reconnect:      config.ValkeyReconnect{InitialBackoff: 50 * time.Millisecond, MaxBackoff: 200 * time.Millisecond},
		CircuitBreaker: config.ValkeyBreaker{FailureThreshold: 3, OpenDuration: time.Second},
		Sentinel:       config.ValkeySentinel{RefreshInterval: time.Second, TLS: true},
	}
}

func sentinelCfg(addrs ...string) config.Valkey {
	c := standaloneCfg("")
	c.Mode = "sentinel"
	c.Sentinel.Addresses = addrs
	c.Sentinel.PrimaryName = "trove"
	c.Sentinel.TLS = false
	return c
}

const testKeyspace = "0123456789abcdef"

func newTestValkey(t *testing.T, cfg config.Valkey) (*Valkey, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	v, err := NewValkey(t.Context(), cfg, "test-pod", "web", nil, reg, WithKeyspaceID(testKeyspace))
	if err != nil {
		t.Fatalf("NewValkey: %v", err)
	}
	t.Cleanup(v.Close)
	return v, reg
}

func eventually(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not happen within %s", what, within)
}

func TestValkeyClient(t *testing.T) {
	t.Run("R-HA-056 every Sentinel unreachable at first start gives a degraded client and no error", func(t *testing.T) {
		start := time.Now()
		v, _ := newTestValkey(t, sentinelCfg("127.0.0.1:1", "127.0.0.1:2"))
		if took := time.Since(start); took > 5*time.Second {
			t.Fatalf("NewValkey waited %s for an unreachable Valkey", took)
		}
		if v.Available() {
			t.Fatal("Available() with no Sentinel reachable")
		}
		if err := v.StartupError(); err != nil {
			t.Fatalf("unreachable Sentinels are not a startup error (Q-HA-13), got %v", err)
		}
		st := v.Status()
		if st.Available || !strings.Contains(st.DegradedReason, "no Sentinel reachable (0 of 2)") {
			t.Fatalf("status = %+v", st)
		}
		if _, _, err := v.Get(t.Context(), mustKey(t, v)); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Get = %v, want ErrUnavailable", err)
		}
	})
	t.Run("R-ARCH-031 connects, reads the version from HELLO and writes with an expiry", func(t *testing.T) {
		srv := standaloneServer(t, "valkey", "9.1.3")
		v, _ := newTestValkey(t, standaloneCfg(srv.addr()))
		eventually(t, "availability", 5*time.Second, v.Available)
		st := v.Status()
		if st.Version != "9.1.3" || st.Primary != srv.addr() || st.Mode != "standalone" {
			t.Fatalf("status = %+v", st)
		}
		k := mustKey(t, v)
		if err := v.Set(t.Context(), k, "v"); err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, c := range srv.seen() {
			if len(c) >= 5 && strings.EqualFold(c[0], "SET") && c[1] == k.String() {
				found = true
				if !strings.EqualFold(c[3], "PX") || c[4] != "120000" {
					t.Fatalf("SET carries %v, want PX 120000", c[3:])
				}
			}
		}
		if !found {
			t.Fatalf("no SET of %s reached the server: %v", k, srv.seen())
		}
	})
	t.Run("R-ARCH-031 a server below 9.0 is refused and the client runs degraded", func(t *testing.T) {
		srv := standaloneServer(t, "valkey", "8.1.5")
		v, _ := newTestValkey(t, standaloneCfg(srv.addr()))
		eventually(t, "version check", 5*time.Second, func() bool { return v.Status().Version != "" })
		st := v.Status()
		if st.Available || st.DegradedReason != "valkey 8.1.5 is not supported; trove requires 9.0 or later" {
			t.Fatalf("status = %+v", st)
		}
		if v.StartupError() != nil {
			t.Fatalf("an unsupported version is degraded, not a startup error: %v", v.StartupError())
		}
		if err := v.Set(t.Context(), mustKey(t, v), "v"); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Set = %v, want ErrUnavailable", err)
		}
		time.Sleep(500 * time.Millisecond) // several probe intervals
		for _, c := range srv.seen() {
			if strings.EqualFold(c[0], "SET") || strings.EqualFold(c[0], "GET") {
				t.Fatalf("the client sent %v to an unsupported server", c)
			}
		}
	})
	t.Run("Q-HA-13 a rejected credential is a startup error with the cause", func(t *testing.T) {
		srv := errorServer(t, "WRONGPASS invalid username-password pair or user is disabled.")
		v, _ := newTestValkey(t, standaloneCfg(srv.addr()))
		err := v.StartupError()
		if err == nil || !strings.Contains(err.Error(), "WRONGPASS") {
			t.Fatalf("StartupError = %v", err)
		}
		if v.Available() {
			t.Fatal("available with rejected credentials")
		}
	})
	t.Run("R-ARCH-031 NewValkey rejects settings the process cannot run with", func(t *testing.T) {
		dir := t.TempDir()
		bad := []struct {
			name string
			mod  func(*config.Valkey)
		}{
			{"unreadable password file", func(c *config.Valkey) { c.PasswordFile = filepath.Join(dir, "missing") }},
			{"unreadable sentinel password file", func(c *config.Valkey) { c.Sentinel.PasswordFile = filepath.Join(dir, "missing") }},
			{"unknown mode", func(c *config.Valkey) { c.Mode = "cluster" }},
			{"standalone without address", func(c *config.Valkey) { c.Address = "" }},
			{"missing CA file", func(c *config.Valkey) {
				c.TLS = config.ValkeyTLS{Mode: "verify-full", CAFile: filepath.Join(dir, "ca.pem")}
			}},
		}
		for _, b := range bad {
			cfg := standaloneCfg("127.0.0.1:1")
			b.mod(&cfg)
			if v, err := NewValkey(t.Context(), cfg, "pod", "web", nil, nil); err == nil {
				v.Close()
				t.Errorf("%s: accepted", b.name)
			}
		}
		if _, err := NewValkey(t.Context(), standaloneCfg("127.0.0.1:1"), "", "web", nil, nil); err == nil {
			t.Error("empty instance accepted")
		}
	})
	t.Run("R-HA-057 metrics are registered under the trove_valkey_ names", func(t *testing.T) {
		v, reg := newTestValkey(t, sentinelCfg("127.0.0.1:1"))
		v.CountDegraded(MechanismFlightLock)
		v.CountDegraded("not-a-mechanism")
		v.CountCacheLookup("negative", "hit")
		if got := gaugeValue(t, reg, "trove_valkey_available", ""); got != 0 {
			t.Fatalf("trove_valkey_available = %v while degraded", got)
		}
		mfs, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		have := map[string]bool{}
		for _, mf := range mfs {
			have[mf.GetName()] = true
		}
		for _, name := range []string{
			"trove_valkey_available", "trove_valkey_circuit_open", "trove_valkey_sentinels_reachable",
			"trove_valkey_pubsub_subscribed", "trove_valkey_errors_total", "trove_valkey_degraded_total", "trove_valkey_cache_lookups_total",
		} {
			if !have[name] {
				t.Errorf("%s is not exported", name)
			}
		}
		if got := gaugeValue(t, reg, "trove_valkey_degraded_total", MechanismFlightLock); got != 1 {
			t.Errorf("degraded_total{flight_lock} = %v, want 1", got)
		}
		if _, err := NewValkey(t.Context(), sentinelCfg("127.0.0.1:1"), "pod", "web", nil, reg); err == nil {
			t.Error("a second client on one registry must fail, not panic")
		}
	})
	t.Run("R-ARCH-031 Close is idempotent and ends the background work", func(t *testing.T) {
		v, _ := newTestValkey(t, sentinelCfg("127.0.0.1:1"))
		v.Close()
		v.Close()
		if v.Available() || v.Status().DegradedReason != "client closed" {
			t.Fatalf("status = %+v", v.Status())
		}
		if _, err := v.Subscribe(t.Context(), Channel{name: "c"}, func(Message) {}); err == nil {
			t.Fatal("Subscribe on a closed client")
		}
	})
	t.Run("R-ARCH-031 a subscription made while Valkey is down is accepted and ends with its context", func(t *testing.T) {
		v, _ := newTestValkey(t, sentinelCfg("127.0.0.1:1"))
		ctx, cancel := context.WithCancel(t.Context())
		if _, err := v.Subscribe(ctx, Channel{name: "trove:x:sig:v1:jobs"}, func(Message) {}); err != nil {
			t.Fatal(err)
		}
		if n := len(v.ps.wanted()); n != 1 {
			t.Fatalf("wanted = %d", n)
		}
		cancel()
		eventually(t, "removal on context end", time.Second, func() bool { return len(v.ps.wanted()) == 0 })
		if _, err := v.Subscribe(t.Context(), Channel{}, func(Message) {}); err == nil {
			t.Error("zero Channel accepted")
		}
	})
}

func TestValkeyBreakerIntegration(t *testing.T) {
	t.Run("R-HA-057 repeated errors open the circuit, the client reports degraded, then it closes", func(t *testing.T) {
		var sick atomic.Bool
		sick.Store(true)
		var gets atomic.Int64
		srv := newFakeServer(t, func(args []string) string {
			switch strings.ToUpper(args[0]) {
			case "HELLO":
				return helloReplyFor("valkey", "9.0.0")
			case "CLIENT":
				return "+OK\r\n"
			case "PING":
				return "+PONG\r\n"
			case "GET":
				gets.Add(1)
				if sick.Load() {
					return "-OOM command not allowed when used memory > 'maxmemory'.\r\n"
				}
				return "$-1\r\n"
			case "SET":
				if sick.Load() {
					return "-OOM command not allowed when used memory > 'maxmemory'.\r\n"
				}
				return "+OK\r\n"
			}
			return "+OK\r\n"
		})
		cfg := standaloneCfg(srv.addr())
		cfg.HealthCheckInterval = time.Hour // only the calls and our own probes drive the breaker
		v, reg := newTestValkey(t, cfg)
		eventually(t, "connection", 5*time.Second, func() bool { return v.Status().Version != "" })
		k := mustKey(t, v)
		for range cfg.CircuitBreaker.FailureThreshold {
			if _, _, err := v.Get(t.Context(), k); err == nil || errors.Is(err, ErrUnavailable) {
				t.Fatalf("Get = %v, want the server's OOM error", err)
			}
		}
		if v.Available() || !v.Status().CircuitOpen || v.Status().DegradedReason != "circuit open" {
			t.Fatalf("status after %d errors = %+v", cfg.CircuitBreaker.FailureThreshold, v.Status())
		}
		if got := gaugeValue(t, reg, "trove_valkey_circuit_open", ""); got != 1 {
			t.Fatalf("trove_valkey_circuit_open = %v", got)
		}
		before := gets.Load()
		if _, _, err := v.Get(t.Context(), k); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Get with the circuit open = %v, want ErrUnavailable", err)
		}
		if gets.Load() != before {
			t.Fatal("a call reached the server while the circuit was open")
		}
		if got := gaugeValue(t, reg, "trove_valkey_errors_total", string(KindOOM)); got < float64(cfg.CircuitBreaker.FailureThreshold) {
			t.Fatalf("errors_total{oom} = %v", got)
		}

		sick.Store(false)
		time.Sleep(cfg.CircuitBreaker.OpenDuration + 100*time.Millisecond)
		if _, _, err := v.Get(t.Context(), k); err != nil {
			t.Fatalf("probe after open_duration: %v", err)
		}
		eventually(t, "recovery", time.Second, v.Available)
		if got := gaugeValue(t, reg, "trove_valkey_circuit_open", ""); got != 0 {
			t.Fatalf("trove_valkey_circuit_open = %v after recovery", got)
		}
	})
}

func TestCredentials(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cfg := config.Valkey{
		UsernameFile: write("u", "trove\n"), PasswordFile: write("p", "secret-1\n"),
		Sentinel: config.ValkeySentinel{PasswordFile: write("sp", "sentinel-1")},
	}
	c := newCredentials(cfg)

	t.Run("R-ARCH-031 data nodes and Sentinels get their own credential", func(t *testing.T) {
		u, p, err := c.read(peerData)
		if err != nil || u != "trove" || p != "secret-1" {
			t.Fatalf("data = %q %q %v", u, p, err)
		}
		u, p, err = c.read(peerSentinel)
		if err != nil || u != "" || p != "sentinel-1" {
			t.Fatalf("sentinel = %q %q %v", u, p, err)
		}
	})
	t.Run("R-ARCH-031 a rotated file applies to the next connection attempt", func(t *testing.T) {
		write("p", "secret-2\n")
		_, p, err := c.read(peerData)
		if err != nil || p != "secret-2" {
			t.Fatalf("password = %q, %v", p, err)
		}
	})
	t.Run("R-ARCH-031 the error names the file, not its content", func(t *testing.T) {
		c2 := newCredentials(config.Valkey{PasswordFile: filepath.Join(dir, "gone")})
		_, _, err := c2.read(peerData)
		if err == nil || !strings.Contains(err.Error(), "gone") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-ARCH-031 AuthCredentialsFn picks the credential by the kind of the peer", func(t *testing.T) {
		got, err := c.fn(authCtx(peerAddr{kind: peerSentinel}))
		if err != nil || got.Password != "sentinel-1" {
			t.Fatalf("sentinel = %+v, %v", got, err)
		}
		got, err = c.fn(authCtx(peerAddr{kind: peerData}))
		if err != nil || got.Password != "secret-2" || got.Username != "trove" {
			t.Fatalf("data = %+v, %v", got, err)
		}
	})
}

func mustKey(t *testing.T, v *Valkey) Key {
	t.Helper()
	k, err := v.Key(testFamily, "k1")
	if err != nil {
		t.Fatal(err)
	}
	return k
}

var testFamily = RegisterKeyFamily("testclient", 1, 2*time.Minute)

// gaugeValue reads one series of a family from the registry: the series whose
// first label value is label, or the only series when label is empty.
func gaugeValue(t *testing.T, reg *prometheus.Registry, name, label string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if label != "" && (len(m.GetLabel()) == 0 || m.GetLabel()[0].GetValue() != label) {
				continue
			}
			switch {
			case m.GetGauge() != nil:
				return m.GetGauge().GetValue()
			case m.GetCounter() != nil:
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}
