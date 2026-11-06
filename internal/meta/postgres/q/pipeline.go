package q

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// Tx is the transaction of the PostgreSQL store, named here so that a package that may not import the driver can hold it.
type Tx = pgx.Tx

// Pipeline runs the statements of several steps in one round trip to the server (Q-VULN-82). A step is a function that calls
// the methods of the generated [Queries] it is given and keeps their results; the statements of all steps are collected, sent
// as one pgx batch on the transaction and answered together, so that n independent reads cost one round trip and not n. The
// generated methods are used as they are: no statement is written twice, and the rows are scanned by the code that scans them
// everywhere else.
//
// Every step runs twice. The first time its methods only record the statement and its arguments and answer as if the table
// were empty (no rows, and pgx.ErrNoRows for a statement that expects one), and the error of the step is ignored; the second
// time its methods get the real answer of the batch, in the order the first pass recorded, and the error of the step is
// returned. A step therefore has to be a pure function of its arguments: the same statements in the same order whatever the
// answers were, no effect except keeping its results in variables the caller declared, and no statement whose arguments
// depend on the result of another step of the same pipeline (the caller runs those as the next pipeline). A step that issues
// a different statement the second time is reported as an error.
//
// The statements run in the order of the steps and in the transaction of tx, so a statement sees what the one before it wrote.
// The first statement that fails fails the pipeline, and the transaction is aborted as after any failed statement.
func Pipeline(ctx context.Context, tx Tx, steps ...func(*Queries) error) error {
	rec := &recorder{}
	rq := New(rec)
	for _, step := range steps {
		_ = step(rq) // the answers are empty: only the statements matter
	}
	if len(rec.calls) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, c := range rec.calls {
		batch.Queue(c.sql, c.args...)
	}
	res := tx.SendBatch(ctx, batch)
	rp := &replayer{res: res, calls: rec.calls}
	pq := New(rp)
	var first error
	for _, step := range steps {
		if err := step(pq); err != nil && first == nil {
			first = err
		}
	}
	if err := res.Close(); err != nil && first == nil {
		first = err
	}
	if first == nil && rp.next != len(rec.calls) {
		first = fmt.Errorf("pipeline: the steps issued %d statements the second time and %d the first", rp.next, len(rec.calls))
	}
	return first
}

type call struct {
	kind byte // 'e' Exec, 'q' Query, 'r' QueryRow
	sql  string
	args []any
}

// recorder is the DBTX of the first pass: it records the statements.
type recorder struct{ calls []call }

func (r *recorder) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	r.calls = append(r.calls, call{'e', sql, args})
	return pgconn.CommandTag{}, nil
}

func (r *recorder) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	r.calls = append(r.calls, call{'q', sql, args})
	return noRows{}, nil
}

func (r *recorder) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	r.calls = append(r.calls, call{'r', sql, args})
	return noRow{}
}

// replayer is the DBTX of the second pass: it hands out the answers of the batch in the order the first pass recorded.
type replayer struct {
	res   pgx.BatchResults
	calls []call
	next  int
}

// take returns the recorded statement the next call must be, or an error when the step went another way the second time.
func (p *replayer) take(kind byte, sql string) error {
	if p.next >= len(p.calls) {
		return errors.New("pipeline: a step issued a statement the first pass did not record")
	}
	c := p.calls[p.next]
	p.next++
	if c.kind != kind || c.sql != sql {
		return fmt.Errorf("pipeline: a step issued another statement the second time (statement %d)", p.next)
	}
	return nil
}

func (p *replayer) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	if err := p.take('e', sql); err != nil {
		return pgconn.CommandTag{}, err
	}
	return p.res.Exec()
}

func (p *replayer) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	if err := p.take('q', sql); err != nil {
		return nil, err
	}
	return p.res.Query()
}

func (p *replayer) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	if err := p.take('r', sql); err != nil {
		return errRow{err}
	}
	return p.res.QueryRow()
}

// noRows is the empty answer of the first pass to a Query.
type noRows struct{}

func (noRows) Close()                                       {}
func (noRows) Err() error                                   { return nil }
func (noRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (noRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (noRows) Next() bool                                   { return false }
func (noRows) Scan(...any) error                            { return pgx.ErrNoRows }
func (noRows) Values() ([]any, error)                       { return nil, pgx.ErrNoRows }
func (noRows) RawValues() [][]byte                          { return nil }
func (noRows) Conn() *pgx.Conn                              { return nil }
func (noRows) TypeMap() *pgtype.Map                         { return nil }

// noRow is the empty answer of the first pass to a QueryRow.
type noRow struct{}

func (noRow) Scan(...any) error { return pgx.ErrNoRows }

// errRow answers a QueryRow of the second pass that does not match the first.
type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }
