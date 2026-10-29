package cluster

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Held is a lease this replica holds: the session-level advisory lock and the
// connection it lives on. The singleton's writes go through [Held.DB] or
// [Held.Tx], so they share the lock's session (R-HA-006 step 2): if the session
// died, the lock is gone and the write fails.
type Held struct {
	ID LeaseID

	l      *Leases
	conn   *pgxpool.Conn
	ctx    context.Context
	cancel context.CancelCauseFunc
	token  int64
	since  time.Time
	valid  atomic.Bool
	done   chan struct{}

	mu sync.Mutex // one statement at a time on conn
}

// Context ends when the lease is lost or released. Work that holds the lease
// stops when it ends.
func (h *Held) Context() context.Context { return h.ctx }

// Token is the fencing token: it grows with every acquisition of any lease of
// the cluster, so of two tokens of one lease the higher belongs to the later
// holder. A holder includes it in writes that outlive its session.
func (h *Held) Token() int64 { return h.token }

// Since is when this replica acquired the lease (its own clock; for display).
func (h *Held) Since() time.Time { return h.since }

// Valid reports whether the replica still holds the lease.
func (h *Held) Valid() bool { return h.valid.Load() }

// ErrNotHeld is returned by writes on a lease that is no longer held.
var ErrNotHeld = errors.New("cluster: the lease is not held")

func (h *Held) lock(ctx context.Context) error {
	if !h.Valid() {
		return ErrNotHeld
	}
	for !h.mu.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
		if !h.Valid() {
			return ErrNotHeld
		}
	}
	if !h.Valid() {
		h.mu.Unlock()
		return ErrNotHeld
	}
	return nil
}

// check turns a connection failure into the loss of the lease.
func (h *Held) check(err error) error {
	if err != nil && h.conn.Conn().IsClosed() {
		h.l.lose(h, err)
	}
	return err
}

func (h *Held) ping(ctx context.Context) error {
	if err := h.lock(ctx); err != nil {
		if errors.Is(err, ErrNotHeld) {
			return nil
		}
		return nil // busy with a statement of the holder; its own failure reports the loss
	}
	defer h.mu.Unlock()
	_, err := h.conn.Exec(ctx, "SELECT 1")
	return err
}

// Fence proves that this session still holds the lock: pg_locks lists the lock
// as granted to this backend.
func (h *Held) Fence(ctx context.Context) error {
	if err := h.lock(ctx); err != nil {
		return err
	}
	defer h.mu.Unlock()
	return h.fenceLocked(ctx, h.conn)
}

type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (h *Held) fenceLocked(ctx context.Context, c queryRower) error {
	var n int
	err := c.QueryRow(ctx,
		`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND classid = $1::int::oid AND objid = $2::int::oid
		   AND objsubid = 2 AND pid = pg_backend_pid() AND granted`, LockLease, h.ID.number).Scan(&n)
	if err != nil {
		return h.check(err)
	}
	if n != 1 {
		h.l.lose(h, errors.New("the session no longer holds the lock"))
		return ErrNotHeld
	}
	return nil
}

// DB returns the lease connection as a statement runner. Statements run one at
// a time and fail with [ErrNotHeld] after the lease is gone.
func (h *Held) DB() q.DBTX { return heldDB{h} }

// Tx runs fn in a transaction on the lease connection. The first statement
// checks the fence, so a transaction of a replica that lost its lease never
// writes.
func (h *Held) Tx(ctx context.Context, fn func(q.DBTX) error) error {
	if err := h.lock(ctx); err != nil {
		return err
	}
	defer h.mu.Unlock()
	tx, err := h.conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return h.check(err)
	}
	if err := h.fenceLocked(ctx, tx); err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		return h.check(err)
	}
	return h.check(tx.Commit(ctx))
}

// release unlocks explicitly and returns the connection to the pool; a failed
// unlock destroys the connection, which ends the session and so the lock.
func (h *Held) release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var ok bool
	if h.conn.Conn().IsClosed() {
		_ = h.conn.Conn().Close(ctx)
		h.conn.Release()
		return
	}
	if err := h.conn.QueryRow(ctx, "SELECT pg_advisory_unlock($1::int, $2::int)", LockLease, h.ID.number).Scan(&ok); err != nil || !ok {
		_ = h.conn.Hijack().Close(ctx)
		return
	}
	h.conn.Release()
}

// destroy closes the connection of a lost lease.
func (h *Held) destroy() {
	h.mu.Lock()
	defer h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = h.conn.Hijack().Close(ctx)
}

// heldDB implements q.DBTX on the lease connection.
type heldDB struct{ h *Held }

func (d heldDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if err := d.h.lock(ctx); err != nil {
		return pgconn.CommandTag{}, err
	}
	defer d.h.mu.Unlock()
	tag, err := d.h.conn.Exec(ctx, sql, args...)
	return tag, d.h.check(err)
}

func (d heldDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if err := d.h.lock(ctx); err != nil {
		return nil, err
	}
	rows, err := d.h.conn.Query(ctx, sql, args...)
	if err != nil {
		d.h.mu.Unlock()
		return nil, d.h.check(err)
	}
	return &lockedRows{Rows: rows, h: d.h}, nil
}

func (d heldDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	rows, err := d.Query(ctx, sql, args...)
	return &lockedRow{rows: rows, err: err}
}

// lockedRows keeps the connection locked until the rows are closed.
type lockedRows struct {
	pgx.Rows
	h    *Held
	once sync.Once
}

func (r *lockedRows) Close() {
	r.Rows.Close()
	r.once.Do(func() {
		_ = r.h.check(r.Err())
		r.h.mu.Unlock()
	})
}

type lockedRow struct {
	rows pgx.Rows
	err  error
}

func (r *lockedRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	defer r.rows.Close()
	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return err
		}
		return pgx.ErrNoRows
	}
	if err := r.rows.Scan(dest...); err != nil {
		return fmt.Errorf("scanning: %w", err)
	}
	return r.rows.Err()
}
