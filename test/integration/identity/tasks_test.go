//go:build integration

package identity

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/config"
)

// TestR_AUTH_032_ExpiryNoticeIsWrittenOnce: a PAT expiring in 3 days produces
// exactly one token.expiring event across overlapping runs on two replicas.
func TestR_AUTH_032_ExpiryNoticeIsWrittenOnce(t *testing.T) {
	e := newEnv(t)
	a, b := e.newReplica("a", replicaOpts{valkey: vkDead}), e.newReplica("b", replicaOpts{valkey: vkDead})
	ctx := context.Background()
	u := a.user("alice")
	soon := a.pat(u, func(r *token.CreateRequest) { s := int64(3 * 24 * 3600); r.TTL.Seconds = &s })
	far := a.pat(u)
	revoked := a.pat(u, func(r *token.CreateRequest) { s := int64(2 * 24 * 3600); r.TTL.Seconds = &s })
	if _, err := a.tokens.Revoke(ctx, admin, revoked.Record.ID, token.ReasonOwner); err != nil {
		t.Fatal(err)
	}
	_ = far

	var wg sync.WaitGroup
	for _, r := range []*replica{a, b, a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.tokens.NoticeExpiring(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	n := 0
	for _, r := range []*replica{a, b} {
		for _, ev := range r.rec.named(audit.EventTokenExpiring) {
			n++
			if ev.Subject.ID != soon.Record.ID {
				t.Errorf("a notice for %s", ev.Subject.ID)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d token.expiring events for one token", n)
	}
	if got, _ := a.tokens.NoticeExpiring(ctx); got != 0 {
		t.Fatalf("a later run announced %d tokens again", got)
	}
	// Rotation creates a new row, which starts unnotified.
	rotated, err := a.tokens.Rotate(ctx, token.RotateRequest{Caller: admin, ID: soon.Record.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := a.tokens.NoticeExpiring(ctx); got != 1 {
		t.Fatalf("the rotated token is announced separately: %d", got)
	}
	_ = rotated
}

// TestR_AUTH_023_CleanupDeletesRowsPastRetention: token-cleanup deletes rows
// retention_after_end after expiry or revocation, in batches, and nothing else.
func TestR_AUTH_023_CleanupDeletesRowsPastRetention(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead, tokens: func(c *config.AuthTokens) { c.RetentionAfterEnd = time.Hour }})
	ctx := context.Background()
	u := a.user("alice")
	live, oldRevoked, oldExpired, freshRevoked := a.pat(u), a.pat(u), a.pat(u), a.pat(u)
	if _, err := a.tokens.Revoke(ctx, admin, oldRevoked.Record.ID, token.ReasonOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := a.tokens.Revoke(ctx, admin, freshRevoked.Record.ID, token.ReasonOwner); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE api_token SET revoked_at = now() - interval '2 hours' WHERE id = $1::uuid`, oldRevoked.Record.ID)
	e.exec(`UPDATE api_token SET created_at = now() - interval '3 hours', expires_at = now() - interval '2 hours' WHERE id = $1::uuid`, oldExpired.Record.ID)

	for i := 0; i < 2; i++ { // idempotent
		n, err := a.tokens.Cleanup(ctx)
		if err != nil || (i == 0 && n != 2) || (i == 1 && n != 0) {
			t.Fatalf("run %d deleted %d: %v", i, n, err)
		}
	}
	for id, want := range map[string]int64{live.Record.ID: 1, freshRevoked.Record.ID: 1, oldRevoked.Record.ID: 0, oldExpired.Record.ID: 0} {
		if got := e.scalar(`SELECT count(*) FROM api_token WHERE id = $1::uuid`, id); got != want {
			t.Errorf("token %s: %d rows, want %d", id, got, want)
		}
	}
	// A deleted row is an unknown token.
	if _, err := a.bearer(oldRevoked.Secret, "/x"); err == nil {
		t.Fatal("accepted")
	}
}
