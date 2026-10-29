package app

import (
	"context"
	"encoding/json"

	"github.com/guided-traffic/trove/internal/buildinfo"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/jobs"
)

// closer adapts a type with Close to a Component.
type closer struct {
	name  string
	close func()
}

func (c closer) Name() string                { return c.name }
func (c closer) Start(context.Context) error { return nil }
func (c closer) Stop(context.Context) error  { c.close(); return nil }

// startCoordination wires signals, the change log, the cache registry and the
// replica registry, in the order of docs/developer/coordination.md (R-ARCH-024
// step 7): signals and changes first, caches before the first poll, the
// `invalidation` check, then the registry row.
func (a *App) startCoordination(ctx context.Context) error {
	db := a.database()
	var bus cluster.Bus
	if vk := a.valkey(); vk != nil {
		bus = vk // never a typed nil: a nil Bus means "Valkey never available"
	}
	reg := a.tele.metrics.Registry()
	log := a.log
	wd := a.cfg.Health.WatchdogTimeout

	signals, err := cluster.NewSignals(cluster.SignalsOptions{Bus: bus, Config: a.cfg.Cluster, Log: log.With("component", "signals"), Metrics: reg})
	if err != nil {
		return err
	}
	if err := a.startComponent(ctx, closer{name: "signals", close: signals.Close}); err != nil {
		return err
	}

	changes, err := cluster.NewChanges(cluster.ChangesOptions{
		DB: db.ControlPool(), Bus: bus, Config: a.cfg.Cluster,
		WatchdogTimeout: wd, Watchdog: a.health, Log: log.With("component", "changes"), Metrics: reg,
	})
	if err != nil {
		return err
	}
	caches, err := cache.NewRegistry(changes, reg)
	if err != nil {
		return err
	}
	changes.AddListener(caches)
	if err := a.startComponent(ctx, changes); err != nil {
		return err
	}
	// The poll depends on PostgreSQL only, never on Valkey (R-OPS-001).
	a.health.AddReadiness("invalidation", changes.Check, false)

	reps, err := cluster.NewRegistry(a.registryOptions())
	if err != nil {
		return err
	}
	if err := a.startComponent(ctx, reps); err != nil {
		return err
	}

	registry, err := a.jobRegistry()
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.signals, a.changes, a.caches = signals, changes, caches
	a.mu.Unlock()

	if a.sub.JobLoops {
		return a.startWorker(ctx, registry, signals)
	}
	a.mu.Lock()
	a.queue = jobs.NewQueue(jobs.QueueOptions{DB: db.Main(), Registry: registry, Signals: signals, Log: log})
	a.mu.Unlock()
	return nil
}

// registryOptions describes this replica to the registry (R-OPS-003).
func (a *App) registryOptions() cluster.RegistryOptions {
	bi := buildinfo.Get()
	return cluster.RegistryOptions{
		DB: a.database().ControlPool(),
		Identity: cluster.ReplicaIdentity{
			UID: a.id.UID, Name: a.id.Instance, Version: bi.Version, Revision: bi.Commit, Role: a.id.Role,
			Node: a.id.Node, Pool: a.id.Pool, StartedAt: a.startedAt,
			SecretKeyIDs: a.ring.KeyIDs(), ActiveSecretKeyID: a.ring.ActiveKeyID(),
		},
		Snapshot: a.replicaSnapshot,
		Leases: func() []cluster.HeldInfo {
			if l := a.leasesOrNil(); l != nil {
				return l.Held()
			}
			return nil
		},
		Config: a.cfg.System, WatchdogTimeout: a.cfg.Health.WatchdogTimeout, Watchdog: a.health,
		Log: a.log.With("component", "registry"),
	}
}

// replicaSnapshot is what each heartbeat writes: the state, every check and the
// Valkey view, from the cached readiness results.
func (a *App) replicaSnapshot() cluster.ReplicaSnapshot {
	snap := a.health.Snapshot()
	checks := make(map[string]cluster.ReplicaCheck, len(snap.Checks))
	for _, c := range snap.Checks {
		checks[c.Name] = cluster.ReplicaCheck{Status: string(c.Status), Reason: c.Reason, ConfigError: c.ConfigError}
	}
	out := cluster.ReplicaSnapshot{State: string(snap.State), Checks: checks}
	if v := a.valkeyView(); v != nil {
		if b, err := json.Marshal(v); err == nil {
			out.Valkey = b
		}
	}
	return out
}

// workerGroup is the lease manager and the job worker as one component: the
// worker starts before the leases (it registers the scheduler hold), and on
// shutdown the worker stops before the leases do (coordination.md).
type workerGroup struct {
	leases *cluster.Leases
	worker *jobs.Worker
}

func (g *workerGroup) Name() string { return "jobs" }

func (g *workerGroup) Start(ctx context.Context) error {
	if err := g.worker.Start(ctx); err != nil {
		return err
	}
	return g.leases.Start(ctx)
}
func (g *workerGroup) Drain(ctx context.Context)        { g.worker.Drain(ctx) }
func (g *workerGroup) Settle(ctx context.Context) error { return g.worker.Settle(ctx) }
func (g *workerGroup) Quiesce(ctx context.Context)      { g.worker.Quiesce(ctx) }
func (g *workerGroup) Stop(ctx context.Context) error {
	err := g.worker.Stop(ctx)
	if lerr := g.leases.Stop(ctx); err == nil {
		err = lerr
	}
	return err
}

// startWorker builds the lease manager and the job worker on worker and all.
func (a *App) startWorker(ctx context.Context, registry *jobs.Registry, signals *cluster.Signals) error {
	db := a.database()
	wd := a.cfg.Health.WatchdogTimeout
	leases, err := cluster.NewLeases(cluster.LeaseOptions{
		Pool: db.LeasePool(), Config: a.cfg.Cluster, CheckInterval: a.cfg.Database.LeaseCheckInterval,
		WatchdogTimeout: wd, Watchdog: a.health, Log: a.log.With("component", "leases"), Metrics: a.tele.metrics.Registry(),
	})
	if err != nil {
		return err
	}
	worker, err := jobs.NewWorker(jobs.WorkerOptions{
		Role: a.id.Role, Instance: a.id.Instance, UID: a.id.UID, Main: db.Main(), Control: db.ControlPool(),
		Registry: registry, Config: a.cfg.Jobs, Maintenance: a.cfg.Maintenance, Signals: signals, Leases: leases,
		Ready: a.health.Ready, Watchdog: a.health, WatchdogTimeout: wd, Log: a.log, Metrics: a.tele.metrics.Registry(),
	})
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.leases, a.queue = leases, worker.Queue()
	a.mu.Unlock()
	return a.startComponent(ctx, &workerGroup{leases: leases, worker: worker})
}

func (a *App) leasesOrNil() *cluster.Leases { a.mu.Lock(); defer a.mu.Unlock(); return a.leases }
