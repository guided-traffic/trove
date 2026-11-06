package testenv

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type requestKey struct{}

// R-VULN-028 V-5b (Q-VULN-98): the counter attributes a transaction, with its statements, to the request whose context ran it,
// the way PostgreSQL counts transactions, and leaves the statements of other contexts out.
func TestR_VULN_028_RequestTransactionsCountsWhatARequestRan(t *testing.T) {
	r := NewRequestTransactions()
	r.idOf = func(ctx context.Context) string { s, _ := ctx.Value(requestKey{}).(string); return s }
	r.Count(true)
	a := context.WithValue(context.Background(), requestKey{}, "a")
	b := context.WithValue(context.Background(), requestKey{}, "b")
	loop := context.Background()
	c1, c2 := &pgx.Conn{}, &pgx.Conn{}
	q := func(ctx context.Context, c *pgx.Conn, sql string) {
		r.TraceQueryStart(ctx, c, pgx.TraceQueryStartData{SQL: sql})
	}

	// a: one explicit transaction of two statements with a savepoint, then a statement outside a transaction.
	q(a, c1, "begin isolation level read committed")
	q(a, c1, "-- name: GetLiveItem :many\nSELECT 1")
	q(a, c1, "SAVEPOINT s1")
	q(a, c1, "-- name: NPMTombstoned :one\nSELECT 2")
	q(a, c1, "ROLLBACK TO SAVEPOINT s1")
	q(a, c1, "commit;")
	q(a, c1, "-- name: IdentityTokenBySecret :many\nSELECT 3")
	// b: a batch outside a transaction is one transaction, a batch inside one adds none.
	r.TraceBatchStart(b, c2, pgx.TraceBatchStartData{})
	r.TraceBatchQuery(b, c2, pgx.TraceBatchQueryData{SQL: "-- name: X :one\nSELECT 1"})
	r.TraceBatchQuery(b, c2, pgx.TraceBatchQueryData{SQL: "-- name: Y :one\nSELECT 2"})
	r.TraceBatchEnd(b, c2, pgx.TraceBatchEndData{})
	q(b, c2, "BEGIN")
	q(b, c2, "-- name: Z :one\nSELECT 3")
	r.TraceBatchStart(b, c2, pgx.TraceBatchStartData{})
	r.TraceBatchQuery(b, c2, pgx.TraceBatchQueryData{SQL: "-- name: X :one\nSELECT 1"})
	r.TraceBatchEnd(b, c2, pgx.TraceBatchEndData{})
	q(b, c2, "ROLLBACK")
	// the replica's own loop: no request ID, not counted, and it does not leave c1 inside a transaction.
	q(loop, c1, "begin")
	q(loop, c1, "-- name: UpsertDownloadStats :execrows\nSELECT 4")
	q(loop, c1, "commit")
	q(a, c1, "-- name: GetLiveItem :many\nSELECT 1")
	// stopped: nothing more is counted, and what was counted stays.
	r.Count(false)
	q(a, c1, "-- name: GetLiveItem :many\nSELECT 1")
	r.TraceBatchStart(b, c2, pgx.TraceBatchStartData{})
	r.TraceBatchQuery(b, c2, pgx.TraceBatchQueryData{SQL: "-- name: X :one\nSELECT 1"})

	want := map[string][][]string{
		"a":       {{"GetLiveItem", "NPMTombstoned"}, {"IdentityTokenBySecret"}, {"GetLiveItem"}},
		"b":       {{"X", "Y"}, {"Z", "X"}},
		"unknown": nil,
		"":        nil,
	}
	for id, w := range want {
		got := r.Of(id).Transactions
		if len(got) != len(w) {
			t.Errorf("request %q: transactions %q, want %q", id, got, w)
			continue
		}
		for i := range w {
			if strings.Join(got[i], ",") != strings.Join(w[i], ",") {
				t.Errorf("request %q: transactions %q, want %q", id, got, w)
				break
			}
		}
	}
	if got := r.Of("a").Statements(); got["GetLiveItem"] != 2 || got["NPMTombstoned"] != 1 || got["IdentityTokenBySecret"] != 1 || len(got) != 3 {
		t.Errorf("the statements of request a by name: %v", got)
	}
}
