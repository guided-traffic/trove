//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestR_REPO_060_AMemberWriteAndAMoveOfItsRepositoryAreSerialisedByTheDatabase: a member write and a move of its
// repository meet in either order and end in one state, whatever the Go path locked (F-68). The writers below take
// no lock of their own (raw SQL, as a path that forgot the share locks would). A member write in flight makes the
// move wait and then refuse (AI007). A move in flight made the write pass the same-project guard on the project the
// repository was about to leave, because the guard read it before the row's foreign key waited for the move; the
// guard now locks the repositories FOR SHARE before it reads, so the write waits and is refused (AI006).
func TestR_REPO_060_AMemberWriteAndAMoveOfItsRepositoryAreSerialisedByTheDatabase(t *testing.T) {
	st := getStack(t)
	ensureRuntimeRole(t, st)
	_, name, admin := migrated(t, st)
	const web, shop = "bbbbbbbb-0000-0000-0000-000000000001", "bbbbbbbb-0000-0000-0000-000000000002"
	mustExec(t, admin, "INSERT INTO trove.project (id, key, display_name) VALUES ($1, 'web', 'Web'), ($2, 'shop', 'Shop')", web, shop)
	mustExec(t, admin, `INSERT INTO trove.repositories (key, format, class, project_id) VALUES
		('npm-web', 'npm', 'local', $1), ('npm-all', 'npm', 'virtual', $1)`, web)

	const addMember = `INSERT INTO trove.virtual_members (virtual_id, position, member_id)
		SELECT v.id, 0, m.id FROM trove.repositories v, trove.repositories m WHERE v.key = 'npm-all' AND m.key = 'npm-web'`
	const policy = "bbbbbbbb-0000-0000-0000-000000000011"
	mustExec(t, admin, "INSERT INTO trove.authz_policy (id, name, project_id) VALUES ($1, 'web/files', $2)", policy, web)
	const addSelector = `INSERT INTO trove.authz_policy_selector (policy_id, ordinal, kind, repository_id)
		SELECT 'bbbbbbbb-0000-0000-0000-000000000011'::uuid, 0, 'key', id FROM trove.repositories WHERE key = 'npm-web'`
	const move = `UPDATE trove.repositories SET project_id = $1::uuid WHERE key = 'npm-web'`
	reset := func() {
		mustExec(t, admin, "DELETE FROM trove.virtual_members")
		mustExec(t, admin, "DELETE FROM trove.authz_policy_selector")
		mustExec(t, admin, "UPDATE trove.repositories SET project_id = $1::uuid", web)
	}
	// run starts sql on c in the background; the result arrives on the channel.
	run := func(c *pgx.Conn, sql string, args ...any) <-chan error {
		done := make(chan error, 1)
		go func() {
			_, err := c.Exec(context.Background(), sql, args...)
			done <- err
		}()
		return done
	}
	// waits requires the statement of c to wait for a lock held by the other transaction; a statement that
	// finishes first did not conflict with it.
	waits := func(c *pgx.Conn, done <-chan error, what string) {
		t.Helper()
		pid := c.PgConn().PID()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case err := <-done:
				t.Fatalf("%s did not wait for the open transaction (error: %v)", what, err)
			default:
			}
			if scalar[bool](t, admin, "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid = $1 AND wait_event_type = 'Lock')", pid) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("%s neither finished nor waited for a lock", what)
	}
	conn := func() *pgx.Conn { return st.admin(t, name) }

	t.Run("R-REPO-060 a member written in an open transaction makes the move wait, and the move is refused", func(t *testing.T) {
		reset()
		writer, mover := conn(), conn()
		ctx := context.Background()
		if _, err := writer.Exec(ctx, "BEGIN"); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Exec(ctx, addMember); err != nil {
			t.Fatal(err)
		}
		done := run(mover, move, shop)
		waits(mover, done, "the move")
		if _, err := writer.Exec(ctx, "COMMIT"); err != nil {
			t.Fatal(err)
		}
		if got := sqlState(<-done); got != "AI007" {
			t.Errorf("the move after a member write: SQLSTATE %q, want AI007", got)
		}
		assertNoForeignMember(t, admin)
	})

	t.Run("R-REPO-060 a move in an open transaction makes the member write wait, and the write is refused", func(t *testing.T) {
		reset()
		writer, mover := conn(), conn()
		ctx := context.Background()
		if _, err := mover.Exec(ctx, "BEGIN"); err != nil {
			t.Fatal(err)
		}
		if _, err := mover.Exec(ctx, move, shop); err != nil {
			t.Fatal(err)
		}
		done := run(writer, addMember)
		waits(writer, done, "the member write")
		if _, err := mover.Exec(ctx, "COMMIT"); err != nil {
			t.Fatal(err)
		}
		if got := sqlState(<-done); got != "AI006" {
			t.Errorf("the member write after a move: SQLSTATE %q, want AI006", got)
		}
		assertNoForeignMember(t, admin)
	})

	t.Run("R-REPO-060 a key selector written in an open transaction makes the move wait, and the move is refused", func(t *testing.T) {
		reset()
		writer, mover := conn(), conn()
		ctx := context.Background()
		if _, err := writer.Exec(ctx, "BEGIN"); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Exec(ctx, addSelector); err != nil {
			t.Fatal(err)
		}
		done := run(mover, move, shop)
		waits(mover, done, "the move")
		if _, err := writer.Exec(ctx, "COMMIT"); err != nil {
			t.Fatal(err)
		}
		if got := sqlState(<-done); got != "AI007" {
			t.Errorf("the move after a key selector write: SQLSTATE %q, want AI007", got)
		}
		assertNoForeignSelector(t, admin)
	})

	t.Run("R-REPO-060 a move in an open transaction makes the key selector write wait, and the write is refused", func(t *testing.T) {
		reset()
		writer, mover := conn(), conn()
		ctx := context.Background()
		if _, err := mover.Exec(ctx, "BEGIN"); err != nil {
			t.Fatal(err)
		}
		if _, err := mover.Exec(ctx, move, shop); err != nil {
			t.Fatal(err)
		}
		done := run(writer, addSelector)
		waits(writer, done, "the key selector write")
		if _, err := mover.Exec(ctx, "COMMIT"); err != nil {
			t.Fatal(err)
		}
		if got := sqlState(<-done); got != "AI007" {
			t.Errorf("the key selector write after a move: SQLSTATE %q, want AI007", got)
		}
		assertNoForeignSelector(t, admin)
	})
}

// assertNoForeignSelector requires that no key selector of a project policy names a repository of another project.
func assertNoForeignSelector(t *testing.T, admin *pgx.Conn) {
	t.Helper()
	if n := scalar[int](t, admin, `SELECT count(*) FROM trove.authz_policy_selector s
		JOIN trove.authz_policy a ON a.id = s.policy_id JOIN trove.repositories r ON r.id = s.repository_id
		WHERE a.project_id IS NOT NULL AND a.project_id IS DISTINCT FROM r.project_id`); n != 0 {
		t.Errorf("%d key selectors of project policies name a repository of another project", n)
	}
}

// assertNoForeignMember requires that no member row names a repository of another project than its virtual repository.
func assertNoForeignMember(t *testing.T, admin *pgx.Conn) {
	t.Helper()
	if n := scalar[int](t, admin, `SELECT count(*) FROM trove.virtual_members vm
		JOIN trove.repositories v ON v.id = vm.virtual_id JOIN trove.repositories m ON m.id = vm.member_id
		WHERE v.project_id IS DISTINCT FROM m.project_id`); n != 0 {
		t.Errorf("%d member rows name a repository of another project", n)
	}
}
