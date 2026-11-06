package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/guided-traffic/trove/internal/errs"
)

func TestUnavailableCodes(t *testing.T) {
	t.Run("R-ARCH-019 a statement timeout is database_timeout, a lost connection database_unavailable", func(t *testing.T) {
		for _, code := range []string{"57014", "55P03"} {
			e, ok := errs.As(Unavailable(&pgconn.PgError{Code: code}))
			if !ok || e.Code != errs.CodeDatabaseTimeout || e.Status() != 503 {
				t.Errorf("%s: %+v", code, e)
			}
		}
		e, _ := errs.As(Unavailable(errors.New("connection refused")))
		if e.Code != errs.CodeDatabaseUnavailable {
			t.Errorf("code = %s", e.Code)
		}
	})
}

func statementTimeoutError() *pgconn.PgError {
	return &pgconn.PgError{Severity: "ERROR", Code: "57014", Message: "canceling statement due to statement timeout"}
}

func TestStatementTimeoutNamesItsStatement(t *testing.T) {
	t.Run("R-VULN-028 a sqlc query is named by its header in the logged error", func(t *testing.T) {
		err := statementTimeoutError()
		if !tagStatementTimeout(err, "-- name: VulnAliasClosure :many\nSELECT 1") {
			t.Fatal("a statement timeout was not recognised")
		}
		got := Unavailable(err).Error()
		if !strings.Contains(got, "database_timeout") || !strings.Contains(got, "[statement: VulnAliasClosure]") {
			t.Errorf("logged error %q does not name the statement", got)
		}
	})
	t.Run("R-VULN-028 a statement without header is named by its start, bounded", func(t *testing.T) {
		err := statementTimeoutError()
		tagStatementTimeout(err, "SELECT  a,\n b FROM t WHERE "+strings.Repeat("x", 200))
		if !strings.Contains(err.Message, "[statement: SELECT a, b FROM t WHERE xxx") || !strings.HasSuffix(err.Message, "...]") || len(err.Message) > 200 {
			t.Errorf("message %q", err.Message)
		}
	})
	t.Run("R-VULN-028 the tag is added once and the client-facing message does not carry it", func(t *testing.T) {
		err := statementTimeoutError()
		tagStatementTimeout(err, "-- name: A :one")
		tagStatementTimeout(err, "-- name: B :one")
		if n := strings.Count(err.Message, "[statement: "); n != 1 {
			t.Errorf("%d tags", n)
		}
		e, _ := errs.As(Unavailable(err))
		if strings.Contains(e.Detail, "statement") {
			t.Errorf("message %q", e.Detail)
		}
	})
	t.Run("R-VULN-028 other errors stay untouched", func(t *testing.T) {
		err := &pgconn.PgError{Code: "23505", Message: "duplicate"}
		if tagStatementTimeout(err, "-- name: A :one") || err.Message != "duplicate" {
			t.Errorf("%q", err.Message)
		}
	})
	t.Run("R-VULN-028 the tracer tags the error of a query and hands the event on", func(t *testing.T) {
		var ended bool
		tr := stmtTracer{next: endSpy{&ended}}
		ctx := tr.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "-- name: VulnX :exec\nUPDATE t"})
		err := statementTimeoutError()
		tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: err})
		if !ended || !strings.Contains(err.Message, "[statement: VulnX]") {
			t.Errorf("ended=%v message %q", ended, err.Message)
		}
	})
}

type endSpy struct{ ended *bool }

func (endSpy) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}
func (s endSpy) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) { *s.ended = true }
