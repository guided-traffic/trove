package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
)

// grantBackupRole gives the login role named by database.backup_role the privileges of the backup pod (R-BAK-015): SELECT on
// every table and sequence of the schema, writes on the run record and its pins, INSERT on the audit trail, and SELECT by default
// on the tables the owner creates later, such as the monthly audit partitions. It runs after the steps, as the schema owner,
// idempotently on every migration run, so a table added by a later migration is covered at once. trove never creates the role
// (R-HA-014): the database administrator does, with CONNECT and USAGE on the schema. With no role configured nothing happens and
// the backup pod uses the runtime credential.
func grantBackupRole(ctx context.Context, conn *pgx.Conn, schema, role string, log *slog.Logger) error {
	if role == "" {
		return nil
	}
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", role).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("database.backup_role %q does not exist: the database administrator creates the login role once (R-BAK-015)", role)
	}
	s, r := pgx.Identifier{schema}.Sanitize(), pgx.Identifier{role}.Sanitize()
	for _, stmt := range []string{
		fmt.Sprintf("GRANT SELECT ON ALL TABLES IN SCHEMA %s TO %s", s, r),
		fmt.Sprintf("GRANT SELECT ON ALL SEQUENCES IN SCHEMA %s TO %s", s, r),
		fmt.Sprintf("GRANT INSERT, UPDATE, DELETE ON %s.backup_run, %s.backup_blob_pin TO %s", s, s, r),
		fmt.Sprintf("GRANT INSERT ON %s.audit_event TO %s", s, r),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT SELECT ON TABLES TO %s", s, r),
	} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("granting the backup role %q: %w", role, err)
		}
	}
	log.Info("backup role privileges granted", "role", role)
	return nil
}
