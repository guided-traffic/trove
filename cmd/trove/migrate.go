package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/guided-traffic/trove/internal/buildinfo"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/observability"
)

// migrateCmd applies or lists the embedded migrations (R-ARCH-001, R-ARCH-026,
// R-HA-019).
//
// Exit status: 0 on success, also when nothing was pending; 1 on an error, which
// rolled the running step back; 2 on a usage error; 3 on a refusal that retrying
// cannot change (the contract guard, a half-restored database), which the Helm
// Job maps to FailJob. `up` prints one JSON summary on stdout; the progress log
// is JSON on stderr.
const migrateUsage = "usage: trove migrate [up|status] [--config <file>]"

func migrateCmd(args []string, s streams) error {
	c := newFlags("migrate", s)
	action := "up"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action, args = args[0], args[1:]
	}
	if action != "up" && action != "status" {
		return usageError{migrateUsage}
	}
	flags, names, err := c.parse(args, map[string]string{"log-level": "log.level"})
	if err != nil {
		return err
	}
	if c.fs.NArg() > 0 {
		return usageError{migrateUsage}
	}
	loaded, err := c.loadConfig(s, config.CommandMigrate, flags, names)
	if err != nil {
		return err
	}
	cfg := loaded.Config

	// The logger redacts secrets and honours log.level like every other process (R-OPS-013).
	log := observability.NewLogger(cfg.Log, s.stderr, observability.WithIdentity(instanceName(s.environ), "migrate")).With("component", "migrate")

	// An eviction or Ctrl-C ends the session, which rolls the open step back and
	// releases the migration lock.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	opts := postgres.MigrateOptions{
		Instance:          instanceName(s.environ),
		Migrate:           cfg.Migrate,
		Version:           buildinfo.Get().Version,
		ReplicaStaleAfter: cfg.System.HeartbeatStaleAfter,
		Log:               log,
	}
	switch action {
	case "status":
		rep, err := postgres.Status(ctx, cfg.Database, opts)
		if err != nil {
			return err
		}
		return printJSON(s, rep)
	default:
		res, err := postgres.Migrate(ctx, cfg.Database, opts)
		if err != nil {
			return refusalExit(err)
		}
		log.Info("migrate finished", "applied", len(res.Applied), "schema_applied", res.SchemaApplied,
			"compat_floor", res.CompatFloor, "nothing_to_do", res.NothingToDo, "waited_for_lock", res.WaitedForLock)
		return printJSON(s, res)
	}
}

func printJSON(s streams, v any) error {
	enc := json.NewEncoder(s.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// instanceName is the pod name (TROVE_POD_NAME), else hostname-pid, the name the
// connections carry in application_name (R-ARCH-019).
func instanceName(environ []string) string {
	for _, kv := range environ {
		if v, ok := strings.CutPrefix(kv, "TROVE_POD_NAME="); ok && v != "" {
			return v
		}
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}

// refusalExit turns the runner's refusal into exit status 3 and leaves every
// other error at status 1.
func refusalExit(err error) error {
	if r, ok := err.(*postgres.Refusal); ok { //nolint:errorlint // the runner returns the refusal itself
		return exitError{code: r.ExitCode(), err: r}
	}
	return err
}

// exitError carries an exit status other than 1 or 2.
type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string { return e.err.Error() }
func (e exitError) Unwrap() error { return e.err }
func (e exitError) ExitCode() int { return e.code }
