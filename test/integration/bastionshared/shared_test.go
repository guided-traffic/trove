//go:build integration

package bastionshared

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/guided-traffic/trove/internal/bastion/cache"
	"github.com/guided-traffic/trove/internal/bastion/guard"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/bastion/shared"
)

// answerKey is the key of an answer record as R-BAS-034 and docs/developer/valkey.md name it.
func answerKey(s *setup, kind shared.AnswerKind, hash string) string {
	return "trove-bastion:" + s.prefix + ":ans:v1:" + string(kind) + ":" + hash
}

func rawGet(t *testing.T, c valkey.Client, key string) (string, bool) {
	t.Helper()
	v, err := c.Do(ctx, c.B().Get().Key(key).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return "", false
	}
	if err != nil {
		t.Fatalf("GET %s: %v", key, err)
	}
	return v, true
}

func rawSet(t *testing.T, c valkey.Client, key, value string) {
	t.Helper()
	if err := c.Do(ctx, c.B().Set().Key(key).Value(value).Ex(60*time.Second).Build()).Error(); err != nil {
		t.Fatalf("SET %s: %v", key, err)
	}
}

func TestR_BAS_034_AnAnswerStoredByReplicaAIsFoundByReplicaB(t *testing.T) {
	s := newSetup(t)
	a, b := s.pair(t)
	adm := admin(t)
	h := hashOf("credential:" + randHex(8))
	record := []byte(`{"subject":"u1","readShape":"full"}`)

	if _, ok, err := b.st.Answers.Get(ctx, shared.AnswerCredential, h); err != nil || ok {
		t.Fatalf("an answer that was never stored: %v %v", ok, err)
	}
	if err := a.st.Answers.Put(ctx, shared.AnswerCredential, h, record, 5*time.Minute); err != nil {
		t.Fatalf("Put on replica A: %v", err)
	}
	got, ok, err := b.st.Answers.Get(ctx, shared.AnswerCredential, h)
	if err != nil || !ok || !bytes.Equal(got, record) {
		t.Fatalf("replica B found %q %v %v, want the record of replica A", got, ok, err)
	}
	if _, ok, _ := b.st.Answers.Get(ctx, shared.AnswerShape, h); ok {
		t.Fatal("a record of another kind was found")
	}

	// What Valkey holds is signed, below the Bastion's prefix, and expires with the answer's validity.
	raw, found := rawGet(t, adm, answerKey(s, shared.AnswerCredential, h))
	if !found || !strings.HasPrefix(raw, string(record)+"\n") || len(raw) != len(record)+1+64 {
		t.Fatalf("stored value %q is not the record with its signature", raw)
	}
	ttl, err := adm.Do(ctx, adm.B().Pttl().Key(answerKey(s, shared.AnswerCredential, h)).Build()).AsInt64()
	if err != nil || ttl <= 0 || ttl > int64(5*time.Minute/time.Millisecond) {
		t.Fatalf("PTTL = %d, %v: want within the 5 minutes of validity", ttl, err)
	}

	// An answer with no validity left is not stored.
	h2 := hashOf("expired:" + randHex(8))
	if err := a.st.Answers.Put(ctx, shared.AnswerShape, h2, record, 0); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := b.st.Answers.Get(ctx, shared.AnswerShape, h2); ok {
		t.Fatal("an answer with no validity left was stored")
	}
	// Delete on one replica is seen on the other.
	if err := a.st.Answers.Delete(ctx, shared.AnswerCredential, h); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := b.st.Answers.Get(ctx, shared.AnswerCredential, h); ok {
		t.Fatal("a deleted record was found")
	}
}

