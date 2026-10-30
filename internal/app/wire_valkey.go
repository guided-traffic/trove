package app

import (
	"context"
	"strings"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/observability/health"
)

// valkeyComponent starts the Valkey client in the background (R-ARCH-024 step 7).
// Start never waits for an unreachable Valkey; a configuration error at the first
// connection holds back the first readiness only (Q-HA-13).
type valkeyComponent struct {
	a     *App
	facts postgres.SystemFacts
	vk    *cluster.Valkey
}

// wireValkey is the valkey area. The client is keyed by the keyspace of the
// system facts, which the postgres area read.
func (a *App) wireValkey(ctx context.Context, pg postgresArea) (valkeyArea, error) {
	if err := requireInputs(AreaValkey, given("database", pg.db)); err != nil {
		return valkeyArea{}, err
	}
	c := &valkeyComponent{a: a, facts: pg.facts}
	if err := a.startComponent(ctx, c); err != nil {
		return valkeyArea{}, err
	}
	return valkeyArea{vk: c.vk}, nil
}

func (c *valkeyComponent) Name() string { return "valkey" }

func (c *valkeyComponent) Start(ctx context.Context) error {
	a := c.a
	opts := []cluster.ValkeyOption{cluster.WithTLSMetrics(a.tele.tls), cluster.WithKeyspaceID(keyspaceHex(c.facts.KeyspaceID))}
	vk, err := cluster.NewValkey(ctx, a.cfg.Valkey, a.id.Instance, a.id.Role, a.log, a.tele.metrics.Registry(), opts...)
	if err != nil {
		return err
	}
	c.vk = vk
	a.setValkey(vk)
	a.health.AddReported("valkey", func(context.Context) error {
		return valkeyCheck(vk.StartupError(), vk.Status())
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

// valkeyCheck is the readiness check of the Valkey client. The reason it reports
// is the client's own code (cluster.ValkeyStatus.ReasonCode), the fixed
// vocabulary of cluster.ReasonCodes: the ops listener never shows the free text
// of a degraded reason, which can carry addresses and error strings of the
// Valkey topology; the text goes to the log and to the authenticated replica
// list only. A start-up error keeps the code of the failed connection, so a
// rejected credential reads auth_failed, and holds back the first readiness
// (Q-HA-13).
func valkeyCheck(startupErr error, st cluster.ValkeyStatus) error {
	code := string(st.ReasonCode)
	if startupErr != nil {
		if code == "" {
			code = string(cluster.ReasonConfigError)
		}
		return health.ConfigError(startupErr, code)
	}
	if !st.Available {
		if code == "" {
			code = string(cluster.ReasonProbeFailed)
		}
		return health.WithReason(errUnavailable, code)
	}
	return nil
}
