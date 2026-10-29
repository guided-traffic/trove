//go:build integration

package identity

import (
	"context"
	"crypto/sha256"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
)

// The token lineage under rotation and concurrency (R-AUTH-027, R-AUTH-029,
// R-AUTH-030, Q-AUTH-89): a token never escapes the revocation of the token it
// was derived from, by rotating or by racing the revocation.

// conn opens a second connection to the test database, for a transaction that
// holds locks while the service runs.
func (e *env) conn() *pgx.Conn {
	e.t.Helper()
	c := e.st.admin(e.t, e.db)
	if _, err := c.Exec(context.Background(), "SET search_path = "+schemaName); err != nil {
		e.t.Fatal(err)
	}
	return c
}

// stillRunning reports whether done has not been closed within d.
func stillRunning(done <-chan struct{}, d time.Duration) bool {
	select {
	case <-done:
		return false
	case <-time.After(d):
		return true
	}
}

func TestR_AUTH_029_RotationKeepsTheLineage(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead})
	ctx := context.Background()
	alice := a.user("alice")
	secs := int64(7200)
	parent := a.pat(alice, func(r *token.CreateRequest) { r.SelfRotatable = true; r.TTL.Seconds = &secs })
	pinfo, err := a.tokens.Validate(ctx, parent.Secret)
	if err != nil {
		t.Fatal(err)
	}
	derive := func(selfRotatable bool) (token.Created, error) {
		s := int64(3600)
		return a.tokens.Create(ctx, token.CreateRequest{
			Caller: admin, Type: token.TypePAT, OwnerID: alice.ID, Description: "child", Via: "api",
			Parent: &pinfo, SelfRotatable: selfRotatable, TTL: token.TTLRequest{Seconds: &s},
		})
	}

	t.Run("R-AUTH-029 a derived token that rotated itself is revoked with its parent", func(t *testing.T) {
		child, err := derive(true)
		if err != nil {
			t.Fatal(err)
		}
		succ, err := a.tokens.Rotate(ctx, token.RotateRequest{Caller: admin, ID: child.Record.ID, Self: true})
		if err != nil {
			t.Fatal(err)
		}
		if succ.Record.DerivedFrom != parent.Record.ID {
			t.Fatalf("the successor's derivedFrom is %q, want the parent %s", succ.Record.DerivedFrom, parent.Record.ID)
		}
		if succ.Record.ExpiresAt == nil || succ.Record.ExpiresAt.After(*parent.Record.ExpiresAt) {
			t.Fatalf("the successor expires %v, after its parent %v", succ.Record.ExpiresAt, parent.Record.ExpiresAt)
		}
		if _, err := a.tokens.Revoke(ctx, admin, parent.Record.ID, token.ReasonOwner); err != nil {
			t.Fatal(err)
		}
		if _, err := a.bearer(succ.Secret, "/x"); errCode(err) != errs.CodeInvalidToken {
			t.Fatalf("the successor survived its parent's revocation: %v", err)
		}
		rec, _ := a.tokens.Get(ctx, succ.Record.ID)
		if rec.RevocationReason != token.ReasonParentRevoked {
			t.Fatalf("reason %q", rec.RevocationReason)
		}
	})

	t.Run("R-AUTH-027 only a token that may rotate itself derives one that may", func(t *testing.T) {
		plain := a.pat(alice, func(r *token.CreateRequest) { r.TTL.Seconds = &secs })
		info, err := a.tokens.Validate(ctx, plain.Secret)
		if err != nil {
			t.Fatal(err)
		}
		s := int64(600)
		_, err = a.tokens.Create(ctx, token.CreateRequest{
			Caller: admin, Type: token.TypePAT, OwnerID: alice.ID, Description: "child", Via: "api",
			Parent: &info, SelfRotatable: true, TTL: token.TTLRequest{Seconds: &s},
		})
		wantCode(t, err, 403, "derivation_exceeds_parent")
	})
}