func TestR_BAS_034_AForgedOrAlteredRecordIsIgnoredAndDeleted(t *testing.T) {
	s := newSetup(t)
	a, b := s.pair(t)
	adm := admin(t)
	other, err := cache.NewSigner(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	h := hashOf("k:" + randHex(8))
	elsewhere := hashOf("other:" + randHex(8))
	key := answerKey(s, shared.AnswerCredential, h)

	valid := []byte(`{"subject":"u1","readShape":"none"}`)
	tamper := map[string]func(t *testing.T) string{
		"a payload altered after signing": func(t *testing.T) string {
			if err := a.st.Answers.Put(ctx, shared.AnswerCredential, h, valid, time.Minute); err != nil {
				t.Fatal(err)
			}
			raw, _ := rawGet(t, adm, key)
			return strings.Replace(raw, "none", "full", 1)
		},
		"a record signed with another key": func(*testing.T) string {
			return string(other.Sign(cache.DomainAnswer, "credential:"+h, []byte(`{"subject":"u1","readShape":"full"}`)))
		},
		"a record without a signature": func(*testing.T) string { return `{"subject":"u1","readShape":"full"}` },
		"a record of another key moved here": func(t *testing.T) string {
			if err := a.st.Answers.Put(ctx, shared.AnswerCredential, elsewhere, valid, time.Minute); err != nil {
				t.Fatal(err)
			}
			raw, _ := rawGet(t, adm, answerKey(s, shared.AnswerCredential, elsewhere))
			return raw
		},
		"a record of another kind moved here": func(t *testing.T) string {
			if err := a.st.Answers.Put(ctx, shared.AnswerURL, h, valid, time.Minute); err != nil {
				t.Fatal(err)
			}
			raw, _ := rawGet(t, adm, answerKey(s, shared.AnswerURL, h))
			return raw
		},
		"a signature of the wrong length": func(*testing.T) string { return "{}\nabcd" },
	}
	for name, forge := range tamper {
		t.Run(name, func(t *testing.T) {
			before := metricValue(t, b.reg, metrics.AnswersTotal, map[string]string{"kind": "credential", "result": metrics.AnswerBadSignature})
			rawSet(t, adm, key, forge(t))
			got, ok, err := b.st.Answers.Get(ctx, shared.AnswerCredential, h)
			if err != nil || ok || got != nil {
				t.Fatalf("a forged record was returned: %q %v %v", got, ok, err)
			}
			if _, still := rawGet(t, adm, key); still {
				t.Fatal("the forged record is still in Valkey")
			}
			after := metricValue(t, b.reg, metrics.AnswersTotal, map[string]string{"kind": "credential", "result": metrics.AnswerBadSignature})
			if after != before+1 {
				t.Fatalf("bad_signature counted %v times, want once", after-before)
			}
		})
	}
}

func TestR_BAS_034_TheKeysAreBelowThePrefixOfTheBastion(t *testing.T) {
	s := newSetup(t)
	a, _ := s.pair(t)
	adm := admin(t)
	addr := address()
	// Drive a block, a timeout and the level: the three address keys.
	s.mutate = nil
	for i := 0; i < 2000; i++ {
		if a.g.Blocked(ctx, addr).Allowed {
			a.g.Request(ctx, addr, false)
		}
		s.clk.advance(30 * time.Millisecond)
	}
	for i := 0; i < 20; i++ {
		a.g.FailedAuth(ctx, addr)
	}
	base := "trove-bastion:" + s.prefix + ":"
	part := strings.NewReplacer(".", "-").Replace(addr)
	for _, k := range []string{"block:v1:" + part, "block:level:v1:" + part, "timeout:v1:" + part} {
		if _, ok := rawGet(t, adm, base+k); !ok {
			t.Errorf("%s%s does not exist", base, k)
		}
	}
	ttl, err := adm.Do(ctx, adm.B().Pttl().Key(base+"block:v1:"+part).Build()).AsInt64()
	if err != nil || ttl <= 0 || ttl > int64(15*time.Minute/time.Millisecond) {
		t.Fatalf("PTTL of the block = %d, %v: want within the 15 minutes of the duration", ttl, err)
	}
	// Held by the Bastion's keys only: nothing was written outside its prefix.
	keys, err := adm.Do(ctx, adm.B().Keys().Pattern("*"+part+"*").Build()).AsStrSlice()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if !strings.HasPrefix(k, base) {
			t.Errorf("key %s is outside %s", k, base)
		}
	}
}

