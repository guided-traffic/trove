package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

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

// Query labels that are no sqlc name: a statement written inline in code, and a batch of a pipeline.
const (
	unnamedQuery  = "unnamed"
	pipelineQuery = "pipeline"
)

// queryLabel is the value of the label query of the statement metrics (R-OPS-008): the sqlc query name of the "-- name: Foo :one"
// header, never the text, whose values are unbounded; the begin, commit and rollback of pgx are labelled by their verb; any
// other statement without the header is unnamed.
func queryLabel(sql string) string {
	s := strings.TrimLeft(sql, " \t\r\n")
	if rest, ok := strings.CutPrefix(s, "-- name:"); ok {
		if w := firstWord(rest); w != "" {
			return w
		}
	}
	// The statements of a transaction's protocol, which pgx sends without a name, are labelled by their verb (the COMMIT of a
	// cache fill is where a stalled replication shows).
	verb := strings.TrimRight(firstWord(s), ";")
	switch {
	case strings.EqualFold(verb, "begin"):
		return "begin"
	case strings.EqualFold(verb, "commit"):
		return "commit"
	case strings.EqualFold(verb, "rollback"):
		return "rollback"
	}
	return unnamedQuery
}

// firstWord is the first whitespace-separated word of s, read without splitting the rest.
func firstWord(s string) string {
	s = strings.TrimLeft(s, " \t\r\n")
	if i := strings.IndexAny(s, " \t\r\n"); i >= 0 {
		return s[:i]
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

// stmtRun is a statement in flight: its text, for the name of a timeout, and its start, for the statement metrics.
type stmtRun struct {
	sql string
	at  time.Time
}

type (
	stmtSQLKey   struct{}
	batchTimeKey struct{}
)

var (
	_ pgx.QueryTracer = stmtTracer{}
	_ pgx.BatchTracer = stmtTracer{}
)

func (t stmtTracer) TraceQueryStart(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	ctx = context.WithValue(ctx, stmtSQLKey{}, stmtRun{sql: d.SQL, at: time.Now()})
	if t.next != nil {
		ctx = t.next.TraceQueryStart(ctx, c, d)
	}
	return ctx
}

func (t stmtTracer) TraceQueryEnd(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryEndData) {
	run, _ := ctx.Value(stmtSQLKey{}).(stmtRun)
	if d.Err != nil {
		tagStatementTimeout(d.Err, run.sql)
	}
	if !run.at.IsZero() {
		t.metrics.queried(run.sql, time.Since(run.at))
	}
	if t.next != nil {
		t.next.TraceQueryEnd(ctx, c, d)
	}
}

func (t stmtTracer) TraceBatchStart(ctx context.Context, c *pgx.Conn, d pgx.TraceBatchStartData) context.Context {
	ctx = context.WithValue(ctx, batchTimeKey{}, time.Now())
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
	if at, ok := ctx.Value(batchTimeKey{}).(time.Time); ok {
		t.metrics.queriedBatch(time.Since(at)) // the batch is one round trip: its statements have no time of their own
	}
	if b, ok := t.next.(pgx.BatchTracer); ok {
		b.TraceBatchEnd(ctx, c, d)
	}
}
