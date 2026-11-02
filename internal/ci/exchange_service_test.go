package ci

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/ci/reason"
	"github.com/guided-traffic/trove/internal/ci/subject"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The exchange without PostgreSQL: the order of R-CI-007 against fakes of the issuer service, the mapping store, the
// token service and the statements of the transaction. The integration tests of test/integration/ci run the same
// against the stack; these hold what the stack cannot produce, the second mismatch inside one transaction
// (Q-CI-43), and the parts that need no database.

const testIss = "https://gitlab.test"

type fakeTx struct{ meta.Tx }

type fakeRunner struct{}

func (fakeRunner) InTx(_ context.Context, fn func(tx meta.Tx) error) error { return fn(fakeTx{}) }

type fakeKeys struct{ keys []subject.Key }

func (k fakeKeys) Keys(context.Context, string, string) ([]subject.Key, error) { return k.keys, nil }

// fakeIssuers is the issuer service: the copy holds copy, the row FOR SHARE is row.
type fakeIssuers struct {
	Issuers
	copy, row Issuer
	keys      subject.KeySource
	reloads   int
}

func (f *fakeIssuers) Resolve(_ context.Context, iss string) (Issuer, bool, error) {
	return f.copy, iss == f.copy.Issuer, nil
}
func (f *fakeIssuers) KeySource(Issuer) subject.KeySource { return f.keys }
func (f *fakeIssuers) LockShareTx(context.Context, meta.Tx, string) (Issuer, error) {
	return f.row, nil
}
func (f *fakeIssuers) Reload(context.Context, Issuer) { f.reloads++ }

// fakeMappings is the mapping store: the copy is set, the read in the transaction fresh.
type fakeMappings struct {
	Mappings
	set, fresh MappingSet
	freshReads int
}

func (f *fakeMappings) Set(context.Context, string) (MappingSet, error) { return f.set, nil }
func (f *fakeMappings) SetTx(context.Context, meta.Tx, string) (MappingSet, error) {
	f.freshReads++
	return f.fresh, nil
}

type fakeTokens struct{ issued []token.CITRequest }

func (f *fakeTokens) IssueCITx(_ context.Context, _ meta.Tx, r token.CITRequest) (token.CITIssued, error) {
	if err := r.Frozen.Check(); err != nil {
		return token.CITIssued{}, err
	}
	f.issued = append(f.issued, r)
	return token.CITIssued{ID: "tok-" + fmt.Sprint(len(f.issued)), Secret: "trv_cit_secret", Hint: "trv_cit_…cret", ExpiresAt: time.Now().Add(r.TTL)}, nil
}

// fakeQueries are the statements: every matched mapping unchanged unless changed names it.
type fakeQueries struct {
	versions map[string]int64
	uses     map[string]int32
}

func (f *fakeQueries) CiExchangeLockMappings(_ context.Context, ids []string) ([]q.CiExchangeLockMappingsRow, error) {
	var out []q.CiExchangeLockMappingsRow
	for _, id := range ids {
		out = append(out, q.CiExchangeLockMappingsRow{ID: id, Enabled: true, Version: f.versions[id]})
	}
	return out, nil
}
func (f *fakeQueries) CiExchangeRestoredAt(context.Context) (int64, error) { return 0, nil }
func (f *fakeQueries) CiExchangeUseJTI(_ context.Context, a q.CiExchangeUseJTIParams) (int32, error) {
	f.uses[a.Jti]++
	return f.uses[a.Jti], nil
}

func (f *fakeQueries) CiExchangeInsertIssuance(context.Context, q.CiExchangeInsertIssuanceParams) error {
	return nil
}

type noIndex struct{}

func (noIndex) UnknownKey(string) bool          { return false }
func (noIndex) Held(claims.Action, string) bool { return false }
func (noIndex) PolicyItems() []string           { return nil }

