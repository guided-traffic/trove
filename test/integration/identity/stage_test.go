//go:build integration

package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
)

// exceeded is a failed-credential limiter whose every address is in throttled mode.
type exceeded struct{}

func (exceeded) Exceeded(context.Context, string) bool { return true }
func (exceeded) Count(context.Context, string)         {}

func TestR_AUTH_001_StageAgainstTheRealStores(t *testing.T) {
	e := newEnv(t)
	a, b := e.newReplica("a", replicaOpts{valkey: vkDead}), e.newReplica("b", replicaOpts{valkey: vkDead})
	ctx := context.Background()
	alice := a.user("alice")
	pat := a.pat(alice)
	sa := a.serviceAccount("ci-bot")
	sat := a.sat(sa)

	t.Run("R-AUTH-033 Bearer and Basic with the token as password yield the owner on any replica", func(t *testing.T) {
		for _, r := range []*replica{a, b} {
			res, err := r.bearer(pat.Secret, "/api/v1/auth/whoami")
			if err != nil || res.Principal.ID != alice.ID || res.Credential.TokenID != pat.Record.ID || res.Credential.TokenType != token.TypePAT || !res.Credential.Scope.Inherit {
				t.Fatalf("%s: %+v %v", r.name, res, err)
			}
		}
		req := httptest.NewRequest("GET", "/files/generic-local/a/file.bin", nil)
		req.SetBasicAuth("ci", sat.Secret)
		res, err := b.stage.Authenticate(req, auth.EndpointRules{Name: "default", TokenTypes: []token.Type{token.TypeSAT}})
		if err != nil || res.Principal.Name != "ci-bot" || res.Credential.Method != auth.MethodBasic || res.Credential.BasicUsername != "ci" {
			t.Fatalf("%+v %v", res, err)
		}
	})
	t.Run("R-AUTH-001 an expired PAT on a repository anonymous may read is 401, not the anonymous response", func(t *testing.T) {
		if _, err := a.settings.Update(ctx, admin, token.SettingsPatch{Expected: errs.AnyRevision, AnonymousAccess: ptr(true)}); err != nil {
			t.Fatal(err)
		}
		short := a.pat(alice, func(r *token.CreateRequest) { s := int64(60); r.TTL.Seconds = &s })
		e.exec(`UPDATE api_token SET expires_at = now() - interval '1 second', created_at = now() - interval '2 minutes' WHERE id = $1::uuid`, short.Record.ID)
		_, err := a.bearer(short.Secret, "/api/npm/npm-all/lodash")
		wantCode(t, err, 401, errs.CodeInvalidToken)
	})
	t.Run("R-AUTH-002 a valid token of a disabled owner is 401 principal_disabled", func(t *testing.T) {
		u := a.user("bob")
		p := a.pat(u)
		_ = a.manager.DisableUser(ctx, admin, "bob", principal.DisableOptions{})
		_, err := a.bearer(p.Secret, "/x")
		wantCode(t, err, 401, errs.CodePrincipalDisabled)
	})
	t.Run("R-EP-011 tokens bound to endpoints", func(t *testing.T) {
		bound := a.pat(alice, func(r *token.CreateRequest) { r.Scope = []byte(`{"inherit":true,"endpoints":["default"]}`) })
		if _, err := a.bearer(bound.Secret, "/x"); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("GET", "/x", nil)
		req.Header.Set("Authorization", "Bearer "+bound.Secret)
		_, err := a.stage.Authenticate(req, auth.EndpointRules{Name: "customers", TokenTypes: []token.Type{token.TypePAT}})
		wantCode(t, err, 401, errs.CodeInvalidToken)
	})
	t.Run("R-AUTH-023 token.rejected for a revoked token is written once, whatever the replica", func(t *testing.T) {
		p := a.pat(alice)
		if _, err := a.tokens.Revoke(ctx, admin, p.Record.ID, token.ReasonOwner); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 5; i++ {
			for _, r := range []*replica{a, b} {
				if _, err := r.bearer(p.Secret, "/x"); err == nil {
					t.Fatal("accepted")
				}
			}
		}
		n := 0
		for _, r := range []*replica{a, b} {
			for _, ev := range r.rec.named(audit.EventTokenRejected) {
				if ev.Subject.ID == p.Record.ID {
					n++
					var d struct{ Reason string }
					_ = json.Unmarshal(ev.Details, &d)
					if d.Reason != "revoked" || ev.Source.ClientAddr != "203.0.113.7" {
						t.Errorf("%+v %s", ev, ev.Details)
					}
				}
			}
		}
		if n != 1 {
			t.Fatalf("%d token.rejected events for one token in the whole cluster", n)
		}
	})
}

