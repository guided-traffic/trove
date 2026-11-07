package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// stmtTagMax bounds the text of an untagged statement that names it in an error.
const stmtTagMax = 60

// statementName names a statement for a log line: the sqlc query name of its "-- name: Foo :one" header, or the start of the
// text, whitespace collapsed, for a statement that has none.
func statementName(sql string) string {
	s := strings.TrimSpace(sql)
	if rest, ok := strings.CutPrefix(s, "-- name:"); ok {
		if f := strings.Fields(rest); len(f) > 0 {
			return f[0]
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > stmtTagMax {
		s = s[:stmtTagMax] + "..."
	}
	return s
}

// tagStatementTimeout appends the statement to the message of a statement or lock timeout, so that the logged error names it
// (the server's message does not). The text reaches the log through the error's cause only; the client-facing problem body
// carries the fixed message of database_timeout. It reports whether err was a timeout. The error is the one pgx returns for
// this statement alone, and a second tag on the same error is not added.
func tagStatementTimeout(err error, sql string) bool {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || (pe.Code != "57014" && pe.Code != "55P03") {
		return false
	}
	if !strings.Contains(pe.Message, "[statement: ") {
		pe.Message += " [statement: " + statementName(sql) + "]"
	}
	return true
}

// stmtTracer is the tracer of every pool: it names the statement of a timeout in the error (tagStatementTimeout) and passes
// every event on to the tracer of WithQueryTracer, which may be nil.
type stmtTracer struct {
	next    pgx.QueryTracer
	pool    string   // the db_pool label of the acquire histogram
	metrics *Metrics // nil records nothing
}

type stmtSQLKey struct{}

var (
	_ pgx.QueryTracer = stmtTracer{}
	_ pgx.BatchTracer = stmtTracer{}
)

func (t stmtTracer) TraceQueryStart(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	ctx = context.WithValue(ctx, stmtSQLKey{}, d.SQL)
	if t.next != nil {
		ctx = t.next.TraceQueryStart(ctx, c, d)
	}
	return ctx
}

func (t stmtTracer) TraceQueryEnd(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryEndData) {
	if d.Err != nil {
		sql, _ := ctx.Value(stmtSQLKey{}).(string)
		tagStatementTimeout(d.Err, sql)
	}
	if t.next != nil {
		t.next.TraceQueryEnd(ctx, c, d)
	}
}

func (t stmtTracer) TraceBatchStart(ctx context.Context, c *pgx.Conn, d pgx.TraceBatchStartData) context.Context {
	if b, ok := t.next.(pgx.BatchTracer); ok {
		return b.TraceBatchStart(ctx, c, d)
	}
	return ctx
}

func (t stmtTracer) TraceBatchQuery(ctx context.Context, c *pgx.Conn, d pgx.TraceBatchQueryData) {
	if d.Err != nil {
		tagStatementTimeout(d.Err, d.SQL)
	}
	if b, ok := t.next.(pgx.BatchTracer); ok {
		b.TraceBatchQuery(ctx, c, d)
	}
}

func (t stmtTracer) TraceBatchEnd(ctx context.Context, c *pgx.Conn, d pgx.TraceBatchEndData) {
	if b, ok := t.next.(pgx.BatchTracer); ok {
		b.TraceBatchEnd(ctx, c, d)
	}
}
