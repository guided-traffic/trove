package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// backupRoleKey is the system_state key that records which role the grants below went to, so that a later run that is
// configured with another role (or none) takes them back.
const backupRoleKey = "backup_role"

// grantBackupRole gives the login role named by database.backup_role the privileges of the backup pod (R-BAK-015): SELECT on
// every table and sequence of the schema, writes on the run record and its pins, EXECUTE on the function that writes the backup's
// audit events (no INSERT on the audit trail itself, Q-BAK-50), and SELECT by default on the tables the owner creates later,
// such as the monthly audit partitions. It runs after the steps, as the schema owner, idempotently on every migration run, so a
// table added by a later migration is covered at once. trove never creates the role (R-HA-014): the database administrator
// does, with CONNECT and USAGE on the schema. With no role configured nothing is granted and the backup pod uses the runtime
// credential.
//
// The role is recorded in system_state. When the setting changes or is emptied, the grants of the role that was configured before
// are revoked, exactly the ones this function gave. A privilege the administrator gave that role by hand stays.
func grantBackupRole(ctx context.Context, conn *pgx.Conn, schema, role string, log *slog.Logger) error {
	qs := q.New(conn)
	prev, err := recordedBackupRole(ctx, qs)
	if err != nil {
		return err
	}
	if prev != "" && prev != role {
		if err := revokeBackupRole(ctx, conn, schema, prev, log); err != nil {
			return err
		}
	}
	if role == "" {
		return forgetBackupRole(ctx, qs, prev)
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
		fmt.Sprintf("GRANT EXECUTE ON FUNCTION %s.backup_record_event(text, text, text, text, jsonb) TO %s", s, r),
		// An earlier release granted INSERT on the audit trail; the role must not keep it.
		fmt.Sprintf("REVOKE INSERT ON %s.audit_event FROM %s", s, r),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT SELECT ON TABLES TO %s", s, r),
	} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("granting the backup role %q: %w", role, err)
		}
	}
	if err := recordBackupRole(ctx, qs, role); err != nil {
		return err
	}
	log.Info("backup role privileges granted", "role", role)
	return nil
}

// revokeBackupRole takes back what grantBackupRole gave a role that is no longer the backup role. A role that was dropped
// meanwhile has nothing left to revoke.
func revokeBackupRole(ctx context.Context, conn *pgx.Conn, schema, role string, log *slog.Logger) error {
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", role).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	s, r := pgx.Identifier{schema}.Sanitize(), pgx.Identifier{role}.Sanitize()
	for _, stmt := range []string{
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s REVOKE SELECT ON TABLES FROM %s", s, r),
		fmt.Sprintf("REVOKE EXECUTE ON FUNCTION %s.backup_record_event(text, text, text, text, jsonb) FROM %s", s, r),
		fmt.Sprintf("REVOKE INSERT, UPDATE, DELETE ON %s.backup_run, %s.backup_blob_pin FROM %s", s, s, r),
		fmt.Sprintf("REVOKE INSERT ON %s.audit_event FROM %s", s, r),
		fmt.Sprintf("REVOKE SELECT ON ALL SEQUENCES IN SCHEMA %s FROM %s", s, r),
		fmt.Sprintf("REVOKE SELECT ON ALL TABLES IN SCHEMA %s FROM %s", s, r),
	} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("revoking the privileges of the former backup role %q: %w", role, err)
		}
	}
	log.Info("backup role privileges revoked from the role that is no longer configured", "role", role)
	return nil
}

func recordedBackupRole(ctx context.Context, qs *q.Queries) (string, error) {
	row, err := qs.GetSystemState(ctx, backupRoleKey)
	if isNoRows(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var role string
	if err := json.Unmarshal(row.Value, &role); err != nil {
		return "", fmt.Errorf("system_state %s is not a JSON string: %w", backupRoleKey, err)
	}
	return role, nil
}

func recordBackupRole(ctx context.Context, qs *q.Queries, role string) error {
	raw, err := json.Marshal(role)
	if err != nil {
		return err
	}
	return qs.UpsertSystemState(ctx, q.UpsertSystemStateParams{Key: backupRoleKey, Value: raw})
}

func forgetBackupRole(ctx context.Context, qs *q.Queries, prev string) error {
	if prev == "" {
		return nil
	}
	_, err := qs.DeleteSystemState(ctx, backupRoleKey)
	return err
}
