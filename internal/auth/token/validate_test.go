package token

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// downDB is a database that cannot be reached.
type downDB struct{ reads int }

func (d *downDB) InTx(context.Context, func(meta.Tx) error) error { return errors.New("down") }
func (d *downDB) Read(context.Context, func(q.DBTX) error) error {
	d.reads++
	return errs.E(errs.Unavailable, errs.CodeDatabaseUnavailable, "the database is not available")
}

type freshness struct{ fresh bool }

func (f *freshness) FreshWithin(time.Duration) bool { return f.fresh }

type noMarkers struct{ marked, seen int }

func (n *noMarkers) Mark(context.Context, []byte) error { n.marked++; return nil }
func (n *noMarkers) Seen(context.Context, []byte) bool  { n.seen++; return false }

type countThrottle struct {
	exceeded bool
	counted  int
}

func (c *countThrottle) Exceeded(context.Context) bool { return c.exceeded }
func (c *countThrottle) Count(context.Context)         { c.counted++ }

func newTestService(t *testing.T, db principal.DB, fresh *freshness, rec RecentTokens) *Service {
	t.Helper()
	reg, err := cache.NewRegistry(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.AuthTokens{CacheTTL: time.Minute}
	gate := principal.Gate{Fresh: fresh, Bound: 2 * time.Second}
	return NewService(ServiceOptions{DB: db, Cache: reg, Gate: gate, Tokens: cfg, Recent: rec})
}

func TestR_AUTH_023_ClosedGateServesNoCachedAcceptance(t *testing.T) {
	raw, _ := Generate(TypePAT)
	hash := Hash(raw)
	db := &downDB{}
	fresh := &freshness{fresh: true}
	s := newTestService(t, db, fresh, nil)
	s.tokens.Set(hash, cachedToken{info: Info{ID: "t1", Type: TypePAT, Owner: principal.Principal{ID: "u", Status: principal.StatusActive}}})

	if info, err := s.Check(context.Background(), raw, Request{}); err != nil || info.ID != "t1" || db.reads != 0 {
		t.Fatalf("with an open gate the cache answers: %+v %v (%d reads)", info, err, db.reads)
	}
	fresh.fresh = false
	_, err := s.Check(context.Background(), raw, Request{})
	if e, ok := errs.As(err); !ok || e.Status() != 503 {
		t.Fatalf("with the gate closed and PostgreSQL away the answer is 503, got %v", err)
	}
	if db.reads == 0 {
		t.Fatal("the closed gate did not read PostgreSQL")
	}
}

func TestR_AUTH_023_SyntaxFailureNeedsNoDatabase(t *testing.T) {
	db := &downDB{}
	s := newTestService(t, db, &freshness{fresh: true}, nil)
	th := &countThrottle{}
	for _, v := range []string{"hunter2", "eyJhbGciOiJSUzI1NiJ9.e30.c2ln", ""} {
		if _, err := s.Check(context.Background(), v, Request{Throttle: th}); !errors.Is(err, ErrMalformed) {
			t.Fatalf("%q: %v", v, err)
		}
	}
	if db.reads != 0 {
		t.Fatal("a malformed credential reached the database")
	}
	if th.counted != 3 {
		t.Fatalf("%d rejected credentials counted", th.counted)
	}
}

func TestR_AUTH_038_ThrottleIsConsultedOnlyOnACacheMiss(t *testing.T) {
	raw, _ := Generate(TypePAT)
	db := &downDB{}
	markers := &noMarkers{}
	s := newTestService(t, db, &freshness{fresh: true}, markers)
	th := &countThrottle{exceeded: true}

	t.Run("a throttled address without a marker is refused with 429 and the credential is not validated", func(t *testing.T) {
		_, err := s.Check(context.Background(), raw, Request{Throttle: th, RetryAfter: time.Minute})
		var te *ThrottledError
		if !errors.As(err, &te) || !errors.Is(err, ErrThrottled) || te.RetryAfter != time.Minute {
			t.Fatalf("err = %v", err)
		}
		if db.reads != 0 || markers.seen != 1 {
			t.Fatalf("reads %d, marker lookups %d", db.reads, markers.seen)
		}
	})
	t.Run("a cache hit costs no throttle check", func(t *testing.T) {
		s.tokens.Set(Hash(raw), cachedToken{info: Info{ID: "t1", Owner: principal.Principal{Status: principal.StatusActive}}})
		seen := markers.seen
		if _, err := s.Check(context.Background(), raw, Request{Throttle: th}); err != nil {
			t.Fatal(err)
		}
		if markers.seen != seen {
			t.Fatal("the marker store was consulted for a cache hit")
		}
	})
}

func TestR_AUTH_023_ChangesEvictWhatTheyName(t *testing.T) {
	s := newTestService(t, &downDB{}, &freshness{fresh: true}, nil)
	a, _ := Generate(TypePAT)
	b, _ := Generate(TypePAT)
	put := func() {
		for _, raw := range []string{a, b} {
			s.tokens.Set(Hash(raw), cachedToken{info: Info{ID: raw[8:12], Owner: principal.Principal{Status: principal.StatusActive}}})
		}
	}
	apply := func(c principal.Change) {
		raw, _ := json.Marshal(c)
		s.o.Cache.ApplyChange("auth", principal.ChangeVersion, raw)
	}
	hex := func(raw string) string {
		h := Hash(raw)
		const digits = "0123456789abcdef"
		out := make([]byte, 64)
		for i, v := range h {
			out[2*i], out[2*i+1] = digits[v>>4], digits[v&15]
		}
		return string(out)
	}
	put()
	apply(principal.Change{Kind: principal.ChangeToken, Tokens: []principal.TokenRef{{ID: "x", Hash: hex(a)}}})
	if _, ok := s.tokens.Get(Hash(a)); ok {
		t.Fatal("the revoked token stayed cached")
	}
	if _, ok := s.tokens.Get(Hash(b)); !ok {
		t.Fatal("an unrelated token was evicted")
	}
	for _, kind := range []string{principal.ChangeMembership, principal.ChangeSettings, principal.ChangeSession} {
		apply(principal.Change{Kind: kind, IDs: []string{"x"}})
	}
	if _, ok := s.tokens.Get(Hash(b)); !ok {
		t.Fatal("a change that cannot touch a token flushed the cache")
	}
	apply(principal.Change{Kind: principal.ChangePrincipal, IDs: []string{"x"}})
	if _, ok := s.tokens.Get(Hash(b)); ok {
		t.Fatal("a principal change leaves the cached owner status in place")
	}
	put()
	apply(principal.Change{Kind: "something-new"})
	if s.tokens.Len() != 0 {
		t.Fatal("an unknown kind must flush")
	}
}
