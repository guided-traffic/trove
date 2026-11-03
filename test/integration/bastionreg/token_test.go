//go:build integration

package bastionreg

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/bastionmain"
)

// issue creates a registration and a token through the API and returns the token's ID and value.
func issue(t *testing.T, r *replica, admin, name string) (id, secret string) {
	t.Helper()
	if rp := r.call("GET", bastions+"/"+name, admin, nil); rp.Status == http.StatusNotFound {
		want(t, r.call("POST", bastions, admin, map[string]any{"name": name}), http.StatusCreated, "")
	}
	rp := r.call("POST", bastions+"/"+name+"/tokens", admin, map[string]any{})
	want(t, rp, http.StatusCreated, "")
	b := rp.json(t)
	return b["id"].(string), b["token"].(string)
}

// TestR_BAS_005_ABastionTokenIsNoCredentialOnAnyOtherRoute is the first acceptance criterion of R-BAS-005 on
// the real chain: $BST as Authorization: Bearer, as the Basic password and as X-NuGet-ApiKey on the routes of
// the management API (the same stage serves the package routes, shown at the unit level in internal/auth) answers
// 401 invalid_token, and the token table is not asked for it, while a personal token on the same routes is.
func TestR_BAS_005_ABastionTokenIsNoCredentialOnAnyOtherRoute(t *testing.T) {
	e := newEnv(t)
	r, c := e.newCast("web-1", replicaOpts{})
	_, bst := issue(t, r, c.admin, "partners")

	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("partners:"+bst))
	routes := []string{"/api/v1/repositories", "/api/v1/tokens", "/api/v1/bastions"}
	before := r.counted.calls()
	for _, path := range routes {
		for name, hdr := range map[string][2]string{
			"Authorization: Bearer": {"Authorization", "Bearer " + bst},
			"the Basic password":    {"Authorization", basic},
			"X-NuGet-ApiKey":        {"X-NuGet-ApiKey", bst},
		} {
			rp := r.call("GET", path, "", nil, hdr[0], hdr[1])
			if name == "X-NuGet-ApiKey" {
				// The key is read on the routes that declare it only; elsewhere it is not a credential at all,
				// and the request is the anonymous one.
				if rp.Status == http.StatusOK {
					t.Errorf("%s with %s: the request was admitted", path, name)
				}
				continue
			}
			if rp.Status != http.StatusUnauthorized || rp.code() != "invalid_token" {
				t.Errorf("%s with %s: %d %s", path, name, rp.Status, rp.Body)
			}
		}
	}
	if after := r.counted.calls(); after != before {
		t.Fatalf("the token service read the database %d times for Bastion tokens", after-before)
	}

	t.Run("control: a personal token on the same routes is looked up", func(t *testing.T) {
		before := r.counted.calls()
		want(t, r.call("GET", "/api/v1/tokens", c.admin, nil), http.StatusOK, "")
		if r.counted.calls() == before {
			t.Fatal("the counter did not move for a personal token: the proof above proves nothing")
		}
	})
	t.Run("a Bastion token on a route that admits every authenticated caller is refused too", func(t *testing.T) {
		rp := r.call("GET", "/api/v1/tokens", bst, nil)
		want(t, rp, http.StatusUnauthorized, "invalid_token")
	})
}

