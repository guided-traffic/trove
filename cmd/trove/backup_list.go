package main

import (
	"fmt"
	"strings"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/listing"
)

// encryptionText is the encryption column of the listing: the server-side mode, "none" included (R-BAK-026), and the
// client-side layer when there is one (R-BAK-027).
func encryptionText(e *format.Encryption) string {
	if e == nil {
		return "unknown"
	}
	out := e.SSE
	if e.ClientSide != nil {
		out += "+" + *e.ClientSide
	}
	return out
}

func remoteCachesText(o *format.Options) string {
	if o != nil && o.ExcludeRemoteCaches {
		return "  excludes-remote-caches"
	}
	return ""
}

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
		if !l.Complete {
			if _, err := fmt.Fprintf(s.stdout, "%s  %s\n", l.BackupID, state); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(s.stdout, "%s  %s  %s  trove %s  step %d  db %d B  blobs %d (%d B)  copied %d  damaged %t  keys %s  incarnation %s  encryption %s%s\n",
			l.BackupID, state, l.SnapshotAt.UTC().Format("2006-01-02T15:04:05Z"), l.TroveVersion, l.SchemaStep, l.DatabaseBytes, l.BlobCount, l.BlobBytes,
			l.BlobsCopied, l.Damaged, strings.Join(l.KeyIDs, ","), l.Incarnation, encryptionText(l.Encryption), remoteCachesText(l.Options)); err != nil {
			return err
		}
	}
	return nil
}

// newLister builds the Lister over the target.
func newLister(env *backupEnv) (backup.Lister, error) {
	ctx, stop := env.context()
	defer stop()
	t, layout, err := openTarget(ctx, env)
	if err != nil {
		return nil, err
	}
	return &listing.Lister{Store: t, Layout: layout}, nil
}
