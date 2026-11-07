package main

import (
	"fmt"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
)

// backupRestoreCmd is trove backup restore <id> (R-BAK-020, R-BAK-021). Owned by the restore work package: it replaces
// newRestorer.
func backupRestoreCmd(args []string, s streams) error {
	c, asJSON := newBackupFlags("restore", s)
	check := c.fs.Bool("check", false, "run the preconditions of R-BAK-020 and change nothing")
	skip := c.fs.Bool("skip-revocation-journal", false, "restore without replaying the revocation journal (R-BAK-024)")
	journalCreds := c.fs.String("journal-credentials-file", "", "credentials that read the source's revocation journal when the blob-store credentials cannot")
	identity := c.fs.String("identity-file", "", "age identity of a client-side encrypted backup (R-BAK-027)")
	pos, flags, names, err := parseBackup(c, args, 1)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError{backupUsage}
	}
	id, err := format.ParseID(pos[0])
	if err != nil {
		return usageError{err.Error()}
	}
	env, err := openBackupEnv(c, s, flags, names)
	if err != nil {
		return err
	}
	env.json = *asJSON
	restorer, err := newRestorer(env)
	if err != nil {
		return err
	}
	ctx, stop := env.context()
	defer stop()
	rep, err := restorer.Restore(ctx, backup.RestoreOptions{
		ID: id, Check: *check, SkipRevocationJournal: *skip, JournalCredentialsFile: *journalCreds, IdentityFile: *identity,
	})
	if err != nil {
		return err
	}
	if env.json {
		return env.printJSON(rep)
	}
	if rep.Checked {
		_, err = fmt.Fprintf(s.stdout, "restore of %s: every precondition holds\n", rep.BackupID)
		return err
	}
	_, err = fmt.Fprintf(s.stdout, "restored %s (snapshot %s), journal %s\n", rep.BackupID, rep.SnapshotAt.UTC().Format("2006-01-02T15:04:05Z"), rep.Journal)
	return err
}

// newRestorer builds the Restorer. The restore work package replaces the body.
func newRestorer(*backupEnv) (backup.Restorer, error) {
	return backup.NotBuilt{Operation: "restore"}, nil
}
