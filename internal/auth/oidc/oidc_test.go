package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/config"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// testProvider is a provider with a discovery document and keys in place.
func testProvider(t *testing.T, mut func(*config.AuthOIDCProvider), signers ...*signer) *Provider {
	t.Helper()
	raw := config.AuthOIDCProvider{DisplayName: "Corp", Issuer: "https://idp.test", ClientID: "trove", ClientAuthMethod: "none"}
	if mut != nil {
		mut(&raw)
	}
	p := &Provider{ID: "corp", cfg: raw.Defaulted(), redirectURI: "https://trove.test/auth/oidc/corp/callback", log: quiet}
	p.doc = &discovery{Issuer: raw.Issuer, AuthorizationEndpoint: "https://idp.test/auth?tenant=a", TokenEndpoint: "https://idp.test/token", JWKSURI: "https://idp.test/jwks"}
	p.keys = keysOf(t, signers...)
	p.refetchAt = time.Now() // no network in unit tests
	return p
}

func goodClaims(now time.Time, nonce string) map[string]any {
	return map[string]any{
		"iss": "https://idp.test", "aud": "trove", "sub": "s-1", "exp": now.Add(5 * time.Minute).Unix(),
		"iat": now.Unix(), "nonce": nonce, "preferred_username": "Alice", "email": "a@example.com",
		"email_verified": true, "name": "Alice Example", "groups": []string{"trove-team-web", "unrelated"},
	}
}

func TestR_AUTH_006_IDTokenValidation(t *testing.T) {
	rs := newRSASigner(t, "k1", "RS256")
	p := testProvider(t, func(c *config.AuthOIDCProvider) { c.TrustedAudiences = []string{"other-trusted"} }, rs)
	now := time.Now()
	nonce := "n-0123456789"
	nonceHash := sha256Of(nonce)

	cases := []struct {
		name   string
		mut    func(map[string]any)
		reason string
	}{
		{"a valid token passes", func(map[string]any) {}, ""},
		{"iss other than the configured issuer is id_token_invalid", func(c map[string]any) { c["iss"] = "https://evil.test" }, ReasonIDTokenInvalid},
		{"aud without the client ID is id_token_invalid", func(c map[string]any) { c["aud"] = "someone" }, ReasonIDTokenInvalid},
		{"an extra untrusted audience is id_token_invalid", func(c map[string]any) { c["aud"] = []string{"trove", "untrusted"}; c["azp"] = "trove" }, ReasonIDTokenInvalid},
		{"an extra trusted audience with azp passes", func(c map[string]any) { c["aud"] = []string{"trove", "other-trusted"}; c["azp"] = "trove" }, ""},
		{"several audiences without azp are id_token_invalid", func(c map[string]any) { c["aud"] = []string{"trove", "other-trusted"} }, ReasonIDTokenInvalid},
		{"azp other than the client ID is id_token_invalid", func(c map[string]any) { c["azp"] = "other" }, ReasonIDTokenInvalid},
		{"exp in the past beyond the skew is id_token_expired", func(c map[string]any) { c["exp"] = now.Add(-2 * time.Minute).Unix() }, ReasonIDTokenExpired},
		{"exp within the skew passes", func(c map[string]any) { c["exp"] = now.Add(-30 * time.Second).Unix() }, ""},
		{"nbf in the future beyond the skew is id_token_expired", func(c map[string]any) { c["nbf"] = now.Add(5 * time.Minute).Unix() }, ReasonIDTokenExpired},
		{"iat older than max_id_token_age is id_token_invalid", func(c map[string]any) { c["iat"] = now.Add(-11 * time.Minute).Unix() }, ReasonIDTokenInvalid},
		{"iat in the future beyond the skew is id_token_invalid", func(c map[string]any) { c["iat"] = now.Add(2 * time.Minute).Unix() }, ReasonIDTokenInvalid},
		{"a wrong nonce is nonce_mismatch", func(c map[string]any) { c["nonce"] = "other" }, ReasonNonceMismatch},
		{"a missing nonce is nonce_mismatch", func(c map[string]any) { delete(c, "nonce") }, ReasonNonceMismatch},
		{"an empty sub is id_token_invalid", func(c map[string]any) { c["sub"] = "" }, ReasonIDTokenInvalid},
		{"a sub of 256 characters is id_token_invalid", func(c map[string]any) { c["sub"] = strings.Repeat("s", 256) }, ReasonIDTokenInvalid},
	}
	for _, c := range cases {
		t.Run("R-AUTH-006 "+c.name, func(t *testing.T) {
			cl := goodClaims(now, nonce)
			c.mut(cl)
			_, lerr := p.validateIDToken(context.Background(), rs.sign(t, nil, cl), nonceHash, now)
			switch {
			case c.reason == "" && lerr != nil:
				t.Fatalf("refused: %v", lerr)
			case c.reason != "" && (lerr == nil || lerr.reason != c.reason):
				t.Fatalf("got %v, want %s", lerr, c.reason)
			}
		})
	}

	t.Run("R-AUTH-006 a token whose kid is unknown and no refetch is allowed is id_token_invalid", func(t *testing.T) {
		other := newRSASigner(t, "k2", "RS256")
		_, lerr := p.validateIDToken(context.Background(), other.sign(t, nil, goodClaims(now, nonce)), nonceHash, now)
		if lerr == nil || lerr.reason != ReasonIDTokenInvalid {
			t.Fatalf("got %v", lerr)
		}
	})
}

