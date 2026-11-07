package main

import (
	"context"
	"fmt"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/postrestore"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/journal/replay"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/internal/outbound"
)

const postRestoreUsage = "usage: trove admin post-restore --restored-to <RFC 3339 time> [--credentials-file <path>] [--skip-revocation-journal] [--json] [--config <file>]"

// postRestoreCmd is trove admin post-restore (R-HA-025 step 4): the clean-up after a platform (PITR) restore. Owned by the
// journal work package: it replaces newPostRestorer.
func postRestoreCmd(args []string, s streams) error {
	c, asJSON := newBackupFlags("admin post-restore", s)
	at := c.fs.String("restored-to", "", "t, the time the platform restore went to, as RFC 3339")
	creds := c.fs.String("credentials-file", "", "credentials that may list object versions, so a delete marker cannot hide a journal entry")
	skip := c.fs.Bool("skip-revocation-journal", false, "skip the journal replay, for the disaster in which the source bucket is gone")
	_, flags, names, err := parseBackup(c, args, 0)
	if err != nil {
		return backupExit(err)
	}
	t, perr := time.Parse(time.RFC3339, *at)
	if *at == "" || perr != nil {
		return usageError{postRestoreUsage}
	}
	env, err := openPostRestoreEnv(c, s, flags, names)
	if err != nil {
		return backupExit(err)
	}
	env.json = *asJSON
	pr, err := newPostRestorer(env)
	if err != nil {
		return backupExit(err)
	}
	ctx, stop := env.context()
	defer stop()
	rep, err := pr.PostRestore(ctx, backup.PostRestoreOptions{RestoredTo: t, CredentialsFile: *creds, SkipJournal: *skip})
	if err != nil {
		return backupExit(err)
	}
	if env.json {
		return env.printJSON(rep)
	}
	for _, step := range rep.Steps {
		if _, err := fmt.Fprintln(s.stdout, step); err != nil {
			return err
		}
	}
	return nil
}

// openPostRestoreEnv loads the configuration as an administration command: post-restore needs the database and the blob
// store, not backup.target, which a platform restore does not touch.
func openPostRestoreEnv(c *commonFlags, s streams, flags, names map[string]string) (*backupEnv, error) {
	loaded, err := c.loadConfig(s, config.CommandAdmin, flags, names)
	if err != nil {
		return nil, backup.Fail(backup.ExitUsage, backup.CodeConfig, err)
	}
	instance := instanceName(s.environ)
	log := observability.NewLogger(loaded.Config.Log, s.stderr, observability.WithIdentity(instance, "admin")).With("component", "admin")
	return &backupEnv{loaded: loaded, cfg: loaded.Config, log: log, instance: instance, stdout: s.stdout, stderr: s.stderr, environ: s.environ}, nil
}

// postRestoreRunner opens the database and the journal when it runs, because the constructor has no context.
type postRestoreRunner struct{ env *backupEnv }

// newPostRestorer builds the PostRestorer of `trove admin post-restore` (R-HA-025 step 4).
func newPostRestorer(env *backupEnv) (backup.PostRestorer, error) {
	return postRestoreRunner{env: env}, nil
}

// PostRestore implements backup.PostRestorer.
func (r postRestoreRunner) PostRestore(ctx context.Context, opts backup.PostRestoreOptions) (*backup.PostRestoreReport, error) {
	cfg := r.env.cfg
	db, err := postgres.Open(ctx, cfg.Database, r.env.instance, meta.RoleWeb, r.env.log)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rec, err := audit.New(audit.Options{DB: db, Replica: r.env.instance, Config: cfg.Audit, Log: r.env.log, AppLog: r.env.log})
	if err != nil {
		return nil, err
	}
	// The change log writes the rows of the areas the replay changed, so that a pod that is already running learns of them
	// instead of keeping its cached tokens and grants until it restarts (R-HA-025 step 4, audit finding H1). It does not
	// poll: the command holds no replica.
	changes, err := cluster.NewChanges(cluster.ChangesOptions{DB: db.ControlPool(), Config: cfg.Cluster, WatchdogTimeout: cfg.Health.WatchdogTimeout, Log: r.env.log})
	if err != nil {
		return nil, err
	}
	out := outbound.New(cfg.Proxy.Outbound)
	pr := postrestore.New(postrestore.Options{
		DB: db, Recorder: rec, Margin: cfg.Backup.RevocationJournal.ReplayMargin, Log: r.env.log,
		OpenReplayer: func(ctx context.Context, credentialsFile string) (journal.Replayer, error) {
			reader, err := replay.OpenJournal(ctx, cfg.Storage.S3, out, credentialsFile)
			if err != nil {
				return nil, err
			}
			return replay.New(replay.Options{DB: db, Reader: reader, Recorder: rec, Notifier: changes, AdminBinding: authz.HasAdminBinding, Log: r.env.log}), nil
		},
	})
	return pr.PostRestore(ctx, opts)
}
