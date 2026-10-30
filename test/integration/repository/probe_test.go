//go:build integration

package repository

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	_ "github.com/guided-traffic/trove/internal/formats/all"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// probe is a snapshot reader on the installation's database with the change-log
// poll of a replica, built from the same components the composition root
// builds, and a runner that counts transactions. A replica does not expose its
// runner, so the assertions about how many queries the reader makes read this.
type probe struct {
	runner *countingRunner
	reader *repository.SnapshotReader
}

func (e *stackEnv) newProbe(name string, poll time.Duration) *probe {
	t := e.t
	t.Helper()
	clu := config.Cluster{
		LeaseRetryInterval: 500 * time.Millisecond, EpochPollInterval: poll, EpochMaxStaleness: 20 * time.Second,
		ChangeRetention: time.Hour, SignalCoalesceWindow: 20 * time.Millisecond,
	}
	quiet := testenv.Quiet()
	reg := prometheus.NewRegistry()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, e.stack.DatabaseConfig(), name, "web", quiet)
	if err != nil {
		t.Fatalf("Open(%s): %v", name, err)
	}
	t.Cleanup(db.Close)
	changes, err := cluster.NewChanges(cluster.ChangesOptions{DB: db.ControlPool(), Config: clu, WatchdogTimeout: 30 * time.Second, Log: quiet, Metrics: reg})
	if err != nil {
		t.Fatal(err)
	}
	caches, err := cache.NewRegistry(changes, reg)
	if err != nil {
		t.Fatal(err)
	}
	changes.AddListener(caches)
	if err := changes.Start(ctx); err != nil {
		t.Fatalf("changes.Start: %v", err)
	}
	t.Cleanup(func() { _ = changes.Stop(context.Background()) })
	p := &probe{runner: &countingRunner{Runner: db}}
	p.reader, err = repository.NewReader(repository.ReaderOptions{
		Runner: p.runner, Catalog: catalog.Default(), Caches: caches, Poller: changes,
		SafetyTTL: 5 * time.Minute, UnknownKeys: 10000, LookupWait: 5 * time.Second, Log: quiet,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
