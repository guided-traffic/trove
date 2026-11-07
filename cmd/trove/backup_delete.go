package main

import (
	"fmt"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
)

// backupDeleteCmd is trove backup delete <id> (R-BAK-016). Owned by the target work package: it replaces newDeleter.
func backupDeleteCmd(args []string, s streams) error {
	c, asJSON := newBackupFlags("delete", s)
	yes := c.fs.Bool("yes", false, "do not ask for confirmation")
	allowLast := c.fs.Bool("allow-last", false, "allow deleting the only complete backup")
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
	if !*yes {
		if err := confirm(s, "Delete backup "+string(id)+" and sweep the pool?"); err != nil {
			return err
		}
	}
	deleter, err := newDeleter(env)
	if err != nil {
		return err
	}
	ctx, stop := env.context()
	defer stop()
	res, err := deleter.Delete(ctx, backup.DeleteOptions{ID: id, Yes: *yes, AllowLast: *allowLast})
	if err != nil {
		return err
	}
	return printPruneResult(env, res)
}

// backupPruneCmd is trove backup prune (R-BAK-016, R-BAK-017). Owned by the target work package: it replaces newPruner.
func backupPruneCmd(args []string, s streams) error {
	c, asJSON := newBackupFlags("prune", s)
	_, flags, names, err := parseBackup(c, args, 0)
	if err != nil {
		return err
	}
	env, err := openBackupEnv(c, s, flags, names)
	if err != nil {
		return err
	}
	env.json = *asJSON
	pruner, err := newPruner(env)
	if err != nil {
		return err
	}
	ctx, stop := env.context()
	defer stop()
	res, err := pruner.Prune(ctx)
	if err != nil {
		return err
	}
	return printPruneResult(env, res)
}

func printPruneResult(env *backupEnv, res *backup.Result) error {
	if env.json {
		return env.printJSON(res)
	}
	_, err := fmt.Fprintf(env.stdout, "%d backups deleted, %d incomplete directories deleted, %d pool objects deleted (%d bytes)\n",
		len(res.BackupsDeleted), len(res.IncompleteDeleted), res.PoolObjectsDeleted, res.PoolBytesReclaimed)
	return err
}

// newDeleter builds the Deleter.
func newDeleter(env *backupEnv) (backup.Deleter, error) { return newRetentionRunner(env) }

// newPruner builds the Pruner.
func newPruner(env *backupEnv) (backup.Pruner, error) {
	r, err := newRetentionRunner(env)
	if err != nil {
		return nil, err
	}
	return r.Pruner(), nil
}
