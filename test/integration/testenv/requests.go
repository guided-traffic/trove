package testenv

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// RequestTransactions counts, inside the replicas it is given to (ReplicaOptions.Database), the database transactions and
// statements each HTTP request ran, by the request's ID: the ID the chain puts into the request's context and the client
// receives as X-Request-Id (httpserver.RequestID). It is the pgx tracer of the replica's request pool and control
// connection, so it sees every statement and batch the replica sends there, and attributes one to a request when it ran
// with that request's context. A statement of the replica's own loops (health, registration, change-log poll), of the
// batched writers (download statistics, token last use) or of another process has no request ID and is not counted, and
// nothing depends on when PostgreSQL publishes its statistics (R-VULN-028 V-5, Q-VULN-98).
//
// A transaction is what PostgreSQL counts as one: an explicit transaction from BEGIN to its end, a statement outside one, and
// a batch outside one (one implicit transaction). The connection's liveness ping and its set-up are not statements of the
// tracer and are not counted. It counts between Count(true) and Count(false) only; outside that the tracer reads one flag,
// so the requests of a test that are not measured run as they would without it.
type RequestTransactions struct {
	// idOf reads the request ID from a statement's context: httpserver.RequestID.
	idOf func(context.Context) string
	on   atomic.Bool
	mu   sync.Mutex
	// inTx is the connections inside an explicit transaction.
	inTx map[*pgx.Conn]bool
	reqs map[string]*RequestCost
}

// RequestCost is what one request ran: its transactions and its statements by name (the sqlc name, or the start of the
// text), without the transaction control.
type RequestCost struct {
	Transactions int
	Statements   map[string]int
}

var (
	_ pgx.QueryTracer = (*RequestTransactions)(nil)
	_ pgx.BatchTracer = (*RequestTransactions)(nil)
)

// NewRequestTransactions returns an empty counter.
func NewRequestTransactions() *RequestTransactions {
	return &RequestTransactions{idOf: httpserver.RequestID, inTx: map[*pgx.Conn]bool{}, reqs: map[string]*RequestCost{}}
}

// Option is the database option that installs the counter in a replica (ReplicaOptions.Database). One counter may serve
// several replicas: request IDs and connections are their own in each.
func (r *RequestTransactions) Option() postgres.Option { return postgres.WithQueryTracer(r) }

// Count starts (true) or stops (false) the counting. Starting it forgets which connections were inside a transaction, so a
// test starts it while none of the requests it measures runs; the counts of earlier requests are kept.
func (r *RequestTransactions) Count(on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if on {
		clear(r.inTx)
	}
	r.on.Store(on)
}

// Of returns a copy of what the request with the ID ran; the zero cost when it ran no statement.
func (r *RequestTransactions) Of(id string) RequestCost {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.reqs[id]
	if !ok {
		return RequestCost{Statements: map[string]int{}}
	}
	out := RequestCost{Transactions: c.Transactions, Statements: make(map[string]int, len(c.Statements))}
	for k, v := range c.Statements {
		out.Statements[k] = v
	}
	return out
}

// cost returns the entry of a request; the caller holds the lock.
func (r *RequestTransactions) cost(id string) *RequestCost {
	c, ok := r.reqs[id]
	if !ok {
		c = &RequestCost{Statements: map[string]int{}}
		r.reqs[id] = c
	}
	return c
}

// control classifies the transaction control: "begin" opens a transaction, "end" closes it, "savepoint" is a savepoint
// statement inside one; "" is any other statement. It reads the first two words only.
func control(sql string) string {
	first, rest := word(sql)
	switch strings.ToLower(first) {
	case "begin", "start":
		return "begin"
	case "commit", "end", "abort":
		return "end"
	case "rollback":
		if second, _ := word(rest); strings.EqualFold(second, "to") {
			return "savepoint"
		}
		return "end"
	case "savepoint", "release":
		return "savepoint"
	}
	return ""
}

// word returns the first word of s and what follows it.
func word(s string) (first, rest string) {
	s = strings.TrimLeft(s, " \t\r\n")
	if i := strings.IndexAny(s, " \t\r\n;"); i >= 0 {
		return s[:i], s[i:]
	}
	return s, ""
}

// TraceQueryStart counts a statement of a request, and a transaction for a BEGIN or a statement outside a transaction.
func (r *RequestTransactions) TraceQueryStart(ctx context.Context, conn *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if !r.on.Load() {
		return ctx
	}
	id := r.idOf(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	switch control(d.SQL) {
	case "begin":
		r.inTx[conn] = true
		if id != "" {
			r.cost(id).Transactions++
		}
		return ctx
	case "end":
		delete(r.inTx, conn)
		return ctx
	case "savepoint":
		return ctx
	}
	if id == "" {
		return ctx
	}
	c := r.cost(id)
	if !r.inTx[conn] {
		c.Transactions++
	}
	c.Statements[StatementName(d.SQL)]++
	return ctx
}

// TraceQueryEnd closes the transaction of a connection whose BEGIN failed.
func (r *RequestTransactions) TraceQueryEnd(_ context.Context, conn *pgx.Conn, d pgx.TraceQueryEndData) {
	if d.Err == nil || !r.on.Load() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if conn.PgConn().TxStatus() == 'I' {
		delete(r.inTx, conn)
	}
}

// TraceBatchStart counts a batch outside a transaction as one transaction of the request.
func (r *RequestTransactions) TraceBatchStart(ctx context.Context, conn *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	if !r.on.Load() {
		return ctx
	}
	id := r.idOf(ctx)
	if id == "" {
		return ctx
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.inTx[conn] {
		r.cost(id).Transactions++
	}
	return ctx
}

// TraceBatchQuery counts a statement of a batch.
func (r *RequestTransactions) TraceBatchQuery(ctx context.Context, _ *pgx.Conn, d pgx.TraceBatchQueryData) {
	if !r.on.Load() {
		return
	}
	id := r.idOf(ctx)
	if id == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cost(id).Statements[StatementName(d.SQL)]++
}

// TraceBatchEnd ends a batch; nothing to count.
func (r *RequestTransactions) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData) {}
