package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/signal"
	"os/user"
	"syscall"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/oidc"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/observability"
)

// The recovery commands of the login area (R-AUTH-015). They use the pod's
// configuration and a short-lived database connection of their own, load
// neither the OIDC providers nor the break-glass password (so they run in a
// worker pod too), and need no Valkey: every end and every unlock commits
// through the change log or the lockout row, which every replica reads.

const (
	breakGlassUsage = "usage: trove admin break-glass status|unlock|end-sessions [--config <file>]"
	sessionsUsage   = "usage: trove admin sessions end --user <username> | --all [--config <file>]"
)

// adminEnv is what a recovery command works with.
type adminEnv struct {
	cfg      *config.Config
	db       *postgres.DB
	changes  *cluster.Changes
	log      *slog.Logger
	sessions *session.Store
	recorder audit.Recorder
	actor    audit.Actor
}

// openAdmin loads the configuration, opens the database and builds the session
// store with a change log that writes but does not poll.
//
// Transactional events are written by an audit writer on the same database.
func openAdmin(ctx context.Context, c *commonFlags, s streams, args []string) (*adminEnv, func(), error) {
	flags, names, err := c.parse(args, map[string]string{"log-level": "log.level"})
	if err != nil {
		return nil, nil, err
	}
	loaded, err := c.loadConfig(s, config.CommandAdmin, flags, names)
	if err != nil {
		return nil, nil, err
	}
	cfg := loaded.Config
	instance := instanceName(s.environ)
	log := observability.NewLogger(cfg.Log, s.stderr, observability.WithIdentity(instance, "admin")).With("component", "admin")
	db, err := postgres.Open(ctx, cfg.Database, instance, meta.RoleWeb, log)
	if err != nil {
		return nil, nil, err
	}
	changes, err := cluster.NewChanges(cluster.ChangesOptions{DB: db.ControlPool(), Config: cfg.Cluster, WatchdogTimeout: cfg.Health.WatchdogTimeout, Log: log})
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	recorder, err := audit.New(audit.Options{DB: db, Replica: instance, Config: cfg.Audit, Log: log, AppLog: log})
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	store, err := session.New(session.Options{
		DB: db, Changes: changes, Audit: recorder, Log: log, Sessions: cfg.Auth.Sessions, BreakGlass: cfg.Auth.BreakGlass,
		RevocationBound: cfg.Auth.RevocationBound, CacheTTL: cfg.Auth.Tokens.CacheTTL,
	})
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	env := &adminEnv{cfg: cfg, db: db, changes: changes, sessions: store, recorder: recorder, actor: adminActor(instance), log: log}
	return env, db.Close, nil
}

// adminActor is the actor of a command: admin-cli with the pod name and the OS
// user (R-AUTH-015).
func adminActor(instance string) audit.Actor {
	name := "unknown"
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	return audit.Actor{Kind: "admin-cli", Name: name + "@" + instance}
}

func adminContext() (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	return ctx, func() { cancel(); stop() }
}

// breakGlassCmd is trove admin break-glass status|unlock|end-sessions.
func breakGlassCmd(args []string, s streams) error {
	if len(args) == 0 {
		return usageError{breakGlassUsage}
	}
	action := args[0]
	switch action {
	case "status", "unlock", "end-sessions":
	default:
		return usageError{breakGlassUsage}
	}
	ctx, done := adminContext()
	defer done()
	env, closeDB, err := openAdmin(ctx, newFlags("admin break-glass "+action, s), s, args[1:])
	if err != nil {
		return err
	}
	defer closeDB()
	switch action {
	case "status":
		return breakGlassStatus(ctx, env, s)
	case "unlock":
		return breakGlassUnlock(ctx, env, s)
	default:
		if err := env.sessions.EndBreakGlass(ctx, session.EndRevoked, env.actor); err != nil {
			return err
		}
		_, err := fmt.Fprintln(s.stdout, "every break-glass session has ended")
		return err
	}
}

