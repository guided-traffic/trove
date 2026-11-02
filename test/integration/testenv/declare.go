package testenv

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/app"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// Declare runs `trove migrate up` in the test process with authz.role_bindings set
// to bindings (R-AUTHZ-010, Q-AUTHZ-170): the migration runner with nothing to
// apply, then the step that applies the declared role bindings, exactly as the
// command composes it (app.DeclaredState). Replicas that run pick the change up
// from the change log. The error is the one `trove migrate` would exit with.
func (s *Stack) Declare(bindings ...config.AuthzRoleBinding) error {
	s.tb.Helper()
	return DeclareRoleBindings(s.pg, bindings...)
}

// DeclareRoleBindings is [Stack.Declare] for a suite with an environment of its
// own: db is the configuration the installation's replicas open (the session host
// is the direct server also behind PgBouncer).
func DeclareRoleBindings(db config.Database, bindings ...config.AuthzRoleBinding) error {
	cfg := &config.Config{Database: db}
	cfg.Authz.RoleBindings = bindings
	cfg.Cluster = config.Cluster{EpochPollInterval: time.Second, EpochMaxStaleness: 20 * time.Second, ChangeRetention: time.Hour}
	cfg.Health.WatchdogTimeout = 30 * time.Second
	cfg.Audit = config.Audit{AsyncBuffer: 100, RetentionDays: 365}
	cfg.Auth.BreakGlass.Username = "admin"
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err := postgres.Migrate(ctx, db, postgres.MigrateOptions{
		Instance: "it-migrate", Version: "0.1.0", Log: Quiet(), AuditRetentionDays: &cfg.Audit.RetentionDays,
		Migrate:  config.Migrate{AdvisoryLockWait: 20 * time.Second, DDLLockTimeout: 2 * time.Second, DDLLockRetries: 3},
		Declared: app.DeclaredState(cfg, "it-migrate", Quiet()),
	})
	return err
}
