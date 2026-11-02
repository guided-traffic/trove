//go:build integration

package nuget

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_NUGET_007_APushAndAHardDeleteOfAnotherVersionNeverDeadlock: a push of a new version holds
// the ID's folder FOR SHARE from its link step and then waits for the package row; a hard delete
// of another version holds the package row and empties that version's folder. Were the delete to
// wait for the ID's folder inside its transaction, the two would wait for each other in a circle
// (40P01, broken only by PostgreSQL's deadlock detector and the transaction retry; F-23,
// Q-NUGET-88).
//
// The test holds the row of the deleted version's package file, so that the delete stops with the
// package row locked; lets the push run up to the package row behind it, holding the ID's folder;
// and lets go. Both answer, the ID's folder stays with the new version below it and the deleted
// version's folder is gone. A free race of pushes and hard deletes of other versions on two
// replicas follows. At the end the replicas are stopped, so that every backend of the database has
// flushed its statistics, and PostgreSQL has counted no deadlock in the test's database.
func TestR_NUGET_007_APushAndAHardDeleteOfAnotherVersionNeverDeadlock(t *testing.T) {
	e := newWriteEnv(t)
	// The requests of the test wait behind the test's own lock while it arranges the next one; a
	// loaded runner must not turn that into the 503 of the stack's lock_timeout of 5 s.
	const patient = "TROVE_DATABASE_SESSION_LOCK_TIMEOUT=60s"
	a, b := e.replica("web-a", patient), e.replica("web-b", patient)
	pkg := func(v string) []byte { return nupkg(t, pkgSpec{id: "Acme.Order", version: v}) }
	// 0.9.0 stays throughout, so no push here is the ID's first (which would ask the shadowing
	// check again when the last version went meanwhile).
	race := []string{"1.1.0", "1.1.1", "1.1.2", "1.1.3", "1.1.4", "1.1.5"}
	for _, v := range append([]string{"0.9.0", "1.0.0"}, race...) {
		e.mustPush(a, "nuget-hard", e.tokDev, pkg(v))
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, testenv.PgDSN(t, e.st.Database()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	var holder int64
	if err := conn.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&holder); err != nil {
		t.Fatal(err)
	}
	hold, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.Rollback(context.Background()) }()
	// OF i: the row of the repository stays free, every insert into items checks its key.
	if tag, err := hold.Exec(ctx, `SELECT i.id FROM trove.items i JOIN trove.repositories r ON r.id = i.repo_id
		WHERE r.key = 'nuget-hard' AND i.path = 'acme.order/1.0.0/acme.order.1.0.0.nupkg' FOR UPDATE OF i`); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("lock the package file: %v %v", tag, err)
	}

	deleted := make(chan testenv.Answer, 1)
	go func() { deleted <- e.send(a, "DELETE", "nuget-hard", "acme.order/1.0.0", e.tokDev) }()
	var deleter int64
	waitLocked(t, e, "the delete waiting for the package file with the package row locked", func() bool {
		deleter = e.st.Number(`SELECT COALESCE((SELECT pid FROM pg_stat_activity WHERE $1::int = ANY(pg_blocking_pids(pid)) LIMIT 1), 0)`, holder)
		return deleter != 0
	})
	pushed := make(chan testenv.Answer, 1)
	go func() { pushed <- e.push(b, "nuget-hard", e.tokDev, pkg("2.0.0")) }()
	waitLocked(t, e, "the push waiting for the package row behind the delete", func() bool {
		return e.st.Number(`SELECT count(*) FROM pg_stat_activity WHERE $1::int = ANY(pg_blocking_pids(pid))`, deleter) > 0
	})
	// The premise: the push that waits holds the ID's folder.
	if free := e.st.Number(`SELECT count(*) FROM (SELECT i.id FROM items i JOIN repositories r ON r.id = i.repo_id
		WHERE r.key = 'nuget-hard' AND i.path = 'acme.order' AND i.kind = 'folder' FOR UPDATE OF i SKIP LOCKED) f`); free != 0 {
		t.Fatal("the waiting push does not hold the ID's folder; the test shows nothing")
	}
	if err := hold.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if ans := <-deleted; ans.Status != 204 {
		t.Fatalf("delete: %d %s", ans.Status, ans.Body)
	}
	if ans := <-pushed; ans.Status != 201 {
		t.Fatalf("push: %d %s", ans.Status, ans.Body)
	}
	if kind, _ := e.tombstone("nuget-hard", "acme.order", "1.0.0"); kind == "" {
		t.Error("the deleted version has no tombstone")
	}
	folder := func(path string) int64 {
		return e.st.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id
			WHERE r.key = 'nuget-hard' AND i.path = $1 AND i.kind = 'folder'`, path)
	}
	if folder("acme.order") != 1 || folder("acme.order/2.0.0") != 1 || folder("acme.order/1.0.0") != 0 {
		t.Errorf("folders: acme.order %d, acme.order/2.0.0 %d, acme.order/1.0.0 %d; want 1, 1, 0",
			folder("acme.order"), folder("acme.order/2.0.0"), folder("acme.order/1.0.0"))
	}

	// The free race: a push of a new version and a hard delete of another one at once, on both replicas.
	var wg sync.WaitGroup
	answers := make([]testenv.Answer, 2*len(race))
	for i, v := range race {
		on, other := a, b
		if i%2 == 1 {
			on, other = b, a
		}
		wg.Add(2)
		go func() {
			defer wg.Done()
			answers[2*i] = e.push(on, "nuget-hard", e.tokDev, pkg(fmt.Sprintf("3.0.%d", i)))
		}()
		go func() {
			defer wg.Done()
			answers[2*i+1] = e.send(other, "DELETE", "nuget-hard", "acme.order/"+v, e.tokDev)
		}()
	}
	wg.Wait()
	for i, ans := range answers {
		if want := 201 + 3*(i%2); ans.Status != want {
			t.Errorf("request %d of the race: %d %s, want %d", i, ans.Status, ans.Body, want)
		}
	}
	if got, want := e.versions("nuget-hard", "acme.order"), int64(2+len(race)); got != want {
		t.Errorf("%d versions after the race, want %d", got, want)
	}
	if e.st.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id
		WHERE r.key = 'nuget-hard' AND i.kind = 'folder' AND i.path LIKE 'acme.order/1.%'`) != 0 {
		t.Error("a folder of a deleted version stayed")
	}

	if n := deadlocks(t, e, conn, a, b); n != 0 {
		t.Errorf("PostgreSQL counted %d deadlocks in the database of the test, want none", n)
	}
}

