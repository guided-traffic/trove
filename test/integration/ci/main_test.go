//go:build integration

// Package ci tests the GitLab CI OIDC federation (docs/spec/gitlab-ci-oidc.md) against the integration
// stack with full in-process replicas (test/integration/testenv): the issuers and their signing keys (work
// package W1), the mappings (W3), the token exchange (W4) and the use of a CI token (W5) of
// docs/developer/ci-oidc.md, against the issuer double of test/oidcstub, which serves discovery and the key
// set over TLS, mints GitLab-shaped ID tokens, and rotates, fails and counts. Every test creates a database of its own; the replicas are the
// production composition root (app.New) with Valkey running or, where the criterion says so, stopped.
//
// The files of the package are named by what they test (issuers_test.go, keys_test.go, declared_test.go,
// tasks_test.go, exchange*_test.go, replay_test.go, ratelimit_test.go, …). The criteria that need what is not
// built yet have no test here; the contract page lists them (docs/developer/ci-oidc.md "Pending criteria").
package ci

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/ci"
	"github.com/guided-traffic/trove/internal/ci/subject"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/oidcstub"
)

// gitlabKeys is the number of keys the issuer double publishes, as GitLab 19.0.1 did when it was recorded
// (test/oidcstub/testdata/issuer.json: the OpenID Connect key and the CI signing key).
const gitlabKeys = 2

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

// options configure one installation.
type options struct {
	// valkey false is the state "Valkey stopped": every replica's Sentinel never answers.
	valkey bool
	// kidInterval is ci.signing_keys.unknown_kid_refresh_interval (default 1 s: the tests that rotate twice).
	kidInterval string
	// env is appended to every replica's environment.
	env []string
}

// env is one installation: its database, the principals and the replicas.
type env struct {
	t     *testing.T
	stack *testenv.Stack

	opsPAT   string // a user with the admin role, admin scope
	alicePAT string // a user without a role
	aliceID  string
	bobID    string
	bobPAT   string // a user who becomes a project Owner (admin scope, no role)
}

func newEnv(t *testing.T, o options) *env {
	t.Helper()
	if o.kidInterval == "" {
		o.kidInterval = "1s"
	}
	s := testenv.NewStack(t, testenv.StackOptions{Valkey: o.valkey, Name: "ci", Env: append([]string{
		// The issuer double listens on the loopback address, which the egress policy denies by default.
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.0/8","::1/128"]`,
		"TROVE_CLUSTER_EPOCH_POLL_INTERVAL=500ms", "TROVE_JOBS_POLL_INTERVAL=200ms",
		"TROVE_CI_SIGNING_KEYS_UNKNOWN_KID_REFRESH_INTERVAL=" + o.kidInterval,
	}, o.env...)})
	e := &env{t: t, stack: s}
	ops, alice, bob := s.User("ops"), s.User("alice"), s.User("bob")
	s.Bind("admin", ops)
	e.opsPAT = s.Token(ops, scope.Scope{Inherit: true, Admin: true})
	e.aliceID, e.alicePAT = alice.ID, s.Token(alice, scope.Default())
	e.bobID, e.bobPAT = bob.ID, s.Token(bob, scope.Scope{Inherit: true, Admin: true})
	return e
}

func (e *env) web(name string) *testenv.Replica {
	e.t.Helper()
	return e.stack.Start(e.t, testenv.ReplicaOptions{Name: name, Role: "web"})
}

func (e *env) worker(name string) *testenv.Replica {
	e.t.Helper()
	return e.stack.Start(e.t, testenv.ReplicaOptions{Name: name, Role: "worker"})
}

// do sends a management API request as the administrator to a replica.
func (e *env) do(r *testenv.Replica, method, path string, body any, hdr ...string) testenv.Answer {
	e.t.Helper()
	return e.stack.Do(r, method, path, e.opsPAT, body, hdr...)
}

// as sends a request with another credential.
func (e *env) as(r *testenv.Replica, token, method, path string, body any, hdr ...string) testenv.Answer {
	e.t.Helper()
	return e.stack.Do(r, method, path, token, body, hdr...)
}

func (e *env) exec(sql string, args ...any)        { e.t.Helper(); e.stack.Exec(sql, args...) }
func (e *env) text(sql string, args ...any) string { e.t.Helper(); return e.stack.Text(sql, args...) }

func (e *env) number(sql string, args ...any) int64 {
	e.t.Helper()
	return e.stack.Number(sql, args...)
}

// epoch is the epoch of a change area.
func (e *env) epoch(area string) int64 {
	e.t.Helper()
	return e.number("SELECT epoch FROM cluster_epoch WHERE area = $1", area)
}

