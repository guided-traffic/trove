package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

func asProblem(t *testing.T, err error) *errs.Error {
	t.Helper()
	e, ok := errs.As(err)
	if !ok {
		t.Fatalf("not an *errs.Error: %v", err)
	}
	return e
}

func TestMergePatchMembersOfTheIdentityResources(t *testing.T) {
	t.Run("R-API-013 absent is unchanged, null resets, a value replaces", func(t *testing.T) {
		if v, err := patchString(nil, "/x"); v != nil || err != nil {
			t.Fatalf("absent: %v %v", v, err)
		}
		if v, err := patchString(json.RawMessage(`null`), "/x"); err != nil || v == nil || *v != "" {
			t.Fatalf("null: %v %v", v, err)
		}
		if v, err := patchString(json.RawMessage(`"a"`), "/x"); err != nil || *v != "a" {
			t.Fatalf("value: %v %v", v, err)
		}
		e := asProblem(t, func() error { _, err := patchString(json.RawMessage(`3`), "/x"); return err }())
		if e.Status() != 400 || e.Fields[0].Pointer != "/x" {
			t.Fatalf("%+v", e)
		}
		if v, err := patchBool(json.RawMessage(`null`), "/b"); err != nil || *v {
			t.Fatalf("%v %v", v, err)
		}
		if _, err := patchBool(json.RawMessage(`"yes"`), "/b"); err == nil {
			t.Fatal("a string is not a boolean")
		}
		if _, err := patchRequiredString(json.RawMessage(`null`), "/name"); asProblem(t, err).Status() != 422 {
			t.Fatal("a member without a default cannot be removed")
		}
	})
	t.Run("R-API-013 an equal immutable member is ignored, a different one is 422", func(t *testing.T) {
		if err := immutableString(nil, "/name", "a"); err != nil {
			t.Fatal(err)
		}
		if err := immutableString(json.RawMessage(`"a"`), "/name", "a"); err != nil {
			t.Fatal(err)
		}
		if e := asProblem(t, immutableString(json.RawMessage(`"b"`), "/name", "a")); e.Status() != 422 || e.Fields[0].Code != "immutable" {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("R-API-021 the preferences patch validates the enumerations and keeps the rest", func(t *testing.T) {
		cur := prefs{locale: "en", revision: 4, keyboardShortcuts: true}
		var in prefsPatchBody
		if err := StrictUnmarshal([]byte(`{"locale":"de","theme":null,"revision":99}`), &in); err != nil {
			t.Fatal(err)
		}
		next, err := in.apply(cur)
		if err != nil || next.locale != "de" || !next.keyboardShortcuts || next.revision != 4 {
			t.Fatalf("%+v %v", next, err)
		}
		for _, bad := range []string{`{"locale":"fr"}`, `{"theme":"blue"}`, `{"keyboardShortcuts":"x"}`} {
			var in prefsPatchBody
			if err := StrictUnmarshal([]byte(bad), &in); err != nil {
				t.Fatal(err)
			}
			if _, err := in.apply(cur); err == nil {
				t.Errorf("%s was accepted", bad)
			}
		}
		if err := StrictUnmarshal([]byte(`{"locale":"de","bogus":1}`), &in); asProblem(t, err).Status() != 400 {
			t.Fatal("an unknown member is a 400")
		}
	})
}

func TestIdentityRepresentations(t *testing.T) {
	t.Run("R-AUTH-016 a user without values shows null, never an empty string", func(t *testing.T) {
		u := toUser(principal.User{ID: "0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a10", Name: "alice", Status: principal.StatusActive, Origin: "jit", CreatedAt: time.Unix(0, 0)})
		raw, _ := json.Marshal(u)
		for _, want := range []string{`"email":null`, `"displayName":null`, `"lockedUntil":null`, `"lastLoginAt":null`, `"adminVia":[]`, `"groups":[]`, `"identities":[]`} {
			if !strings.Contains(string(raw), want) {
				t.Errorf("%s missing in %s", want, raw)
			}
		}
	})
	t.Run("R-AUTH-027 a scope is shown with all four members", func(t *testing.T) {
		raw, _ := json.Marshal(toScope(token.Scope{Inherit: true}))
		if string(raw) != `{"admin":false,"inherit":true,"repositories":[],"system":[]}` {
			t.Fatalf("%s", raw)
		}
		sc := toScope(token.Scope{Repositories: []token.ScopeGrant{{Repositories: []string{"npm-local"}, Actions: []string{"read"}, Paths: token.Patterns{Include: []string{"a/**"}}}}, Endpoints: []string{"ci"}})
		raw, _ = json.Marshal(sc)
		if !strings.Contains(string(raw), `"paths":{"include":["a/**"]}`) || !strings.Contains(string(raw), `"endpoints":["ci"]`) || strings.Contains(string(raw), `"packages"`) {
			t.Fatalf("%s", raw)
		}
	})
	t.Run("R-AUTH-028 a record never shows the value or its hash", func(t *testing.T) {
		raw, _ := json.Marshal((&Server{}).toToken(token.Record{ID: "0192f0d1-1c2a-7c3e-8a51-6f7d2b9c4e11", Type: token.TypePAT, Hint: "trv_pat_3kQ9", State: "active", Owner: token.Owner{Kind: principal.KindUser, Name: "alice"}}))
		for _, banned := range []string{`"token"`, `"secret`, `"hash`} {
			if strings.Contains(string(raw), banned) {
				t.Errorf("%s in %s", banned, raw)
			}
		}
	})
	t.Run("R-AUTH-027 the value is part of the creation answer only", func(t *testing.T) {
		c := token.Created{Secret: "trv_pat_secretvalue", Record: token.Record{ID: "0192f0d1-1c2a-7c3e-8a51-6f7d2b9c4e11", Type: token.TypePAT, Owner: token.Owner{Kind: principal.KindUser, Name: "alice"}}}
		if toCreated(c).Token != "trv_pat_secretvalue" {
			t.Fatal("the creation answer has no value")
		}
	})
}

func TestIdentityErrors(t *testing.T) {
	t.Run("R-AUTH-016 the break-glass refusal is a 409 break_glass_immutable", func(t *testing.T) {
		if e := asProblem(t, identityErr(principal.ErrBreakGlass)); e.Status() != 409 || e.Code != errs.CodeBreakGlassImmutable {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("R-API-010 a filter value outside its set, and a descending sort, are 400", func(t *testing.T) {
		if err := oneOf("status", "bogus", "active"); asProblem(t, err).Status() != 400 {
			t.Fatal("bogus")
		}
		if err := oneOf("status", "", "active"); err != nil {
			t.Fatal(err)
		}
		if err := ascending(ListQuery{Desc: true}); asProblem(t, err).Status() != 400 {
			t.Fatal("desc")
		}
		if _, err := afterName(ListQuery{After: []string{"a", "b"}}); asProblem(t, err).Code != errs.CodeInvalidCursor {
			t.Fatal("a cursor of two values")
		}
	})
	t.Run("R-AUTH-007 a pending identity names a configured provider and has no subject", func(t *testing.T) {
		s := &Server{o: Options{Auth: AuthServices{Config: config.Auth{OIDC: config.AuthOIDC{Providers: map[string]config.AuthOIDCProvider{"corp": {Issuer: "https://idp.example.com"}}}}}}}
		got, err := s.pendingIdentities([]gen.PendingIdentity{{Provider: "corp"}})
		if err != nil || got[0].Issuer != "https://idp.example.com" {
			t.Fatalf("%+v %v", got, err)
		}
		if _, err := s.pendingIdentities([]gen.PendingIdentity{{Provider: "nope"}}); asProblem(t, err).Status() != 422 {
			t.Fatal("unknown provider")
		}
		if _, err := s.pendingIdentities([]gen.PendingIdentity{{Provider: "corp", Subject: "sub"}}); asProblem(t, err).Status() != 422 {
			t.Fatal("subject")
		}
	})
	t.Run("R-AUTH-027 an owner other than the caller is refused", func(t *testing.T) {
		alice := httpserver.Principal{Kind: httpserver.KindUser, Name: "alice"}
		other := "bob"
		self := "alice"
		if err := refuseOtherOwner(nil, alice); err != nil {
			t.Fatal(err)
		}
		if err := refuseOtherOwner(&self, alice); err != nil {
			t.Fatal(err)
		}
		if asProblem(t, refuseOtherOwner(&other, alice)).Code != errs.CodeForbidden {
			t.Fatal("impersonation")
		}
		if asProblem(t, refuseBreakGlass(httpserver.Principal{Kind: httpserver.KindBreakGlass})).Code != errs.CodeBreakGlassRestricted {
			t.Fatal("break-glass")
		}
	})
	t.Run("R-AUTH-029 a replay of a create that issued a value names the token", func(t *testing.T) {
		e := asProblem(t, tokenIssued("0192f0d1-1c2a-7c3e-8a51-6f7d2b9c4e11"))
		if e.Status() != 409 || e.Code != errs.CodeTokenAlreadyIssued || e.Extensions["tokenId"] != "0192f0d1-1c2a-7c3e-8a51-6f7d2b9c4e11" {
			t.Fatalf("%+v", e)
		}
	})
}

// TestIdentityOperationsWithoutServices: a replica without the identity
// services answers 503 and never panics (a worker has none).
func TestIdentityOperationsWithoutServices(t *testing.T) {
	s := newSetup(t, nil, authed)
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/v1/users", ""},
		{"GET", "/api/v1/users/me", ""},
		{"POST", "/api/v1/users", `{"username":"x"}`},
		{"GET", "/api/v1/groups", ""},
		{"GET", "/api/v1/service-accounts", ""},
		{"GET", "/api/v1/tokens", ""},
		{"GET", "/api/v1/users/me/sessions", ""},
		{"GET", "/api/v1/auth/login-options", ""},
		{"GET", "/api/v1/settings/auth", ""},
		{"GET", "/api/v1/users/me/preferences", ""},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		s.handler.ServeHTTP(w, req)
		// The M0 access stand-in decides the permission first (403 for a capability
		// the stand-in does not grant); what matters is no panic and a problem answer.
		if w.Code != http.StatusServiceUnavailable && w.Code != http.StatusForbidden {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
	}
}
