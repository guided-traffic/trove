package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/create"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/retention"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/buildinfo"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/internal/outbound"
)

// backupCreateCmd is trove backup create (R-BAK-009).
func backupCreateCmd(args []string, s streams) error {
	c, asJSON := newBackupFlags("create", s)
	trigger := c.fs.String("trigger", string(backup.TriggerManual), "who started the run: schedule or manual; the chart's CronJob passes schedule")
	exclude := c.fs.Bool("exclude-remote-caches", false, "leave out blobs that only remote-repository caches reference (R-BAK-008)")
	noPrune := c.fs.Bool("no-prune", false, "skip the retention and pool sweep of the pruning phase")
	adopt := c.fs.Bool("adopt-target", false, "adopt a target that names the installation this one was restored from (R-BAK-002)")
	_, flags, names, err := parseBackup(c, args, 0)
	if err != nil {
		return err
	}
	if *trigger != string(backup.TriggerSchedule) && *trigger != string(backup.TriggerManual) {
		return usageError{backupUsage}
	}
	env, err := openBackupEnv(c, s, flags, names)
	if err != nil {
		return err
	}
	env.json = *asJSON
	creator, err := newCreator(env)
	if err != nil {
		return err
	}
	ctx, stop := env.context()
	defer stop()
	res, err := creator.Create(ctx, backup.CreateOptions{
		Trigger: backup.Trigger(*trigger), ExcludeRemoteCaches: *exclude, NoPrune: *noPrune, AdoptTarget: *adopt,
	})
	if err != nil {
		return err
	}
	if env.json {
		return env.printJSON(res)
	}
	_, err = fmt.Fprintf(s.stdout, "backup %s %s: %d blobs copied (%d bytes), %d damaged\n", res.BackupID, res.Outcome, res.BlobsCopied, res.BytesCopied, res.DamagedBlobs)
	return err
}

// newCreator builds the Creator of a backup run. The connections open when the run starts, with the run's context, so a
// SIGTERM during the connect ends the command.
func newCreator(e *backupEnv) (backup.Creator, error) { return &commandCreator{env: e}, nil }

// commandCreator opens the database, the blob store and the target of one run and runs the create pipeline on them.
type commandCreator struct{ env *backupEnv }

// Create implements backup.Creator.
func (c *commandCreator) Create(ctx context.Context, opts backup.CreateOptions) (*backup.Result, error) {
	e := c.env
	cfg := e.cfg
	db, err := postgres.Open(ctx, cfg.Database, e.instance, meta.RoleBackup, e.log)
	if err != nil {
		return nil, mapOpenError(err)
	}
	defer db.Close()
	factory := outbound.New(cfg.Proxy.Outbound)
	blobs, err := s3.NewFromFactory(ctx, cfg.Storage.S3, factory)
	if err != nil {
		return nil, backup.Fail(backup.ExitUsage, backup.CodeConfig, fmt.Errorf("storage.s3: %w", err))
	}
	tgt, layout, err := openTarget(ctx, e)
	if err != nil {
		return nil, err
	}
	recorder, err := audit.New(audit.Options{DB: db, Replica: e.instance, Config: cfg.Audit, Log: e.log, AppLog: e.log})
	if err != nil {
		return nil, err
	}
	metrics := observability.NewMetrics(meta.RoleBackup)
	stopOps := serveBackupMetrics(cfg.Server.OpsListen, metrics, e.log)
	defer stopOps()
	facts := backup.NewPGFacts(db, cfg.Database.Schema)
	store := backup.NewPGStore(db)
	b := cfg.Backup
	stage := &retention.Stage{
		Store: tgt, Layout: layout, Policy: retention.Policy{KeepLast: b.Retention.KeepLast, KeepWithin: b.Retention.KeepWithin},
		SweepInterval: b.Retention.SweepInterval, Facts: facts, Audit: auditDeleted{db: db, rec: recorder},
		Clock: retention.RunSweepClock{Runs: store}, Log: e.log,
	}
	info := buildinfo.Get()
	endpoint := config.NormalizeEndpoint(cfg.Storage.S3.Endpoint)
	pipeline, err := create.New(create.Deps{
		Cfg:       cfg.Backup,
		SourceRef: format.StorageRef{Endpoint: endpoint, Bucket: cfg.Storage.S3.Bucket, Prefix: cfg.Storage.S3.Prefix},
		Instance:  e.instance, JobName: jobName(e.environ), Version: info.Version, Revision: info.Commit, BackupRole: cfg.Database.BackupRole,
		Store: store, Prune: stage, Facts: facts, Grants: facts, Pins: facts, BlobState: facts,
		Snapshot: backup.NewPGSnapshotter(db),
		Source:   backup.NewStoreSource(blobs, cfg.Storage.S3.Prefix, endpoint, cfg.Storage.S3.Bucket),
		Target:   tgt, Events: create.TxEvents{DB: db, Recorder: recorder},
		Metrics: backup.NewMetrics(metrics), Log: e.log,
	})
	if err != nil {
		return nil, err
	}
	return pipeline.Create(ctx, opts)
}

// mapOpenError gives a failure to open the database the exit code of R-BAK-013: a standby is a usage error, an unreachable
// database is temporary.
func mapOpenError(err error) error {
	switch {
	case errors.Is(err, postgres.ErrBackupStandby):
		return backup.Fail(backup.ExitUsage, backup.CodeStandby, err)
	case postgres.IsState(err, "42501"):
		return backup.Failf(backup.ExitUsage, backup.CodeBackupRoleGrant,
			"the database role of the backup lacks privileges (%v); trove migrate grants them to database.backup_role (R-BAK-015)", err)
	case postgres.IsUnavailable(err):
		return backup.Fail(backup.ExitTemporary, backup.CodeTemporary, err)
	}
	return err
}

// serveBackupMetrics exports the counters of the run on the operations port for as long as it lasts, for clusters that scrape
// Jobs (R-BAK-014). A port that cannot be bound is logged and the run goes on: the counters are not the backup.
func serveBackupMetrics(addr string, m *observability.Metrics, log *slog.Logger) func() {
	if addr == "" {
		return func() {}
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", addr)
	if err != nil {
		log.Warn("the backup run does not export metrics", "addr", addr, "err", err)
		return func() {}
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", m.Handler())
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return func() { _ = srv.Close() }
}

// jobName is the Kubernetes Job the pod belongs to: the chart sets TROVE_JOB_NAME from the pod's job-name label. Empty outside a
// Job (R-BAK-010).
func jobName(environ []string) string {
	for _, kv := range environ {
		if v, ok := strings.CutPrefix(kv, "TROVE_JOB_NAME="); ok {
			return v
		}
	}
	return ""
}
