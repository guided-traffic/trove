package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/gauges"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/observability"
)

// The wiring of what a replica shows of the backup job (R-BAK-014, R-API-050, R-OPS-004, R-OPS-007): the gauges of the job and
// of the revocation journal, read from PostgreSQL in the background, and the sources of the read-only API.

// backupMetrics holds the families of the lane on this replica's registry, registered once (the registry panics on a second
// registration of a name).
type backupMetrics struct {
	once sync.Once
	bak  *backup.Metrics
	jrnl *journal.Metrics
}

func (a *App) laneMetrics() *backupMetrics {
	a.backupMx.once.Do(func() {
		a.backupMx.bak = backup.NewMetrics(a.tele.metrics)
		a.backupMx.jrnl = journal.NewMetrics(a.tele.metrics)
	})
	return &a.backupMx
}

// registerBackupGauges registers the families and adds the source that sets them. The source has no descriptors of its own: the
// values live in the families, and a scrape reads memory. The evaluation runs once per metrics.db_gauge_interval and reuses
// its result for 30 seconds (gauges.Reuse), so a replica adds one evaluation per interval to the database, whatever the
// scrape rate.
func (a *App) registerBackupGauges() {
	m := a.laneMetrics()
	expected := a.cfg.Backup.ExpectedInterval
	m.bak.ExpectedInterval.Set(expected.Seconds())
	var col *gauges.Collector
	a.tele.metrics.DBGauges().Add(observability.GaugeSource{
		Name: "backup",
		Collect: func(ctx context.Context) ([]prometheus.Metric, error) {
			if col == nil {
				db := a.database()
				if db == nil {
					return nil, nil
				}
				col = gauges.New(gauges.Options{Reader: backup.NewPGStore(db), Backup: m.bak, Journal: m.jrnl, ExpectedInterval: expected})
			}
			return nil, col.Refresh(ctx)
		},
	})
}

// backupAPI is the sources of the backup operations and of the backup block of the system information. A replica without a
// database answers them with 503 and null.
func (a *App) backupAPI() v1.BackupServices {
	b := a.cfg.Backup
	out := v1.BackupServices{ExpectedInterval: b.ExpectedInterval, Namespace: b.ManualRun.Namespace, CronJob: b.ManualRun.CronJob}
	if db := a.database(); db != nil {
		out.Runs = backup.NewPGStore(db)
		out.RestoredFrom = restoredFrom(db)
	}
	return out
}

// restoredFrom reads system_state restored_from, the origin a restore recorded (R-BAK-022); nil for an installation that was
// never restored.
func restoredFrom(db *postgres.DB) func(context.Context) (*gen.BackupRestoredFrom, error) {
	return func(ctx context.Context) (*gen.BackupRestoredFrom, error) {
		var out *gen.BackupRestoredFrom
		err := db.Read(ctx, func(c q.DBTX) error {
			out = nil
			row, err := q.New(c).GetSystemState(ctx, "restored_from")
			if postgres.IsNoRows(err) {
				return nil //nolint:nilerr // no row is an installation that was never restored
			}
			if err != nil {
				return err
			}
			var v gen.BackupRestoredFrom
			if err := json.Unmarshal(row.Value, &v); err != nil {
				return fmt.Errorf("system_state restored_from is not the object of R-BAK-022: %w", err)
			}
			if v.BackupId != "" {
				out = &v
			}
			return nil
		})
		return out, err
	}
}
