package app

import (
	"context"
	"log/slog"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
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
		return nil
	}
}
