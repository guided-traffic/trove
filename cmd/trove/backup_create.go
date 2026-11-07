package main

import (
	"fmt"

	"github.com/guided-traffic/trove/internal/backup"
)

// backupCreateCmd is trove backup create (R-BAK-009). Owned by the create work package: it replaces newCreator.
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

// newCreator builds the Creator of a backup run. The create work package replaces the body.
func newCreator(*backupEnv) (backup.Creator, error) { return backup.NotBuilt{Operation: "create"}, nil }
