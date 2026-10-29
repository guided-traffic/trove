package postgres

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/guided-traffic/trove/internal/meta"
)

// txn is the meta.Tx of one attempt. It also carries the hooks.
type txn struct {
	pgx.Tx
	before []func(context.Context, meta.Tx) error
	after  []func()
}

var _ meta.Tx = (*txn)(nil)

func (t *txn) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := t.Tx.Exec(ctx, sql, args...)
	return tag.RowsAffected(), err
}

func (t *txn) Query(ctx context.Context, sql string, args ...any) (meta.Rows, error) {
	rows, err := t.Tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (t *txn) QueryRow(ctx context.Context, sql string, args ...any) meta.Row {
	return t.Tx.QueryRow(ctx, sql, args...)
}

func (t *txn) Before(fn func(context.Context, meta.Tx) error) { t.before = append(t.before, fn) }
func (t *txn) After(fn func())                                { t.after = append(t.after, fn) }

// Unwrap returns the pgx transaction behind a meta.Tx, for the stores in
// infrastructure packages that run sqlc queries on it. It reports false for a
// meta.Tx that this package did not create.
func Unwrap(tx meta.Tx) (pgx.Tx, bool) {
	t, ok := tx.(*txn)
	if !ok {
		return nil, false
	}
	return t.Tx, true
}

// InTx runs fn in a READ COMMITTED transaction on the request pool and repeats
// it after a serialization failure or deadlock (three times) and after a
// failover error (until retry.max_elapsed). fn must be safe to run again: each
// attempt starts a new transaction and the earlier one was rolled back.
func (db *DB) InTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return db.transact(ctx, db.main, func(tx pgx.Tx) (func(), error) { return nil, fn(tx) })
}

// Tx is InTx for code that holds a meta.Tx: it runs the Before hooks last,
// inside the transaction, and the After hooks once COMMIT succeeded. A retried
// attempt registers its hooks again; the hooks of a failed attempt are dropped.
func (db *DB) Tx(ctx context.Context, fn func(meta.Tx) error) error {
	return db.transact(ctx, db.main, func(ptx pgx.Tx) (func(), error) {
		t := &txn{Tx: ptx}
		if err := fn(t); err != nil {
			return nil, err
		}
		for i := 0; i < len(t.before); i++ { // a hook may register another
			if err := t.before[i](ctx, t); err != nil {
				return nil, err
			}
		}
		after := t.after
		return func() {
			for _, f := range after {
				f()
			}
		}, nil
	})
}

// RequestTx is Tx with the request-path statement timeout of R-ARCH-019: the
// first statement of every attempt is SET LOCAL statement_timeout.
func (db *DB) RequestTx(ctx context.Context, timeout time.Duration, fn func(meta.Tx) error) error {
	return db.Tx(ctx, func(tx meta.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('statement_timeout', $1, true)", strconv.FormatInt(timeout.Milliseconds(), 10)); err != nil {
			return err
		}
		return fn(tx)
	})
}

// transact is the core of InTx and Tx: retry around one attempt.
func (db *DB) transact(ctx context.Context, pool *pgxpool.Pool, body func(pgx.Tx) (func(), error)) error {
	var post func()
	err := db.retry.do(ctx, func() error {
		var err error
		post, err = attempt(ctx, pool, body)
		return err
	})
	if err != nil {
		if IsStatementTimeout(err) {
			return Unavailable(err)
		}
		return err
	}
	if post != nil {
		post()
	}
	return nil
}

// attempt runs one transaction. A connection that proves wrong (closed, or a
// demoted primary that is now read-only) is destroyed instead of returned to
// the pool, so the retry finds the new primary.
func attempt(ctx context.Context, pool *pgxpool.Pool, body func(pgx.Tx) (func(), error)) (post func(), err error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	broken := false
	defer func() {
		if broken {
			_ = conn.Hijack().Close(context.Background())
			return
		}
		conn.Release()
	}()

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		broken = conn.Conn().IsClosed() || classify(err) != retryNo
		return nil, err
	}
	finished := false
	defer func() {
		if finished {
			return
		}
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if rerr := tx.Rollback(rctx); rerr != nil && !errors.Is(rerr, pgx.ErrTxClosed) {
			broken = true
		}
	}()

	post, err = body(tx)
	if err != nil {
		if conn.Conn().IsClosed() {
			broken = true
			if classify(err) == retryNo {
				err = &connClosedError{err: err}
			}
		} else if classify(err) == retryFailover {
			broken = true
		}
		return nil, err
	}
	finished = true
	if err := tx.Commit(ctx); err != nil {
		var pe *pgconn.PgError
		switch {
		case conn.Conn().IsClosed() && !errors.As(err, &pe):
			broken = true
			return nil, &outcomeUnknownError{err: err}
		case classify(err) == retryFailover:
			broken = true
		}
		return nil, err
	}
	return post, nil
}
