package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/app"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/cache"
)

// The bootstrap commands of the identity area (R-AUTH-015): service accounts,
// tokens and user disable. They run the same domain functions as the API, in
// the same transaction with the change record and the audit event, with the
// actor admin-cli; the change commits through the change log and is in force on
// every replica within auth.revocation_bound (R-AUTH-023).

const (
	serviceAccountUsage = "usage: trove admin service-account create <name> [--description <text>] [--admin] [--config <file>]"
	tokenUsage          = "usage: trove admin token create --service-account <name> [--ttl 15m] [--scope-file <file>] [--config <file>]\n" +
		"       trove admin token revoke --id <uuid> | --value-stdin [--config <file>]"
	userUsage = "usage: trove admin user disable <username> [--keep-tokens] [--config <file>]"
)

// adminTokenDescription is the description of a token the command creates: the
// token has no human owner to describe it (R-AUTH-027 requires one).
const adminTokenDescription = "created by trove admin token create"

// identityParts are the identity components a command works with, built over
// the command's own connection with empty caches.
type identityParts struct {
	manager *principal.Manager
	tokens  *token.Service
	caller  principal.Caller
}

// identity builds the manager and the token service for a command.
func (e *adminEnv) identity() (*identityParts, error) {
	caches, err := cache.NewRegistry(nil, nil)
	if err != nil {
		return nil, err
	}
	cfg := e.cfg.Auth
	gate := principal.Gate{Bound: cfg.RevocationBound}
	binder := authz.NewService(authz.ServiceOptions{DB: e.db, Changes: e.changes, Recorder: e.recorder})
	manager := principal.NewManager(principal.ManagerOptions{
		DB: e.db, Notifier: e.changes, Recorder: e.recorder, BreakGlassName: cfg.BreakGlass.Username, Admin: binder,
	})
	principals := principal.NewStore(principal.StoreOptions{DB: e.db, Cache: caches, Gate: gate, SafetyTTL: cfg.Tokens.CacheTTL})
	settings := token.NewSettingsStore(token.SettingsOptions{
		DB: e.db, Notifier: e.changes, Recorder: e.recorder, Cache: caches, Gate: gate, Tokens: cfg.Tokens, Anonymous: cfg.Anonymous,
	})
	tokens := token.NewService(token.ServiceOptions{
		DB: e.db, Notifier: e.changes, Recorder: e.recorder, Principals: principals, Settings: settings, Cache: caches, Gate: gate,
		Tokens: cfg.Tokens, Log: e.log, Scope: app.TokenScopeOptions(e.cfg),
	})
	return &identityParts{manager: manager, tokens: tokens, caller: principal.Caller{Actor: e.actor}}, nil
}

// serviceAccountCmd is trove admin service-account create <name>.
func serviceAccountCmd(args []string, s streams) error {
	if len(args) == 0 || args[0] != "create" {
		return usageError{serviceAccountUsage}
	}
	name, rest := positional(args[1:])
	c := newFlags("admin service-account create", s)
	description := c.fs.String("description", "", "description of the service account")
	admin := c.fs.Bool("admin", false, "bind the system role admin")
	if name == "" {
		return usageError{serviceAccountUsage}
	}
	return withIdentity(c, s, rest, func(ctx context.Context, id *identityParts) error {
		sa, err := id.manager.CreateServiceAccount(ctx, id.caller, principal.NewServiceAccount{Name: name, Description: *description, Admin: *admin})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(s.stdout, "service account %s created (%s)\n", sa.Name, sa.ID)
		return err
	})
}

// tokenCmd is trove admin token create|revoke.
func tokenCmd(args []string, s streams) error {
	if len(args) == 0 {
		return usageError{tokenUsage}
	}
	switch args[0] {
	case "create":
		return tokenCreate(args[1:], s)
	case "revoke":
		return tokenRevoke(args[1:], s)
	}
	return usageError{tokenUsage}
}

