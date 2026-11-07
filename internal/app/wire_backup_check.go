package app

import (
	"context"
	"errors"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/platform"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The wiring of the platform backup check (R-HA-023, R-OPS-019): the task backup-check verifies the declared point-in-time
// recovery and bucket versioning once a day and stores the report in system_state; every replica exports the report as
// trove_backup_check_ok (registerBackupGauges). Only a replica that claims jobs needs the bucket client, which wireBlob
// hands to wireBackupCheck.

// KindBackupCheck is the job kind and task of the check.
const KindBackupCheck = "backup-check"

const defaultBackupCheckSchedule = "40 5 * * *"

// backupCheckTimeout bounds one run: two reads of the bucket's settings and one statement.
const backupCheckTimeout = 2 * time.Minute

// wireBackupCheck makes the bucket client available to the job handler (a late value, because the registry is frozen first).
func (a *App) wireBackupCheck(client *s3.Client) { a.checkBucket.set(client) }

// registerBackupCheckJob registers the kind and its task. The report is stored whatever it finds: a failed check is a result,
// not a failed job; only a run that could not read what it checks fails and is retried.
func (a *App) registerBackupCheckJob(r *jobs.Registry) {
	b := a.cfg.Backup
	settings := platform.Settings{RecoveryWindow: b.RecoveryWindow, ObjectVersioning: b.ObjectVersioning, GracePeriod: a.cfg.Storage.GC.GracePeriod}
	r.RegisterKind(KindBackupCheck, jobs.KindSpec{
		Queue: "maintenance", DedupMode: jobs.Exclusive, MaxAttempts: 3, Timeout: backupCheckTimeout,
		Handler: func(ctx context.Context, run *jobs.Run) error {
			bucket, ok := a.checkBucket.get()
			if !ok {
				return errors.New("platform backup check: this replica has not wired it yet")
			}
			rep, err := platform.Checker{ArchiveMode: platform.ArchiveModeOf(run.DB()), Bucket: bucket, Settings: settings}.Run(ctx)
			if err != nil {
				return err
			}
			if err := platform.Store(ctx, run.DB(), rep); err != nil {
				return err
			}
			return run.SetResult(rep)
		},
	})
	spec := jobs.TaskSpec{Schedule: defaultBackupCheckSchedule}
	if t, ok := a.cfg.Maintenance.Tasks[KindBackupCheck]; ok && t.Schedule != "" {
		spec.Schedule, spec.Disabled = t.Schedule, !t.Enabled
	}
	r.RegisterTask(KindBackupCheck, spec)
}

// exportBackupCheck sets trove_backup_check_ok from the stored report. A replica without a report exports no series.
func exportBackupCheck(ctx context.Context, db q.DBTX, m *backup.Metrics) error {
	rep, ok, err := platform.Load(ctx, db)
	if err != nil {
		return err
	}
	if !ok {
		rep = platform.Report{}
	}
	platform.Export(m.CheckOK, rep)
	return nil
}