// TestR_BAS_005_TheAuthenticatorValidatesAgainstTheDatabase: the validation of a Bastion token on the interface
// side: the registration and the expiry for a valid token, one error for every other, the cache with the
// revocation bound of R-AUTH-023 across replicas, and the last use.
func TestR_BAS_005_TheAuthenticatorValidatesAgainstTheDatabase(t *testing.T) {
	e := newEnv(t)
	a, c := e.newCast("web-1", replicaOpts{})
	b := e.newReplica("web-2", replicaOpts{})
	id, secret := issue(t, a, c.admin, "partners")
	ctx := context.Background()

	t.Run("a valid token yields the registration, the token and its expiry", func(t *testing.T) {
		got, err := a.auth.Authenticate(ctx, secret)
		if err != nil {
			t.Fatal(err)
		}
		if got.TokenID != id || got.Name != "partners" || got.Hint != secret[:12] || got.BastionID == "" || time.Until(got.ExpiresAt) < 8000*time.Hour {
			t.Fatalf("token = %+v", got)
		}
	})
	t.Run("a second validation is answered by the replica's cache", func(t *testing.T) {
		before := a.counted.calls()
		for range 5 {
			if _, err := a.auth.Authenticate(ctx, secret); err != nil {
				t.Fatal(err)
			}
		}
		if after := a.counted.calls(); after != before {
			t.Fatalf("%d database calls for cached validations", after-before)
		}
	})
	t.Run("every other token is ErrTokenInvalid: other types, malformed, unknown", func(t *testing.T) {
		pat, _ := token.Generate(token.TypePAT)
		unknown, _ := token.Generate(token.TypeBST)
		for name, raw := range map[string]string{"a personal token": pat, "an unknown Bastion token": unknown, "garbage": "hunter2", "empty": ""} {
			if _, err := a.auth.Authenticate(ctx, raw); !errors.Is(err, bastionmain.ErrTokenInvalid) {
				t.Errorf("%s: %v", name, err)
			}
		}
		// A real personal token of the database is no Bastion token either.
		if _, err := a.auth.Authenticate(ctx, c.admin); !errors.Is(err, bastionmain.ErrTokenInvalid) {
			t.Errorf("a stored personal token: %v", err)
		}
	})
	t.Run("lastUsedAt follows the use, as for other tokens", func(t *testing.T) {
		if _, err := b.auth.Authenticate(ctx, secret); err != nil {
			t.Fatal(err)
		}
		if err := b.tokens.FlushLastUsed(ctx); err != nil {
			t.Fatal(err)
		}
		got := a.call("GET", bastions+"/partners", c.admin, nil)
		toks := tokensOf(t, got)
		if len(toks) != 1 || toks[0].(map[string]any)["lastUsedAt"] == nil {
			t.Fatalf("tokens = %v, want lastUsedAt set", toks)
		}
		if addr := e.text(`SELECT host(last_used_addr) FROM api_token WHERE id = $1::uuid`, id); addr != "198.51.100.20" {
			t.Errorf("last_used_addr = %q", addr)
		}
	})
	t.Run("a revocation reaches the other replica within the revocation bound", func(t *testing.T) {
		if _, err := b.auth.Authenticate(ctx, secret); err != nil {
			t.Fatalf("not cached or not valid on the second replica: %v", err)
		}
		want(t, a.call("DELETE", bastions+"/partners/tokens/"+id, c.admin, nil), http.StatusNoContent, "")
		took := waitFor(t, "the revoked token is refused on the replica that cached it", revocationBound, func() bool {
			_, err := b.auth.Authenticate(ctx, secret)
			return errors.Is(err, bastionmain.ErrTokenInvalid)
		})
		t.Logf("revoked token refused after %v", took)
	})
	t.Run("a revoked token logs token.rejected once an hour, with its hint and no value", func(t *testing.T) {
		for range 3 {
			_, _ = a.auth.Authenticate(ctx, secret)
		}
		a.flush()
		b.flush()
		evs := e.events(audit.EventTokenRejected)
		if len(evs) != 1 || evs[0].Details["reason"] != "revoked" || evs[0].Details["hint"] != secret[:12] || evs[0].Details["owner"] != "bastion:partners" {
			t.Fatalf("token.rejected = %+v, want one event", evs)
		}
		// R-BAS-005 (item of the review): the rejection names the source of the request it came with.
		if src := e.text(`SELECT COALESCE(host(source_ip), '') || ' ' || COALESCE(user_agent, '') FROM audit_event WHERE action = 'token.rejected'`); src != "198.51.100.20 trove-bastion/0.15.0" {
			t.Errorf("token.rejected names the source %q, want the Bastion's address and agent", src)
		}
	})
}

// TestR_BAS_005_ADisabledRegistrationRefusesItsTokens: a disabled registration refuses its tokens like a
// disabled service account's (R-AUTH-020) within the revocation bound on every replica, and enabling it
// restores them (nothing was revoked).
func TestR_BAS_005_ADisabledRegistrationRefusesItsTokens(t *testing.T) {
	e := newEnv(t)
	a, c := e.newCast("web-1", replicaOpts{})
	b := e.newReplica("web-2", replicaOpts{})
	_, secret := issue(t, a, c.admin, "partners")
	ctx := context.Background()
	for _, r := range []*replica{a, b} {
		if _, err := r.auth.Authenticate(ctx, secret); err != nil {
			t.Fatal(err)
		}
	}
	want(t, a.call("PATCH", bastions+"/partners", c.admin, map[string]any{"enabled": false}, "If-Match", "*"), http.StatusOK, "")
	for _, r := range []*replica{a, b} {
		waitFor(t, "the token of a disabled registration is refused on "+r.name, revocationBound, func() bool {
			_, err := r.auth.Authenticate(ctx, secret)
			return errors.Is(err, bastionmain.ErrTokenInvalid)
		})
	}
	if n := e.number(`SELECT count(*) FROM api_token WHERE type = 'bst' AND revoked_at IS NOT NULL`); n != 0 {
		t.Fatalf("disabling revoked %d tokens", n)
	}
	want(t, a.call("PATCH", bastions+"/partners", c.admin, map[string]any{"enabled": true}, "If-Match", "*"), http.StatusOK, "")
	for _, r := range []*replica{a, b} {
		waitFor(t, "the token works again on "+r.name, revocationBound, func() bool {
			_, err := r.auth.Authenticate(ctx, secret)
			return err == nil
		})
	}
}

// TestR_BAS_005_AnExpiredTokenIsRefused: a token past its expiry is refused, and never cached as valid.
func TestR_BAS_005_AnExpiredTokenIsRefused(t *testing.T) {
	e := newEnv(t)
	r, c := e.newCast("web-1", replicaOpts{})
	id, secret := issue(t, r, c.admin, "partners")
	e.exec(`UPDATE api_token SET created_at = now() - interval '2 days', expires_at = now() - interval '1 day' WHERE id = $1::uuid`, id)
	if _, err := r.auth.Authenticate(context.Background(), secret); !errors.Is(err, bastionmain.ErrTokenInvalid) {
		t.Fatalf("an expired token: %v", err)
	}
}