// recorder keeps the events.
type recorder struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *recorder) RecordTx(_ context.Context, _ meta.Tx, ev audit.Event) error {
	r.Record(context.Background(), ev)
	return nil
}

func (r *recorder) Record(_ context.Context, ev audit.Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *recorder) of(typ string) []audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []audit.Event
	for _, e := range r.events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// testExchange is an exchange over the fakes with one issuer and one mapping that matches project 7.
type testExchange struct {
	s        *ExchangeService
	issuers  *fakeIssuers
	mappings *fakeMappings
	tokens   *fakeTokens
	queries  *fakeQueries
	rec      *recorder
	key      *rsa.PrivateKey
}

func testMapping(version int64, priority int) Mapping {
	return Mapping{
		ID: "m-1", IssuerID: "i-1", Issuer: "gitlab-test", PrincipalID: "p-1", Ref: "ci:gitlab-test/read", Name: "read", Tier: TierSystem,
		Priority: priority, Enabled: true, SubjectKinds: []string{SubjectIDToken}, Version: version,
		Match:  mustNode(`{"claim": "project_id", "equals": "7"}`),
		Grants: claims.Spec{Inline: []claims.InlineGrant{{Repositories: []string{"npm-internal"}, Packages: &claims.PatternSet{Include: []string{"*"}}, Actions: []claims.Action{claims.Read}}}},
	}
}

func mustNode(s string) claims.Node {
	n, err := claims.ParseCondition([]byte(s))
	if err != nil {
		panic(err)
	}
	return n
}

