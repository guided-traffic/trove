//go:build integration

package files

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_STOR_018_ARemovalDoesNotWaitForAFolderAnotherTransactionHolds: a removing transaction
// takes a folder it empties only when no other transaction holds it, because it may hold a
// package row that a deploy holding the folder FOR SHARE waits for (Q-NUGET-88). The test holds
// the folder of the only file below it FOR SHARE, as a deploy into it does, and deletes the file:
// the removal commits while the folder is held (the file is gone for every reader) and the folder
// stays under its holder; once the holder ends without a child, the sweep after the commit takes
// the free folder and the emptied folder above it, and the delete answers.
func TestR_STOR_018_ARemovalDoesNotWaitForAFolderAnotherTransactionHolds(t *testing.T) {
	e := newEnv(t)
	e.breakGlass()
	r := e.newReplica("a", replicaOpts{})
	r.createRepository("generic-sweep", nil)
	if res := r.put("admin", "/files/generic-sweep/a/b/only.bin", "only"); res.status != 201 {
		t.Fatalf("%d %s", res.status, res.body)
	}

	ctx := context.Background()
	cfg, err := pgx.ParseConfig(testenv.PgDSN(t, e.db))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if tag, err := tx.Exec(ctx, "SELECT id FROM items WHERE path = 'a/b' AND kind = 'folder' FOR SHARE"); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("hold the folder: %v %v", tag, err)
	}

	done := make(chan response, 1)
	go func() { done <- r.do("admin", "DELETE", "/files/generic-sweep/a/b/only.bin", nil) }()
	waitFor(t, "the removal committed while its folder is held", 8*time.Second, func() bool {
		select {
		case res := <-done:
			t.Fatalf("the delete answered while the removal had not committed: %d %s", res.status, res.body)
		default:
		}
		return e.number("SELECT count(*) FROM items WHERE path = 'a/b/only.bin'") == 0
	})
	if n := e.number("SELECT count(*) FROM items WHERE path IN ('a', 'a/b') AND kind = 'folder'"); n != 2 {
		t.Fatalf("%d of the 2 folders exist while the folder is held", n)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if res := <-done; res.status != 204 {
		t.Fatalf("delete: %d %s", res.status, res.body)
	}
	if n := e.number("SELECT count(*) FROM items WHERE path IN ('a', 'a/b')"); n != 0 {
		t.Errorf("%d emptied folders stayed after the holder ended", n)
	}
}