func TestR_AUTH_027_ARevokedTokenStillCachedMintsNothing(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead})
	s := a.serve(e, nil)
	alice := a.user("alice")
	p := a.pat(alice)
	// The replica caches the token, then the token is revoked behind its back: no
	// change record, so the cache accepts it until the safety TTL (R-AUTH-023).
	want(t, s.call(get, "/api/v1/tokens/current", p.Secret, nil), 200, "")
	e.exec(`UPDATE api_token SET revoked_at = now(), revocation_reason = 'owner' WHERE id = $1::uuid`, p.Record.ID)
	want(t, s.call(get, "/api/v1/tokens/current", p.Secret, nil), 200, "")
	rp := s.call(post, "/api/v1/tokens", p.Secret, map[string]any{"description": "child", "ttlSeconds": 600})
	want(t, rp, 401, errs.CodeInvalidToken)
	if n := e.scalar(`SELECT count(*) FROM api_token WHERE derived_from = $1::uuid`, p.Record.ID); n != 0 {
		t.Fatalf("a revoked token minted %d children", n)
	}
}

func TestR_AUTH_030_RotationLimits(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead, tokens: func(c *config.AuthTokens) {
		c.MaxActive.PAT = 2
		c.PAT.AllowedGroups = []string{"developers"}
	}})
	ctx := context.Background()
	bob := a.user("bob")
	if _, err := a.manager.CreateGroup(ctx, admin, principal.NewGroup{Name: "developers", Members: []string{"bob"}}); err != nil {
		t.Fatal(err)
	}

	t.Run("R-AUTH-026 overlapping rotations cannot exceed the limit of active tokens", func(t *testing.T) {
		p1 := a.pat(bob)
		a.pat(bob)
		p1b, err := a.tokens.Rotate(ctx, token.RotateRequest{Caller: admin, ID: p1.Record.ID, OverlapSeconds: 3600})
		if err != nil {
			t.Fatalf("a rotation at the limit replaces its predecessor: %v", err)
		}
		_, err = a.tokens.Rotate(ctx, token.RotateRequest{Caller: admin, ID: p1b.Record.ID, OverlapSeconds: 3600})
		wantCode(t, err, 409, errs.CodeTokenLimitReached)
	})

	t.Run("R-AUTH-030 a token that was rotated cannot be rotated again", func(t *testing.T) {
		if _, err := a.tokens.RevokeAll(ctx, admin, bob.ID, token.ReasonAdmin); err != nil {
			t.Fatal(err)
		}
		p := a.pat(bob)
		if _, err := a.tokens.Rotate(ctx, token.RotateRequest{Caller: admin, ID: p.Record.ID, OverlapSeconds: 3600}); err != nil {
			t.Fatal(err)
		}
		_, err := a.tokens.Rotate(ctx, token.RotateRequest{Caller: admin, ID: p.Record.ID, OverlapSeconds: 3600})
		wantCode(t, err, 404, errs.CodeNotFound)
	})

	t.Run("R-AUTH-017 a rotation of a personal token applies the group restriction", func(t *testing.T) {
		if _, err := a.tokens.RevokeAll(ctx, admin, bob.ID, token.ReasonAdmin); err != nil {
			t.Fatal(err)
		}
		p := a.pat(bob)
		if _, err := a.manager.PatchMembers(ctx, admin, "developers", "manual", nil, []string{"bob"}); err != nil {
			t.Fatal(err)
		}
		_, err := a.tokens.Rotate(ctx, token.RotateRequest{Caller: admin, ID: p.Record.ID})
		wantCode(t, err, 403, errs.CodePATNotAllowed)
	})
}

func TestR_AUTH_030_ConcurrentRotationsLeaveOneSuccessor(t *testing.T) {
	e := newEnv(t)
	a, b := e.newReplica("a", replicaOpts{valkey: vkDead}), e.newReplica("b", replicaOpts{valkey: vkDead})
	ctx := context.Background()
	sa := a.serviceAccount("ci-bot")
	s1 := a.sat(sa)
	var wg sync.WaitGroup
	errsOf := make([]error, 2)
	start := make(chan struct{})
	for i, r := range []*replica{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errsOf[i] = r.tokens.Rotate(ctx, token.RotateRequest{Caller: admin, ID: s1.Record.ID, OverlapSeconds: 3600})
		}()
	}
	close(start)
	wg.Wait()
	ok := 0
	for _, err := range errsOf {
		switch {
		case err == nil:
			ok++
		case errCode(err) != errs.CodeNotFound:
			t.Fatalf("the losing rotation: %v, want 404", err)
		}
	}
	if n := e.scalar(`SELECT count(*) FROM api_token WHERE rotated_from = $1::uuid`, s1.Record.ID); ok != 1 || n != 1 {
		t.Fatalf("%d rotations succeeded, %d successors exist; want one", ok, n)
	}
}

