package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/registrytoken"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/secrets"
)

// hashTokens is fakeTokens with the lookup by the SHA-256 of a secret that a registry token uses (R-OCI-005).
type hashTokens struct {
	*fakeTokens
}

func (h hashTokens) CheckSecretHash(ctx context.Context, hash [32]byte, typ token.Type, req token.Request) (token.Info, error) {
	h.mu.Lock()
	var raw string
	for k := range h.known {
		if token.Hash(k) == hash {
			raw = k
		}
	}
	for k := range h.errFor {
		if token.Hash(k) == hash {
			raw = k
		}
	}
	h.mu.Unlock()
	if raw == "" {
		h.calls.Add(1)
		return token.Info{}, token.ErrUnknown
	}
	if t, _ := token.Check(raw); t != typ {
		h.calls.Add(1)
		return token.Info{}, token.ErrUnknown
	}
	return h.Check(ctx, raw, req)
}

func testSigner(t *testing.T) *registrytoken.Signer {
	t.Helper()
	p := filepath.Join(t.TempDir(), "k1.key")
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	ring, err := secrets.Load(config.Secrets{EncryptionKeyFiles: []string{p}, ActiveKeyID: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := registrytoken.NewSigner(ring, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var (
	regNow  = time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	regRepo = qname.Repo{Project: "web", Key: "docker-local"}
)

// registryFixture is a stage with a registry signer, the token service's realm and a scope hook, over fakeTokens that
// know one PAT of alice.
type registryFixture struct {
	pat    string
	ft     *fakeTokens
	signer *registrytoken.Signer
	stage  *Stage
	now    time.Time
}

func newRegistryFixture(t *testing.T, anon bool) *registryFixture {
	t.Helper()
	f := &registryFixture{pat: newToken(t, token.TypePAT), signer: testSigner(t), now: regNow}
	exp := regNow.Add(24 * time.Hour)
	f.ft = &fakeTokens{known: map[string]token.Info{f.pat: {ID: "t1", Type: token.TypePAT, Scope: token.Scope{Inherit: true}, Owner: alice, ExpiresAt: &exp}}}
	f.stage = New(Options{
		Tokens: hashTokens{f.ft}, Uses: f.ft, Principals: fakeStore{}, Settings: fakeAnon{on: anon}, BasicEnabled: true,
		Registry: f.signer, Now: func() time.Time { return f.now },
		RegistryRealm: func(*http.Request) (string, string) {
			return registrytoken.Realm("https://trove.example.com")
		},
		RegistryScope: func(r *http.Request) string {
			if strings.HasSuffix(r.URL.Path, "/blobs/uploads/") {
				return registrytoken.RepositoryScope("web/docker-local/app", registrytoken.Push)
			}
			return ""
		},
	})
	return f
}

// issue signs a registry token for the fixture's PAT (or an anonymous one) with the given grant.
func (f *registryFixture) issue(t *testing.T, anonymous bool, mutate ...func(*registrytoken.Claims)) string {
	t.Helper()
	c := registrytoken.Claims{
		Endpoint: "default", IssuedAt: regNow, ExpiresAt: regNow.Add(5 * time.Minute),
		Grant: registrytoken.Grant{Entries: []registrytoken.Entry{{Repo: regRepo, Name: "app", Actions: registrytoken.Pull}}},
	}
	if anonymous {
		c.Anonymous = true
	} else {
		c.PrincipalKind, c.PrincipalID = string(alice.Kind), alice.ID
		c.Binding = registrytoken.Binding{TokenID: "t1", TokenType: "pat", SecretSHA256: token.Hash(f.pat)}
	}
	for _, m := range mutate {
		m(&c)
	}
	tok, err := f.signer.Issue(c)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// R-OCI-005: named in the test name by WP3 once /v2/ is mounted and the grant check runs.
func TestRegistryTokenInTheStage(t *testing.T) {
	f := newRegistryFixture(t, false)
	rgt := f.issue(t, false)
	manifest := "/v2/web/docker-local/app/manifests/1"

	t.Run("a registry token below /v2/ is a request of the trove token's principal, with the grant as a further ceiling", func(t *testing.T) {
		res, err := f.stage.Authenticate(req("GET", manifest, "Authorization", "Bearer "+rgt), defaultRules())
		if err != nil {
			t.Fatal(err)
		}
		c := res.Credential
		if res.Anonymous || res.Principal.ID != alice.ID || c.Method != MethodBearer || c.TokenID != "t1" || c.TokenType != token.TypePAT ||
			c.Scope == nil || c.Registry == nil || !c.Registry.Allows(regRepo, "app", registrytoken.Pull) || c.Registry.Allows(regRepo, "app", registrytoken.Push) {
			t.Fatalf("result %+v", res)
		}
		if c.ExpiresAt == nil || !c.ExpiresAt.Equal(regNow.Add(5*time.Minute)) {
			t.Fatalf("expiry %v: the registry token's, not the trove token's", c.ExpiresAt)
		}
	})
	t.Run("a registry token is accepted below /v2/ only, and as Bearer only", func(t *testing.T) {
		calls := f.ft.calls.Load()
		for _, r := range []*http.Request{
			req("GET", "/api/npm/web/npm-local/lodash", "Authorization", "Bearer "+rgt),
			req("GET", "/api/v1/auth/whoami", "Authorization", "Bearer "+rgt),
			req("GET", "/files/web/docker-local/app/_tags/1", "Authorization", "Bearer "+rgt),
			req("GET", manifest, "Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("x:"+rgt))),
		} {
			_, err := f.stage.Authenticate(r, defaultRules())
			wantErr(t, err, 401, errs.CodeInvalidToken)
		}
		if f.ft.calls.Load() != calls {
			t.Fatal("a registry token outside its place reached the token lookup")
		}
	})
	t.Run("a tampered, expired or foreign token is refused without a token lookup", func(t *testing.T) {
		calls := f.ft.calls.Load()
		flipped := rgt[:20] + string(rgt[20]^1) + rgt[21:]
		other := newRegistryFixture(t, false)
		foreign := other.issue(t, false, func(c *registrytoken.Claims) { c.Binding.SecretSHA256 = token.Hash(f.pat) })
		for name, raw := range map[string]string{"flipped": flipped, "foreign": foreign} {
			_, err := f.stage.Authenticate(req("GET", manifest, "Authorization", "Bearer "+raw), defaultRules())
			e := wantErr(t, err, 401, errs.CodeInvalidToken)
			if !strings.Contains(e.Challenge, `error="invalid_token"`) || !strings.HasPrefix(e.Challenge, `Bearer realm="https://trove.example.com/v2/_token"`) {
				t.Fatalf("%s: challenge %q", name, e.Challenge)
			}
		}
		f.now = regNow.Add(5 * time.Minute)
		_, err := f.stage.Authenticate(req("GET", manifest, "Authorization", "Bearer "+rgt), defaultRules())
		wantErr(t, err, 401, errs.CodeInvalidToken)
		f.now = regNow
		other.now = regNow
		rules := defaultRules()
		rules.Name = "customers"
		_, err = f.stage.Authenticate(req("GET", manifest, "Authorization", "Bearer "+rgt), rules)
		wantErr(t, err, 401, errs.CodeInvalidToken)
		if f.ft.calls.Load() != calls {
			t.Fatalf("%d token lookups for refused registry tokens", f.ft.calls.Load()-calls)
		}
	})
	t.Run("R-AUTH-023 a revoked trove token stops its registry token", func(t *testing.T) {
		f.ft.mu.Lock()
		info := f.ft.known[f.pat]
		delete(f.ft.known, f.pat)
		f.ft.errFor = map[string]error{f.pat: token.ErrRevoked}
		f.ft.mu.Unlock()
		defer func() {
			f.ft.mu.Lock()
			f.ft.known[f.pat], f.ft.errFor = info, nil
			f.ft.mu.Unlock()
		}()
		_, err := f.stage.Authenticate(req("GET", manifest, "Authorization", "Bearer "+rgt), defaultRules())
		wantErr(t, err, 401, errs.CodeInvalidToken)
	})
	t.Run("claims whose trove token is another one than they name are refused", func(t *testing.T) {
		mismatch := f.issue(t, false, func(c *registrytoken.Claims) { c.Binding.TokenID = "t2" })
		_, err := f.stage.Authenticate(req("GET", manifest, "Authorization", "Bearer "+mismatch), defaultRules())
		wantErr(t, err, 401, errs.CodeInvalidToken)
		owner := f.issue(t, false, func(c *registrytoken.Claims) { c.PrincipalID = "someone-else" })
		_, err = f.stage.Authenticate(req("GET", manifest, "Authorization", "Bearer "+owner), defaultRules())
		wantErr(t, err, 401, errs.CodeInvalidToken)
	})
	t.Run("R-EP-010 a claim of another token type than the cached token's is refused, whatever the endpoint accepts", func(t *testing.T) {
		// The fake answers by the hash; a cache hit of the real service does not compare the type either.
		f.ft.mu.Lock()
		info := f.ft.known[f.pat]
		info.Type = token.TypeSAT
		f.ft.known[f.pat] = info
		f.ft.mu.Unlock()
		defer func() {
			f.ft.mu.Lock()
			info.Type = token.TypePAT
			f.ft.known[f.pat] = info
			f.ft.mu.Unlock()
		}()
		_, err := f.stage.Authenticate(req("GET", manifest, "Authorization", "Bearer "+rgt), defaultRules())
		wantErr(t, err, 401, errs.CodeInvalidToken)
	})
	t.Run("R-EP-010 the endpoint's token types apply to the trove token, without a lookup", func(t *testing.T) {
		calls := f.ft.calls.Load()
		seen := &refusals{}
		rules := defaultRules()
		rules.TokenTypes, rules.Refused = []token.Type{token.TypeSAT}, seen.hook
		_, err := f.stage.Authenticate(req("GET", manifest, "Authorization", "Bearer "+rgt), rules)
		wantErr(t, err, 401, errs.CodeInvalidToken)
		if f.ft.calls.Load() != calls || seen.last().Reason != RefusedTokenType {
			t.Fatalf("lookups %d refusal %+v", f.ft.calls.Load()-calls, seen.last())
		}
	})
	t.Run("an escaped path that only decodes to /v2/ does not admit a registry token", func(t *testing.T) {
		calls := f.ft.calls.Load()
		r := req("GET", "/v2/web/docker-local/app/manifests/1", "Authorization", "Bearer "+rgt)
		r.URL.Path, r.URL.RawPath = "/v2/x", "/v2%2Fx"
		_, err := f.stage.Authenticate(r, defaultRules())
		wantErr(t, err, 401, errs.CodeInvalidToken)
		if f.ft.calls.Load() != calls {
			t.Fatal("the registry token reached the token lookup")
		}
	})
	t.Run("a stage without a signer refuses every registry token", func(t *testing.T) {
		s := newStage(f.ft, Options{})
		_, err := s.Authenticate(req("GET", manifest, "Authorization", "Bearer "+rgt), defaultRules())
		wantErr(t, err, 401, errs.CodeInvalidToken)
	})
}

// R-OCI-005: named in the test name by WP3 once /v2/ is mounted and the grant check runs.
func TestAnonymousRegistryToken(t *testing.T) {
	manifest := "/v2/web/docker-hub/library/alpine/manifests/3"
	t.Run("accepted as the anonymous principal while the endpoint and the switch allow anonymous requests", func(t *testing.T) {
		f := newRegistryFixture(t, true)
		res, err := f.stage.Authenticate(req("GET", manifest, "Authorization", "Bearer "+f.issue(t, true)), defaultRules())
		if err != nil || !res.Anonymous || res.Credential.Registry == nil || res.Credential.TokenID != "" {
			t.Fatalf("%+v %v", res, err)
		}
	})
	t.Run("refused once the runtime switch is off", func(t *testing.T) {
		f := newRegistryFixture(t, false)
		_, err := f.stage.Authenticate(req("GET", manifest, "Authorization", "Bearer "+f.issue(t, true)), defaultRules())
		wantErr(t, err, 401, errs.CodeInvalidToken)
	})
	t.Run("refused on an endpoint without anonymous requests", func(t *testing.T) {
		f := newRegistryFixture(t, true)
		seen := &refusals{}
		rules := defaultRules()
		rules.Anonymous, rules.Refused = false, seen.hook
		_, err := f.stage.Authenticate(req("GET", manifest, "Authorization", "Bearer "+f.issue(t, true)), rules)
		wantErr(t, err, 401, errs.CodeInvalidToken)
		if seen.last().Reason != RefusedAnonymous {
			t.Fatalf("refusal %+v", seen.last())
		}
	})
}

// R-OCI-004: named in the test name by WP3 once /v2/ is mounted and the grant check runs.
func TestRegistryRoutesInTheStage(t *testing.T) {
	f := newRegistryFixture(t, false)
	t.Run("the session cookie is never a credential below /v2/", func(t *testing.T) {
		s := New(Options{
			Tokens: hashTokens{f.ft}, Principals: fakeStore{}, Settings: fakeAnon{}, BasicEnabled: true,
			Sessions: fakeSessions{info: session.Info{ID: "s1", Principal: alice}},
			RegistryRealm: func(*http.Request) (string, string) {
				return registrytoken.Realm("https://trove.example.com")
			},
		})
		for _, m := range []string{"GET", "HEAD", "PUT", "DELETE"} {
			r := req(m, "/v2/web/docker-local/app/manifests/1")
			r.AddCookie(&http.Cookie{Name: SessionCookie, Value: "sess"})
			_, err := s.Authenticate(r, defaultRules())
			e := wantErr(t, err, 401, errs.CodeCredentialsRequired)
			if e.Challenge != `Bearer realm="https://trove.example.com/v2/_token",service="trove.example.com"` {
				t.Fatalf("%s: challenge %q", m, e.Challenge)
			}
		}
	})
	t.Run("the challenge below /v2/ names the realm, the service and the scope of the route", func(t *testing.T) {
		_, err := f.stage.Authenticate(req("POST", "/v2/web/docker-local/app/blobs/uploads/"), defaultRules())
		e := wantErr(t, err, 401, errs.CodeCredentialsRequired)
		want := `Bearer realm="https://trove.example.com/v2/_token",service="trove.example.com",scope="repository:web/docker-local/app:push"`
		if e.Challenge != want {
			t.Fatalf("challenge %q", e.Challenge)
		}
		_, err = f.stage.Authenticate(req("GET", "/v2/"), defaultRules())
		e = wantErr(t, err, 401, errs.CodeCredentialsRequired)
		if e.Challenge != `Bearer realm="https://trove.example.com/v2/_token",service="trove.example.com"` {
			t.Fatalf("version check challenge %q", e.Challenge)
		}
	})
	t.Run("the token service's route is not authenticated by the chain: the service presents the credential itself", func(t *testing.T) {
		calls := f.ft.calls.Load()
		for _, m := range []string{"GET", "POST"} {
			res, err := f.stage.Authenticate(req(m, "/v2/_token", "Authorization", "Basic !!!"), EndpointRules{Name: "default"})
			if err != nil || !res.Anonymous {
				t.Fatalf("%s: %+v %v", m, res, err)
			}
		}
		if f.ft.calls.Load() != calls {
			t.Fatal("the chain looked a token request's credential up")
		}
	})
}

// R-OCI-004: named in the test name by WP3 once /v2/ is mounted and the grant check runs.
func TestAuthenticateTokenRequest(t *testing.T) {
	f := newRegistryFixture(t, false)
	tokenReq := func(m string, hdr ...string) *http.Request {
		return req(m, "/v2/_token?service=trove.example.com", hdr...)
	}
	basic := func(secret string) string { return "Basic " + base64.StdEncoding.EncodeToString([]byte("ci:"+secret)) }

	t.Run("a trove token as Basic, as Bearer or as the form's password yields its principal", func(t *testing.T) {
		for name, c := range map[string]Presented{
			"basic":  PresentedFrom(tokenReq("GET", "Authorization", basic(f.pat))),
			"bearer": PresentedFrom(tokenReq("GET", "Authorization", "Bearer "+f.pat)),
			"form":   PresentedForm("ci", f.pat),
		} {
			res, err := f.stage.AuthenticateTokenRequest(tokenReq("GET"), defaultRules(), c)
			if err != nil || res.Principal.ID != alice.ID || res.Credential.TokenID != "t1" || res.Credential.Registry != nil {
				t.Fatalf("%s: %+v %v", name, res, err)
			}
			if c.SecretSHA256() != token.Hash(f.pat) {
				t.Fatalf("%s: the binding is not the hash of the presented secret", name)
			}
		}
	})
	t.Run("an invalid credential is 401 with Basic realm=\"trove\"; a registry token cannot renew itself", func(t *testing.T) {
		rgt := f.issue(t, false)
		for name, c := range map[string]Presented{
			"unknown":    PresentedForm("ci", newToken(t, token.TypePAT)),
			"registry":   PresentedFrom(tokenReq("GET", "Authorization", "Bearer "+rgt)),
			"unreadable": PresentedFrom(tokenReq("GET", "Authorization", "Digest x")),
		} {
			_, err := f.stage.AuthenticateTokenRequest(tokenReq("GET"), defaultRules(), c)
			e := wantErr(t, err, 401, errs.CodeInvalidToken)
			if e.Challenge != `Basic realm="trove"` {
				t.Fatalf("%s: challenge %q", name, e.Challenge)
			}
		}
		if k := PresentedFrom(tokenReq("GET", "Authorization", "Digest x")).Kind(); k != "unreadable" {
			t.Fatalf("kind %q", k)
		}
	})
	t.Run("without a credential: anonymous where the endpoint and the switch allow it, else 401", func(t *testing.T) {
		none := PresentedFrom(tokenReq("GET"))
		if !none.None() || none.Kind() != "none" || !PresentedForm("ci", "").None() {
			t.Fatal("an absent credential is not none")
		}
		_, err := f.stage.AuthenticateTokenRequest(tokenReq("GET"), defaultRules(), none)
		e := wantErr(t, err, 401, errs.CodeCredentialsRequired)
		if e.Challenge != `Basic realm="trove"` {
			t.Fatalf("challenge %q", e.Challenge)
		}
		open := newRegistryFixture(t, true)
		res, err := open.stage.AuthenticateTokenRequest(tokenReq("GET"), defaultRules(), none)
		if err != nil || !res.Anonymous {
			t.Fatalf("%+v %v", res, err)
		}
		rules := defaultRules()
		rules.Anonymous = false
		_, err = open.stage.AuthenticateTokenRequest(tokenReq("GET"), rules, none)
		wantErr(t, err, 401, errs.CodeCredentialsRequired)
	})
	t.Run("a browser gets the Bearer challenge on the token service too", func(t *testing.T) {
		_, err := f.stage.AuthenticateTokenRequest(tokenReq("GET", "Sec-Fetch-Mode", "navigate"), defaultRules(), Presented{})
		e := wantErr(t, err, 401, errs.CodeCredentialsRequired)
		if e.Challenge != `Bearer realm="trove"` {
			t.Fatalf("challenge %q", e.Challenge)
		}
	})
}

// countingLimiter counts the failures the stage reports per bucket and never throttles.
type countingLimiter struct {
	mu sync.Mutex
	n  map[string]int
}

func (l *countingLimiter) Exceeded(context.Context, string) bool { return false }
func (l *countingLimiter) Count(_ context.Context, bucket string) {
	l.mu.Lock()
	l.n[bucket]++
	l.mu.Unlock()
}

func (l *countingLimiter) of(bucket string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.n[bucket]
}

func TestR_AUTH_038_AForgedRegistryTokenCountsAsAFailedCredential(t *testing.T) {
	f := newRegistryFixture(t, false)
	lim := &countingLimiter{n: map[string]int{}}
	s := New(Options{
		Tokens: hashTokens{f.ft}, Principals: fakeStore{}, Settings: fakeAnon{}, Registry: f.signer, Now: func() time.Time { return regNow },
		Limiter: lim, ClientAddr: func(*http.Request) netip.Addr { return netip.MustParseAddr("203.0.113.7") },
	})
	rgt := f.issue(t, false)
	forged := rgt[:20] + string(rgt[20]^1) + rgt[21:]
	_, err := s.Authenticate(req("GET", "/v2/web/docker-local/app/manifests/1", "Authorization", "Bearer "+forged), defaultRules())
	wantErr(t, err, 401, errs.CodeInvalidToken)
	if lim.of("203.0.113.7") != 1 {
		t.Fatalf("failures of the address: %d", lim.of("203.0.113.7"))
	}
	if _, err := s.Authenticate(req("GET", "/v2/web/docker-local/app/manifests/1", "Authorization", "Bearer "+rgt), defaultRules()); err != nil {
		t.Fatal(err)
	}
	if lim.of("203.0.113.7") != 1 {
		t.Fatal("an accepted registry token counted as a failure")
	}
}

func TestR_EP_011_TheTroveTokensRulesReachItsRegistryToken(t *testing.T) {
	manifest := "/v2/web/docker-local/app/manifests/1"
	set := func(f *registryFixture, mutate func(*token.Info)) {
		f.ft.mu.Lock()
		info := f.ft.known[f.pat]
		mutate(&info)
		f.ft.known[f.pat] = info
		f.ft.mu.Unlock()
	}
	t.Run("a trove token bound to another endpoint: bound_elsewhere", func(t *testing.T) {
		f := newRegistryFixture(t, false)
		set(f, func(i *token.Info) { i.Scope.Endpoints = []string{"customers"} })
		seen := &refusals{}
		rules := defaultRules()
		rules.Refused = seen.hook
		_, err := f.stage.Authenticate(req("GET", manifest, "Authorization", "Bearer "+f.issue(t, false)), rules)
		wantErr(t, err, 401, errs.CodeInvalidToken)
		if seen.last().Reason != RefusedTokenBinding {
			t.Fatalf("refusal %+v", seen.last())
		}
	})
	t.Run("an unbound trove token on an endpoint that takes bound tokens only: unbound", func(t *testing.T) {
		f := newRegistryFixture(t, false)
		seen := &refusals{}
		rules := defaultRules()
		rules.BoundTokensOnly, rules.Refused = true, seen.hook
		_, err := f.stage.Authenticate(req("GET", manifest, "Authorization", "Bearer "+f.issue(t, false)), rules)
		wantErr(t, err, 401, errs.CodeInvalidToken)
		if seen.last().Reason != RefusedTokenBinding {
			t.Fatalf("refusal %+v", seen.last())
		}
	})
	t.Run("R-AUTH-002 the trove token's owner is disabled: principal_disabled", func(t *testing.T) {
		f := newRegistryFixture(t, false)
		f.ft.mu.Lock()
		delete(f.ft.known, f.pat)
		f.ft.errFor = map[string]error{f.pat: token.ErrOwner}
		f.ft.mu.Unlock()
		_, err := f.stage.Authenticate(req("GET", manifest, "Authorization", "Bearer "+f.issue(t, false)), defaultRules())
		e := wantErr(t, err, 401, errs.CodePrincipalDisabled)
		if strings.Contains(e.Challenge, "invalid_token") {
			t.Fatalf("challenge %q", e.Challenge)
		}
	})
}

func TestR_AUTH_001_APresentedCredentialNeverPrintsItsSecret(t *testing.T) {
	pat := newToken(t, token.TypePAT)
	r := req("GET", "/v2/_token")
	r.SetBasicAuth("ci", pat)
	holder := struct {
		Credential Presented
		hidden     Presented
	}{PresentedFrom(r), PresentedForm("ci", pat)}
	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("token request", "credential", holder.Credential)
	for _, out := range []string{
		fmt.Sprint(holder.Credential), fmt.Sprintf("%v", holder), fmt.Sprintf("%+v", holder), fmt.Sprintf("%#v", holder),
		fmt.Sprintf("%#v", holder.hidden), holder.hidden.String(), logged.String(),
	} {
		if strings.Contains(out, "trv_") || strings.Contains(out, pat[8:20]) {
			t.Fatalf("printed: %s", out)
		}
	}
	if holder.Credential.String() != "auth.Presented{basic}" || holder.hidden.SecretSHA256() != token.Hash(pat) {
		t.Fatalf("%s", holder.Credential)
	}
}
