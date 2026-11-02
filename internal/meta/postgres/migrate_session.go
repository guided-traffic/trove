package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// MigrationSession is the migration runner's one connection, handed to
// [MigrateOptions.Declared] after the schema steps while the migration lock is
// still held. Its transactions carry the hooks of [meta.Tx], so the domain
// writers (the change log, the audit writer, the role bindings) run on it as on
// a pool: the declared state of the configuration is written by the one session
// that holds the lock, inside the connection budget of the migration Job
// (R-HA-016).
//
// It is valid only during the call of Declared, and it is not safe for
// concurrent use.
type MigrationSession struct {
	conn *pgx.Conn
}

var _ meta.Runner = (*MigrationSession)(nil)

// InTx runs fn in one READ COMMITTED transaction on the session, then its Before
// hooks, COMMIT and the After hooks; on any failure it rolls back and runs the
// OnRollback hooks. There is no retry: the migration Job is retried as a whole,
// and the declared state is applied again from the start.
func (s *MigrationSession) InTx(ctx context.Context, fn func(meta.Tx) error) (err error) {
	ptx, err := s.conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	t := &txn{tx: ptx}
	committed := false
	defer func() {
		if committed {
			return
		}
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if rerr := ptx.Rollback(rctx); rerr != nil && !errors.Is(rerr, pgx.ErrTxClosed) && err == nil {
			err = rerr
		}
		t.RunRollback()
	}()
	if err = fn(t); err != nil {
		return err
	}
	if err = t.RunBefore(ctx, t); err != nil {
		return err
	}
	if err = ptx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	t.RunAfter()
	return nil
}

// Read runs fn on the session outside a transaction (the read side of the audit
// writer's store).
func (s *MigrationSession) Read(_ context.Context, fn func(q.DBTX) error) error {
	return fn(s.conn)
}

// DBTX is the connection itself, for a component that takes a q.DBTX at
// construction (the change log's poll, which a migration never starts).
func (s *MigrationSession) DBTX() q.DBTX { return s.conn }

// applyDeclared runs MigrateOptions.Declared on the runner's connection.
func applyDeclared(ctx context.Context, conn *pgx.Conn, opts MigrateOptions) error {
	if opts.Declared == nil {
		return nil
	}
	return opts.Declared(ctx, &MigrationSession{conn: conn})
}
