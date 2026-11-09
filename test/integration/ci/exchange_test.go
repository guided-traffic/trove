//go:build integration

package ci

// The token exchange (work package W4): R-CI-007's request forms, answer and scope, R-CI-016's lifetime, R-CI-020's
// issuance record and events, R-CI-012's cross-project guard, R-CI-005's issuer selection, through the HTTP endpoint
// of the replicas against the issuer double.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/oidcstub"
)

func TestR_CI_007_AnExchangeIssuesACIToken(t *testing.T) {
	x := newXEnv(t, options{valkey: true})
	a := x.web("web-a")
	x.issuer(a)
	x.addMapping(a, releaseMapping("acme-release-publish", 100, map[string]any{"maxTtlSeconds": 1800}))

	t.Run("R-CI-007 R-CI-006 a form-encoded exchange answers 200 with the fields of the answer and no-store", func(t *testing.T) {
		g := wantToken(t, x.exchange(a, x.mint(oidcstub.MintOptions{})))
		if g.Issuer != xIssuer || len(g.Mappings) != 1 || g.Mappings[0] != "acme-release-publish" {
			t.Errorf("ci_issuer %q, mappings %v", g.Issuer, g.Mappings)
		}
		// The policy's items first, selectors as the policy writes them, then the inline grant's (R-CI-007).
		if g.Scope != "read:it/npm-all read:it/npm-internal deploy:it/npm-internal" {
			t.Errorf("scope = %q", g.Scope)
		}
		// The default 15 minutes, below the mapping's 30 and the token's hour (R-CI-016).
		if g.ExpiresIn != 900 {
			t.Errorf("expires_in = %d, want 900", g.ExpiresIn)
		}
	})

	t.Run("R-CI-007 the same request as JSON answers an equivalent token", func(t *testing.T) {
		tok := x.mint(oidcstub.MintOptions{})
		body, _ := json.Marshal(map[string]any{
			"grant_type": "urn:ietf:params:oauth:grant-type:token-exchange", "subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
			"subject_token": tok, "ttl": 600,
		})
		g := wantToken(t, x.post(a, "application/json", body))
		if g.Scope != "read:it/npm-all read:it/npm-internal deploy:it/npm-internal" || g.ExpiresIn != 600 || g.Mappings[0] != "acme-release-publish" {
			t.Errorf("the JSON answer %+v", g)
		}
	})

	t.Run("R-CI-017 the token installs from the policy's repository and publishes its project's scope", func(t *testing.T) {
		g := wantToken(t, x.exchange(a, x.mint(oidcstub.MintOptions{})))
		pub := x.stack.Do(a, "PUT", npmPath("npm-internal", "@acme-billing/invoice-client"), g.Token, npmBody(t, "@acme-billing/invoice-client", "1.4.0"))
		if pub.Status != 200 && pub.Status != 201 {
			t.Fatalf("npm publish: %d %s", pub.Status, pub.Body)
		}
		if got := x.stack.Do(a, "GET", npmPath("npm-all", "@acme-billing/invoice-client"), g.Token, nil); got.Status != 200 {
			t.Errorf("npm install through npm-all: %d", got.Status)
		}
		// Another scope is outside the inline grant's packages.
		if other := x.stack.Do(a, "PUT", npmPath("npm-internal", "@other/thing"), g.Token, npmBody(t, "@other/thing", "1.0.0")); other.Status != 403 {
			t.Errorf("a publish outside the project's scope: %d", other.Status)
		}
	})
}

func TestR_CI_016_TheLifetimeOfACIToken(t *testing.T) {
	x := newXEnv(t, options{valkey: true, env: []string{"TROVE_CI_TOKEN_MIN_TTL=2s"}})
	a := x.web("web-a")
	x.issuer(a)
	x.addMapping(a, releaseMapping("acme-release-publish", 100))
	now := time.Now().Unix()

	t.Run("R-CI-016 an ID token with 10 minutes left gives at most 600 seconds", func(t *testing.T) {
		g := wantToken(t, x.exchange(a, x.mint(oidcstub.MintOptions{Claims: map[string]any{"exp": now + 600}})))
		if g.ExpiresIn > 600 || g.ExpiresIn < 590 {
			t.Errorf("expires_in = %d", g.ExpiresIn)
		}
	})
	t.Run("R-CI-016 ttl=60 gives 60, ttl=86400 at most ci.token.max_ttl", func(t *testing.T) {
		if g := wantToken(t, x.exchange(a, x.mint(oidcstub.MintOptions{}), "ttl", "60")); g.ExpiresIn != 60 {
			t.Errorf("ttl=60: expires_in = %d", g.ExpiresIn)
		}
		if g := wantToken(t, x.exchange(a, x.mint(oidcstub.MintOptions{Claims: map[string]any{"exp": now + 7200}}), "ttl", "86400")); g.ExpiresIn != 3600 {
			t.Errorf("ttl=86400: expires_in = %d, want ci.token.max_ttl 3600", g.ExpiresIn)
		}
	})
	t.Run("R-CI-016 an ID token with less than ci.token.min_ttl left is subject_token_expires_too_soon", func(t *testing.T) {
		wantOAuth(t, x.exchange(a, x.mint(oidcstub.MintOptions{Claims: map[string]any{"exp": time.Now().Unix() + 1}})), 400, "invalid_request", "subject_token_expires_too_soon")
	})
	t.Run("Q-CI-41 a requested ttl below ci.token.min_ttl is the caller's error, ttl_below_minimum", func(t *testing.T) {
		a := x.exchange(a, x.mint(oidcstub.MintOptions{}), "ttl", "1")
		wantOAuth(t, a, 400, "invalid_request", "ttl_below_minimum")
		if !strings.Contains(a.JSON(t)["error_description"].(string), "ci.token.min_ttl") {
			t.Errorf("the description does not name the floor: %s", a.Body)
		}
	})
	t.Run("R-CI-016 the token answers 401 one second after expires_in", func(t *testing.T) {
		g := wantToken(t, x.exchange(a, x.mint(oidcstub.MintOptions{}), "ttl", "2"))
		if got := x.stack.Do(a, "GET", npmPath("npm-all", "@acme-billing/invoice-client"), g.Token, nil); got.Status == 401 {
			t.Fatalf("the fresh token is refused: %s", got.Body)
		}
		time.Sleep(time.Duration(g.ExpiresIn+1) * time.Second)
		if got := x.stack.Do(a, "GET", npmPath("npm-all", "@acme-billing/invoice-client"), g.Token, nil); got.Status != 401 {
			t.Errorf("one second after expires_in: %d, want 401", got.Status)
		}
	})
}