func TestR_AUTH_029_ARevocationAndADerivationAtTheSameTime(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead})
	ctx := context.Background()
	alice := a.user("alice")
	secs := int64(7200)

	t.Run("R-AUTH-027 a create waits for a revocation of its parent and then refuses", func(t *testing.T) {
		parent := a.pat(alice, func(r *token.CreateRequest) { r.TTL.Seconds = &secs })
		info, err := a.tokens.Validate(ctx, parent.Secret)
		if err != nil {
			t.Fatal(err)
		}
		// A revocation of the parent that has not committed yet.
		tx, err := e.conn().Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `UPDATE api_token SET revoked_at = now(), revocation_reason = 'owner' WHERE id = $1::uuid`, parent.Record.ID); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		var cerr error
		go func() {
			defer close(done)
			s := int64(600)
			_, cerr = a.tokens.Create(ctx, token.CreateRequest{
				Caller: admin, Type: token.TypePAT, OwnerID: alice.ID, Description: "child", Via: "api",
				Parent: &info, TTL: token.TTLRequest{Seconds: &s},
			})
		}()
		if !stillRunning(done, 300*time.Millisecond) {
			t.Fatalf("the create did not wait for the revocation of its parent: %v", cerr)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		<-done
		wantCode(t, cerr, 401, errs.CodeInvalidToken)
		if n := e.scalar(`SELECT count(*) FROM api_token WHERE derived_from = $1::uuid`, parent.Record.ID); n != 0 {
			t.Fatalf("%d children of a revoked token were written", n)
		}
	})

	t.Run("R-AUTH-029 a revocation waits for a child being created and revokes it", func(t *testing.T) {
		parent := a.pat(alice, func(r *token.CreateRequest) { r.TTL.Seconds = &secs })
		// A derivation that holds its parent's lock and has not committed yet.
		tx, err := e.conn().Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SELECT 1 FROM api_token WHERE id = $1::uuid FOR UPDATE`, parent.Record.ID); err != nil {
			t.Fatal(err)
		}
		child := principal.NewID()
		raw, _ := token.Generate(token.TypePAT)
		sum := sha256.Sum256([]byte(raw))
		if _, err := tx.Exec(ctx, `INSERT INTO api_token (id, type, secret_sha256, hint, principal_id, description, scope, created_via, expires_at, derived_from)
			VALUES ($1::uuid, 'pat', $2, 'trv_pat_x', $3::uuid, 'child', '{}'::jsonb, 'api', now() + interval '10 minutes', $4::uuid)`,
			child, sum[:], alice.ID, parent.Record.ID); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		var rerr error
		go func() {
			defer close(done)
			_, rerr = a.tokens.Revoke(ctx, admin, parent.Record.ID, token.ReasonOwner)
		}()
		if !stillRunning(done, 300*time.Millisecond) {
			t.Fatalf("the revocation did not wait for the derivation: %v", rerr)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		<-done
		if rerr != nil {
			t.Fatal(rerr)
		}
		if n := e.scalar(`SELECT count(*) FROM api_token WHERE id = $1::uuid AND revoked_at IS NULL`, child); n != 0 {
			t.Fatal("the child that committed while the revocation waited survived it")
		}
	})

	t.Run("R-AUTH-030 a rotation waits for a revocation of the rotated token and then refuses", func(t *testing.T) {
		p := a.pat(alice, func(r *token.CreateRequest) { r.TTL.Seconds = &secs })
		tx, err := e.conn().Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `UPDATE api_token SET revoked_at = now(), revocation_reason = 'owner' WHERE id = $1::uuid`, p.Record.ID); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		var rerr error
		go func() {
			defer close(done)
			_, rerr = a.tokens.Rotate(ctx, token.RotateRequest{Caller: admin, ID: p.Record.ID, OverlapSeconds: 60})
		}()
		if !stillRunning(done, 300*time.Millisecond) {
			t.Fatalf("the rotation did not wait: %v", rerr)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		<-done
		wantCode(t, rerr, 404, errs.CodeNotFound)
		if n := e.scalar(`SELECT count(*) FROM api_token WHERE rotated_from = $1::uuid`, p.Record.ID); n != 0 {
			t.Fatal("a revoked token got a successor")
		}
	})
}