func TestR_AUTH_037_AnonymousSwitchReachesEveryReplica(t *testing.T) {
	for _, mode := range []vkMode{vkLive, vkDead} {
		t.Run(mode.String(), func(t *testing.T) {
			e := newEnv(t)
			a, b := e.newReplica("a", replicaOpts{valkey: mode}), e.newReplica("b", replicaOpts{valkey: mode})
			anonymous := func(r *replica) bool {
				res, err := r.do("", "/api/npm/npm-all/lodash")
				if err != nil {
					wantCode(t, err, 401, errs.CodeCredentialsRequired)
					return false
				}
				return res.Anonymous
			}
			if anonymous(a) || anonymous(b) {
				t.Fatal("anonymous access is off by default")
			}
			if _, err := a.settings.Update(context.Background(), admin, token.SettingsPatch{Expected: errs.AnyRevision, AnonymousAccess: ptr(true)}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "anonymous access on replica B", revocationBound, func() bool { return anonymous(b) })
			if _, err := a.settings.Update(context.Background(), admin, token.SettingsPatch{Expected: errs.AnyRevision, AnonymousAccess: ptr(false)}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "anonymous access off on replica B", revocationBound, func() bool { return !anonymous(b) })
			evs := a.rec.named(audit.EventSettingsAuthChanged)
			if len(evs) != 2 || len(evs[0].Journal) == 0 {
				t.Fatalf("%d settings.auth.changed events", len(evs))
			}
		})
	}
}

func TestR_AUTH_040_RuntimeSettings(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead})
	ctx := context.Background()
	day := func(n int) *time.Duration { d := time.Duration(n) * 24 * time.Hour; return &d }

	t.Run("a value above its YAML ceiling is 400 exceeds_ceiling", func(t *testing.T) {
		_, err := a.settings.Update(ctx, admin, token.SettingsPatch{Expected: errs.AnyRevision, PATMaxTTL: day(400)})
		wantCode(t, err, 400, errs.CodeExceedsCeiling)
	})
	t.Run("a default above the maximum is 400 default_above_maximum", func(t *testing.T) {
		_, err := a.settings.Update(ctx, admin, token.SettingsPatch{Expected: errs.AnyRevision, PATMaxTTL: day(30), PATDefaultTTL: day(60)})
		wantCode(t, err, 400, errs.CodeDefaultAboveMaximum)
		_, err = a.settings.Update(ctx, admin, token.SettingsPatch{Expected: errs.AnyRevision, SATDefaultTTL: ptr(30 * time.Second)})
		wantCode(t, err, 400, errs.CodeInvalidTTL)
	})
	t.Run("lowering the maximum refuses longer tokens and leaves existing ones alone", func(t *testing.T) {
		u := a.user("alice")
		old := a.pat(u)
		if _, err := a.settings.Update(ctx, admin, token.SettingsPatch{Expected: errs.AnyRevision, PATMaxTTL: day(30), PATDefaultTTL: day(7)}); err != nil {
			t.Fatal(err)
		}
		s := int64(60 * 24 * 3600)
		_, err := a.tokens.Create(ctx, token.CreateRequest{Caller: admin, Type: token.TypePAT, OwnerID: u.ID, Description: "x", TTL: token.TTLRequest{Seconds: &s}, Via: "api"})
		wantCode(t, err, 400, errs.CodeTTLExceedsMaximum)
		fresh := a.pat(u)
		if d := time.Until(*fresh.Record.ExpiresAt); d > 8*24*time.Hour {
			t.Fatalf("the runtime default applies: %s", d)
		}
		if _, err := a.bearer(old.Secret, "/x"); err != nil {
			t.Fatalf("existing tokens are unchanged: %v", err)
		}
		eff, _ := a.settings.Effective(ctx)
		if eff.PATMaxTTL != 30*24*time.Hour || eff.SATMaxTTL != 8760*time.Hour {
			t.Fatalf("%+v", eff)
		}
	})
	t.Run("anonymous access cannot be enabled while the ceiling forbids it", func(t *testing.T) {
		r := e.newReplicaWithAnonymousCeiling(false)
		_, err := r.settings.Update(ctx, admin, token.SettingsPatch{Expected: errs.AnyRevision, AnonymousAccess: ptr(true)})
		wantCode(t, err, 409, errs.CodeAnonymousNotAllowed)
	})
}