// BreakGlassStatus is the output of trove admin break-glass status.
type BreakGlassStatus struct {
	Enabled           bool       `json:"enabled"`
	Username          string     `json:"username"`
	Failures          int32      `json:"failures"`
	LockedUntil       *time.Time `json:"lockedUntil"`
	RotationHoldUntil *time.Time `json:"rotationHoldUntil"`
	LiveSessions      int64      `json:"liveSessions"`
}

func breakGlassStatus(ctx context.Context, env *adminEnv, s streams) error {
	bg := env.cfg.Auth.BreakGlass
	st := BreakGlassStatus{Enabled: bg.Enabled, Username: bg.Username}
	var rows []q.GetLockoutRow
	if err := env.db.Read(ctx, func(db q.DBTX) error {
		var err error
		rows, err = q.New(db).GetLockout(ctx, q.GetLockoutParams{AccountKey: oidc.LockoutKey, HoldS: bg.RotationHold.Seconds()})
		return err
	}); err != nil {
		return err
	}
	now := time.Now().UTC()
	if len(rows) == 1 {
		r := rows[0]
		st.Failures = r.Failures
		if r.LockedForS > 0 {
			t := now.Add(time.Duration(r.LockedForS * float64(time.Second))).Truncate(time.Second)
			st.LockedUntil = &t
		}
		if r.HoldForS > 0 {
			t := now.Add(time.Duration(r.HoldForS * float64(time.Second))).Truncate(time.Second)
			st.RotationHoldUntil = &t
		}
	}
	n, err := env.sessions.LiveBreakGlassSessions(ctx)
	if err != nil {
		return err
	}
	st.LiveSessions = n
	return printJSON(s, st)
}

// breakGlassUnlock clears a lockout at once on every replica: every login
// attempt reads the row (R-AUTH-038).
func breakGlassUnlock(ctx context.Context, env *adminEnv, s streams) error {
	var was bool
	err := env.db.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errors.New("the transaction does not come from the PostgreSQL store")
		}
		rows, err := q.New(ptx).Unlock(ctx, oidc.LockoutKey)
		if err != nil {
			return err
		}
		was = len(rows) == 1 && rows[0]
		details, _ := json.Marshal(map[string]any{"severity": "high", "was_locked": was})
		return env.recorder.RecordTx(ctx, tx, audit.Event{Type: audit.EventAuthBreakGlassUnlocked, Actor: env.actor, Outcome: audit.Success, Details: details})
	})
	if err != nil {
		return err
	}
	msg := "the break-glass account was not locked"
	if was {
		msg = "the break-glass account is unlocked"
	}
	_, err = fmt.Fprintln(s.stdout, msg)
	return err
}

// sessionsCmd is trove admin sessions end --user <username> | --all.
func sessionsCmd(args []string, s streams) error {
	if len(args) == 0 || args[0] != "end" {
		return usageError{sessionsUsage}
	}
	c := newFlags("admin sessions end", s)
	username := c.fs.String("user", "", "end the sessions of this user")
	all := c.fs.Bool("all", false, "end every session")
	ctx, done := adminContext()
	defer done()
	env, closeDB, err := openAdmin(ctx, c, s, args[1:])
	if err != nil {
		return err
	}
	defer closeDB()
	if (*username == "") == !*all {
		return usageError{sessionsUsage}
	}
	if *all {
		n, err := env.sessions.EndAll(ctx, env.actor)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(s.stdout, "%d sessions ended\n", n)
		return err
	}
	var ids []string
	if err := env.db.Read(ctx, func(db q.DBTX) error {
		var err error
		ids, err = q.New(db).UserIDByName(ctx, *username)
		return err
	}); err != nil {
		return err
	}
	if len(ids) == 0 {
		return fmt.Errorf("no user is named %q", *username)
	}
	ended, err := env.sessions.EndPrincipalSessions(ctx, ids[0], env.actor)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(s.stdout, "%d sessions of %s ended\n", len(ended), *username)
	return err
}
