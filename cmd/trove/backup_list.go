package main

import (
	"fmt"

	"github.com/guided-traffic/trove/internal/backup"
)

// backupListCmd is trove backup list (R-BAK-018). Owned by the target work package: it replaces newLister.
func backupListCmd(args []string, s streams) error {
	c, asJSON := newBackupFlags("list", s)
	all := c.fs.Bool("all", false, "include incomplete backup directories, which have no manifest")
	_, flags, names, err := parseBackup(c, args, 0)
	if err != nil {
		return err
	}
	env, err := openBackupEnv(c, s, flags, names)
	if err != nil {
		return err
	}
	env.json = *asJSON
	lister, err := newLister(env)
	if err != nil {
		return err
	}
	ctx, stop := env.context()
	defer stop()
	list, err := lister.List(ctx, backup.ListOptions{All: *all})
	if err != nil {
		return err
	}
	if env.json {
		if list == nil {
			list = []backup.Listing{}
		}
		return env.printJSON(list)
	}
	for _, l := range list {
		state := "complete"
		if !l.Complete {
			state = "incomplete"
		}
		if _, err := fmt.Fprintf(s.stdout, "%s  %s  %s  trove %s  step %d  db %d B  blobs %d (%d B)  copied %d  damaged %t\n",
			l.BackupID, state, l.SnapshotAt.UTC().Format("2006-01-02T15:04:05Z"), l.TroveVersion, l.SchemaStep, l.DatabaseBytes, l.BlobCount, l.BlobBytes, l.BlobsCopied, l.Damaged); err != nil {
			return err
		}
	}
	return nil
}

// newLister builds the Lister. The target work package replaces the body.
func newLister(*backupEnv) (backup.Lister, error) { return backup.NotBuilt{Operation: "list"}, nil }
