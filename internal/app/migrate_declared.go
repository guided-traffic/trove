package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/ci"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// DeclaredState is the step of `trove migrate` that applies the state the system
// configuration declares, after the schema steps and under the migration lock
// (postgres.MigrateOptions.Declared): the role bindings of authz.role_bindings
// (R-AUTHZ-010, Q-AUTHZ-170). The writers are the ones a replica uses, built over
// the runner's one connection: the change log writes the authz change in the
// transaction, so every running replica rebuilds its grant index from the poll
// (R-AUTHZ-028, no Valkey needed); the audit writer records the events with the
// actor system:configuration in the same transaction.
//
// instance is the Job's pod name, stored as the replica of the audit events.
func DeclaredState(cfg *config.Config, instance string, log *slog.Logger) func(context.Context, *postgres.MigrationSession) error {
	return func(ctx context.Context, s *postgres.MigrationSession) error {
		changes, err := cluster.NewChanges(cluster.ChangesOptions{
			DB: s.DBTX(), Config: cfg.Cluster, WatchdogTimeout: cfg.Health.WatchdogTimeout, Log: log,
		})
		if err != nil {
			return err
		}
		recorder, err := audit.New(audit.Options{DB: s, Replica: instance, Config: cfg.Audit, Log: log, AppLog: log})
		if err != nil {
			return err
		}
		groups := principal.NewManager(principal.ManagerOptions{
			DB: s, Notifier: changes, Recorder: recorder, BreakGlassName: cfg.Auth.BreakGlass.Username,
		})
		svc := authz.NewService(authz.ServiceOptions{DB: s, Changes: changes, Recorder: recorder})
		out, err := svc.ApplyDeclared(ctx, cfg.Authz.RoleBindings, groups)
		if err != nil {
			return err
		}
		log.Info("role bindings of the configuration applied", "declared", len(cfg.Authz.RoleBindings),
			"bound", out.Bound, "taken_over", out.Adopted, "unbound", out.Unbound, "groups_created", out.GroupsCreated)
		return applyDeclaredCIIssuers(ctx, s, cfg, log)
	}
}

// applyDeclaredCIIssuers applies ci.issuers in one transaction of the migration session (R-CI-001,
// Q-CI-16): an issuer is created or replaced by name with source configuration, and a configuration
// issuer that is no longer declared is disabled with removedFromConfiguration. No replica writes
// them, so replicas of two releases during a rolling update never overwrite each other.
//
// Work package W1 builds the issuer service over the session and replaces the stub below. The stub
// answers 501 for a declared issuer, so a configuration that declares issuers stops the migration
// loudly while the service does not exist; with none declared nothing is called. W1 calls the
// service unconditionally, because an empty list must disable the configuration issuers that were
// removed from it.
func applyDeclaredCIIssuers(ctx context.Context, s *postgres.MigrationSession, cfg *config.Config, log *slog.Logger) error {
	declared := ci.DeclaredFromConfigList(cfg.CI.Issuers)
	if len(declared) == 0 {
		return nil
	}
	var issuers ci.Issuers = ci.StubIssuers{}
	if err := s.InTx(ctx, func(tx meta.Tx) error { return issuers.ApplyDeclared(ctx, tx, declared) }); err != nil {
		return fmt.Errorf("ci.issuers: %w", err)
	}
	log.Info("CI issuers of the configuration applied", "declared", len(declared))
	return nil
}