func TestR_BAS_025_AFetchLockIsHeldByOneReplicaAcrossTheBastion(t *testing.T) {
	s := newSetup(t)
	a, b := s.pair(t)
	h := hashOf("/api/npm/npm-all/lodash/-/lodash-4.17.21.tgz" + randHex(4))
	held, release, err := a.st.Flights.TryAcquire(ctx, h)
	if err != nil || !held {
		t.Fatalf("replica A: %v %v", held, err)
	}
	if other, _, err := b.st.Flights.TryAcquire(ctx, h); err != nil || other {
		t.Fatalf("replica B got the lock of replica A: %v %v", other, err)
	}
	release()
	if again, rel, err := b.st.Flights.TryAcquire(ctx, h); err != nil || !again {
		t.Fatalf("replica B after the release: %v %v", again, err)
	} else {
		rel()
	}
	// A hash that is no SHA-256 is not a flight: the caller fetches itself and is told why.
	if held, _, err := a.st.Flights.TryAcquire(ctx, "not-a-hash"); !held || err == nil {
		t.Fatalf("an invalid key: held=%v err=%v, want held with an error", held, err)
	}
}

func TestR_BAS_031_OneReplicaHoldsTheSweepLease(t *testing.T) {
	s := newSetup(t)
	a, b := s.pair(t)
	held, release, err := a.st.Sweep.TryAcquire(ctx, 10*time.Minute)
	if err != nil || !held {
		t.Fatalf("replica A: %v %v", held, err)
	}
	if other, _, err := b.st.Sweep.TryAcquire(ctx, 10*time.Minute); err != nil || other {
		t.Fatalf("replica B swept under the lease of replica A: %v %v", other, err)
	}
	release()
	if again, rel, err := b.st.Sweep.TryAcquire(ctx, 10*time.Minute); err != nil || !again {
		t.Fatalf("replica B after the release: %v %v", again, err)
	} else {
		rel()
	}
}

func TestR_BAS_034_ValkeyBeingUnavailableMakesNothingFailAndSaysSo(t *testing.T) {
	s := newSetup(t)
	s.unreachable = true
	a, _ := s.pair(t)
	if a.st.Shared() {
		t.Fatal("the state claims to be shared without Valkey")
	}
	h := hashOf("x")
	// Answers: nothing is stored or found, the error is the caller's cue to ask again.
	if err := a.st.Answers.Put(ctx, shared.AnswerShape, h, []byte("r"), time.Minute); err == nil {
		t.Fatal("Put into an unavailable Valkey reported success")
	}
	if _, ok, _ := a.st.Answers.Get(ctx, shared.AnswerShape, h); ok {
		t.Fatal("an answer from nowhere")
	}
	// A fetch lock is held (the replica fetches itself), the sweep lease is held (every replica sweeps).
	if held, _, _ := a.st.Flights.TryAcquire(ctx, h); !held {
		t.Fatal("a replica without Valkey may not fetch")
	}
	if held, _, err := a.st.Sweep.TryAcquire(ctx, time.Minute); !held || err != nil {
		t.Fatalf("sweep lease = %v %v", held, err)
	}
	// The guard works, and the shared metrics say what happened.
	addr := address()
	if !a.g.Blocked(ctx, addr).Allowed || !a.g.Request(ctx, addr, false).Allowed {
		t.Fatal("the guard refused a first request")
	}
	if got := metricValue(t, a.reg, "trove_valkey_available", nil); got != 0 {
		t.Fatalf("trove_valkey_available = %v, want 0", got)
	}
	if got := metricValue(t, a.reg, "trove_ratelimit_fallback", map[string]string{"limiter": "bastion.requests_anonymous"}); got != 1 {
		t.Fatalf("trove_ratelimit_fallback{limiter=bastion.requests_anonymous} = %v, want 1", got)
	}
}