// tokenCreate prints the secret of a new service-account token to stdout, and
// nothing else, so that T=$(trove admin token create ...) captures it
// (R-AUTH-015). The secret is never logged.
func tokenCreate(args []string, s streams) error {
	c := newFlags("admin token create", s)
	sa := c.fs.String("service-account", "", "name of the service account that owns the token")
	ttl := c.fs.Duration("ttl", 15*time.Minute, "lifetime of the token; at most the SAT maximum")
	scopeFile := c.fs.String("scope-file", "", "file with the scope document of R-AUTH-024; the default scope when absent")
	return withIdentity(c, s, args, func(ctx context.Context, id *identityParts) error {
		if *sa == "" {
			return usageError{tokenUsage}
		}
		var scope []byte
		if *scopeFile != "" {
			b, err := os.ReadFile(*scopeFile)
			if err != nil {
				return fmt.Errorf("--scope-file: %w", err)
			}
			scope = b
		}
		acct, err := id.manager.GetServiceAccount(ctx, *sa)
		if err != nil {
			return err
		}
		secs := int64(ttl.Seconds())
		created, err := id.tokens.Create(ctx, token.CreateRequest{
			Caller: id.caller, Type: token.TypeSAT, OwnerID: acct.ID, Description: adminTokenDescription,
			Scope: scope, TTL: token.TTLRequest{Seconds: &secs}, Via: "admin-cli", Admin: true,
		})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(s.stdout, created.Secret)
		return err
	})
}

// tokenRevoke revokes a token by ID (reason admin), or by value read from stdin
// (reason leaked, R-AUTH-029).
func tokenRevoke(args []string, s streams) error {
	c := newFlags("admin token revoke", s)
	tid := c.fs.String("id", "", "ID of the token")
	stdin := c.fs.Bool("value-stdin", false, "read the token value from standard input")
	return withIdentity(c, s, args, func(ctx context.Context, id *identityParts) error {
		if (*tid == "") == !*stdin {
			return usageError{tokenUsage}
		}
		if *stdin {
			if s.stdin == nil {
				return errors.New("standard input is not available")
			}
			b, err := io.ReadAll(io.LimitReader(s.stdin, 4096))
			if err != nil {
				return err
			}
			res, err := id.tokens.RevokeByValue(ctx, id.caller, strings.TrimSpace(string(b)))
			if err != nil {
				return err
			}
			if !res.Found {
				return errors.New("no token has this value")
			}
			_, err = fmt.Fprintf(s.stdout, "%d tokens revoked\n", len(res.Revoked))
			return err
		}
		revoked, err := id.tokens.Revoke(ctx, id.caller, *tid, token.ReasonAdmin)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(s.stdout, "%d tokens revoked\n", len(revoked))
		return err
	})
}

// userCmd is trove admin user disable <username>.
func userCmd(args []string, s streams) error {
	if len(args) == 0 || args[0] != "disable" {
		return usageError{userUsage}
	}
	name, rest := positional(args[1:])
	c := newFlags("admin user disable", s)
	keep := c.fs.Bool("keep-tokens", false, "keep the user's tokens (they fail with principal_disabled while the user is disabled)")
	if name == "" {
		return usageError{userUsage}
	}
	return withIdentity(c, s, rest, func(ctx context.Context, id *identityParts) error {
		if err := id.manager.DisableUser(ctx, id.caller, name, principal.DisableOptions{Reason: "admin", RevokeTokens: !*keep}); err != nil {
			return err
		}
		_, err := fmt.Fprintf(s.stdout, "user %s disabled\n", name)
		return err
	})
}

// withIdentity opens the command environment, builds the identity components
// and runs fn.
func withIdentity(c *commonFlags, s streams, args []string, fn func(context.Context, *identityParts) error) error {
	ctx, done := adminContext()
	defer done()
	env, closeDB, err := openAdmin(ctx, c, s, args)
	if err != nil {
		return err
	}
	defer closeDB()
	id, err := env.identity()
	if err != nil {
		return err
	}
	return fn(ctx, id)
}

// positional splits a leading positional argument from the flags that follow
// it: the Go flag package stops at the first non-flag.
func positional(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}
