package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/retention"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/buildinfo"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/outbound"
)

// The wiring of the target-side operations (list, verify, delete, prune): the target client, and for the operations that
// write run rows the database, the run store and the audit writer. The connections close with the process; a command is one
// short-lived process.

// openTarget builds the target client from backup.target.
func openTarget(ctx context.Context, env *backupEnv) (*s3.Target, format.Layout, error) {
	t, err := s3.NewTarget(ctx, env.cfg.Backup, outbound.New(env.cfg.Proxy.Outbound))
	if err != nil {
		return nil, format.Layout{}, backup.Fail(backup.ExitUsage, backup.CodeConfig, err)
	}
	return t, format.Layout{Prefix: env.cfg.Backup.Target.Prefix}, nil
}

func targetURL(env *backupEnv) string {
	t := env.cfg.Backup.Target
	return "s3://" + t.Bucket + "/" + strings.Trim(t.Prefix, "/")
}

func backupTiming(env *backupEnv) backup.Timing {
	b := env.cfg.Backup
	return backup.Timing{Lease: b.LeaseDuration, Heartbeat: b.HeartbeatInterval, Stall: b.StallTimeout, Finalize: b.FinalizeTimeout}
}

// openRunDB opens the database in the backup role. A database that cannot be reached is a temporary failure (exit 75).
func openRunDB(ctx context.Context, env *backupEnv) (*postgres.DB, error) {
	db, err := postgres.Open(ctx, env.cfg.Database, env.instance, meta.RoleBackup, env.log)
	if err != nil {
		if postgres.IsUnavailable(err) {
			return nil, backup.Fail(backup.ExitTemporary, backup.CodeTemporary, err)
		}
		return nil, backup.Fail(backup.ExitUsage, backup.CodeConfig, err)
	}
	return db, nil
}

// newRetentionRunner builds the runner of delete and prune.
func newRetentionRunner(env *backupEnv) (*retention.Runner, error) {
	ctx, stop := env.context()
	defer stop()
	t, layout, err := openTarget(ctx, env)
	if err != nil {
		return nil, err
	}
	db, err := openRunDB(ctx, env)
	if err != nil {
		return nil, err
	}
	store := backup.NewPGStore(db)
	b := env.cfg.Backup
	stage := &retention.Stage{
		Store: t, Layout: layout, Policy: retention.Policy{KeepLast: b.Retention.KeepLast, KeepWithin: b.Retention.KeepWithin},
		SweepInterval: b.Retention.SweepInterval, DeleteTimeout: b.FinalizeTimeout, Facts: backup.NewPGFacts(db, env.cfg.Database.Schema), Audit: backup.NewPGEvents(db, env.instance, env.log),
		Clock: retention.RunSweepClock{Runs: store}, Log: env.log,
	}
	return &retention.Runner{
		Stage: stage, Runs: store, Timing: backupTiming(env), RunHistory: b.RunHistory, Target: targetURL(env),
		Pod: env.instance, TroveVersion: buildinfo.Get().Version, NewRunID: principal.NewID,
	}, nil
}

func noConfirmation() error {
	return backup.Failf(backup.ExitUsage, backup.CodeConfig, "refusing to delete without confirmation: pass --yes")
}

// confirm asks on standard error and reads the answer from standard input. Anything but y or yes declines.
func confirm(s streams, prompt string) error {
	if s.stdin == nil {
		return noConfirmation()
	}
	fmt.Fprintf(s.stderr, "%s [y/N] ", prompt)
	var answer string
	done := make(chan struct{})
	go func() { _, _ = fmt.Fscanln(s.stdin, &answer); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Minute):
	}
	if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
		return backup.Failf(backup.ExitUsage, backup.CodeConfig, "not confirmed: nothing was deleted")
	}
	return nil
}