// bump writes a change of an area the way the commit of another writer does (the two statements of
// R-ARCH-018 step 1), so the replicas' poll applies it: the way a test drops a replica's copy without a
// restart.
func (e *env) bump(area string, payload any) {
	e.t.Helper()
	p, _ := json.Marshal(map[string]any{"v": 1, "p": payload})
	e.exec(`WITH e AS (UPDATE cluster_epoch SET epoch = epoch + 1, changed_at = now() WHERE area = $1 RETURNING epoch)
	        INSERT INTO cluster_change (area, epoch, payload) SELECT $1, epoch, $2::text::jsonb FROM e`, area, string(p))
}

// conn opens a connection of its own to the installation's database, for a test that holds locks.
func (e *env) conn(t *testing.T) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(t.Context(), testenv.PgDSN(t, e.stack.Database()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(t.Context(), "SET search_path TO trove"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

// events counts the audit events of an action.
func (e *env) events(action string) int64 {
	e.t.Helper()
	return e.number("SELECT count(*) FROM audit_event WHERE action = $1", action)
}

// double is the issuer double and the files a test needs with it.
type double struct {
	stub   *oidcstub.Server
	run    *oidcstub.Running
	bundle string // the ca_bundle_ref of an issuer of this double
}

func startDouble(t *testing.T, cfg oidcstub.Config) *double {
	t.Helper()
	stub, err := oidcstub.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	run, ca, err := stub.StartSelfSigned()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	path := filepath.Join(t.TempDir(), "gitlab-ca.crt")
	if err := os.WriteFile(path, ca.CAPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return &double{stub: stub, run: run, bundle: "file:" + path}
}

// body is the create request of an issuer of this double, a private instance with the default audiences.
func (d *double) body(name string, extra ...map[string]any) map[string]any {
	b := map[string]any{
		"name": name, "kind": "gitlab", "issuer": d.stub.Issuer(), "publicIssuer": false, "caBundleRef": d.bundle,
	}
	for _, m := range extra {
		for k, v := range m {
			b[k] = v
		}
	}
	return b
}

// issuerOf resolves an issuer of the replica the way the exchange does.
func issuerOf(t *testing.T, r *testenv.Replica, iss string) ci.Issuer {
	t.Helper()
	is, ok, err := r.App.CIIssuers().Resolve(t.Context(), iss)
	if err != nil || !ok {
		t.Fatalf("%s: Resolve(%q) = %v, %v", r.Name, iss, ok, err)
	}
	return is
}

// keyLookup asks a replica's key source for the keys of a token and verifies the token's RS256 signature
// with them, the way the exchange does after it selected the issuer.
func keyLookup(t *testing.T, r *testenv.Replica, iss ci.Issuer, token string) error {
	t.Helper()
	h := tokenHeader(t, token)
	keys, err := r.App.CIIssuers().KeySource(iss).Keys(t.Context(), h.KID, h.Alg)
	if err != nil {
		return err
	}
	return verifyRS256(token, keys)
}

type header struct {
	Alg string `json:"alg"`
	KID string `json:"kid"`
}

func tokenHeader(t *testing.T, token string) header {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	var h header
	if err := json.Unmarshal(raw, &h); err != nil {
		t.Fatal(err)
	}
	return h
}

var errBadSignature = errors.New("the signature matches none of the keys")

func verifyRS256(token string, keys []subject.Key) error {
	parts := strings.Split(token, ".")
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	for _, k := range keys {
		if pub, ok := k.Public.(*rsa.PublicKey); ok && rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig) == nil {
			return nil
		}
	}
	return errBadSignature
}

func mint(t *testing.T, d *double, o oidcstub.MintOptions) string {
	t.Helper()
	tok, err := d.stub.Mint(o)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// problemFields lists "pointer code" of the errors[] of a problem body.
func problemFields(t *testing.T, a testenv.Answer) []string {
	t.Helper()
	var out []string
	list, _ := a.JSON(t)["errors"].([]any)
	for _, e := range list {
		m := e.(map[string]any)
		out = append(out, fmt.Sprintf("%v %v", m["pointer"], m["code"]))
	}
	return out
}

func wantStatus(t *testing.T, a testenv.Answer, status int, code string) {
	t.Helper()
	if a.Status != status || (code != "" && a.Code(t) != code) {
		t.Fatalf("answered %d %q, want %d %q\n%s", a.Status, codeOf(a), status, code, a.Body)
	}
}

func codeOf(a testenv.Answer) string {
	var m map[string]any
	if json.Unmarshal(a.Body, &m) != nil {
		return ""
	}
	c, _ := m["code"].(string)
	return c
}

func within(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	testenv.WaitFor(t, what, d, cond)
}

// etag is the ETag header of an answer.
func etag(a testenv.Answer) string { return a.Header.Get("ETag") }
