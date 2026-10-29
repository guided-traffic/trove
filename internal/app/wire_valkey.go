package app

import (
	"context"
	"strings"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/observability/health"
)

// valkeyComponent starts the Valkey client in the background (R-ARCH-024 step 7).
// Start never waits for an unreachable Valkey; a configuration error at the first
// connection holds back the first readiness only (Q-HA-13).
type valkeyComponent struct {
	a  *App
	vk *cluster.Valkey
}

func (c *valkeyComponent) Name() string { return "valkey" }

func (c *valkeyComponent) Start(ctx context.Context) error {
	a := c.a
	var opts []cluster.ValkeyOption
	if f, ok := a.systemFacts(); ok {
		opts = append(opts, cluster.WithKeyspaceID(keyspaceHex(f.KeyspaceID)))
	}
	vk, err := cluster.NewValkey(ctx, a.cfg.Valkey, a.id.Instance, a.id.Role, a.log, a.tele.metrics.Registry(), opts...)
	if err != nil {
		return err
	}
	c.vk = vk
	a.setValkey(vk)
	a.health.AddReported("valkey", func(context.Context) error {
		if err := vk.StartupError(); err != nil {
			return health.ConfigError(err, "config")
		}
		if st := vk.Status(); !st.Available {
			return health.WithReason(errUnavailable, st.DegradedReason)
		}
		return nil
	}, health.WithInterval(a.cfg.Valkey.HealthCheckInterval))
	return nil
}

func (c *valkeyComponent) Stop(context.Context) error {
	if c.vk != nil {
		c.vk.Close()
	}
	return nil
}

// keyspaceHex turns system_state.keyspace_id, a UUID, into the hex digits the
// key registry takes (R-ARCH-030): the dashes are dropped.
func keyspaceHex(id string) string { return strings.ReplaceAll(id, "-", "") }
