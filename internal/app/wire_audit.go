package app

import (
	"context"
	"fmt"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/observability"
)

// wireAudit is the audit area, step 4 of R-ARCH-024: it reconciles
// audit.retention_days with the stored policy, calls audit_maintain_partitions()
// until the partitions of the current and the next month exist, and builds the
// recorder. An insert therefore never meets a missing partition, and the replica
// is not ready (the startup check) before this has passed. It retries while
// PostgreSQL does not answer, like the steps before it.
func (a *App) wireAudit(ctx context.Context, pg postgresArea) (auditArea, error) {
	if err := requireInputs(AreaAudit, given("database", pg.db)); err != nil {
		return auditArea{}, err
	}
	db := pg.db
	store := audit.NewStore(db)
	log := a.log.With("component", "audit")
	for delay := time.Second; ; delay = min(delay*2, 15*time.Second) {
		err := store.Prepare(ctx, a.cfg.Audit.RetentionDays, log)
		if err == nil {
			break
		}
		log.Warn("audit partitions are not ready, retrying", "error", err, "retry_in", delay.String())
		if !sleep(ctx, delay) {
			return auditArea{}, ctx.Err()
		}
	}
	w, err := audit.New(audit.Options{
		DB: db, Replica: a.id.Instance, Config: a.cfg.Audit,
		Log:    a.tele.logging.Stream(observability.StreamAudit),
		AppLog: log, Metrics: newAuditMetrics(a.tele.metrics),
	})
	if err != nil {
		return auditArea{}, err
	}
	if err := a.startComponent(ctx, &auditComponent{w: w}); err != nil {
		return auditArea{}, err
	}
	a.auditStore.set(store)
	return auditArea{rec: v1.WithRequestSource(w), store: store}, nil
}

// auditComponent runs the asynchronous batch writer and flushes it at shutdown
// (R-OPS-015: step 6 of R-ARCH-024, before the pools close).
type auditComponent struct{ w *audit.Writer }

func (c *auditComponent) Name() string                { return "audit" }
func (c *auditComponent) Start(context.Context) error { c.w.Start(); return nil }

// Flush writes the buffered events and the open deduplication summaries.
func (c *auditComponent) Flush(ctx context.Context) error {
	if err := c.w.Flush(ctx); err != nil {
		return fmt.Errorf("audit buffer: %w", err)
	}
	return nil
}

// Stop ends the writer when no Flush came first.
func (c *auditComponent) Stop(ctx context.Context) error { return c.w.Flush(ctx) }

// counters adapts the two families of R-OPS-008 to audit.Metrics.
type counters struct {
	recorded func(kind, outcome string)
	dropped  func()
}

func newAuditMetrics(m *observability.Metrics) audit.Metrics {
	ev := m.NewCounterVec("trove_audit_events_total", "Audit events recorded.", "kind", "outcome")
	dr := m.NewCounterVec("trove_audit_events_dropped_total", "Access events lost to a full buffer.")
	dr.WithLabelValues() // the series exists from the first scrape
	return counters{
		recorded: func(kind, outcome string) { ev.WithLabelValues(kind, outcome).Inc() },
		dropped:  func() { dr.WithLabelValues().Inc() },
	}
}

func (c counters) Recorded(category string, o audit.Outcome) { c.recorded(category, string(o)) }
func (c counters) Dropped()                                  { c.dropped() }

// taskAuditRetention is the daily partition maintenance of R-OPS-015 and
// R-OPS-017: audit_maintain_partitions() creates the partitions ahead and drops
// the expired ones.
const taskAuditRetention = "audit-retention"

// registerAuditJobs registers the audit-retention task (R-OPS-019). Its body runs
// on a worker, which wires the audit store before it claims anything.
func (a *App) registerAuditJobs(r *jobs.Registry) {
	r.RegisterKind(taskAuditRetention, jobs.KindSpec{
		Queue: "maintenance", DedupMode: jobs.Exclusive, MaxAttempts: 3,
		Handler: a.auditStore.handler("audit store", func(ctx context.Context, store *audit.Store, _ *jobs.Run) error {
			return store.Maintain(ctx)
		}),
	})
	spec := jobs.TaskSpec{Schedule: "30 3 * * *"}
	if c, ok := a.cfg.Maintenance.Tasks[taskAuditRetention]; ok && c.Schedule != "" {
		spec.Schedule, spec.Disabled = c.Schedule, !c.Enabled
	}
	r.RegisterTask(taskAuditRetention, spec)
}

// auditStoreForAPI is the store the query and export operations read; nil in the
// shell profile, which has no audit area.
func (a *App) auditStoreForAPI() *audit.Store {
	store, _ := a.auditStore.get()
	return store
}