func newTestExchange(t *testing.T) *testExchange {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	is := Issuer{
		ID: "i-1", Name: "gitlab-test", Kind: KindGitLab, Issuer: testIss, Enabled: true, AllowedAudiences: []string{"https://trove.test"},
		ExclusiveAudience: true, AllowedAlgorithms: []string{"RS256"}, AllowedTyp: []string{"JWT"}, ClockSkewSeconds: 60,
		PinClaims: []string{"project_id"}, Version: 3, MappingsVersion: 5,
	}
	set := MappingSet{IssuerID: "i-1", IssuerVersion: 3, MappingsVersion: 5, Mappings: []Mapping{testMapping(1, 10)}}
	x := &testExchange{
		issuers:  &fakeIssuers{copy: is, row: is, keys: fakeKeys{keys: []subject.Key{{KID: "k", KTY: "RSA", Alg: "RS256", Public: &key.PublicKey}}}},
		mappings: &fakeMappings{set: set, fresh: set}, tokens: &fakeTokens{},
		queries: &fakeQueries{versions: map[string]int64{"m-1": 1}, uses: map[string]int32{}}, rec: &recorder{}, key: key,
	}
	cfg := config.CI{
		Token:    config.CIToken{DefaultTTL: 15 * time.Minute, MaxTTL: time.Hour, MinTTL: 30 * time.Second},
		Exchange: config.CIExchange{Enabled: true, ExposeReason: true, MaxBodyBytes: 65536, MaxSubjectTokenBytes: 16384},
		Replay:   config.CIReplay{WarnAfterExchanges: 10},
	}
	metrics, err := NewMetrics(nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewExchange(ExchangeOptions{
		DB: fakeRunner{}, Issuers: x.issuers, Mappings: x.mappings, Tokens: x.tokens,
		Grants: GrantIndexFunc(func(*claims.Frozen) GrantIndex { return noIndex{} }), Recorder: x.rec, Metrics: metrics,
		Config: cfg, BaseURL: "https://trove.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	s.queries = func(meta.Tx) (exchangeQueries, error) { return x.queries, nil }
	x.s = s
	return x
}

// sign is an RS256 ID token of the test issuer for project 7, valid for an hour.
func (x *testExchange) sign(t *testing.T, over map[string]any) string {
	t.Helper()
	now := time.Now().Unix()
	c := map[string]any{"iss": testIss, "aud": "https://trove.test", "iat": now, "nbf": now - 5, "exp": now + 3600, "jti": "j-" + fmt.Sprint(now), "project_id": "7", "job_id": "9"}
	for k, v := range over {
		c[k] = v
	}
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "k"})
	p, _ := json.Marshal(c)
	in := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, x.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (x *testExchange) request(tok string) ExchangeRequest {
	return ExchangeRequest{GrantType: GrantTypeTokenExchange, SubjectTokenType: TokenTypeIDToken, SubjectToken: tok, Source: audit.Source{ClientAddr: "192.0.2.1"}}
}

func reasonOf(t *testing.T, err error) Reason {
	t.Helper()
	var ee *ExchangeError
	if !errors.As(err, &ee) {
		t.Fatalf("not an *ExchangeError: %v", err)
	}
	return ee.Reason
}

func TestR_CI_007_TheExchangeWithoutADatabase(t *testing.T) {
	t.Run("R-CI-007 a current copy is used: no read in the transaction", func(t *testing.T) {
		x := newTestExchange(t)
		res, err := x.s.Exchange(t.Context(), x.request(x.sign(t, nil)))
		if err != nil || res.CIIssuer != "gitlab-test" || res.Scope != "read:npm-internal" || res.ExpiresIn != 900 {
			t.Fatalf("Exchange = %+v, %v", res, err)
		}
		if x.mappings.freshReads != 0 || x.issuers.reloads != 0 {
			t.Errorf("a current copy read %d sets and reloaded %d times", x.mappings.freshReads, x.issuers.reloads)
		}
		if got := x.rec.of(audit.EventCIExchangeSucceeded); len(got) != 1 {
			t.Errorf("%d success events", len(got))
		}
	})

	t.Run("Q-CI-15 a copy behind the issuer's mappings_version evaluates the set read in the transaction", func(t *testing.T) {
		x := newTestExchange(t)
		x.issuers.row.MappingsVersion = 6
		shadow := testMapping(1, 5)
		shadow.ID, shadow.Name, shadow.Ref, shadow.PrincipalID = "m-2", "shadow", "ci:gitlab-test/shadow", "p-2"
		shadow.Grants.Inline[0].Repositories = []string{"npm-other"}
		x.mappings.fresh = MappingSet{IssuerID: "i-1", IssuerVersion: 3, MappingsVersion: 6, Mappings: []Mapping{shadow, testMapping(1, 10)}}
		x.queries.versions["m-2"] = 1
		res, err := x.s.Exchange(t.Context(), x.request(x.sign(t, nil)))
		if err != nil || len(res.Mappings) != 1 || res.Mappings[0] != "shadow" {
			t.Fatalf("Exchange = %+v, %v", res, err)
		}
		if x.mappings.freshReads != 1 || x.issuers.reloads != 1 {
			t.Errorf("fresh reads %d, reloads %d", x.mappings.freshReads, x.issuers.reloads)
		}
	})

	t.Run("Q-CI-15 an issuer whose version changed validates the token again against the re-read row", func(t *testing.T) {
		x := newTestExchange(t)
		x.issuers.row.Version = 4
		x.issuers.row.AllowedAudiences = []string{"https://other.test"}
		x.mappings.fresh.IssuerVersion = 4
		_, err := x.s.Exchange(t.Context(), x.request(x.sign(t, nil)))
		if r := reasonOf(t, err); r != reason.AudienceInvalid {
			t.Fatalf("reason %q, want audience_invalid", r)
		}
		if len(x.tokens.issued) != 0 {
			t.Error("a token was issued")
		}
	})

	t.Run("Q-CI-43 a second mismatch inside the transaction is configuration_changed", func(t *testing.T) {
		x := newTestExchange(t)
		x.issuers.row.MappingsVersion = 6
		// The store answers a set at another mappings_version than the row the transaction holds FOR SHARE: a writer
		// outside the lock protocol of R-CI-009.
		x.mappings.fresh.MappingsVersion = 7
		_, err := x.s.Exchange(t.Context(), x.request(x.sign(t, nil)))
		var ee *ExchangeError
		if !errors.As(err, &ee) || ee.Reason != reason.ConfigurationChanged || ee.RetryAfter <= 0 {
			t.Fatalf("err = %v", err)
		}
		if status, oauth := ee.Reason.OAuth(); status != 503 || oauth != "temporarily_unavailable" {
			t.Errorf("OAuth() = %d %s", status, oauth)
		}
	})

	t.Run("Q-CI-43 matched rows that changed again after the fresh read are configuration_changed", func(t *testing.T) {
		x := newTestExchange(t)
		x.queries.versions["m-1"] = 2 // the copy's version 1 differs, and the fresh set's too
		_, err := x.s.Exchange(t.Context(), x.request(x.sign(t, nil)))
		if r := reasonOf(t, err); r != reason.ConfigurationChanged {
			t.Fatalf("reason %q", r)
		}
	})

	t.Run("R-CI-018 single use: the second exchange of a jti is replayed and recorded after the transaction", func(t *testing.T) {
		x := newTestExchange(t)
		x.issuers.copy.SingleUseSubjectTokens, x.issuers.row.SingleUseSubjectTokens = true, true
		tok := x.sign(t, map[string]any{"jti": "once"})
		if _, err := x.s.Exchange(t.Context(), x.request(tok)); err != nil {
			t.Fatal(err)
		}
		_, err := x.s.Exchange(t.Context(), x.request(tok))
		if r := reasonOf(t, err); r != reason.SubjectTokenReplayed {
			t.Fatalf("reason %q", r)
		}
		if ev := x.rec.of(audit.EventCIExchangeReplayed); len(ev) != 1 || !strings.Contains(string(ev[0].Details), `"once"`) {
			t.Errorf("replayed events %v", ev)
		}
	})

	t.Run("R-CI-021 the per-subject limit counts per issuer and first pin claim, after the validation", func(t *testing.T) {
		x := newTestExchange(t)
		lim := &countingLimiter{allow: 1}
		x.s.o.SubjectLimiter = lim
		if _, err := x.s.Exchange(t.Context(), x.request(x.sign(t, nil))); err != nil {
			t.Fatal(err)
		}
		_, err := x.s.Exchange(t.Context(), x.request(x.sign(t, nil)))
		if r := reasonOf(t, err); r != reason.RateLimited {
			t.Fatalf("reason %q", r)
		}
		if lim.buckets[0] != "i-1\x00project_id=7" {
			t.Errorf("bucket %q", lim.buckets[0])
		}
		// A forged token never reaches it.
		bad := x.sign(t, nil)
		_, err = x.s.Exchange(t.Context(), x.request(bad[:len(bad)-4]+"AAAA"))
		if r := reasonOf(t, err); r != reason.SignatureInvalid || len(lim.buckets) != 2 {
			t.Errorf("reason %q after %d takes", r, len(lim.buckets))
		}
	})

	t.Run("R-CI-008 a request prints no token", func(t *testing.T) {
		x := newTestExchange(t)
		tok := x.sign(t, nil)
		req := x.request(tok)
		req.ActorToken = "actor-secret-value"
		for _, s := range []string{req.String(), fmt.Sprint(req), fmt.Sprintf("%v", req), ExchangeResult{AccessToken: "trv_cit_abcdefghij"}.String()} {
			if strings.Contains(s, tok[len(tok)-20:]) || strings.Contains(s, "actor-secret-value") || strings.Contains(s, "trv_cit_abcdefghij") {
				t.Errorf("printed a token: %s", s)
			}
		}
	})
}

type countingLimiter struct {
	allow   int
	buckets []string
}

func (l *countingLimiter) Take(_ context.Context, bucket string) cluster.Verdict {
	l.buckets = append(l.buckets, bucket)
	return cluster.Verdict{Allowed: len(l.buckets) <= l.allow, RetryAfter: 3 * time.Second}
}