// waitLocked polls cond until it holds and fails with the backends of the database and what
// they wait for when it does not within 30 s, half the lock_timeout the replicas of the test run
// with.
func waitLocked(t *testing.T, e *wenv, what string, cond func() bool) {
	t.Helper()
	for start := time.Now(); time.Since(start) < 30*time.Second; time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	var rows []string
	e.st.Query(`SELECT pid, COALESCE(wait_event_type, ''), COALESCE(wait_event, ''), pg_blocking_pids(pid)::text, left(query, 120)
		FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid() ORDER BY pid`, func(r pgx.Rows) error {
		var pid int32
		var typ, ev, by, query string
		if err := r.Scan(&pid, &typ, &ev, &by, &query); err != nil {
			return err
		}
		rows = append(rows, fmt.Sprintf("%d %s/%s blocked by %s: %s", pid, typ, ev, by, query))
		return nil
	})
	t.Fatalf("%s did not happen within 30s; backends:\n%s", what, strings.Join(rows, "\n"))
}

// deadlocks stops the replicas, ends every other backend of the stack's database (the pool of
// PgBouncer included), waits until they are gone, which flushes their statistics, and returns
// pg_stat_database.deadlocks of the database.
func deadlocks(t *testing.T, e *wenv, conn *pgx.Conn, replicas ...*testenv.Replica) int64 {
	t.Helper()
	for _, r := range replicas {
		if err := r.Stop(); err != nil {
			t.Fatalf("stop %s: %v", r.Name, err)
		}
	}
	if err := conn.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.st.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid()`)
	testenv.WaitFor(t, "every other backend of the database ended", 20*time.Second, func() bool {
		return e.st.Number(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid()`) == 0
	})
	return e.st.Number(`SELECT deadlocks FROM pg_stat_database WHERE datname = current_database()`)
}