func TestR_AUTH_007_ClaimMapping(t *testing.T) {
	now := time.Now()
	base := func(mut func(claims)) validated {
		c := goodClaims(now, "n")
		b, _ := json.Marshal(c)
		cl, _ := decodeClaims(b)
		if mut != nil {
			mut(cl)
		}
		return validated{claims: cl, issuer: "https://idp.test", subject: "s-1"}
	}
	p := testProvider(t, func(c *config.AuthOIDCProvider) {
		c.GroupSync.Rules = []config.AuthOIDCGroupRule{
			{Match: "trove-platform-admins", Group: "platform-admins"},
			{MatchRegex: "trove-team-(.+)", Group: "team-$1"},
			{MatchRegex: "(?P<x>Bad.+)", Group: "${x}"},
		}
	})

	t.Run("R-AUTH-007 the username is lower case with the prefix, and the claims carry issuer and subject", func(t *testing.T) {
		pre := testProvider(t, func(c *config.AuthOIDCProvider) { c.UsernamePrefix = "corp." })
		m, lerr := pre.mapClaims(base(func(c claims) { c["preferred_username"] = "Alice.Smith" }), nil, "admin", quiet)
		if lerr != nil {
			t.Fatal(lerr)
		}
		if m.claims.Username != "corp.alice.smith" || m.claims.Issuer != "https://idp.test" || m.claims.Subject != "s-1" || !m.claims.EmailVerified {
			t.Fatalf("%+v", m.claims)
		}
	})

	t.Run("R-AUTH-007 a name outside the ASCII pattern after NFC is username_invalid", func(t *testing.T) {
		// "Ame" plus a combining acute composes to an e with acute under NFC, which the pattern refuses.
		if _, lerr := p.mapClaims(base(func(c claims) { c["preferred_username"] = "Ame\u0301lie" }), nil, "admin", quiet); lerr == nil || lerr.reason != ReasonUsernameInvalid {
			t.Fatalf("got %v", lerr)
		}
	})

	t.Run("R-AUTH-007 a reserved name, the break-glass username and an invalid name are username_invalid", func(t *testing.T) {
		for _, n := range []string{"me", "Anonymous", "authenticated", "system", "admin", "-x", "a b", ""} {
			_, lerr := p.mapClaims(base(func(c claims) { c["preferred_username"] = n }), nil, "admin", quiet)
			if lerr == nil || lerr.reason != ReasonUsernameInvalid {
				t.Errorf("%q: %v", n, lerr)
			}
		}
	})

	t.Run("R-AUTH-008 the rules map trove-team-web to team-web, ignore unrelated and skip invalid names", func(t *testing.T) {
		m, lerr := p.mapClaims(base(func(c claims) {
			c["groups"] = []any{"trove-team-web", "unrelated", "trove-platform-admins", "BadName"}
		}), nil, "admin", quiet)
		if lerr != nil {
			t.Fatal(lerr)
		}
		if got := strings.Join(m.claims.Groups, ","); got != "platform-admins,team-web" {
			t.Fatalf("groups = %s", got)
		}
		if len(m.skipped) != 1 || m.skipped[0] != "BadName" {
			t.Fatalf("skipped = %v", m.skipped)
		}
	})

	missing := []struct {
		name, reason string
		mut          func(claims)
	}{
		{"absent", missingAbsent, func(c claims) { delete(c, "groups") }},
		{"of another type", missingWrongType, func(c claims) { c["groups"] = []any{"a", json.Number("1")} }},
		{"an overage marker", missingOverage, func(c claims) { delete(c, "groups"); c["_claim_names"] = map[string]any{"groups": "src1"} }},
	}
	for _, mc := range missing {
		t.Run("R-AUTH-008 Q-AUTH-4 a group claim that is "+mc.name+" clears by default with the reason "+mc.reason, func(t *testing.T) {
			m, lerr := p.mapClaims(base(mc.mut), nil, "admin", quiet)
			if lerr != nil {
				t.Fatal(lerr)
			}
			c := m.claims
			if !c.GroupSync || c.KeepOnMissing || string(c.ClaimState) != mc.reason || len(c.Groups) != 0 {
				t.Fatalf("%+v", c)
			}
		})
	}

	t.Run("R-AUTH-008 on_missing_claim deny fails with groups_claim_missing, keep keeps", func(t *testing.T) {
		deny := testProvider(t, func(c *config.AuthOIDCProvider) { c.GroupSync.OnMissingClaim = config.OnMissingDeny })
		if _, lerr := deny.mapClaims(base(func(c claims) { delete(c, "groups") }), nil, "admin", quiet); lerr == nil || lerr.reason != ReasonGroupsClaimMissing {
			t.Fatalf("deny: %v", lerr)
		}
		keep := testProvider(t, func(c *config.AuthOIDCProvider) { c.GroupSync.OnMissingClaim = config.OnMissingKeep })
		m, lerr := keep.mapClaims(base(func(c claims) { delete(c, "groups") }), nil, "admin", quiet)
		if lerr != nil || !m.claims.KeepOnMissing {
			t.Fatalf("keep: %+v %v", m.claims, lerr)
		}
	})

	t.Run("R-AUTH-008 only the first 1000 values of the group claim are read", func(t *testing.T) {
		vals := make([]any, 1500)
		for i := range vals {
			vals[i] = "g"
		}
		groups, missing := readGroups(claims{"groups": vals}, nil, "groups")
		if len(groups) != 1000 || missing != "" {
			t.Fatalf("%d %q", len(groups), missing)
		}
	})

	t.Run("R-AUTH-007 gates: require_verified_email and login_requires_groups", func(t *testing.T) {
		v := testProvider(t, func(c *config.AuthOIDCProvider) { c.RequireVerifiedEmail = true })
		if _, lerr := v.mapClaims(base(func(c claims) { c["email_verified"] = false }), nil, "admin", quiet); lerr == nil || lerr.reason != ReasonEmailUnverified {
			t.Fatalf("verified: %v", lerr)
		}
		g := testProvider(t, func(c *config.AuthOIDCProvider) { c.LoginRequiresGroups = []string{"acme"} })
		if _, lerr := g.mapClaims(base(nil), nil, "admin", quiet); lerr == nil || lerr.reason != ReasonNotInAllowedGroup {
			t.Fatalf("groups: %v", lerr)
		}
		if _, lerr := g.mapClaims(base(func(c claims) { c["groups"] = []any{"acme"} }), nil, "admin", quiet); lerr != nil {
			t.Fatalf("member refused: %v", lerr)
		}
	})

	t.Run("R-AUTH-006 userinfo fills claims the ID token lacks; dotted paths reach nested objects", func(t *testing.T) {
		nested := testProvider(t, func(c *config.AuthOIDCProvider) { c.Claims.Groups = "realm_access.roles" })
		v := base(func(c claims) { delete(c, "email"); delete(c, "groups") })
		ui := claims{"sub": "s-1", "email": "from-userinfo@example.com", "realm_access": map[string]any{"roles": []any{"x"}}}
		m, lerr := nested.mapClaims(v, ui, "admin", quiet)
		if lerr != nil || m.claims.Email != "from-userinfo@example.com" || len(m.rawGroups) != 1 {
			t.Fatalf("%+v %v", m, lerr)
		}
	})
}

