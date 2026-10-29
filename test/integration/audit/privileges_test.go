//go:build integration

package audit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/meta"
)

func wantState(t *testing.T, c *pgx.Conn, code, sql string) {
	t.Helper()
	_, err := c.Exec(context.Background(), sql)
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != code {
		t.Errorf("%s: err = %v, want SQLSTATE %s", sql, err, code)
	}
}

// R-OPS-015 (Q-OPS-4): through the runtime role's own pool, the audit writer can
// append and read, and the role can neither change nor delete an audit row nor
// shorten the retention.
func TestR_OPS_015_TheRuntimeRoleAppendsAndCannotChangeOrDeleteAuditRows(t *testing.T) {
	e := newEnv(t)
	rt := e.openWith(e.runtimeCfg())
	w, _, _ := e.writer(rt, auditCfg(), time.Now)
	store := audit.NewStore(rt)
	ctx := context.Background()

	if err := rt.InTx(ctx, func(m meta.Tx) error {
		return w.RecordTx(ctx, m, audit.Event{Type: "probe.thing.created", Actor: user("ann")})
	}); err != nil {
		t.Fatalf("RecordTx as the runtime role: %v", err)
	}
	w.Record(ctx, hit(1))
	fctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := w.Flush(fctx); err != nil {
		t.Fatal(err)
	}
	res, err := store.Query(ctx, audit.Filter{}, audit.Page{})
	if err != nil || len(res.Items) != 2 {
		t.Fatalf("the runtime role reads %d events: %v", len(res.Items), err)
	}

	conn := e.runtimeConn()
	part := "trove.audit_event_" + currentMonth(t, e)
	wantState(t, conn, "42501", "UPDATE trove.audit_event SET outcome = 'success'")
	wantState(t, conn, "42501", "UPDATE trove.audit_event SET details = '{}'::jsonb WHERE action = 'probe.thing.created'")
	wantState(t, conn, "42501", "DELETE FROM trove.audit_event")
	wantState(t, conn, "42501", "TRUNCATE trove.audit_event")
	wantState(t, conn, "42501", "DELETE FROM "+part)
	wantState(t, conn, "42501", "UPDATE "+part+" SET outcome = 'success'")
	wantState(t, conn, "42501", "UPDATE trove.audit_policy SET retention_days = 1")
	wantState(t, conn, "42501", "SELECT trove.audit_create_partitions('2025-01-15Z', '2025-04-02Z')")
	if n := count(t, e, "audit_event", "true"); n != 2 {
		t.Errorf("audit rows after the refused statements = %d, want 2", n)
	}
}
