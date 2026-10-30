package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/observability/health"
)

// wireBlob is the blob area: it builds the object-store client, its guard, the
// blob store and, on worker and all, the garbage collector (R-ARCH-024 step 6).
// The startup check of R-STOR-035 retries a transient failure while the replica
// stays unready, and ends the process on a configuration error such as a wrong
// bucket or credential.
func (a *App) wireBlob(ctx context.Context, keys keysArea, pg postgresArea) (blobArea, error) {
	if err := requireInputs(AreaBlob, given("outbound factory", keys.out), given("database", pg.db)); err != nil {
		return blobArea{}, err
	}
	cfg := a.cfg.Storage
	out, db := keys.out, pg.db
	// A CA file of proxy.outbound.tls.ca_files that has not loaded yet holds the
	// first readiness back (the outbound check) and loads later without a restart,
	// so wait for it here rather than skip the object store: skipping would let the
	// replica turn ready without one. Any other outbound configuration error is
	// fixed for the life of the process and ends the start (CR2-8).
	for delay := time.Second; ; delay = min(delay*2, 15*time.Second) {
		err := out.TrustPending()
		if err == nil {
			break
		}
		a.log.Error("object store waits for the outbound trust store", "error", err, "retry_in", delay.String())
		if !sleep(ctx, delay) {
			return blobArea{}, ctx.Err()
		}
	}
	if err := out.Err(); err != nil {
		return blobArea{}, fmt.Errorf("object store: outbound configuration: %w", err)
	}
	client, err := s3.NewFromFactory(ctx, cfg.S3, out)
	if err != nil {
		return blobArea{}, fmt.Errorf("object store: %w", err)
	}
	bm := blob.NewMetrics(a.tele.metrics)
	log := a.log.With("component", "blob")
	objects := blob.NewGuard(client, cfg.S3.CircuitBreaker, blob.WithGuardMetrics(bm))
	store, err := blob.New(cfg, objects, db, blob.WithLogger(log), blob.WithMetrics(bm))
	if err != nil {
		return blobArea{}, err
	}
	if cfg.S3.StartupCheck {
		if err := blob.AwaitStartupCheck(ctx, objects, cfg.S3.Prefix, a.id.Instance, log); err != nil {
			if errors.Is(err, context.Canceled) {
				return blobArea{}, err
			}
			return blobArea{}, fmt.Errorf("object store startup check: %w", err)
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

	area := blobArea{store: store}
	if a.sub.JobLoops {
		area.gc = blob.NewCollector(cfg.GC, cfg.S3.Prefix, objects, db, blob.WithCollectorLogger(log), blob.WithCollectorMetrics(bm))
		a.gc.set(area.gc)
	}
	return area, nil
}