func TestR_AUTH_005_SafeReturnTo(t *testing.T) {
	for in, want := range map[string]string{
		"/ui/repos/x?tab=1":    "/ui/repos/x?tab=1",
		"https://evil.test/":   "/ui/",
		"//evil.test":          "/ui/",
		"/ui//evil.test":       "/ui/",
		`/ui/\evil`:            "/ui/",
		"/api/v1/system/ping":  "/ui/",
		"":                     "/ui/",
		"/ui/x\r\nSet-Cookie:": "/ui/",
		"javascript:alert(1)":  "/ui/",
	} {
		if got := SafeReturnTo(in); got != want {
			t.Errorf("R-AUTH-005 SafeReturnTo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestR_AUTH_005_AuthorizationRequest(t *testing.T) {
	p := testProvider(t, func(c *config.AuthOIDCProvider) { c.ExtraAuthParams = map[string]string{"kc_idp_hint": "corp"} })
	target, err := p.authorizationURL(p.doc, strings.Repeat("s", 43), strings.Repeat("n", 43), strings.Repeat("v", 43))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(target)
	v := u.Query()
	t.Run("R-AUTH-005 the request carries PKCE S256, state, nonce and the extra parameters", func(t *testing.T) {
		if v.Get("code_challenge_method") != "S256" || len(v.Get("code_challenge")) != 43 || v.Get("response_type") != "code" ||
			v.Get("state") != strings.Repeat("s", 43) || v.Get("nonce") != strings.Repeat("n", 43) ||
			v.Get("redirect_uri") != "https://trove.test/auth/oidc/corp/callback" || v.Get("scope") != "openid profile email" ||
			v.Get("kc_idp_hint") != "corp" || v.Get("tenant") != "a" {
			t.Fatalf("%s", target)
		}
	})
}

func TestR_AUTH_014_PasswordFile(t *testing.T) {
	t.Run("R-AUTH-014 a plaintext password below 20 characters is refused", func(t *testing.T) {
		if _, err := parsePassword([]byte("short-password12\n")); err == nil || !strings.Contains(err.Error(), "minimum is 20") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-AUTH-014 a plaintext password of 20 characters is hashed and verifies", func(t *testing.T) {
		pw, err := parsePassword([]byte("correct-horse-batt-1\n"))
		if err != nil {
			t.Fatal(err)
		}
		if !pw.matches("correct-horse-batt-1") || pw.matches("correct-horse-batt-2") || pw.hashed {
			t.Fatal("verification")
		}
	})
	t.Run("R-AUTH-014 an Argon2id PHC hash verifies; a weaker one or another variant is refused", func(t *testing.T) {
		h, err := HashPassword("operator-chosen-secret")
		if err != nil {
			t.Fatal(err)
		}
		pw, err := parsePassword([]byte(h + "\n"))
		if err != nil || !pw.hashed || !pw.matches("operator-chosen-secret") {
			t.Fatalf("%v", err)
		}
		weak := strings.Replace(h, "m=65536", "m=1024", 1)
		if _, err := parsePassword([]byte(weak)); err == nil {
			t.Fatal("a weak hash was accepted")
		}
		if _, err := parsePassword([]byte(strings.Replace(h, "argon2id", "argon2i", 1))); err == nil {
			t.Fatal("argon2i was accepted")
		}
	})
}

func TestR_AUTH_009_LoginOptions(t *testing.T) {
	mk := func(order int) config.AuthOIDCProvider {
		return config.AuthOIDCProvider{Order: order, DisplayName: "x", Issuer: "https://idp.test", ClientID: "c", ClientAuthMethod: "none"}
	}
	s := &Service{o: Options{Auth: config.Auth{LoginPage: config.AuthLoginPage{AutoRedirect: true}}}}
	for id, order := range map[string]int{"zeta": 1, "alpha": 2, "mid": 1} {
		p := &Provider{ID: id, cfg: mk(order).Defaulted()}
		s.providers = append(s.providers, p)
	}
	sortProviders(s.providers)
	t.Run("R-AUTH-009 Q-AUTH-30 providers in configuration order, no auto redirect with two or more", func(t *testing.T) {
		o := s.LoginOptions(false)
		var ids []string
		for _, p := range o.Providers {
			ids = append(ids, p.ID)
			if p.StartURL != "/auth/oidc/"+p.ID+"/start" || p.Available {
				t.Errorf("%+v", p)
			}
		}
		if strings.Join(ids, ",") != "mid,zeta,alpha" || o.AutoRedirect != "" || o.LocalLogin {
			t.Fatalf("%v %+v", ids, o)
		}
	})
	t.Run("R-AUTH-009 one provider with auto_redirect redirects", func(t *testing.T) {
		one := &Service{o: s.o, providers: s.providers[:1]}
		if o := one.LoginOptions(true); o.AutoRedirect != "mid" || !o.AnonymousAccess {
			t.Fatalf("%+v", o)
		}
	})
}

func TestR_AUTH_007_RefusalReasons(t *testing.T) {
	for _, tc := range []struct {
		err    error
		reason string
	}{
		{principal.ErrUsernameTaken, ReasonUsernameTaken},
		{principal.ErrNotProvisioned, ReasonNotProvisioned},
		{principal.ErrUserDisabled, ReasonUserDisabled},
		{principal.ErrUsernameInvalid, ReasonUsernameInvalid},
		{fmt.Errorf("wrapped: %w", principal.ErrUserDisabled), ReasonUserDisabled},
	} {
		t.Run("R-AUTH-007 the provisioner's refusal "+tc.reason+" is the login's failure reason", func(t *testing.T) {
			if got, ok := refusalReason(tc.err); !ok || got != tc.reason {
				t.Fatalf("%q %v", got, ok)
			}
		})
	}
	t.Run("R-AUTH-007 any other provisioner error is a failure of the system, not a refusal", func(t *testing.T) {
		if _, ok := refusalReason(errors.New("boom")); ok {
			t.Fatal("refusal")
		}
	})
}
