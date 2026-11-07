package main

import (
	"fmt"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/platform"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/outbound"
)

const backupCheckUsage = "usage: trove admin backup check [--json] [--config <file>]"

// adminBackupCmd dispatches trove admin backup <command>.
func adminBackupCmd(args []string, s streams) error {
	if len(args) >= 1 && args[0] == "check" {
		return backupCheckCmd(args[1:], s)
	}
	return usageError{backupCheckUsage}
}

// backupCheckCmd is trove admin backup check (R-HA-023): it runs the checks of the platform protection that the operator
// declared (backup.recovery_window, backup.object_versioning) and prints one line per check. It changes nothing and does not
// store the result; the task backup-check does. It exits 5 when a declared protection does not hold.
func backupCheckCmd(args []string, s streams) error {
	c, asJSON := newBackupFlags("admin backup check", s)
	_, flags, names, err := parseBackup(c, args, 0)
	if err != nil {
		return backupExit(err)
	}
	env, err := openPostRestoreEnv(c, s, flags, names)
	if err != nil {
		return backupExit(err)
	}
	env.json = *asJSON
	ctx, stop := env.context()
	defer stop()
	cfg := env.cfg
	db, err := postgres.Open(ctx, cfg.Database, env.instance, meta.RoleWeb, env.log)
	if err != nil {
		return backupExit(err)
	}
	defer db.Close()
	client, err := s3.NewFromFactory(ctx, cfg.Storage.S3, outbound.New(cfg.Proxy.Outbound))
	if err != nil {
		return backupExit(err)
	}
	var rep platform.Report
	err = db.Read(ctx, func(c q.DBTX) error {
		var rerr error
		rep, rerr = platform.Checker{
			ArchiveMode: platform.ArchiveModeOf(c), Bucket: client,
			Settings: platform.Settings{RecoveryWindow: cfg.Backup.RecoveryWindow, ObjectVersioning: cfg.Backup.ObjectVersioning, GracePeriod: cfg.Storage.GC.GracePeriod},
		}.Run(ctx)
		return rerr
	})
	if err != nil {
		return backupExit(err)
	}
	failed := 0
	if env.json {
		if err := env.printJSON(rep); err != nil {
			return err
		}
	} else {
		if len(rep.Results) == 0 {
			if _, err := fmt.Fprintln(s.stdout, "nothing is declared: backup.recovery_window and backup.object_versioning are at their defaults"); err != nil {
				return err
			}
		}
		for _, r := range rep.Results {
			if _, err := fmt.Fprintf(s.stdout, "%-18s %-8s %s\n", r.Check, r.Status, r.Detail); err != nil {
				return err
			}
		}
	}
	for _, r := range rep.Results {
		if r.Status == platform.StatusFailed {
			failed++
		}
	}
	if failed > 0 {
		return backup.Fail(backup.ExitFindings, backup.CodeFindings, fmt.Errorf("%d declared protections do not hold", failed))
	}
	return nil
}
