package config

import (
	"testing"
	"time"
)

func loadWith(t *testing.T, env ...string) (*Loaded, error) {
	t.Helper()
	return Load(Options{Environ: servableEnv(env...), Command: CommandServe})
}

func TestShutdownBudget(t *testing.T) {
	t.Run("R-HA-011 an unset timeout is the grace period minus the drain delay minus 5 s", func(t *testing.T) {
		for _, c := range []struct {
			grace, drain string
			want         time.Duration
		}{
			{"300s", "15s", 280 * time.Second},
			{"120s", "5s", 110 * time.Second},
			{"45s", "30s", 10 * time.Second},
		} {
			l, err := loadWith(t, "TROVE_RUNTIME_TERMINATION_GRACE_PERIOD="+c.grace, "TROVE_HEALTH_SHUTDOWN_DRAIN_DELAY="+c.drain)
			if err != nil {
				t.Fatal(err)
			}
			if got := l.Config.Server.ShutdownTimeout; got != c.want {
				t.Errorf("grace %s drain %s: timeout %s, want %s", c.grace, c.drain, got, c.want)
			}
			if src := l.Sources["server.shutdown_timeout"].String(); src != "derived" {
				t.Errorf("source %q, want derived", src)
			}
		}
	})
	t.Run("R-ARCH-024 without a known grace period the timeout is 25 s", func(t *testing.T) {
		l, err := loadWith(t)
		if err != nil {
			t.Fatal(err)
		}
		if got := l.Config.Server.ShutdownTimeout; got != 25*time.Second {
			t.Fatalf("timeout %s", got)
		}
		if src := l.Sources["server.shutdown_timeout"].String(); src != "default" {
			t.Fatalf("source %q", src)
		}
	})
	t.Run("R-HA-011 an explicit timeout that fits the grace period is kept", func(t *testing.T) {
		l, err := loadWith(t, "TROVE_RUNTIME_TERMINATION_GRACE_PERIOD=120s", "TROVE_SERVER_SHUTDOWN_TIMEOUT=60s")
		if err != nil {
			t.Fatal(err)
		}
		if l.Config.Server.ShutdownTimeout != 60*time.Second || l.Sources["server.shutdown_timeout"].String() != "env" {
			t.Fatalf("%s %s", l.Config.Server.ShutdownTimeout, l.Sources["server.shutdown_timeout"])
		}
	})
	t.Run("R-HA-011 an explicit timeout that overruns the grace period is refused", func(t *testing.T) {
		// 110 s + 5 s drain + 5 s margin = 120 s fits; 111 s does not.
		if _, err := loadWith(t, "TROVE_RUNTIME_TERMINATION_GRACE_PERIOD=120s", "TROVE_SERVER_SHUTDOWN_TIMEOUT=110s"); err != nil {
			t.Fatalf("exact fit refused: %v", err)
		}
		_, err := loadWith(t, "TROVE_RUNTIME_TERMINATION_GRACE_PERIOD=120s", "TROVE_SERVER_SHUTDOWN_TIMEOUT=111s")
		if !hasIssue(err, "server.shutdown_timeout") {
			t.Fatalf("want an issue naming server.shutdown_timeout, got %v", err)
		}
	})
	t.Run("R-HA-011 a grace period that leaves no time after the drain is refused", func(t *testing.T) {
		_, err := loadWith(t, "TROVE_RUNTIME_TERMINATION_GRACE_PERIOD=10s", "TROVE_HEALTH_SHUTDOWN_DRAIN_DELAY=5s")
		if !hasIssue(err, "runtime.termination_grace_period") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestForwardedHeaderKey(t *testing.T) {
	t.Run("R-ARCH-006 forwarded_header defaults to x-forwarded-for and takes forwarded", func(t *testing.T) {
		l, err := loadWith(t)
		if err != nil || l.Config.Server.ForwardedHeader != "x-forwarded-for" {
			t.Fatalf("%v %v", err, l)
		}
		l, err = loadWith(t, "TROVE_SERVER_FORWARDED_HEADER=forwarded")
		if err != nil || l.Config.Server.ForwardedHeader != "forwarded" {
			t.Fatalf("%v", err)
		}
		if _, err := loadWith(t, "TROVE_SERVER_FORWARDED_HEADER=x-real-ip"); !hasIssue(err, "server.forwarded_header") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("R-ARCH-006 a /0 prefix in trusted_proxies is refused", func(t *testing.T) {
		for _, p := range []string{"0.0.0.0/0", "::/0"} {
			_, err := loadWith(t, `TROVE_SERVER_TRUSTED_PROXIES=["`+p+`"]`)
			if !hasIssue(err, "server.trusted_proxies") {
				t.Errorf("%s: got %v", p, err)
			}
		}
		if _, err := loadWith(t, `TROVE_SERVER_TRUSTED_PROXIES=["10.0.0.0/8","2001:db8::/32"]`); err != nil {
			t.Fatal(err)
		}
	})
}

func TestBootstrapAnonymousReadGuard(t *testing.T) {
	t.Run("R-OPS-002 anonymous read is refused behind an https base_url", func(t *testing.T) {
		_, err := loadWith(t, "TROVE_SYSTEM_BOOTSTRAP_ANONYMOUS_READ=true", "TROVE_SERVER_BASE_URL=https://trove.example.com")
		if !hasIssue(err, "system.bootstrap_anonymous_read") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("R-OPS-002 anonymous read is allowed on loopback, plain http and without a base_url", func(t *testing.T) {
		for _, u := range []string{"https://localhost:8443", "https://127.0.0.1", "https://[::1]:8443", "http://trove.dev.example", ""} {
			env := []string{"TROVE_SYSTEM_BOOTSTRAP_ANONYMOUS_READ=true"}
			if u != "" {
				env = append(env, "TROVE_SERVER_BASE_URL="+u)
			}
			if _, err := loadWith(t, env...); err != nil {
				t.Errorf("%q: %v", u, err)
			}
		}
	})
}

func TestWatchedLoopCadence(t *testing.T) {
	t.Run("R-ARCH-016 a schedule_tick at or above the watchdog timeout is refused naming both keys", func(t *testing.T) {
		_, err := loadWith(t, "TROVE_JOBS_SCHEDULE_TICK=3m", "TROVE_HEALTH_WATCHDOG_TIMEOUT=2m")
		if !hasIssue(err, "jobs.schedule_tick") || !hasIssue(err, "health.watchdog_timeout") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("R-OPS-001 the defaults pass", func(t *testing.T) {
		if _, err := loadWith(t); err != nil {
			t.Fatal(err)
		}
	})
}

func TestStorageCouplingAtLoad(t *testing.T) {
	t.Run("R-STOR-024 a GC hand-over delay not above lease_duration plus twice delete_settle is refused at load", func(t *testing.T) {
		_, err := loadWith(t, "TROVE_STORAGE_GC_HANDOVER_DELAY=1m", "TROVE_JOBS_LEASE_DURATION=2m")
		if !hasIssue(err, "storage.gc.handover_delay") {
			t.Fatalf("got %v", err)
		}
	})
}
