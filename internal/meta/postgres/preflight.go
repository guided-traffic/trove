package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// minServerVersion is the PostgreSQL floor (R-HA-013, Q-HA-2).
const minServerVersion = 160000

// serverChecks is the part of the preflight that every process runs
// (R-HA-013, R-HA-014): server version, encoding and the runtime role.
func serverChecks(ctx context.Context, db q.DBTX, cfg config.Database) (q.ServerInfoRow, error) {
	info, err := q.New(db).ServerInfo(ctx)
	if err != nil {
		return info, err
	}
	if info.VersionNum < minServerVersion {
		return info, fmt.Errorf("PostgreSQL %s is older than the supported minimum 16 (server_version_num %d)", info.Version, info.VersionNum)
	}
	if !strings.EqualFold(info.Encoding, "UTF8") {
		return info, fmt.Errorf("the database encoding is %s, trove needs UTF8", info.Encoding)
	}
	if info.IsSuperuser && !cfg.Preflight.AllowSuperuser {
		return info, fmt.Errorf("the database role %q is a superuser; trove refuses it unless database.preflight.allow_superuser is true", info.RoleName)
	}
	return info, nil
}

// preflight runs when a replica opens its pools.
func (db *DB) preflight(ctx context.Context) error {
	conn, err := db.control.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	info, err := serverChecks(ctx, conn, db.cfg)
	if err != nil {
		return err
	}
	owner, err := q.New(conn).SchemaOwnedBy(ctx, db.cfg.Schema)
	switch {
	case err == nil && owner == info.RoleName:
		db.log.Warn("the runtime role owns the schema (single-role mode): a SQL injection would have DDL rights",
			"role", info.RoleName, "schema", db.cfg.Schema)
	case err != nil && !isNoRows(err):
		return err
	}
	if db.cfg.Pooler.Mode == "transaction" {
		if err := checkRoleDefaults(ctx, conn, db.cfg, "control"); err != nil {
			return err
		}
		mc, err := db.main.Acquire(ctx)
		if err != nil {
			return err
		}
		defer mc.Release()
		return checkRoleDefaults(ctx, mc, db.cfg, "request")
	}
	return nil
}

