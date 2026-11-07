package main

import (
	"fmt"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
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
	env, err := openBackupEnv(c, s, flags, names)
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

// newPostRestorer builds the PostRestorer. The journal work package replaces the body.
func newPostRestorer(*backupEnv) (backup.PostRestorer, error) {
	return backup.NotBuilt{Operation: "post-restore"}, nil
}
