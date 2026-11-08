//go:build integration

package identity

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/auth/registrytoken"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/secrets"
)

// marked counts the statements a replica sends with a context that carries markKey: the statements of the one request
// a test marks, whatever else the replica's loops send meanwhile (the change-log poll, the last-use flush).
type marked struct{ n atomic.Int64 }

type markKey struct{}

func (m *marked) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	if ctx.Value(markKey{}) != nil {
		m.n.Add(1)
	}
	return ctx
}
func (m *marked) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func (m *marked) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	if ctx.Value(markKey{}) != nil {
		m.n.Add(1)
	}
	return ctx
}
func (m *marked) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {}
func (m *marked) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData)     {}

// keyRing writes the key files of the ids into dir (once per id) and loads the ring with the active key.
func keyRing(t *testing.T, dir, active string, ids ...string) *secrets.KeyRing {
	t.Helper()
	var files []string
	for _, id := range ids {
		p := filepath.Join(dir, id+".key")
		if _, err := os.Stat(p); err != nil {
			key := make([]byte, 32)
			_, _ = rand.Read(key)
			if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		files = append(files, p)
	}
	ring, err := secrets.Load(config.Secrets{EncryptionKeyFiles: files, ActiveKeyID: active})
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

// registryStage is the authentication stage of a replica with the registry's signer, built the way the composition root
// builds it (wire_identity.go wireAuth) on the replica's own stores.
func registryStage(t *testing.T, r *replica, ring *secrets.KeyRing) *auth.Stage {
	t.Helper()
	signer, err := registrytoken.NewSigner(ring, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return auth.New(auth.Options{
		Tokens: r.tokens, Uses: r.tokens, Principals: r.principals, Settings: r.settings, BasicEnabled: true,
		Limiter: auth.NewLocalLimiter(300, time.Minute, nil, nil), Log: quiet, Registry: signer,
		ClientAddr: func(*http.Request) netip.Addr { return netip.MustParseAddr("203.0.113.7") },
		RegistryRealm: func(*http.Request) (string, string) {
			return registrytoken.Realm("https://trove.test")
		},
	})
}

var registryRules = auth.EndpointRules{
	Name: "default", AcceptsSession: true, Anonymous: true, TokenTypes: []token.Type{token.TypePAT, token.TypeSAT, token.TypeCIT},
}

// issueFor runs a token request with the secret through the stage, as the token service does, and signs a registry token
// for the caller with a pull grant on web/docker-local/app; mutate changes the claims before they are signed.
func issueFor(t *testing.T, s *auth.Stage, ring *secrets.KeyRing, c auth.Presented, rules auth.EndpointRules, mutate ...func(*registrytoken.Claims)) string {
	t.Helper()
	res, err := s.AuthenticateTokenRequest(httptest.NewRequest(http.MethodGet, "/v2/_token", nil), rules, c)
	if err != nil {
		t.Fatalf("token request: %v", err)
	}
	signer, err := registrytoken.NewSigner(ring, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	claims := registrytoken.Claims{
		Anonymous: res.Anonymous, Endpoint: rules.Name, IssuedAt: now, ExpiresAt: now.Add(5 * time.Minute),
		Grant: registrytoken.Grant{Entries: []registrytoken.Entry{{Repo: qname.Repo{Project: "web", Key: "docker-local"}, Name: "app", Actions: registrytoken.Pull}}},
	}
	if !res.Anonymous {
		claims.PrincipalKind, claims.PrincipalID = string(res.Principal.Kind), res.Principal.ID
		claims.Binding = registrytoken.Binding{TokenID: res.Credential.TokenID, TokenType: string(res.Credential.TokenType), SecretSHA256: c.SecretSHA256()}
	}
	for _, m := range mutate {
		m(&claims)
	}
	tok, err := signer.Issue(claims)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// pull authenticates a GET of a manifest with the registry token on the stage; ctx may carry markKey.
func pull(ctx context.Context, s *auth.Stage, rgt string, rules auth.EndpointRules) (auth.Result, error) {
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/v2/web/docker-local/app/manifests/1", nil)
	r.Header.Set("Authorization", "Bearer "+rgt)
	return s.Authenticate(r, rules)
}

func basicPresented(secret string) auth.Presented {
	r := httptest.NewRequest(http.MethodGet, "/v2/_token", nil)
	r.SetBasicAuth("ci", secret)
	return auth.PresentedFrom(r)
}

// R-OCI-005: named in the test name by WP3 once /v2/ is mounted and the grant check runs.
func TestRegistryTokensAgainstTheRealStores(t *testing.T) {
	for _, mode := range []vkMode{vkLive, vkDead} {
		t.Run(mode.String(), func(t *testing.T) {
			e := newEnv(t)
			counter := &marked{}
			a := e.newReplica("a", replicaOpts{valkey: mode})
			b := e.newReplica("b", replicaOpts{valkey: mode, dbOpts: []postgres.Option{postgres.WithQueryTracer(counter)}})
			dir := t.TempDir()
			ring := keyRing(t, dir, "k1", "k1")
			sa, sb := registryStage(t, a, ring), registryStage(t, b, ring)
			alice := a.user("alice")
			pat := a.pat(alice)
			rgt := issueFor(t, sa, ring, basicPresented(pat.Secret), registryRules)

			t.Run("a registry token issued on one replica authenticates on every replica as its trove token's principal", func(t *testing.T) {
				for _, s := range []*auth.Stage{sa, sb} {
					res, err := pull(context.Background(), s, rgt, registryRules)
					if err != nil || res.Principal.ID != alice.ID || res.Credential.TokenID != pat.Record.ID || res.Credential.Registry == nil {
						t.Fatalf("%+v %v", res, err)
					}
				}
			})
			t.Run("a forged, tampered, expired or foreign registry token causes no database query", func(t *testing.T) {
				foreign := issueFor(t, registryStage(t, a, keyRing(t, t.TempDir(), "k1", "k1")), keyRing(t, t.TempDir(), "k1", "k1"),
					basicPresented(pat.Secret), registryRules)
				flipped := rgt[:30] + string(rgt[30]^1) + rgt[31:]
				expired := issueFor(t, sa, ring, basicPresented(pat.Secret), registryRules, func(c *registrytoken.Claims) {
					c.IssuedAt, c.ExpiresAt = c.IssuedAt.Add(-10*time.Minute), c.IssuedAt.Add(-5*time.Minute)
				})
				other := registryRules
				other.Name = "customers"
				fresh := e.newReplica("fresh", replicaOpts{valkey: mode, dbOpts: []postgres.Option{postgres.WithQueryTracer(counter)}})
				sf := registryStage(t, fresh, ring)
				ctx := context.WithValue(context.Background(), markKey{}, true)
				before := counter.n.Load()
				for name, c := range map[string]struct {
					raw   string
					rules auth.EndpointRules
				}{"tampered": {flipped, registryRules}, "foreign key": {foreign, registryRules}, "expired": {expired, registryRules}, "another endpoint": {rgt, other}} {
					_, err := pull(ctx, sf, c.raw, c.rules)
					if ee, ok := errs.As(err); !ok || ee.Status() != 401 {
						t.Fatalf("%s: %v", name, err)
					}
				}
				if n := counter.n.Load() - before; n != 0 {
					t.Fatalf("%d statements for refused registry tokens", n)
				}
				// The control: the valid token on a replica whose cache does not hold it reads PostgreSQL, so the counter
				// counts what it should.
				if _, err := pull(ctx, sf, rgt, registryRules); err != nil {
					t.Fatal(err)
				}
				if counter.n.Load() == before {
					t.Fatal("the counter saw no statement of a valid token's first lookup")
				}
			})
			t.Run("R-AUTH-023 revoking the PAT stops its registry token on every replica within the revocation bound", func(t *testing.T) {
				if _, err := pull(context.Background(), sb, rgt, registryRules); err != nil {
					t.Fatal(err) // cached on B
				}
				if _, err := a.tokens.Revoke(context.Background(), admin, pat.Record.ID, token.ReasonOwner); err != nil {
					t.Fatal(err)
				}
				took := waitFor(t, "replica B rejecting the registry token of the revoked PAT", revocationBound, func() bool {
					_, err := pull(context.Background(), sb, rgt, registryRules)
					ee, ok := errs.As(err)
					return ok && ee.Status() == 401 && ee.Code == errs.CodeInvalidToken
				})
				t.Logf("%s: replica B rejected the registry token after %s", mode, took)
				if _, err := pull(context.Background(), sa, rgt, registryRules); err == nil {
					t.Fatal("replica A still accepts the registry token")
				}
			})
		})
	}
}

// R-OCI-005: named in the test name by WP3 once /v2/ is mounted and the grant check runs.
func TestAnonymousRegistryTokenAgainstTheRealStores(t *testing.T) {
	e := newEnv(t)
	a, b := e.newReplica("a", replicaOpts{}), e.newReplica("b", replicaOpts{})
	ring := keyRing(t, t.TempDir(), "k1", "k1")
	sa, sb := registryStage(t, a, ring), registryStage(t, b, ring)
	ctx := context.Background()
	set := func(on bool) {
		t.Helper()
		if _, err := a.settings.Update(ctx, admin, token.SettingsPatch{Expected: errs.AnyRevision, AnonymousAccess: ptr(on)}); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("R-AUTH-037 without auth.anonymous a token request without a credential is 401; with it, an anonymous token", func(t *testing.T) {
		closed := registryRules
		closed.Anonymous = false
		set(true)
		_, err := sa.AuthenticateTokenRequest(httptest.NewRequest(http.MethodGet, "/v2/_token", nil), closed, auth.PresentedFrom(httptest.NewRequest(http.MethodGet, "/", nil)))
		if ee, ok := errs.As(err); !ok || ee.Status() != 401 || ee.Challenge != `Basic realm="trove"` {
			t.Fatalf("endpoint without anonymous: %v", err)
		}
		rgt := issueFor(t, sa, ring, auth.PresentedFrom(httptest.NewRequest(http.MethodGet, "/", nil)), registryRules)
		res, err := pull(ctx, sb, rgt, registryRules)
		if err != nil || !res.Anonymous || res.Credential.Registry == nil {
			t.Fatalf("%+v %v", res, err)
		}
		if _, err := pull(ctx, sb, rgt, closed); err == nil {
			t.Fatal("an anonymous registry token passed an endpoint without anonymous requests")
		}

		t.Run("R-AUTH-037 switching anonymous access off stops the anonymous token on every replica within the bound", func(t *testing.T) {
			set(false)
			waitFor(t, "replica B refusing the anonymous registry token", revocationBound, func() bool {
				_, err := pull(ctx, sb, rgt, registryRules)
				return err != nil
			})
		})
	})
}

// R-OCI-005: the registry token's part is named in the test name by WP3 once /v2/ is mounted and the grant check runs.
func TestR_ARCH_021_RegistryTokenKeyRingRotationAcrossReplicas(t *testing.T) {
	e := newEnv(t)
	a, b := e.newReplica("a", replicaOpts{}), e.newReplica("b", replicaOpts{})
	alice := a.user("alice")
	pat := a.pat(alice)
	dir := t.TempDir()
	before := keyRing(t, dir, "k1", "k1")       // the ring before the rotation
	during := keyRing(t, dir, "k2", "k1", "k2") // step 2 of R-ARCH-021: k2 active, k1 still in the ring
	after := keyRing(t, dir, "k2", "k2")        // step 4: k1 removed
	old := issueFor(t, registryStage(t, a, before), before, basicPresented(pat.Secret), registryRules)

	t.Run("a token signed before the rotation is accepted while its key is in the ring", func(t *testing.T) {
		if _, err := pull(context.Background(), registryStage(t, b, during), old, registryRules); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("new tokens name the new key, and the old token ends with the old key's removal", func(t *testing.T) {
		fresh := issueFor(t, registryStage(t, a, during), during, basicPresented(pat.Secret), registryRules)
		s := registryStage(t, b, after)
		if _, err := pull(context.Background(), s, fresh, registryRules); err != nil {
			t.Fatal(err)
		}
		if _, err := pull(context.Background(), s, old, registryRules); err == nil {
			t.Fatal("a token of a removed key authenticated")
		}
	})
}
