package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/observability/health"
)

func TestStandingWarnings(t *testing.T) {
	t.Run("R-OPS-005 an unset base_url warns at start", func(t *testing.T) {
		h := start(t, "web", map[string]string{"TROVE_SERVER_BASE_URL": ""})
		if !strings.Contains(h.logs.String(), "server.base_url is not set") {
			t.Fatalf("no warning at start:\n%s", h.logs.String())
		}
	})
	t.Run("R-OPS-005 a configured base_url does not warn", func(t *testing.T) {
		h := start(t, "web", nil)
		if strings.Contains(h.logs.String(), "server.base_url is not set") {
			t.Fatalf("warned although base_url is set:\n%s", h.logs.String())
		}
	})
	t.Run("R-OPS-005 the warnings repeat on the interval", func(t *testing.T) {
		h := start(t, "web", map[string]string{"TROVE_SERVER_BASE_URL": "", "TROVE_SYSTEM_BOOTSTRAP_ANONYMOUS_READ": "true"})
		before := strings.Count(h.logs.String(), "server.base_url is not set")
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { h.app.warnLoop(ctx, 20*time.Millisecond); close(done) }()
		time.Sleep(200 * time.Millisecond)
		cancel()
		<-done
		if after := strings.Count(h.logs.String(), "server.base_url is not set"); after < before+3 {
			t.Fatalf("base_url warning count %d -> %d, want at least 3 repeats", before, after)
		}
		if n := strings.Count(h.logs.String(), "system.bootstrap_anonymous_read is true"); n < 4 {
			t.Fatalf("anonymous-read warning logged %d times", n)
		}
	})
	t.Run("R-OPS-002 bootstrap_anonymous_read warns at start", func(t *testing.T) {
		h := start(t, "web", map[string]string{"TROVE_SYSTEM_BOOTSTRAP_ANONYMOUS_READ": "true", "TROVE_SERVER_BASE_URL": "http://localhost:8080"})
		if !strings.Contains(h.logs.String(), "system.bootstrap_anonymous_read is true") {
			t.Fatalf("no warning at start:\n%s", h.logs.String())
		}
	})
	t.Run("R-OPS-005 a worker does not warn about the client origin", func(t *testing.T) {
		h := start(t, "worker", map[string]string{"TROVE_SERVER_BASE_URL": ""})
		if strings.Contains(h.logs.String(), "server.base_url is not set") {
			t.Fatalf("a worker has no client listener:\n%s", h.logs.String())
		}
	})
}

func TestValkeyReasonCodes(t *testing.T) {
	reasonOf := func(err error) string {
		var r interface{ HealthReason() string }
		if !errors.As(err, &r) {
			return "<none>"
		}
		return r.HealthReason()
	}
	t.Run("R-OPS-001 /readyz shows the client's own code, from the fixed vocabulary, never the text with addresses", func(t *testing.T) {
		for _, code := range cluster.ReasonCodes() {
			st := cluster.ValkeyStatus{ReasonCode: code, DegradedReason: "not connected: dial tcp 10.1.2.3:6379: i/o timeout"}
			err := valkeyCheck(nil, st)
			if got := reasonOf(err); got != string(code) {
				t.Errorf("%s -> %q", code, got)
			}
			if health.IsConfigError(err) {
				t.Errorf("%s without a start-up error is a config error", code)
			}
			if strings.ContainsAny(reasonOf(err), ".: ") {
				t.Errorf("%s: the reason is not a code", code)
			}
		}
	})
	t.Run("R-OPS-001 an available client passes", func(t *testing.T) {
		if err := valkeyCheck(nil, cluster.ValkeyStatus{Available: true}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-OPS-001 R-HA-056 a start-up error with a rejected credential shows auth_failed and holds back readiness", func(t *testing.T) {
		st := cluster.ValkeyStatus{ReasonCode: cluster.ReasonAuthFailed, DegradedReason: "not connected: WRONGPASS"}
		err := valkeyCheck(errors.New("WRONGPASS for 10.1.2.3:6379"), st)
		if !health.IsConfigError(err) || reasonOf(err) != "auth_failed" {
			t.Fatalf("config error = %v, reason = %q", health.IsConfigError(err), reasonOf(err))
		}
	})
	t.Run("R-OPS-001 a start-up error without a code falls back to config_error", func(t *testing.T) {
		err := valkeyCheck(errors.New("bad"), cluster.ValkeyStatus{})
		if reasonOf(err) != "config_error" {
			t.Fatalf("reason = %q", reasonOf(err))
		}
	})
}