func (e *env) newReplicaWithAnonymousCeiling(allowed bool) *replica {
	r := e.newReplica("ceil", replicaOpts{valkey: vkDead})
	reg, err := cache.NewRegistry(nil, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	r.settings = token.NewSettingsStore(token.SettingsOptions{
		DB: r.db, Notifier: r.changes, Recorder: r.rec, Cache: reg, Gate: principal.Gate{}, Tokens: r.tokenCfg, Anonymous: config.AuthAnonymous{Allowed: allowed},
	})
	return r
}

// TestR_AUTH_038_ThrottleExemptions: in throttled mode a credential that is in the
// replica's cache or marked as recently validated on any replica still works; an
// unknown one gets 429 and is not validated.
func TestR_AUTH_038_ThrottleExemptions(t *testing.T) {
	e := newEnv(t)
	live := e.newReplica("a", replicaOpts{valkey: vkLive})
	waitFor(t, "Valkey on replica a", 20*time.Second, live.vk.Available)
	b := e.newReplica("b", replicaOpts{valkey: vkLive, limiter: exceeded{}})
	waitFor(t, "Valkey on replica b", 20*time.Second, b.vk.Available)
	u := live.user("alice")
	pat := live.pat(u)

	t.Run("an address in throttled mode gets 429 for a credential nobody validated", func(t *testing.T) {
		_, err := b.bearer(pat.Secret, "/x")
		wantCode(t, err, 429, errs.CodeRateLimited)
		if e, _ := errs.As(err); e.RetryAfter <= 0 {
			t.Fatal("429 without Retry-After")
		}
	})
	t.Run("a credential validated on another replica carries a marker and still works on a cold replica", func(t *testing.T) {
		if _, err := live.bearer(pat.Secret, "/x"); err != nil {
			t.Fatal(err)
		}
		if _, err := b.bearer(pat.Secret, "/x"); err != nil {
			t.Fatalf("the marker does not exempt the credential: %v", err)
		}
	})
	t.Run("a marker never authenticates anybody: a revoked token is rejected on the throttled replica", func(t *testing.T) {
		if _, err := live.tokens.Revoke(context.Background(), admin, pat.Record.ID, token.ReasonAdmin); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "replica b rejecting the revoked token", revocationBound, func() bool {
			_, err := b.bearer(pat.Secret, "/x")
			return errCode(err) == errs.CodeInvalidToken
		})
	})
	t.Run("with Valkey stopped only the replica's own cache exempts", func(t *testing.T) {
		d := e.newReplica("d", replicaOpts{valkey: vkDead, limiter: exceeded{}})
		p := live.pat(u)
		_, err := d.bearer(p.Secret, "/x")
		wantCode(t, err, 429, errs.CodeRateLimited)
		d2 := e.newReplica("d2", replicaOpts{valkey: vkDead})
		if _, err := d2.bearer(p.Secret, "/x"); err != nil {
			t.Fatal(err)
		}
		d2.stage = auth.New(auth.Options{
			Tokens: d2.tokens, Principals: d2.principals, Settings: d2.settings, Limiter: exceeded{}, BasicEnabled: true,
			ClientAddr: func(*http.Request) netip.Addr { return netip.MustParseAddr("203.0.113.7") },
		})
		if _, err := d2.bearer(p.Secret, "/x"); err != nil {
			t.Fatalf("a cache hit costs no throttle check: %v", err)
		}
	})
}
