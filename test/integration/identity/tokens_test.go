//go:build integration

package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
)

var tokenPattern = regexp.MustCompile(`trv_(?:pat|sat|cit)_[0-9A-Za-z]{5,}`)

func errCode(err error) string {
	if e, ok := errs.As(err); ok {
		return e.Code
	}
	return ""
}

func wantCode(t *testing.T, err error, status int, code string) {
	t.Helper()
	e, ok := errs.As(err)
	if !ok || e.Status() != status || e.Code != code {
		t.Fatalf("err = %v, want %d %s", err, status, code)
	}
}

// TestE1_2_NoTokenValueInTheDatabase is the domain part of exit criterion E1.2
// (R-AUTH-023, R-AUTH-022): after tokens were created, rotated, used, revoked
// and rejected, a scan of every table in text form finds no token value, the
// audit events carry none, and the stored hash is the SHA-256 of the token.
func TestE1_2_NoTokenValueInTheDatabase(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{})
	alice := a.user("alice")
	sa := a.serviceAccount("ci-bot")

	var secrets []string
	pat := a.pat(alice)
	secrets = append(secrets, pat.Secret)
	sat := a.sat(sa)
	secrets = append(secrets, sat.Secret)
	rot, err := a.tokens.Rotate(context.Background(), token.RotateRequest{Caller: admin, ID: sat.Record.ID, OverlapSeconds: 0})
	if err != nil {
		t.Fatal(err)
	}
	secrets = append(secrets, rot.Secret)
	if _, err := a.bearer(pat.Secret, "/api/v1/auth/whoami"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.tokens.RevokeByValue(context.Background(), admin, pat.Secret); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // a revoked token presented again: token.rejected
		if _, err := a.bearer(pat.Secret, "/api/v1/auth/whoami"); err == nil {
			t.Fatal("a revoked token authenticated")
		}
	}

	// A full-table text scan, as a stand-in for a dump.
	tables := scanTables(t, e)
	if len(tables) < 10 {
		t.Fatalf("scanned only %d tables", len(tables))
	}
	for _, s := range secrets {
		for _, tbl := range tables {
			if n := e.scalar(fmt.Sprintf(`SELECT count(*) FROM %s x WHERE x::text LIKE '%%' || $1 || '%%'`, tbl), s[8:]); n != 0 {
				t.Errorf("R-AUTH-023 table %s contains token material", tbl)
			}
			if n := e.scalar(fmt.Sprintf(`SELECT count(*) FROM %s x WHERE x::text LIKE '%%' || $1 || '%%'`, tbl), s); n != 0 {
				t.Errorf("R-AUTH-023 table %s contains a token value", tbl)
			}
		}
		sum := sha256.Sum256([]byte(s))
		if n := e.scalar(`SELECT count(*) FROM api_token WHERE encode(secret_sha256, 'hex') = $1`, hex.EncodeToString(sum[:])); n != 1 {
			t.Errorf("R-AUTH-023 the hash of a live token is stored %d times", n)
		}
	}
	for _, ev := range a.rec.all() {
		raw, _ := json.Marshal(ev)
		if tokenPattern.Match(raw) || strings.Contains(string(raw), "eyJ") {
			t.Errorf("R-AUTH-041 event %s carries a secret: %s", ev.Type, raw)
		}
	}
	if len(a.rec.named(audit.EventTokenCreated)) != 3 || len(a.rec.named(audit.EventTokenRevoked)) != 1 {
		t.Errorf("events: created %d, revoked %d", len(a.rec.named(audit.EventTokenCreated)), len(a.rec.named(audit.EventTokenRevoked)))
	}
	t.Run("R-AUTH-023 token.rejected is written once per token and hour", func(t *testing.T) {
		if n := len(a.rec.named(audit.EventTokenRejected)); n != 1 {
			t.Fatalf("%d token.rejected events for three uses of one revoked token", n)
		}
	})
}

