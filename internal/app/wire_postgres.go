package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/guided-traffic/trove/internal/buildinfo"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// postgresComponent connects the pools and runs the schema compatibility check
// (R-ARCH-024 step 3). It retries until PostgreSQL answers; only a failure that
// retrying cannot fix (configuration, preflight, a schema floor above this
// binary) ends the process.
type postgresComponent struct {
	a  *App
	db *postgres.DB
}

func (c *postgresComponent) Name() string { return "postgres" }

func (c *postgresComponent) Start(ctx context.Context) (err error) {
	a := c.a
	// startComponent records a component only after Start succeeded, so Stop
	// never runs for one that failed: close the pools here on every return that
	// follows a successful Open (a floor error, a cancelled context).
	defer func() {
		if err != nil && c.db != nil {
			c.db.Close()
			c.db = nil
		}
	}()
	for delay := time.Second; ; delay = min(delay*2, 15*time.Second) {
		db, err := postgres.Open(ctx, a.cfg.Database, a.id.Instance, a.id.Role, a.log.With("component", "postgres"))
		if err == nil {
			c.db = db
			break
		}
		if !postgres.IsUnavailable(err) {
			return fmt.Errorf("database: %w", err)
		}
		a.log.Warn("database unavailable, retrying", "error", err, "retry_in", delay.String())
		if !sleep(ctx, delay) {
			return ctx.Err()
		}
	}
	// The schema may still be behind: the pre-upgrade Job normally migrates first,
	// so wait, not ready, until it has.
	for delay := time.Second; ; delay = min(delay*2, 15*time.Second) {
		err := c.db.CheckSchema(ctx, buildinfo.Get().Version)
		if err == nil {
			break
		}
		var floor *postgres.FloorError
		if errors.As(err, &floor) {
			return err
		}
		a.log.Warn("waiting for the schema", "error", err, "retry_in", delay.String())
		if !sleep(ctx, delay) {
			return ctx.Err()
		}
	}
	// system_state holds the installation, the Valkey keyspace and the incarnation
	// (R-ARCH-030): read once, before the Valkey client starts.
	for delay := time.Second; ; delay = min(delay*2, 15*time.Second) {
		f, err := c.db.SystemFacts(ctx)
		if err == nil {
			a.setSystemFacts(f)
			break
		}
		a.log.Warn("cannot read system_state, retrying", "error", err, "retry_in", delay.String())
		if !sleep(ctx, delay) {
			return ctx.Err()
		}
	}
	a.health.AddReadiness("database", c.db.Ready, false)
	a.health.AddReadiness("schema", func(ctx context.Context) error { return c.db.CheckSchema(ctx, buildinfo.Get().Version) }, false)
	a.setDB(c.db)
	return nil
}

// Stop closes the pools. It runs last of the infrastructure, after the flushes
// and the release of leases (R-ARCH-024 step 6).
func (c *postgresComponent) Stop(context.Context) error {
	if c.db != nil {
		c.db.Close()
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
