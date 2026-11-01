//go:build integration

package identity

import (
	"context"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/config"
)

// TestR_AUTH_031_LastUsedIsRecordedInBatches: a successful authentication records
// last_used_at and last_used_addr when the replica flushes its buffer, the
// filter lastUsedBefore finds only the tokens not used since, and a second use
// inside last_used_granularity does not change the row again.
func TestR_AUTH_031_LastUsedIsRecordedInBatches(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{tokens: func(c *config.AuthTokens) { c.LastUsedGranularity = time.Hour }})
	alice := a.user("alice")
	used, idle := a.pat(alice), a.pat(alice)
	ctx := context.Background()

	for i := 0; i < 2; i++ { // the second presentation answers from the cache and counts as well
		if _, err := a.bearer(used.Secret, "/api/v1/auth/whoami"); err != nil {
			t.Fatal(err)
		}
	}
	stale := func() map[string]bool {
		recs, err := a.tokens.List(ctx, token.ListFilter{OwnerID: alice.ID, LastUsedBefore: time.Now().Add(-time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]bool{}
		for _, r := range recs {
			m[r.ID] = true
		}
		return m
	}
	if m := stale(); !m[used.Record.ID] || !m[idle.Record.ID] {
		t.Fatalf("before the flush neither token has a last use: %v", m)
	}
	if err := a.tokens.FlushLastUsed(ctx); err != nil {
		t.Fatal(err)
	}
	if m := stale(); m[used.Record.ID] || !m[idle.Record.ID] {
		t.Fatalf("after the flush only the unused token is stale: %v", m)
	}
	recs, err := a.tokens.List(ctx, token.ListFilter{OwnerID: alice.ID, State: "all"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.ID == used.Record.ID && (r.LastUsedAt == nil || time.Since(*r.LastUsedAt) > time.Minute) {
			t.Fatalf("lastUsedAt of the used token = %v", r.LastUsedAt)
		}
		if r.ID == idle.Record.ID && r.LastUsedAt != nil {
			t.Fatalf("lastUsedAt of the idle token = %v, want null", r.LastUsedAt)
		}
	}

	read := func() time.Time {
		var at time.Time
		if err := e.admin.QueryRow(ctx, `SELECT last_used_at FROM api_token WHERE id = $1::uuid`, used.Record.ID).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	first := read()
	time.Sleep(20 * time.Millisecond)
	if _, err := a.bearer(used.Secret, "/api/v1/auth/whoami"); err != nil {
		t.Fatal(err)
	}
	if err := a.tokens.FlushLastUsed(ctx); err != nil {
		t.Fatal(err)
	}
	if second := read(); !second.Equal(first) {
		t.Fatalf("a use inside the granularity changed the row: %v -> %v", first, second)
	}
}
