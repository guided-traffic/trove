package testenv

import (
	"context"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/config"
)

// ThroughPgBouncer turns cfg, a direct configuration of the database cfg.Name,
// into one whose main pool goes through PgBouncer in transaction mode
// (TROVE_TEST_PG_POOLED=1, see [PgPooled]) and whose session class goes
// straight to PostgreSQL. A transaction-mode pooler cannot carry the session
// settings as startup parameters, so they become role defaults of user in this
// database (R-HA-014), as a deployment does. maint is a connection that may
// ALTER ROLE.
func ThroughPgBouncer(tb testing.TB, cfg config.Database, user string, maint *pgx.Conn) config.Database {
	tb.Helper()
	bport, err := strconv.Atoi(Get(tb, "TROVE_TEST_PG_PGBOUNCER_PORT"))
	if err != nil {
		tb.Fatalf("TROVE_TEST_PG_PGBOUNCER_PORT: %v", err)
	}
	for _, set := range []string{
		"statement_timeout = " + strconv.FormatInt(cfg.Session.StatementTimeout.Milliseconds(), 10),
		"lock_timeout = " + strconv.FormatInt(cfg.Session.LockTimeout.Milliseconds(), 10),
		"idle_in_transaction_session_timeout = " + strconv.FormatInt(cfg.Session.IdleInTransactionSessionTimeout.Milliseconds(), 10),
		"search_path = " + cfg.Schema,
	} {
		if _, err := maint.Exec(context.Background(), "ALTER ROLE "+user+" IN DATABASE "+cfg.Name+" SET "+set); err != nil {
			tb.Fatalf("role default %s: %v", set, err)
		}
	}
	cfg.SessionHost, cfg.SessionPort = cfg.Host, cfg.Port
	cfg.Port = bport
	cfg.Pooler = config.DatabasePooler{Mode: "transaction", ServerConnections: 20}
	return cfg
}
