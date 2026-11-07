package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os/signal"
	"strings"
	"syscall"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/observability"
)

// The `backup` commands (R-ARCH-001, docs/spec/backup-and-restore.md). This file is the shared part: the dispatcher, the
// environment every command opens, the exit-code mapping of R-BAK-013 and the output helpers. Each command has a file of its
// own that parses its flags and builds its runner:
//
//	backup_create.go    create                 -> newCreator
//	backup_list.go      list                   -> newLister
//	backup_verify.go    verify                 -> newVerifier
//	backup_delete.go    delete, prune          -> newDeleter, newPruner
//	backup_restore.go   restore                -> newRestorer
//	admin_postrestore.go  admin post-restore   -> newPostRestorer
//
// The work package that builds an operation replaces the body of its constructor, which returns [backup.NotBuilt] until then;
// the flags, the exit codes and the output stay as they are here.
const backupUsage = `usage: trove backup <command> [flags]

commands:
  create [--trigger schedule|manual] [--exclude-remote-caches] [--no-prune] [--adopt-target] [--json]
                                 write one backup to the configured target (R-BAK-009)
  list [--all] [--json]          list the backups of the target (R-BAK-018)
  verify <id> | --latest | --all [--deep] [--bytes-per-second N] [--json]
                                 check backups without changing them (R-BAK-019)
  delete <id> [--yes] [--allow-last]   delete one backup, then sweep the pool (R-BAK-016)
  prune                          apply the retention policy and sweep the pool (R-BAK-016, R-BAK-017)
  restore <id> [--check] [--skip-revocation-journal] [--journal-credentials-file <path>]
                                 restore a backup into an empty installation (R-BAK-020, R-BAK-021)

exit codes: 0 ok, 1 failed, 2 usage or configuration, 3 another run is active, 4 foreign target,
5 verify findings, 6 precondition not met, 75 temporary failure (retry)
every command takes --config <file> and --log-level <level>`

// backupCmd dispatches trove backup <command>.
func backupCmd(args []string, s streams) error {
	if len(args) == 0 {
		return usageError{backupUsage}
	}
	var err error
	switch args[0] {
	case "create":
		err = backupCreateCmd(args[1:], s)
	case "list":
		err = backupListCmd(args[1:], s)
	case "verify":
		err = backupVerifyCmd(args[1:], s)
	case "delete":
		err = backupDeleteCmd(args[1:], s)
	case "prune":
		err = backupPruneCmd(args[1:], s)
	case "restore":
		err = backupRestoreCmd(args[1:], s)
	case "help", "-h", "--help":
		_, err = fmt.Fprintln(s.stdout, backupUsage)
	default:
		return usageError{backupUsage}
	}
	return backupExit(err)
}

// backupExit maps an error of a backup command to the exit code of R-BAK-013: the code travels with the error to run, which
// prints the message and exits with it. A usage error and a help request pass through untouched.
func backupExit(err error) error {
	var ue usageError
	if err == nil || errors.As(err, &ue) || errors.Is(err, flag.ErrHelp) {
		return err
	}
	var ee *backup.ExitError
	if errors.As(err, &ee) {
		return err
	}
	return backup.Fail(backup.ExitCode(err), backup.ErrorCode(err), err)
}

// backupEnv is what every backup command works with once its configuration is loaded.
type backupEnv struct {
	loaded   *config.Loaded
	cfg      *config.Config
	log      *slog.Logger
	instance string
	stdout   io.Writer
	stderr   io.Writer
	environ  []string
	json     bool
}

// openBackupEnv loads the configuration for a backup command. A configuration error is exit 2 and names the key
// (R-BAK-011); the backup commands need the database, the backup keys and the blob store, never a role or Valkey.
func openBackupEnv(c *commonFlags, s streams, flags, names map[string]string) (*backupEnv, error) {
	loaded, err := c.loadConfig(s, config.CommandBackup, flags, names)
	if err != nil {
		return nil, backup.Fail(backup.ExitUsage, backup.CodeConfig, err)
	}
	instance := instanceName(s.environ)
	log := observability.NewLogger(loaded.Config.Log, s.stderr, observability.WithIdentity(instance, "backup")).With("component", "backup")
	return &backupEnv{loaded: loaded, cfg: loaded.Config, log: log, instance: instance, stdout: s.stdout, stderr: s.stderr, environ: s.environ}, nil
}

// context returns the context of a run: a SIGTERM (a drain, an eviction, a deleted pod) or SIGINT cancels it, and the run
// ends itself within the grace period (R-BAK-012).
func (e *backupEnv) context() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}

// printJSON writes v as one JSON document on standard output.
func (e *backupEnv) printJSON(v any) error {
	enc := json.NewEncoder(e.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// newBackupFlags returns the flag set every backup command starts from, with --json.
func newBackupFlags(name string, s streams) (*commonFlags, *bool) {
	c := newFlags("backup "+name, s)
	asJSON := c.fs.Bool("json", false, "print the result as JSON on standard output")
	return c, asJSON
}

// parseBackup parses args with the common flags. up to n positional arguments (an ID) may stand before or after the flags,
// which the standard flag package alone would not allow; more are a usage error.
func parseBackup(c *commonFlags, args []string, n int) (pos []string, flags, names map[string]string, err error) {
	for len(args) > 0 && !strings.HasPrefix(args[0], "-") && len(pos) < n {
		pos, args = append(pos, args[0]), args[1:]
	}
	flags, names, err = c.parse(args, map[string]string{"log-level": "log.level"})
	if err != nil {
		return nil, nil, nil, err
	}
	for _, a := range c.fs.Args() {
		if len(pos) >= n {
			return nil, nil, nil, usageError{backupUsage}
		}
		pos = append(pos, a)
	}
	return pos, flags, names, nil
}
