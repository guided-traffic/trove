//go:build integration

package valkey

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestBreaker(t *testing.T) {
	t.Run("R-HA-057 repeated timeouts open the circuit, the client reports degraded, then it closes", func(t *testing.T) {
		v := testenv.ValkeyConfig(t)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		c := newClient(t, stackConfig(t, 2*time.Second))
		waitAvailable(t, c)
		admin := adminAt(t, v, c.Status().Primary)
		k, _ := c.Key(kvFamily, "breaker")

		// CLIENT PAUSE WRITE holds every write: the SETs run into command_timeout
		if err := admin.Do(ctx, admin.B().Arbitrary("CLIENT", "PAUSE", "3500", "WRITE").Build()).Error(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = admin.Do(context.Background(), admin.B().Arbitrary("CLIENT", "UNPAUSE").Build()).Error() })
		// the health probe runs into the pause as well, so the circuit may open
		// before the fifth Set of this loop
		for i := 0; i < 10 && !c.Status().CircuitOpen; i++ {
			if err := c.Set(ctx, k, "x"); err == nil {
				t.Fatalf("Set %d during the pause succeeded", i)
			}
		}
		if c.Available() || !c.Status().CircuitOpen {
			t.Fatalf("status after 5 timeouts = %+v", c.Status())
		}
		if got := metric(t, c.Reg, "trove_valkey_circuit_open", ""); got != 1 {
			t.Fatalf("trove_valkey_circuit_open = %v", got)
		}
		if got := metric(t, c.Reg, "trove_valkey_errors_total", "timeout"); got < 5 {
			t.Fatalf("errors_total{timeout} = %v", got)
		}
		start := time.Now()
		if err := c.Set(ctx, k, "x"); !errors.Is(err, cluster.ErrUnavailable) || time.Since(start) > 50*time.Millisecond {
			t.Fatalf("Set with the circuit open = %v after %s, want ErrUnavailable at once", err, time.Since(start))
		}

		// the pause ends; open_duration (2 s) and the next probe close the circuit
		eventually(t, "recovery", 15*time.Second, c.Available)
		if got := metric(t, c.Reg, "trove_valkey_circuit_open", ""); got != 0 {
			t.Fatalf("trove_valkey_circuit_open = %v after recovery", got)
		}
		if err := c.Set(ctx, k, "back"); err != nil {
			t.Fatalf("Set after recovery: %v", err)
		}
	})
}
