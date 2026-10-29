package cluster

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

func TestInvalidationCheckCause(t *testing.T) {
	newChanges := func(t *testing.T) *Changes {
		c, err := NewChanges(ChangesOptions{DB: nopDB{}, Config: config.Cluster{EpochPollInterval: time.Second, EpochMaxStaleness: 20 * time.Second}, Metrics: prometheus.NewRegistry()})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	transient := func(err error) bool {
		var tr interface{ HealthTransient() bool }
		return errors.As(err, &tr) && tr.HealthTransient()
	}

	t.Run("R-OPS-001 gap-b a poll that is stale because the database is away is reported as an outage", func(t *testing.T) {
		c := newChanges(t)
		c.lastEnd = time.Now().Add(-time.Minute)
		c.lastErr = postgres.ErrStandby
		if err := c.Check(context.Background()); err == nil || !transient(err) {
			t.Fatalf("Check = %v, want a transient failure", err)
		}
	})

	t.Run("R-OPS-001 gap-b a poll that is stale for another reason gates as a plain failure", func(t *testing.T) {
		c := newChanges(t)
		c.lastEnd = time.Now().Add(-time.Minute)
		c.lastErr = errors.New("area cache: listener rejected the change")
		if err := c.Check(context.Background()); err == nil || transient(err) {
			t.Fatalf("Check = %v, want a failure that is not transient", err)
		}
	})

	t.Run("R-OPS-001 gap-b a fresh poll passes", func(t *testing.T) {
		c := newChanges(t)
		c.lastEnd = time.Now()
		if err := c.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}
