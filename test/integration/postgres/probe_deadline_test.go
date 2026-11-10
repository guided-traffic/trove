//go:build integration

package postgres

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// endsOnStatement ends a context when a statement that contains match starts: the deadline of a readiness check passing
// while its statement runs, on an event instead of a clock.
type endsOnStatement struct {
	match string
	end   atomic.Pointer[context.CancelFunc]
}

func (e *endsOnStatement) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if f := e.end.Load(); f != nil && strings.Contains(d.SQL, e.match) {
		(*f)()
	}
	return ctx
}

func (e *endsOnStatement) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// R-OPS-001, R-ARCH-019, R-HA-011 (Q-HA-284): the end of a check's deadline while its statement runs does not close the control
// connection. The driver closes a connection whose statement was cut, and the control pool is one connection big: until the
// closed one is cleaned up the pool hands out nothing, and the change-log poll, the heartbeat and the drain of the replica wait
// for it.
func TestR_OPS_001_AProbeWhoseDeadlineEndsDuringItsStatementKeepsTheControlConnection(t *testing.T) {
	st := getStack(t)
	cfg, _, _ := migrated(t, st)
	tr := &endsOnStatement{}
	db, err := postgres.Open(ctxT(t, 30*time.Second), cfg, "pod-probe", meta.RoleWeb, quiet, postgres.WithQueryTracer(tr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Ready(ctxT(t, 10*time.Second)); err != nil {
		t.Fatal(err)
	}
	opened := db.Stats().Control.NewConns

	for _, probe := range []struct {
		name  string
		match string
		run   func(context.Context) error
	}{
		{"database", "pg_is_in_recovery", db.Ready},
		{"schema", "schema_state", func(ctx context.Context) error { return db.CheckSchema(ctx, "0.1.0") }},
	} {
		ctx, end := context.WithTimeout(context.Background(), 10*time.Second)
		tr.match = probe.match
		tr.end.Store(&end)
		err := probe.run(ctx)
		tr.end.Store(nil)
		end()
		if ctx.Err() == nil {
			t.Fatalf("%s: the statement never started, so the deadline never ended", probe.name)
		}
		if err != nil {
			t.Errorf("%s: the probe's statement was cut by the end of the probe's deadline: %v", probe.name, err)
		}
	}

	// The connection is still the one the pool had, and the loops get it at once.
	if err := db.ControlPool().Ping(ctxT(t, 10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := db.Stats().Control.NewConns; got != opened {
		t.Errorf("the control pool opened %d connections after the probes, it had %d: a probe closed the control connection", got, opened)
	}
}