func scanTables(t *testing.T, e *env) []string {
	t.Helper()
	rows, err := e.admin.Query(context.Background(), `SELECT table_name FROM information_schema.tables WHERE table_schema = 'trove' AND table_type = 'BASE TABLE' AND table_name NOT LIKE 'audit_event_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, "trove."+n)
	}
	return out
}

// TestR_AUTH_029_RevocationReachesTheOtherReplicaWithinTheBound is the revocation
// part of E1.2 and R-AUTH-042: revoke through replica A, replica B rejects within
// 2 s, with Valkey running and with Valkey stopped.
func TestR_AUTH_029_RevocationReachesTheOtherReplicaWithinTheBound(t *testing.T) {
	for _, mode := range []vkMode{vkLive, vkDead} {
		t.Run(mode.String(), func(t *testing.T) {
			e := newEnv(t)
			a, b := e.newReplica("a", replicaOpts{valkey: mode}), e.newReplica("b", replicaOpts{valkey: mode})
			u := a.user("alice")
			pat := a.pat(u)
			for _, r := range []*replica{a, b} { // both cache the token
				if res, err := r.bearer(pat.Secret, "/api/v1/auth/whoami"); err != nil || res.Principal.Name != "alice" {
					t.Fatalf("%s: %+v %v", r.name, res, err)
				}
			}
			if _, err := a.tokens.Revoke(context.Background(), principal.Caller{Actor: audit.Actor{Kind: "user", ID: u.ID, Name: "alice"}}, pat.Record.ID, token.ReasonOwner); err != nil {
				t.Fatal(err)
			}
			took := waitFor(t, "replica B rejecting the revoked token", revocationBound, func() bool {
				_, err := b.bearer(pat.Secret, "/api/v1/auth/whoami")
				return errCode(err) == errs.CodeInvalidToken
			})
			t.Logf("%s: replica B rejected the token after %s", mode, took)
			if _, err := a.bearer(pat.Secret, "/api/v1/auth/whoami"); errCode(err) != errs.CodeInvalidToken {
				t.Fatalf("the revoking replica: %v", err)
			}
		})
	}
}

// TestR_AUTH_023_ClosedGateBypassesTheCache: while the replica's change log is
// older than the revocation bound, every lookup goes to PostgreSQL and is not
// cached, so a revocation is seen at once (Q-AUTH-10).
func TestR_AUTH_023_ClosedGateBypassesTheCache(t *testing.T) {
	e := newEnv(t)
	a, b := e.newReplica("a", replicaOpts{valkey: vkDead}), e.newReplica("b", replicaOpts{valkey: vkDead})
	u := a.user("alice")
	pat := a.pat(u)
	if _, err := b.bearer(pat.Secret, "/x"); err != nil {
		t.Fatal(err)
	}
	b.fresh.off.Store(true)
	if _, err := a.tokens.Revoke(context.Background(), admin, pat.Record.ID, token.ReasonAdmin); err != nil {
		t.Fatal(err)
	}
	// No waiting for a poll: the cached acceptance of B is not served.
	if _, err := b.bearer(pat.Secret, "/x"); errCode(err) != errs.CodeInvalidToken {
		t.Fatalf("a replica with a stale change log answered from its cache: %v", err)
	}
	b.caches.FlushAll("test")
	p2 := a.pat(u)
	for i := 0; i < 3; i++ {
		if _, err := b.bearer(p2.Secret, "/x"); err != nil {
			t.Fatal(err)
		}
	}
	if n := b.tokenCacheEntries(); n != 0 {
		t.Fatalf("%d entries cached while the gate was closed", n)
	}
	b.fresh.off.Store(false)
	if _, err := b.bearer(p2.Secret, "/x"); err != nil {
		t.Fatal(err)
	}
	if n := b.tokenCacheEntries(); n != 1 {
		t.Fatalf("%d entries cached after the gate reopened", n)
	}
}

func (r *replica) tokenCacheEntries() int {
	for _, s := range r.caches.Stats() {
		if s.Name == "auth_tokens" {
			return s.Entries
		}
	}
	return -1
}

// TestR_AUTH_023_ValidationReadsDatabaseWhenCacheIsEmpty: lookups that miss the
// cache read PostgreSQL, cache only live tokens, and check expiry on every hit.
func TestR_AUTH_023_ValidationReadsDatabaseWhenCacheIsEmpty(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead})
	u := a.user("alice")
	pat := a.pat(u)
	if _, err := a.bearer(pat.Secret, "/x"); err != nil {
		t.Fatal(err)
	}
	if a.tokenCacheEntries() != 1 {
		t.Fatal("a valid token is cached")
	}
	t.Run("R-AUTH-023 an expired token is rejected and not cached", func(t *testing.T) {
		short := a.pat(u, func(r *token.CreateRequest) { s := int64(60); r.TTL.Seconds = &s })
		e.exec(`UPDATE api_token SET expires_at = now() - interval '1 second', created_at = now() - interval '2 minutes' WHERE id = $1::uuid`, short.Record.ID)
		a.caches.FlushAll("test")
		_, err := a.bearer(short.Secret, "/x")
		wantCode(t, err, 401, errs.CodeInvalidToken)
		if a.tokenCacheEntries() != 0 {
			t.Fatal("an expired token was cached")
		}
	})
	t.Run("R-AUTH-023 a cached token expires on its own clock", func(t *testing.T) {
		soon := a.pat(u, func(r *token.CreateRequest) { s := int64(61); r.TTL.Seconds = &s })
		if _, err := a.bearer(soon.Secret, "/x"); err != nil {
			t.Fatal(err)
		}
		e.exec(`UPDATE api_token SET expires_at = now() + interval '1 second' WHERE id = $1::uuid`, soon.Record.ID)
		a.caches.FlushAll("test")
		if _, err := a.bearer(soon.Secret, "/x"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1200 * time.Millisecond)
		_, err := a.bearer(soon.Secret, "/x")
		wantCode(t, err, 401, errs.CodeInvalidToken)
	})
}

// TestR_AUTH_027_CreationRules covers token creation against PostgreSQL.
func TestR_AUTH_027_CreationRules(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead, tokens: func(c *config.AuthTokens) {
		c.MaxActive = config.AuthTokenMax{PAT: 3, SAT: 2}
		c.SAT.AllowNonExpiring = true
	}})
	b := e.newReplica("b", replicaOpts{valkey: vkDead, tokens: func(c *config.AuthTokens) { c.MaxActive = config.AuthTokenMax{PAT: 3, SAT: 2} }})
	alice := a.user("alice")

	t.Run("R-AUTH-026 the default lifetime is 90 days, also for an administrator", func(t *testing.T) {
		c := a.pat(alice)
		if d := time.Until(*c.Record.ExpiresAt); d < 89*24*time.Hour || d > 91*24*time.Hour {
			t.Fatalf("expires in %s", d)
		}
		if len(c.Secret) != 57 || c.Record.Hint != c.Secret[:12] {
			t.Fatalf("secret %q hint %q", c.Secret, c.Record.Hint)
		}
	})
	t.Run("R-AUTH-026 lifetime errors", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			secs int64
			code string
		}{{"above the maximum", 40000000, "ttl_exceeds_maximum"}, {"zero for a PAT", 0, "non_expiring_not_allowed"}, {"negative", -5, "invalid_ttl"}, {"30 seconds", 30, "invalid_ttl"}} {
			secs := tc.secs
			_, err := a.tokens.Create(context.Background(), token.CreateRequest{Caller: admin, Type: token.TypePAT, OwnerID: alice.ID, Description: "x", TTL: token.TTLRequest{Seconds: &secs}, Via: "api"})
			wantCode(t, err, 400, tc.code)
		}
	})
	t.Run("R-AUTH-027 a description is required", func(t *testing.T) {
		_, err := a.tokens.Create(context.Background(), token.CreateRequest{Caller: admin, Type: token.TypePAT, OwnerID: alice.ID, Via: "api"})
		wantCode(t, err, 400, "description_required")
	})
	t.Run("R-AUTH-024 an invalid scope is refused", func(t *testing.T) {
		_, err := a.tokens.Create(context.Background(), token.CreateRequest{
			Caller: admin, Type: token.TypePAT, OwnerID: alice.ID, Description: "x",
			Scope: []byte(`{"repositories":[{"repositories":["a"],"include":["@acme/*"],"actions":["read"]}]}`), Via: "api",
		})
		wantCode(t, err, 400, "invalid_scope")
	})
	t.Run("R-AUTH-026 a non-expiring SAT by an administrator, not by anyone else", func(t *testing.T) {
		sa := a.serviceAccount("ci-bot")
		zero := int64(0)
		c := a.sat(sa, func(r *token.CreateRequest) { r.TTL.Seconds = &zero })
		if c.Record.ExpiresAt != nil {
			t.Fatal("expiresAt is set")
		}
		_, err := a.tokens.Create(context.Background(), token.CreateRequest{Caller: admin, Type: token.TypeSAT, OwnerID: sa.ID, Description: "x", TTL: token.TTLRequest{Seconds: &zero}, Via: "api"})
		wantCode(t, err, 403, "non_expiring_restricted")
		// A token derived from the non-expiring SAT gets a finite lifetime.
		info, err := a.tokens.Validate(context.Background(), c.Secret)
		if err != nil {
			t.Fatal(err)
		}
		child, err := a.tokens.Create(context.Background(), token.CreateRequest{Caller: admin, Type: token.TypeSAT, OwnerID: sa.ID, Description: "child", Via: "api", Parent: &info})
		if err != nil || child.Record.ExpiresAt == nil || child.Record.DerivedFrom != info.ID {
			t.Fatalf("%+v %v", child.Record, err)
		}
	})
	t.Run("R-AUTH-027 concurrent creates on two replicas cannot exceed the limit", func(t *testing.T) {
		bob := a.user("bob")
		var ok, limited atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < 12; i++ {
			r := []*replica{a, b}[i%2]
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := r.tokens.Create(context.Background(), token.CreateRequest{Caller: admin, Type: token.TypePAT, OwnerID: bob.ID, Description: "x", Via: "api"})
				switch {
				case err == nil:
					ok.Add(1)
				case errCode(err) == errs.CodeTokenLimitReached:
					limited.Add(1)
				default:
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if ok.Load() != 3 || limited.Load() != 9 {
			t.Fatalf("created %d, refused %d; want 3 and 9", ok.Load(), limited.Load())
		}
	})
	t.Run("R-AUTH-027 the break-glass account and a wrong owner kind hold no token", func(t *testing.T) {
		bg := a.user("breakglass-holder")
		e.exec(`UPDATE app_user SET is_break_glass = true WHERE principal_id = $1::uuid`, bg.ID)
		_, err := a.tokens.Create(context.Background(), token.CreateRequest{Caller: admin, Type: token.TypePAT, OwnerID: bg.ID, Description: "x", Via: "api"})
		if !errors.Is(err, principal.ErrBreakGlass) {
			t.Fatalf("err = %v", err)
		}
		_, err = a.tokens.Create(context.Background(), token.CreateRequest{Caller: admin, Type: token.TypeSAT, OwnerID: alice.ID, Description: "x", Via: "api"})
		wantCode(t, err, 400, errs.CodeValidation)
		_, err = a.tokens.Create(context.Background(), token.CreateRequest{Caller: admin, Type: token.TypeCIT, OwnerID: alice.ID, Description: "x", Via: "api"})
		wantCode(t, err, 400, errs.CodeValidation)
	})
	t.Run("R-AUTH-027 a disabled owner cannot get a token", func(t *testing.T) {
		carol := a.user("carol")
		if err := a.manager.DisableUser(context.Background(), admin, "carol", principal.DisableOptions{}); err != nil {
			t.Fatal(err)
		}
		_, err := a.tokens.Create(context.Background(), token.CreateRequest{Caller: admin, Type: token.TypePAT, OwnerID: carol.ID, Description: "x", Via: "api"})
		wantCode(t, err, 403, errs.CodeForbidden)
	})
}

func TestR_AUTH_017_PATAllowedGroups(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead, tokens: func(c *config.AuthTokens) { c.PAT.AllowedGroups = []string{"developers"} }})
	alice, bob := a.user("alice"), a.user("bob")
	if _, err := a.manager.CreateGroup(context.Background(), admin, principal.NewGroup{Name: "developers", Members: []string{"bob"}}); err != nil {
		t.Fatal(err)
	}
	_, err := a.tokens.Create(context.Background(), token.CreateRequest{Caller: admin, Type: token.TypePAT, OwnerID: alice.ID, Description: "x", Via: "api"})
	wantCode(t, err, 403, "pat_not_allowed")
	if _, err := a.tokens.Create(context.Background(), token.CreateRequest{Caller: admin, Type: token.TypePAT, OwnerID: bob.ID, Description: "x", Via: "api"}); err != nil {
		t.Fatalf("a member of the group: %v", err)
	}
}

// TestR_AUTH_027_Derivation covers the derivation rule against PostgreSQL.
func TestR_AUTH_027_DerivationRule(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead})
	alice := a.user("alice")
	ctx := context.Background()
	scope := `{"repositories":[{"repositories":["npm-local"],"actions":["read","deploy"]}]}`
	secs := int64(7200)
	parent := a.pat(alice, func(r *token.CreateRequest) { r.Scope = []byte(scope); r.TTL.Seconds = &secs })
	info, err := a.tokens.Validate(ctx, parent.Secret)
	if err != nil {
		t.Fatal(err)
	}
	derive := func(mut func(*token.CreateRequest)) (token.Created, error) {
		req := token.CreateRequest{Caller: admin, Type: token.TypePAT, OwnerID: alice.ID, Description: "child", Via: "api", Parent: &info}
		mut(&req)
		return a.tokens.Create(ctx, req)
	}
	t.Run("a narrower child with an earlier expiry", func(t *testing.T) {
		s := int64(3600)
		c, err := derive(func(r *token.CreateRequest) {
			r.Scope = []byte(`{"repositories":[{"repositories":["npm-local"],"actions":["read"]}]}`)
			r.TTL.Seconds = &s
		})
		if err != nil || c.Record.DerivedFrom != info.ID {
			t.Fatalf("%v", err)
		}
	})
	t.Run("R-AUTH-027 a lifetime beyond the presenting token's expiry", func(t *testing.T) {
		s := int64(86400)
		_, err := derive(func(r *token.CreateRequest) { r.Scope = []byte(scope); r.TTL.Seconds = &s })
		wantCode(t, err, 403, "derivation_exceeds_parent")
	})
	t.Run("R-AUTH-027 the default lifetime is capped at the presenting token's expiry", func(t *testing.T) {
		c, err := derive(func(r *token.CreateRequest) { r.Scope = []byte(scope) })
		if err != nil || c.Record.ExpiresAt.After(*info.ExpiresAt) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("R-AUTH-027 a broader scope", func(t *testing.T) {
		_, err := derive(func(r *token.CreateRequest) { r.Scope = []byte(`{"inherit":true}`) })
		wantCode(t, err, 403, "derivation_exceeds_parent")
	})
	t.Run("R-AUTH-027 a CI token creates nothing", func(t *testing.T) {
		ci := info
		ci.Type = token.TypeCIT
		_, err := a.tokens.Create(ctx, token.CreateRequest{Caller: admin, Type: token.TypePAT, OwnerID: alice.ID, Description: "x", Via: "api", Parent: &ci})
		wantCode(t, err, 403, "ci_token_restricted")
	})
	t.Run("R-AUTH-029 revoking the parent revokes the derived tokens with reason parent_revoked", func(t *testing.T) {
		child, err := derive(func(r *token.CreateRequest) { r.Scope = []byte(scope) })
		if err != nil {
			t.Fatal(err)
		}
		grand, err := a.tokens.Validate(ctx, child.Secret)
		if err != nil {
			t.Fatal(err)
		}
		g, err := a.tokens.Create(ctx, token.CreateRequest{Caller: admin, Type: token.TypePAT, OwnerID: alice.ID, Description: "grandchild", Via: "api", Parent: &grand, Scope: []byte(scope)})
		if err != nil {
			t.Fatal(err)
		}
		res, err := a.tokens.RevokeByValue(ctx, admin, parent.Secret)
		if err != nil || !res.Found || len(res.Revoked) < 3 {
			t.Fatalf("%+v %v", res, err)
		}
		for _, s := range []string{child.Secret, g.Secret} {
			if _, err := a.bearer(s, "/x"); errCode(err) != errs.CodeInvalidToken {
				t.Fatalf("a derived token survived its parent: %v", err)
			}
		}
		rec, _ := a.tokens.Get(ctx, g.Record.ID)
		if rec.RevocationReason != token.ReasonParentRevoked {
			t.Fatalf("reason %q", rec.RevocationReason)
		}
	})
}

func TestR_AUTH_029_RevocationEndpoints(t *testing.T) {
	e := newEnv(t)
	a, b := e.newReplica("a", replicaOpts{valkey: vkDead}), e.newReplica("b", replicaOpts{valkey: vkDead})
	alice := a.user("alice")
	sa := a.serviceAccount("ci-bot")
	ctx := context.Background()

	t.Run("revoking is idempotent and an unknown ID is 404", func(t *testing.T) {
		p := a.pat(alice)
		for i := 0; i < 2; i++ {
			if _, err := a.tokens.Revoke(ctx, admin, p.Record.ID, token.ReasonOwner); err != nil {
				t.Fatalf("revoke #%d: %v", i, err)
			}
		}
		_, err := a.tokens.Revoke(ctx, admin, principal.NewID(), token.ReasonOwner)
		wantCode(t, err, 404, errs.CodeNotFound)
		if n := len(a.rec.named(audit.EventTokenRevoked)); n != 1 {
			t.Fatalf("%d token.revoked events for one revocation", n)
		}
	})
	t.Run("a value of unknown or malformed form", func(t *testing.T) {
		res, err := a.tokens.RevokeByValue(ctx, admin, "trv_pat_"+strings.Repeat("0", 49))
		if !errors.Is(err, token.ErrMalformed) {
			t.Fatalf("a checksum mismatch: %v", err)
		}
		unknown, _ := token.Generate(token.TypePAT)
		if res, err = a.tokens.RevokeByValue(ctx, admin, unknown); err != nil || res.Found {
			t.Fatalf("%+v %v", res, err)
		}
	})
	t.Run("R-AUTH-029 revoking one SAT leaves the others of the account", func(t *testing.T) {
		s1, s2 := a.sat(sa), a.sat(sa)
		if _, err := a.tokens.Revoke(ctx, admin, s1.Record.ID, token.ReasonOwner); err != nil {
			t.Fatal(err)
		}
		if _, err := a.bearer(s2.Secret, "/x"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-AUTH-029 revocation by value follows a rotation forward, also from a revoked predecessor", func(t *testing.T) {
		s1 := a.sat(sa)
		s2, err := a.tokens.Rotate(ctx, token.RotateRequest{Caller: admin, ID: s1.Record.ID, OverlapSeconds: 0})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.bearer(s1.Secret, "/x"); errCode(err) != errs.CodeInvalidToken {
			t.Fatalf("the predecessor with overlap 0 must be revoked: %v", err)
		}
		if _, err := b.bearer(s2.Secret, "/x"); err != nil {
			t.Fatal(err)
		}
		res, err := a.tokens.RevokeByValue(ctx, admin, s1.Secret)
		if err != nil || !res.Found {
			t.Fatalf("%+v %v", res, err)
		}
		waitFor(t, "the successor being rejected on replica B", revocationBound, func() bool {
			_, err := b.bearer(s2.Secret, "/x")
			return errCode(err) == errs.CodeInvalidToken
		})
		rec, _ := a.tokens.Get(ctx, s1.Record.ID)
		if rec.RevocationReason != token.ReasonRotated {
			t.Fatalf("the presented token's own fields changed: %q", rec.RevocationReason)
		}
		rec2, _ := a.tokens.Get(ctx, s2.Record.ID)
		if rec2.RevocationReason != token.ReasonParentRevoked || rec2.RotatedFrom != s1.Record.ID {
			t.Fatalf("%+v", rec2)
		}
	})
	t.Run("R-AUTH-029 revoking all tokens of a principal", func(t *testing.T) {
		dave := a.user("dave")
		p1, p2 := a.pat(dave), a.pat(dave)
		n, err := a.tokens.RevokeAll(ctx, admin, dave.ID, token.ReasonAdmin)
		if err != nil || n != 2 {
			t.Fatalf("%d %v", n, err)
		}
		for _, p := range []token.Created{p1, p2} {
			waitFor(t, "replica B rejecting", revocationBound, func() bool {
				_, err := b.bearer(p.Secret, "/x")
				return errCode(err) == errs.CodeInvalidToken
			})
		}
	})
}

func TestR_AUTH_030_Rotation(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead})
	sa := a.serviceAccount("ci-bot")
	ctx := context.Background()
	s1 := a.sat(sa, func(r *token.CreateRequest) { r.SelfRotatable = true })
	t.Run("R-AUTH-030 with an overlap both secrets work and the predecessor expires with it", func(t *testing.T) {
		s2, err := a.tokens.Rotate(ctx, token.RotateRequest{Caller: admin, ID: s1.Record.ID, OverlapSeconds: 3600})
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range []string{s1.Secret, s2.Secret} {
			if _, err := a.bearer(s, "/x"); err != nil {
				t.Fatalf("overlap: %v", err)
			}
		}
		old, _ := a.tokens.Get(ctx, s1.Record.ID)
		if old.ExpiresAt == nil || time.Until(*old.ExpiresAt) > time.Hour+time.Minute || old.RevokedAt != nil {
			t.Fatalf("predecessor %+v", old)
		}
		if s2.Record.RotatedFrom != s1.Record.ID || s2.Record.CreatedVia != "rotation" || !s2.Record.SelfRotatable || s2.Record.Scope.Inherit != s1.Record.Scope.Inherit {
			t.Fatalf("successor %+v", s2.Record)
		}
		if len(a.rec.named(audit.EventTokenRotated)) != 1 {
			t.Fatal("token.rotated")
		}
	})
	t.Run("R-AUTH-030 refusals", func(t *testing.T) {
		plain := a.sat(sa)
		_, err := a.tokens.Rotate(ctx, token.RotateRequest{Caller: admin, ID: plain.Record.ID, Self: true})
		wantCode(t, err, 403, errs.CodeForbidden)
		_, err = a.tokens.Rotate(ctx, token.RotateRequest{Caller: admin, ID: plain.Record.ID, OverlapSeconds: 90000})
		wantCode(t, err, 400, errs.CodeValidation)
		if _, err := a.tokens.Revoke(ctx, admin, plain.Record.ID, token.ReasonAdmin); err != nil {
			t.Fatal(err)
		}
		_, err = a.tokens.Rotate(ctx, token.RotateRequest{Caller: admin, ID: plain.Record.ID})
		wantCode(t, err, 404, errs.CodeNotFound)
	})
	t.Run("R-AUTH-030 a token whose scope exceeds the presenting token's cannot rotate", func(t *testing.T) {
		alice := a.user("alice")
		full := a.pat(alice)
		narrow := a.pat(alice, func(r *token.CreateRequest) {
			r.Scope = []byte(`{"repositories":[{"repositories":["npm-local"],"actions":["read"]}]}`)
		})
		info, _ := a.tokens.Validate(ctx, narrow.Secret)
		_, err := a.tokens.Rotate(ctx, token.RotateRequest{Caller: admin, ID: full.Record.ID, Parent: &info})
		wantCode(t, err, 403, "derivation_exceeds_parent")
	})
}

func TestR_AUTH_028_ListAndInspect(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead})
	alice, bob := a.user("alice"), a.user("bob")
	ctx := context.Background()
	p1, p2, pb := a.pat(alice), a.pat(alice), a.pat(bob)
	if _, err := a.tokens.Revoke(ctx, admin, p1.Record.ID, token.ReasonOwner); err != nil {
		t.Fatal(err)
	}
	list := func(f token.ListFilter) []string {
		recs, err := a.tokens.List(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, r := range recs {
			ids = append(ids, r.ID)
		}
		return ids
	}
	if got := list(token.ListFilter{OwnerID: alice.ID}); len(got) != 1 || got[0] != p2.Record.ID {
		t.Fatalf("the default state is active: %v", got)
	}
	if got := list(token.ListFilter{OwnerID: alice.ID, State: "all"}); len(got) != 2 || got[0] != p2.Record.ID {
		t.Fatalf("newest first: %v", got)
	}
	if got := list(token.ListFilter{State: "revoked"}); len(got) != 1 || got[0] != p1.Record.ID {
		t.Fatalf("revoked: %v", got)
	}
	if got := list(token.ListFilter{State: "all", After: p2.Record.ID, OwnerID: alice.ID}); len(got) != 1 || got[0] != p1.Record.ID {
		t.Fatalf("keyset: %v", got)
	}
	if got := list(token.ListFilter{State: "all", Limit: 2}); len(got) != 2 || got[0] != pb.Record.ID {
		t.Fatalf("limit: %v", got)
	}
	if got := list(token.ListFilter{ExpiresBefore: time.Now().Add(time.Hour)}); len(got) != 0 {
		t.Fatalf("expiresBefore: %v", got)
	}
	if got := list(token.ListFilter{ExpiresAfter: time.Now().Add(time.Hour), Type: token.TypePAT}); len(got) != 2 {
		t.Fatalf("expiresAfter: %v", got)
	}
	rec, err := a.tokens.Get(ctx, p1.Record.ID)
	if err != nil || rec.State != "revoked" || rec.RevocationReason != "owner" || rec.Owner.Name != "alice" {
		t.Fatalf("%+v %v", rec, err)
	}
	if _, err := a.tokens.Get(ctx, "not-an-id"); errCode(err) != errs.CodeNotFound {
		t.Fatal("a malformed ID is a 404")
	}
}
