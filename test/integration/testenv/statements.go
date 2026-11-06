package testenv

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The statements a replica sends, counted from the PostgreSQL log: a test turns log_statement on for its own database (every
// test has a database of its own, so no other test's statement is in the window) before its replicas connect, and reads the
// container's log between two instants. A statement is counted by its sqlc name.

var statementLine = regexp.MustCompile(`(?:statement|execute [^:]*): (.*)$`)

// LogStatements turns the statement log on for the database of the stack; replicas started after it log their statements.
// The test is skipped when docker is not available.
func (s *Stack) LogStatements(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not available: the statements are counted from the PostgreSQL log")
	}
	s.Exec("ALTER DATABASE " + s.Database() + " SET log_statement = 'all'")
}

// Only databases that set log_statement are logged, so the log holds the statements of the tests that turned it on for their own
// database. The line prefix of the stack (%m [%p]) names no database, so a test that runs beside another one that logs too cannot
// tell their statements apart; filtering by database needs %d in log_line_prefix, a change of test/integration/compose.yaml and a
// restart of the stack, which is not made here. The counting tests therefore run on a stack no other logging test shares.
//
// StatementsSince returns the statements the database logged since an instant, without the transaction control.
func StatementsSince(t testing.TB, at time.Time) []string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "docker", "logs", "--since", at.UTC().Format(time.RFC3339Nano), "trove-it-postgres").CombinedOutput() //nolint:gosec // constant arguments and a formatted instant
	if err != nil {
		t.Fatalf("docker logs: %v\n%s", err, out)
	}
	var stmts []string
	for _, line := range strings.Split(string(out), "\n") {
		m := statementLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		s := strings.TrimSpace(m[1])
		switch strings.ToUpper(strings.Fields(s + " x")[0]) {
		case "BEGIN", "COMMIT", "ROLLBACK", "SET", "RESET", "SHOW", "DISCARD", "DEALLOCATE":
			continue
		}
		stmts = append(stmts, s)
	}
	return stmts
}

// StatementName is the sqlc name of a statement ("-- name: GetLiveItem :many"), or its first words.
func StatementName(s string) string {
	if i := strings.Index(s, "-- name: "); i >= 0 {
		// The first word after the marker, without splitting the rest of the statement (RequestTransactions names every
		// statement of a request with it while the request runs).
		rest := strings.TrimLeft(s[i+len("-- name: "):], " \t\r\n")
		if j := strings.IndexAny(rest, " \t\r\n"); j >= 0 {
			rest = rest[:j]
		}
		if rest != "" {
			return rest
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}
