package main

import (
	"fmt"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/maintenance"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

const maintenanceUsage = `usage: trove admin maintenance pause-deletions --reason <text> [--json] [--config <file>]
       trove admin maintenance resume-deletions [--json] [--config <file>]

pause-deletions   set the deletion pause: garbage collection, the orphan sweep, retention and cap enforcement delete nothing until it is cleared, for example before a planned restore drill (R-HA-025)
resume-deletions  clear the deletion pause that a post-restore or pause-deletions set, after the restored installation is verified (R-HA-025 step 7)

exit codes: 0 done (also when the pause was already in the state asked for), 1 failed, 2 usage or configuration, 75 temporary (the database could not be reached)`

// maintenanceCmd dispatches trove admin maintenance <command>.
func maintenanceCmd(args []string, s streams) error {
	if len(args) >= 1 {
		switch args[0] {
		case "pause-deletions":
			return deletionsCmd(args[1:], s, true)
		case "resume-deletions":
			return deletionsCmd(args[1:], s, false)
		}
	}
	return usageError{maintenanceUsage}
}

// deletionsCmd is trove admin maintenance pause-deletions and resume-deletions (R-HA-025): it sets or clears the deletion pause
// in system_state with the runtime database role and writes one audit event when the state changed. Repeating a command is safe
// and says that nothing changed.
func deletionsCmd(args []string, s streams, pause bool) error {
	name := "admin maintenance resume-deletions"
	if pause {
		name = "admin maintenance pause-deletions"
	}
	c, asJSON := newBackupFlags(name, s)
	var reason *string
	if pause {
		reason = c.fs.String("reason", "", "why deletions are paused; recorded with the pause")
	}
	_, flags, names, err := parseBackup(c, args, 0)
	if err != nil {
		return backupExit(err)
	}
	if pause && *reason == "" {
		return usageError{maintenanceUsage}
	}
	env, err := openPostRestoreEnv(c, s, flags, names)
	if err != nil {
		return backupExit(err)
	}
	env.json = *asJSON
	ctx, stop := env.context()
	defer stop()
	db, err := postgres.Open(ctx, env.cfg.Database, env.instance, meta.RoleWeb, env.log)
	if err != nil {
		if postgres.IsUnavailable(err) {
			return backup.Fail(backup.ExitTemporary, backup.CodeTemporary, err)
		}
		return backupExit(err)
	}
	defer db.Close()
	rec, err := audit.New(audit.Options{DB: db, Replica: env.instance, Config: env.cfg.Audit, Log: env.log, AppLog: env.log})
	if err != nil {
		return backupExit(err)
	}
	d := maintenance.New(maintenance.Options{DB: db, Recorder: rec})
	var st maintenance.Pause
	if pause {
		st, err = d.Pause(ctx, *reason)
	} else {
		st, err = d.Resume(ctx)
	}
	if err != nil {
		if postgres.IsUnavailable(err) {
			return backup.Fail(backup.ExitTemporary, backup.CodeTemporary, err)
		}
		return backupExit(err)
	}
	if env.json {
		return env.printJSON(st)
	}
	var line string
	switch {
	case pause && st.Changed:
		line = fmt.Sprintf("deletion pause set (%s): garbage collection, the orphan sweep, retention and cap enforcement delete nothing until `trove admin maintenance resume-deletions`", st.Reason)
	case pause:
		line = fmt.Sprintf("deletion pause was already set since %s (%s); left as it is", st.At, st.Reason)
	case st.Changed:
		line = fmt.Sprintf("deletion pause cleared (it was set at %s: %s): deletions resume with the next run of the collector and the sweep", st.At, st.Reason)
	default:
		line = "deletion pause was not set; nothing changed"
	}
	_, err = fmt.Fprintln(s.stdout, line)
	return err
}
