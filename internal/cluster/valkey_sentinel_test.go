package cluster

import (
	"errors"
	"strings"
	"testing"
)

func TestClassifyStartup(t *testing.T) {
	refused := errors.New("dial tcp: connect: connection refused")
	wrongpass := realReplyError(t, "WRONGPASS invalid username-password pair")
	noauth := realReplyError(t, "NOAUTH Authentication required.")
	someErr := errors.New("connection refused")

	t.Run("Q-HA-13 every Sentinel unreachable is transient", func(t *testing.T) {
		probes := []sentinelProbe{{Addr: "a", Err: refused}, {Addr: "b", Err: refused}}
		if cfg, _ := classifyStartup("trove", probes, someErr); cfg {
			t.Fatal("unreachable Sentinels classified as a configuration error")
		}
	})
	t.Run("Q-HA-13 a primary name no reachable Sentinel knows is a configuration error", func(t *testing.T) {
		probes := []sentinelProbe{{Addr: "a", Unknown: true}, {Addr: "b", Unknown: true}, {Addr: "c", Err: refused}}
		cfg, cause := classifyStartup("trvoe", probes, someErr)
		if !cfg || !strings.Contains(cause.Error(), `"trvoe"`) || classify(cause) != KindNoPrimary {
			t.Fatalf("cause = %v, config = %v", cause, cfg)
		}
	})
	t.Run("Q-HA-13 one Sentinel that names the primary makes a null answer elsewhere irrelevant", func(t *testing.T) {
		probes := []sentinelProbe{{Addr: "a", Unknown: true}, {Addr: "b", Primary: "10.0.0.1:6379"}}
		if cfg, _ := classifyStartup("trove", probes, someErr); cfg {
			t.Fatal("classified as a configuration error although a Sentinel knows the primary")
		}
	})
	t.Run("Q-HA-13 rejected Sentinel credentials are a configuration error", func(t *testing.T) {
		probes := []sentinelProbe{{Addr: "a", Err: noauth}, {Addr: "b", Err: refused}}
		cfg, cause := classifyStartup("trove", probes, someErr)
		if !cfg || classify(cause) != KindAuth {
			t.Fatalf("cause = %v, config = %v", cause, cfg)
		}
	})
	t.Run("Q-HA-13 a rejected data-node credential is a configuration error", func(t *testing.T) {
		probes := []sentinelProbe{{Addr: "a", Primary: "10.0.0.1:6379"}}
		cfg, cause := classifyStartup("trove", probes, wrongpass)
		if !cfg || classify(cause) != KindAuth {
			t.Fatalf("cause = %v, config = %v", cause, cfg)
		}
	})
	t.Run("Q-HA-13 a data node that refuses connections is transient", func(t *testing.T) {
		probes := []sentinelProbe{{Addr: "a", Primary: "10.0.0.1:6379"}}
		if cfg, _ := classifyStartup("trove", probes, refused); cfg {
			t.Fatal("refused connection classified as a configuration error")
		}
	})
	t.Run("Q-HA-13 standalone mode classifies the connection error alone", func(t *testing.T) {
		if cfg, _ := classifyStartup("", nil, wrongpass); !cfg {
			t.Fatal("WRONGPASS in standalone mode is a configuration error")
		}
		if cfg, _ := classifyStartup("", nil, refused); cfg {
			t.Fatal("refused in standalone mode is transient")
		}
	})
	t.Run("R-ARCH-031 the status names the reason", func(t *testing.T) {
		got := describeConnectError(refused, []sentinelProbe{{Err: refused}, {Err: refused}})
		if !strings.Contains(got, "no Sentinel reachable (0 of 2)") {
			t.Fatalf("reason = %q", got)
		}
	})
}