func TestR_BAS_035_WithoutValkeyTheAnonymousLimitHoldsInTotalOverTwoReplicas(t *testing.T) {
	s := newSetup(t)
	s.unreachable = true
	a, b := s.pair(t)
	addr := address()
	// 1,000 anonymous requests within 10 s, spread over the two replicas as the Service spreads them. The
	// configured limit admits 40 plus 10 per second, 140 at most; each replica holds half of it, so the total
	// is bounded by the configured rate plus at most one (halved) burst per replica.
	admitted := 0
	for i := 0; i < 1000; i++ {
		r := a
		if i%2 == 1 {
			r = b
		}
		if r.g.Request(ctx, addr, false).Allowed {
			admitted++
		}
		s.clk.advance(10 * time.Millisecond)
	}
	if admitted > 140+2*20 {
		t.Fatalf("%d admitted in 10 s: more than the configured rate and one burst per replica", admitted)
	}
	if admitted > 140 {
		t.Fatalf("%d admitted in 10 s with the budgets divided by the replicas, the configured bound is 140", admitted)
	}
	if admitted < 100 {
		t.Fatalf("%d admitted in 10 s: the divided limit refuses a client within its rate", admitted)
	}
	if !a.g.Request(ctx, address(), false).Allowed {
		t.Fatal("a second address was slowed down")
	}
}

func TestR_BAS_035_WithValkeyAnonymousRequestsAreBoundedAcrossReplicas(t *testing.T) {
	s := newSetup(t)
	a, b := s.pair(t)
	addr, other := address(), address()
	admitted := 0
	for i := 0; i < 1000; i++ {
		r := a
		if i%2 == 1 {
			r = b
		}
		if v := r.g.Blocked(ctx, addr); !v.Allowed {
			continue
		}
		if r.g.Request(ctx, addr, false).Allowed {
			admitted++
		}
		s.clk.advance(10 * time.Millisecond)
	}
	if admitted > 140 {
		t.Fatalf("%d admitted from one address within 10 s, the bound is 140 (burst 40 plus 10 per second)", admitted)
	}
	// The sliding window counts a refused request like an admitted one, so a client that keeps sending at ten
	// times the rate stays refused after its burst: the burst is what it gets.
	if admitted < 40 {
		t.Fatalf("%d admitted: the burst of 40 was not admitted", admitted)
	}
	if !a.g.Request(ctx, other, false).Allowed || !b.g.Request(ctx, other, false).Allowed {
		t.Fatal("a second address was slowed down")
	}
}

func TestR_BAS_035_AddressedMissesAreBoundedPerMinuteAcrossReplicas(t *testing.T) {
	for _, tc := range []struct {
		name        string
		unreachable bool
	}{{"with Valkey", false}, {"without Valkey", true}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSetup(t)
			s.unreachable = tc.unreachable
			a, b := s.pair(t)
			addr := address()
			// 100 random missing names within a minute, anonymous: at most 30 reach the main installation.
			admitted := 0
			for i := 0; i < 100; i++ {
				r := a
				if i%2 == 1 {
					r = b
				}
				if r.g.Miss(ctx, addr, false).Allowed {
					admitted++
				}
				s.clk.advance(600 * time.Millisecond)
			}
			if admitted > 30 {
				t.Fatalf("%d misses admitted within a minute, the bound is 30", admitted)
			}
			if admitted < 20 {
				t.Fatalf("%d misses admitted: the limit refuses a client within it", admitted)
			}
		})
	}
}

func TestR_BAS_036_TheTimeoutHoldsOnEveryReplicaAndSparesAKnownCredential(t *testing.T) {
	s := newSetup(t)
	a, b := s.pair(t)
	addr := address()
	// 21 random well-formed tokens from one address, alternating between the replicas. The engine asks the
	// guard only for a credential it holds no usable answer for, so $FULL, used from here before, is never asked.
	for i := 1; i <= 21; i++ {
		r := a
		if i%2 == 0 {
			r = b
		}
		v := r.g.AuthTimeout(ctx, addr)
		if i <= 20 {
			if !v.Allowed {
				t.Fatalf("token %d refused: %+v", i, v)
			}
			r.g.FailedAuth(ctx, addr)
			s.clk.advance(time.Second)
			continue
		}
		if v.Allowed || v.Status != http.StatusTooManyRequests || v.Code != guard.CodeAuthTimeout {
			t.Fatalf("token 21: %+v, want 429 auth_timeout", v)
		}
	}
	// Every later unknown token is refused on both replicas, for 15 minutes.
	for _, r := range []*replica{a, b} {
		if v := r.g.AuthTimeout(ctx, addr); v.Allowed || v.RetryAfter <= 0 || v.RetryAfter > 15*time.Minute {
			t.Fatalf("%s: %+v", r.name, v)
		}
	}
	// A known credential and an anonymous request are not the guard's to refuse: the limits of the address are
	// untouched, and another address has no timeout.
	if !a.g.Request(ctx, addr, true).Allowed || !b.g.Request(ctx, addr, false).Allowed {
		t.Fatal("the timeout reached the requests of the address")
	}
	if !a.g.AuthTimeout(ctx, address()).Allowed {
		t.Fatal("another address is in the timeout")
	}
	s.clk.advance(16 * time.Minute)
	if !b.g.AuthTimeout(ctx, addr).Allowed {
		t.Fatal("the timeout did not end")
	}
}