// checkRoleDefaults verifies, on a pooled connection, that the four session
// settings that cannot travel as startup parameters through a transaction-mode
// pooler come from the runtime login's role defaults (R-HA-058, R-HA-014).
func checkRoleDefaults(ctx context.Context, db q.DBTX, cfg config.Database, endpoint string) error {
	rows, err := q.New(db).SessionSettings(ctx)
	if err != nil {
		return err
	}
	want := map[string]string{
		"statement_timeout":                   strconv.FormatInt(cfg.Session.StatementTimeout.Milliseconds(), 10),
		"lock_timeout":                        strconv.FormatInt(cfg.Session.LockTimeout.Milliseconds(), 10),
		"idle_in_transaction_session_timeout": strconv.FormatInt(cfg.Session.IdleInTransactionSessionTimeout.Milliseconds(), 10),
		"search_path":                         cfg.Schema,
	}
	var problems []string
	for _, r := range rows {
		w, ok := want[r.Name]
		if !ok {
			continue
		}
		got := strings.ReplaceAll(r.Setting, `"`, "")
		if got != w {
			problems = append(problems, fmt.Sprintf("%s is %q, want %q", r.Name, r.Setting, w))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	info, _ := q.New(db).ServerInfo(ctx)
	return fmt.Errorf("through the transaction-mode pooler (%s endpoint) the login %q lacks its role defaults: %s; "+
		"set them with ALTER ROLE %s IN DATABASE %s SET ... (R-HA-014, R-HA-058)",
		endpoint, info.RoleName, strings.Join(problems, "; "), info.RoleName, cfg.Name)
}

// ErrSessionState is returned when a session-scoped connection lands on a pooler
// that does not keep session state (R-ARCH-017).
var ErrSessionState = errors.New("session connection must keep session state; set database.session_host to a direct or session-mode endpoint")

// sessionStateError is ErrSessionState with the connection failure that revealed
// it. Its message is ErrSessionState's, so the process exits with the text of
// R-ARCH-017; the cause stays available to errors.Unwrap and the log.
type sessionStateError struct{ cause error }

func (e *sessionStateError) Error() string   { return ErrSessionState.Error() }
func (e *sessionStateError) Unwrap() []error { return []error{ErrSessionState, e.cause} }

// refusedStartupParameter maps the failure a pooler gives a session connection:
// PgBouncer refuses the startup parameters (statement_timeout and the others)
// that session classes send, with SQLSTATE 08P01. That already proves the
// endpoint is a pooler.
func refusedStartupParameter(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "08P01" && strings.Contains(pe.Message, "unsupported startup parameter") {
		return &sessionStateError{cause: err}
	}
	return err
}

// sessionGuard runs SELECT pg_backend_pid() five times, outside a transaction,
// on one connection. If the values differ, the endpoint is a transaction-mode
// pooler. It is a guard, not a proof (R-ARCH-017).
func sessionGuard(ctx context.Context, db q.DBTX) error {
	var first int32
	for i := 0; i < 5; i++ {
		var pid int32
		if err := db.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
			return err
		}
		if i == 0 {
			first = pid
		} else if pid != first {
			return ErrSessionState
		}
	}
	return nil
}

// checkLeaseEndpoint runs the guard on a lease-pool connection before the
// replica takes its first lease.
func (db *DB) checkLeaseEndpoint(ctx context.Context) error {
	return db.retry.do(ctx, func() error {
		conn, err := db.lease.Acquire(ctx)
		if err != nil {
			return refusedStartupParameter(err)
		}
		defer conn.Release()
		return sessionGuard(ctx, conn)
	})
}

// Connection budget of R-HA-016.
const (
	// migrationConns is the migration Job's share.
	migrationConns = 2
	// backupConns is the backup Job's share once per cluster: 2 + backup.database.parallel
	// with its default of 2 (R-BAK-005).
	backupConns = 4
)

// Budget is the connection count the PostgreSQL server must allow (R-HA-016).
type Budget struct {
	WebMax, WorkerMax int
	Pooled            int // the main pools: declared pooler server connections, or replicas x max_conns
	Session           int // lease pools: worker replicas x lease_pool_size
	Migration, Backup int
	Reserved          int // database.preflight.reserved_connections
	SuperuserReserved int // the server's superuser_reserved_connections
	Total             int
}

// ConnectionBudget computes the formula of R-HA-016. webMax and workerMax are the
// replica maxima the chart passes to the migration Job.
func ConnectionBudget(c config.Database, webMax, workerMax, superuserReserved int) Budget {
	b := Budget{
		WebMax: webMax, WorkerMax: workerMax, Migration: migrationConns, Backup: backupConns,
		Reserved: c.Preflight.ReservedConnections, SuperuserReserved: superuserReserved,
	}
	if c.Pooler.Mode == "transaction" {
		b.Pooled = c.Pooler.ServerConnections
	} else {
		b.Pooled = (webMax + workerMax) * c.Pool.MaxConns
	}
	b.Session = workerMax * c.LeasePoolSize
	b.Total = b.Pooled + b.Session + b.Migration + b.Backup + b.Reserved + b.SuperuserReserved
	return b
}

// checkBudget compares the budget with max_connections. With the replica counts
// unset (0 and 0) there is nothing to compare and the check says so.
func checkBudget(c config.Database, maxConnections, superuserReserved int, log *slog.Logger) error {
	web, worker := c.Preflight.ExpectedWebReplicas, c.Preflight.ExpectedWorkerReplicas
	if web+worker == 0 {
		log.Info("connection budget not checked: database.preflight.expected_web_replicas and expected_worker_replicas are not set")
		return nil
	}
	b := ConnectionBudget(c, web, worker, superuserReserved)
	if b.Total <= maxConnections {
		log.Info("connection budget ok", "required", b.Total, "max_connections", maxConnections)
		return nil
	}
	msg := fmt.Sprintf("the connection budget needs %d connections but the server allows %d (max_connections): "+
		"main pools %d, lease pools %d, migration %d, backup %d, reserved %d, superuser reserved %d (R-HA-016)",
		b.Total, maxConnections, b.Pooled, b.Session, b.Migration, b.Backup, b.Reserved, b.SuperuserReserved)
	if c.Preflight.ConnectionBudget == "warn" {
		log.Warn(msg)
		return nil
	}
	return errors.New(msg)
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
