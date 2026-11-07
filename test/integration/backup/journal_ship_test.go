//go:build integration

package backup

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/journal"
)

func (e *jenv) tokenID(raw string) string {
	e.t.Helper()
	h := token.Hash(raw)
	return e.stack.Text("SELECT id::text FROM api_token WHERE secret_sha256 = $1", h[:])
}

func (e *jenv) revokedAt(id string) bool {
	e.t.Helper()
	return e.stack.Text("SELECT (revoked_at IS NOT NULL)::text FROM api_token WHERE id = $1::uuid", id) == "true"
}

// unrevoke puts the database back to the state before the revocation, which is what a restore to a point in time does to it.
func (e *jenv) unrevoke(id string) {
	e.stack.Exec("UPDATE api_token SET revoked_at = NULL, revoked_by = NULL, revocation_reason = NULL WHERE id = $1::uuid", id)
}

func pending(t *testing.T, e *jenv) int64 {
	t.Helper()
	n, _, err := e.outbox.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestR_HA_054_RevokedTokenStaysRevokedAfterReplay: a token revoked after the restore point is rejected after the replay
// (acceptance criterion 1 of R-HA-054), the output lists token.revoked as applied, and a second replay reports it as already
// in effect and changes nothing (R-BAK-024).
func TestR_HA_054_RevokedTokenStaysRevokedAfterReplay(t *testing.T) {
	e := newJEnv(t)
	alice := e.stack.User("alice")
	raw := e.stack.Token(alice, scope.Default())
	id := e.tokenID(raw)
	ctx := context.Background()
	if _, err := e.tokens.Validate(ctx, raw); err != nil {
		t.Fatalf("the token must be valid before the revocation: %v", err)
	}
	restorePoint := time.Now().Add(-time.Second)

	if _, err := e.tokens.Revoke(ctx, principal.Caller{Actor: audit.Actor{Kind: "admin-cli", Name: "test"}}, id, token.ReasonAdmin); err != nil {
		t.Fatal(err)
	}
	if n := pending(t, e); n != 1 {
		t.Fatalf("the revocation must leave one outbox row, has %d", n)
	}
	shipped, err := e.shipper.Ship(ctx)
	if err != nil || shipped != 1 {
		t.Fatalf("Ship = %d, %v; want 1 row", shipped, err)
	}
	if n := pending(t, e); n != 0 {
		t.Fatalf("a shipped row must leave the outbox, %d remain", n)
	}
	var keys []string
	if err := e.store.List(ctx, journal.ListPrefix(e.prefix), func(k string) error { keys = append(keys, k); return nil }); err != nil || len(keys) != 1 {
		t.Fatalf("the journal holds %v (%v), want one object", keys, err)
	}

	// The restore: the database is back before the revocation, the token lives again.
	e.unrevoke(id)
	if e.revokedAt(id) {
		t.Fatal("setup: the token must be live in the restored database")
	}
	if _, err := e.tokens.Validate(ctx, raw); err != nil {
		t.Fatalf("setup: the restored token must validate: %v", err)
	}

	r := e.replayer(e.store, e.rec)
	sum, out := e.replayAt(r, restorePoint)
	if sum.Counts[journal.Applied] != 1 || !strings.Contains(out, "token.revoked") || !strings.Contains(out, "applied") {
		t.Fatalf("replay = %v\n%s\nwant token.revoked applied", sum.Counts, out)
	}
	if !e.revokedAt(id) {
		t.Fatal("the token must be revoked after the replay")
	}
	// A replica that starts after the restore has empty caches.
	if _, err := e.freshTokens().Validate(ctx, raw); err == nil {
		t.Fatal("the revoked token must be rejected after the replay")
	}

	again, out := e.replayAt(r, restorePoint)
	if again.Counts[journal.AlreadyInEffect] != 1 || again.Counts[journal.Applied] != 0 {
		t.Fatalf("second replay = %v\n%s\nwant one entry already in effect", again.Counts, out)
	}
	if n := e.stack.Text("SELECT count(*)::text FROM audit_event WHERE action = 'system.restore.journal_replayed'"); n != "2" {
		t.Fatalf("system.restore.journal_replayed written %s times, want 2", n)
	}
}

// TestR_HA_054_ObjectAppearsAfterTheStoreReturns: with the object store unreachable, revoking a token takes effect at once and
// the outbox grows; the journal object appears when the store returns (acceptance criterion 3). The store is a fault-injecting
// wrapper: the shared SeaweedFS is never stopped.
func TestR_HA_054_ObjectAppearsAfterTheStoreReturns(t *testing.T) {
	e := newJEnv(t)
	alice := e.stack.User("alice")
	raw := e.stack.Token(alice, scope.Default())
	id := e.tokenID(raw)
	ctx := context.Background()

	e.store.down.Store(true)
	if _, err := e.tokens.Revoke(ctx, principal.Caller{Actor: audit.Actor{Kind: "admin-cli", Name: "test"}}, id, token.ReasonAdmin); err != nil {
		t.Fatalf("a revocation must not wait for the object store: %v", err)
	}
	if !e.revokedAt(id) {
		t.Fatal("the revocation takes effect at once")
	}
	if n, err := e.shipper.Ship(ctx); err == nil || n != 0 {
		t.Fatalf("Ship = %d, %v with the store down; want 0 shipped and an error", n, err)
	}
	if n := pending(t, e); n != 1 {
		t.Fatalf("the outbox must hold the row while the store is down, has %d", n)
	}
	n, age, err := e.outbox.Pending(ctx)
	if err != nil || n != 1 || age <= 0 {
		t.Fatalf("Pending = %d, %v, %v; want one row with an age (trove_revocation_journal_pending)", n, age, err)
	}

	e.store.down.Store(false)
	if shipped, err := e.shipper.Ship(ctx); err != nil || shipped != 1 {
		t.Fatalf("Ship after the store returned = %d, %v; want 1", shipped, err)
	}
	var keys int
	if err := e.store.List(ctx, journal.ListPrefix(e.prefix), func(string) error { keys++; return nil }); err != nil || keys != 1 {
		t.Fatalf("the journal holds %d objects (%v), want 1", keys, err)
	}
	if n := pending(t, e); n != 0 {
		t.Fatalf("the outbox must be empty, has %d", n)
	}
}

// TestR_HA_054_ShippingTwiceWritesTheSameObject: a row whose object was written and whose deletion did not commit is shipped
// again under the same key with the same bytes; an object is never deleted.
func TestR_HA_054_ShippingTwiceWritesTheSameObject(t *testing.T) {
	e := newJEnv(t)
	alice := e.stack.User("alice")
	id := e.tokenID(e.stack.Token(alice, scope.Default()))
	ctx := context.Background()
	if _, err := e.tokens.Revoke(ctx, principal.Caller{Actor: audit.Actor{Kind: "admin-cli", Name: "test"}}, id, token.ReasonAdmin); err != nil {
		t.Fatal(err)
	}
	rows, err := e.outbox.Claim(ctx, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("Claim = %v, %v", rows, err)
	}
	obj := journal.NewObject(rows[0])
	first, err := obj.Encode()
	if err != nil {
		t.Fatal(err)
	}
	key := journal.Key(e.prefix, obj)
	if err := e.store.Put(ctx, key, first); err != nil { // the object was written, the deletion did not commit
		t.Fatal(err)
	}
	if n, err := e.shipper.Ship(ctx); err != nil || n != 1 {
		t.Fatalf("Ship = %d, %v", n, err)
	}
	got, err := e.store.Get(ctx, key)
	if err != nil || string(got) != string(first) {
		t.Fatalf("the object after a second write differs: %v", err)
	}
	var keys int
	_ = e.store.List(ctx, journal.ListPrefix(e.prefix), func(string) error { keys++; return nil })
	if keys != 1 {
		t.Fatalf("the journal holds %d objects, want 1", keys)
	}
}

// TestR_HA_054_VersionListingOnTheBlobStore: the store lists with versions on request and reads what it listed (the path a
// restore with --credentials-file takes); the bucket of the stack is not versioned, so every object is its only version.
func TestR_HA_054_VersionListingOnTheBlobStore(t *testing.T) {
	e := newJEnv(t)
	ctx := context.Background()
	id := e.put(e.store, time.Now(), "token.revoked", `{"tokenId":"0192f0a1-0000-7000-8000-000000000001","reason":"admin"}`)
	vs := s3.NewJournalStore(e.client, true)
	var got []string
	if err := vs.List(ctx, journal.ListPrefix(e.prefix), func(k string) error { got = append(got, k); return nil }); err != nil || len(got) != 1 {
		t.Fatalf("List with versions = %v, %v; want the one object", got, err)
	}
	body, err := vs.Get(ctx, got[0])
	if err != nil {
		t.Fatal(err)
	}
	o, err := journal.Decode(body)
	if err != nil || o.ID != id {
		t.Fatalf("Get with versions returned %v, %v; want entry %s", o, err, id)
	}
}