func TestR_BAS_037_AFloodIsBlockedOnBothReplicasForFifteenThenThirtyMinutes(t *testing.T) {
	s := newSetup(t)
	a, b := s.pair(t)
	addr := address()
	flood := func() {
		for i := 0; i < 2000; i++ { // 2,000 anonymous requests within a minute, alternating between the replicas
			r := a
			if i%2 == 1 {
				r = b
			}
			if r.g.Blocked(ctx, addr).Allowed {
				r.g.Request(ctx, addr, false)
			}
			s.clk.advance(30 * time.Millisecond)
		}
	}
	expectBlocked := func(minRemaining, maxRemaining time.Duration) {
		t.Helper()
		for _, r := range []*replica{a, b} {
			v := r.g.Blocked(ctx, addr)
			if v.Allowed || v.Status != http.StatusTooManyRequests || v.Code != guard.CodeAddressBlocked || !v.Quiet {
				t.Fatalf("%s: %+v, want 429 address_blocked", r.name, v)
			}
			if v.RetryAfter <= minRemaining || v.RetryAfter > maxRemaining {
				t.Fatalf("%s: Retry-After %s, want in (%s, %s]", r.name, v.RetryAfter, minRemaining, maxRemaining)
			}
		}
	}
	flood()
	expectBlocked(0, 15*time.Minute)
	// The block holds for its 15 minutes and ends.
	s.clk.advance(14 * time.Minute)
	expectBlocked(0, time.Minute+30*time.Second)
	s.clk.advance(2 * time.Minute)
	for _, r := range []*replica{a, b} {
		if !r.g.Blocked(ctx, addr).Allowed {
			t.Fatalf("%s: still blocked after the 15 minutes", r.name)
		}
	}
	// A second flood after the block: 30 minutes.
	flood()
	expectBlocked(15*time.Minute, 30*time.Minute)
	if n := metricValue(t, a.reg, metrics.BlocksTotal, map[string]string{"reason": metrics.BlockRefusals}) +
		metricValue(t, b.reg, metrics.BlocksTotal, map[string]string{"reason": metrics.BlockRefusals}); n < 2 {
		t.Fatalf("trove_bastion_blocks_total over the replicas = %v, want at least 2", n)
	}
	s.clk.advance(31 * time.Minute)
	for _, r := range []*replica{a, b} {
		if !r.g.Blocked(ctx, addr).Allowed {
			t.Fatalf("%s: still blocked after the 30 minutes", r.name)
		}
	}
	if !a.g.Blocked(ctx, address()).Allowed {
		t.Fatal("another address was blocked")
	}
}

func TestR_BAS_037_WithoutValkeyEachReplicaBlocksByItsOwnCount(t *testing.T) {
	// H-389: a block set by one replica's count does not hold on the other until that one counts enough itself.
	s := newSetup(t)
	s.unreachable = true
	a, b := s.pair(t)
	addr := address()
	for i := 0; i < 2000; i++ {
		if a.g.Blocked(ctx, addr).Allowed {
			a.g.Request(ctx, addr, false)
		}
		s.clk.advance(30 * time.Millisecond)
	}
	if a.g.Blocked(ctx, addr).Allowed {
		t.Fatal("replica A did not block a flood it counted alone")
	}
	if !b.g.Blocked(ctx, addr).Allowed {
		t.Fatal("replica B blocked an address it has not seen: the block list is shared without Valkey")
	}
}
