package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/observability/health"
)

// startBlob builds the object-store client, its guard, the blob store and, on
// worker and all, the garbage collector (R-ARCH-024 step 6). The startup check of
// R-STOR-035 retries a transient failure while the replica stays unready, and
// ends the process on a configuration error such as a wrong bucket or credential.
func (a *App) startBlob(ctx context.Context) error {
	cfg := a.cfg.Storage
	if err := a.out.Err(); err != nil {
		// The outbound configuration error holds the first readiness back (the
		// outbound check); without a client there is nothing to build.
		a.log.Error("object store not started: outbound configuration error", "error", err)
		return nil
	}
	client, err := s3.NewFromFactory(ctx, cfg.S3, a.out)
	if err != nil {
		return fmt.Errorf("object store: %w", err)
	}
	bm := blob.NewMetrics(a.tele.metrics)
	log := a.log.With("component", "blob")
	objects := blob.NewGuard(client, cfg.S3.CircuitBreaker, blob.WithGuardMetrics(bm))
	db := a.database()
	store, err := blob.New(cfg, objects, db, blob.WithLogger(log), blob.WithMetrics(bm))
	if err != nil {
		return err
	}
	if cfg.S3.StartupCheck {
		if err := blob.AwaitStartupCheck(ctx, objects, cfg.S3.Prefix, a.id.Instance, log); err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			return fmt.Errorf("object store startup check: %w", err)
		}
	}
	// After the first readiness the object store is only reported unless
	// health.require_object_store opts in (Q-OPS-2): first = !RequireObjectStore.
	probe := strings.TrimPrefix(cfg.S3.Prefix+"/_probe/health", "/")
	a.health.AddReadiness("objectStore", func(ctx context.Context) error {
		_, err := objects.Head(ctx, probe)
		if err == nil || errors.Is(err, blob.ErrNotFound) {
			return nil
		}
		if blob.IsConfig(err) {
			return health.ConfigError(err, "config")
		}
		return health.WithReason(err, "unavailable")
	}, !a.cfg.Health.RequireObjectStore)

	a.mu.Lock()
	a.blobs = store
	if a.sub.JobLoops {
		a.gc = blob.NewCollector(cfg.GC, cfg.S3.Prefix, objects, db, blob.WithCollectorLogger(log), blob.WithCollectorMetrics(bm))
	}
	a.mu.Unlock()
	return nil
}
