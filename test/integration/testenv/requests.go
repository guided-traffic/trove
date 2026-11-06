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
// a batch outside one (one implicit transaction). Each is kept with the names of its statements, so that a test can take
// out the transactions of named statements exactly. The connection's liveness ping and its set-up are not statements of
// the tracer and are not counted. It counts between Count(true) and Count(false) only; outside that the tracer reads one
// flag, so the requests of a test that are not measured run as they would without it.
type RequestTransactions struct {
	// idOf reads the request ID from a statement's context: httpserver.RequestID.
	idOf func(context.Context) string
	on   atomic.Bool
	mu   sync.Mutex
	// tx is the explicit transaction each connection is inside, batch the batch it sends.
	tx, batch map[*pgx.Conn]open
	reqs      map[string]*RequestCost
}

// open is a transaction a connection is inside: the request that began it ("" for none) and its index among the request's
// transactions.
type open struct {
	id string
	i  int
}

// RequestCost is what one request ran: its transactions in order, each with the names of its statements in order (the sqlc
// name, or the start of the text), without the transaction control.
type RequestCost struct {
	Transactions [][]string
}

// Statements is the number of the request's statements by name.
func (c RequestCost) Statements() map[string]int {
	out := map[string]int{}
	for _, tx := range c.Transactions {
		for _, s := range tx {
			out[s]++
		}
	}
	return out
}

var (
	_ pgx.QueryTracer = (*RequestTransactions)(nil)
	_ pgx.BatchTracer = (*RequestTransactions)(nil)
)

// NewRequestTransactions returns an empty counter.
func NewRequestTransactions() *RequestTransactions {
	return &RequestTransactions{idOf: httpserver.RequestID, tx: map[*pgx.Conn]open{}, batch: map[*pgx.Conn]open{}, reqs: map[string]*RequestCost{}}
}

// Option is the database option that installs the counter in a replica (ReplicaOptions.Database). One counter may serve
// several replicas: request IDs and connections are their own in each.
func (r *RequestTransactions) Option() postgres.Option { return postgres.WithQueryTracer(r) }

// Count starts (true) or stops (false) the counting. Starting it forgets which connections were inside a transaction or a
// batch, so a test starts it while none of the requests it measures runs; the counts of earlier requests are kept.
func (r *RequestTransactions) Count(on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if on {
		clear(r.tx)
		clear(r.batch)
	}
	r.on.Store(on)
}

// Of returns a copy of what the request with the ID ran; no transaction when it ran no statement.
func (r *RequestTransactions) Of(id string) RequestCost {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.reqs[id]
	if !ok {
		return RequestCost{}
	}
	out := RequestCost{Transactions: make([][]string, len(c.Transactions))}
	for i, tx := range c.Transactions {
		out.Transactions[i] = append([]string{}, tx...)
	}
	return out
}

// begin opens a transaction of the request and returns it; the caller holds the lock.
func (r *RequestTransactions) begin(id string, first ...string) open {
	c, ok := r.reqs[id]
	if !ok {
		c = &RequestCost{}
		r.reqs[id] = c
	}
	c.Transactions = append(c.Transactions, append([]string{}, first...))
	return open{id: id, i: len(c.Transactions) - 1}
}

// add puts a statement of the request into the open transaction o when the request began it, and into a transaction of its
// own otherwise; the caller holds the lock.
func (r *RequestTransactions) add(id string, o open, ok bool, name string) {
	if ok && o.id == id {
		c := r.reqs[id]
		c.Transactions[o.i] = append(c.Transactions[o.i], name)
		return
	}
	r.begin(id, name)
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

// TraceQueryStart opens a transaction of a request for its BEGIN, and puts a statement of a request into its transaction, or
// into one of its own outside a transaction.
func (r *RequestTransactions) TraceQueryStart(ctx context.Context, conn *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if !r.on.Load() {
		return ctx
	}
	id := r.idOf(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	switch control(d.SQL) {
	case "begin":
		o := open{i: -1}
		if id != "" {
			o = r.begin(id)
		}
		r.tx[conn] = o
		return ctx
	case "end":
		delete(r.tx, conn)
		return ctx
	case "savepoint":
		return ctx
	}
	if id == "" {
		return ctx
	}
	o, ok := r.tx[conn]
	r.add(id, o, ok, StatementName(d.SQL))
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
		delete(r.tx, conn)
	}
}

// TraceBatchStart opens the transaction of a request's batch: the explicit transaction the batch runs in, or a transaction
// of its own outside one.
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
	if o, ok := r.tx[conn]; ok && o.id == id {
		r.batch[conn] = o
	} else {
		r.batch[conn] = r.begin(id)
	}
	return ctx
}

// TraceBatchQuery puts a statement of a batch into the batch's transaction.
func (r *RequestTransactions) TraceBatchQuery(ctx context.Context, conn *pgx.Conn, d pgx.TraceBatchQueryData) {
	if !r.on.Load() {
		return
	}
	id := r.idOf(ctx)
	if id == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.batch[conn]
	r.add(id, o, ok, StatementName(d.SQL))
}

// TraceBatchEnd ends a batch.
func (r *RequestTransactions) TraceBatchEnd(_ context.Context, conn *pgx.Conn, _ pgx.TraceBatchEndData) {
	if !r.on.Load() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.batch, conn)
}
