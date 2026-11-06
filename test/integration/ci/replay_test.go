//go:build integration

package ci

// R-CI-018: the replay set in PostgreSQL, single use on and off, concurrent exchanges of one jti on two replicas, a
// failed exchange that leaves no trace of its jti, the restore cut-off; the events ci.exchange.replayed and
// ci.exchange.repeated (R-CI-020).

import (
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/oidcstub"
)

// jtiCount is the exchange count of a jti in the replay set, 0 when it has no row.
func (x *xenv) jtiCount(jti string) int64 {
	x.t.Helper()
	return x.number("SELECT coalesce((SELECT exchange_count FROM ci_subject_token_uses WHERE jti = $1), 0)::bigint", jti)
}

// parallel sends one subject token to every replica at once and returns the statuses.
func parallel(t *testing.T, tok string, reps ...*testenv.Replica) []int {
	t.Helper()
	out := make([]int, len(reps))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, r := range reps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out[i], _ = rawExchange(t, r, "", tok)
		}()
	}
	close(start)
	wg.Wait()
	return out
}

func count(statuses []int, status int) int {
	n := 0
	for _, s := range statuses {
		if s == status {
			n++
		}
	}
	return n
}

func TestR_CI_018_SingleUse(t *testing.T) {
	x := newXEnv(t, options{valkey: true, env: []string{"TROVE_CI_REPLAY_WARN_AFTER_EXCHANGES=2"}})
	a, b := x.web("web-a"), x.web("web-b")
	x.issuer(a, map[string]any{"singleUseSubjectTokens": true})
	x.addMapping(a, releaseMapping("acme-release-publish", 100))

	t.Run("R-CI-018 with single use on, the second exchange of a jti is subject_token_replayed", func(t *testing.T) {
		jti := "jti-sequential"
		tok := x.mint(oidcstub.MintOptions{Claims: map[string]any{"jti": jti}})
		wantToken(t, x.exchange(a, tok))
		wantOAuth(t, x.exchange(b, tok), 400, "invalid_request", "subject_token_replayed")
		if n := x.jtiCount(jti); n != 1 {
			t.Errorf("the refused exchange counted: %d", n)
		}
		x.waitEvents(audit.EventCIExchangeReplayed, 1)
		if d := x.eventDetails(audit.EventCIExchangeReplayed); d["jti"] != jti || d["exchangeCount"] != float64(2) || d["issuer"] != xIssuer {
			t.Errorf("ci.exchange.replayed records %v", d)
		}
	})

	t.Run("R-CI-018 concurrent exchanges of one jti on two replicas yield exactly one 200", func(t *testing.T) {
		for i := range 10 {
			tok := x.mint(oidcstub.MintOptions{})
			got := parallel(t, tok, a, b)
			if count(got, 200) != 1 || count(got, 400) != 1 {
				t.Fatalf("trial %d: %v", i, got)
			}
		}
	})

	t.Run("R-CI-018 with single use on, a token without jti is required_claim_missing", func(t *testing.T) {
		wantOAuth(t, x.exchange(a, x.mint(oidcstub.MintOptions{Remove: []string{"jti"}})), 400, "invalid_request", "required_claim_missing")
	})

	t.Run("R-CI-018 a failed exchange does not count its jti, so the retry succeeds", func(t *testing.T) {
		// A refusal after the upsert: no mapping matches the claims yet.
		jti := "jti-retry"
		tok := x.mint(oidcstub.MintOptions{Claims: map[string]any{"jti": jti, "namespace_id": "8080"}})
		wantOAuth(t, x.exchange(a, tok), 400, "invalid_request", "no_matching_mapping")
		if n := x.jtiCount(jti); n != 0 {
			t.Fatalf("the refused exchange left the jti with count %d", n)
		}
		// The database rejects the token row: the mapping's principal is disabled behind the mapping's back, which
		// IssueCITx refuses at the end of the transaction (R-CI-017).
		x.addMapping(a, mapping("retry-read", 300, all(eq("namespace_id", "8080")), grants(inline("it/npm-internal", []string{"*"}, "read"))))
		x.exec(`UPDATE principal SET status = 'disabled' WHERE kind = 'ci_mapping' AND name = $1`, xIssuer+"/retry-read")
		wantOAuth(t, x.exchange(a, tok), 500, "server_error", "internal")
		if n := x.jtiCount(jti); n != 0 {
			t.Fatalf("the failed transaction left the jti with count %d", n)
		}
		x.exec(`UPDATE principal SET status = 'active' WHERE kind = 'ci_mapping' AND name = $1`, xIssuer+"/retry-read")
		wantToken(t, x.exchange(b, tok))
		if n := x.jtiCount(jti); n != 1 {
			t.Errorf("after the retry the count is %d, want 1", n)
		}
	})
}

func TestR_CI_018_RepeatedExchangesAreCountedWithoutSingleUse(t *testing.T) {
	x := newXEnv(t, options{valkey: true, env: []string{"TROVE_CI_REPLAY_WARN_AFTER_EXCHANGES=2"}})
	a, b := x.web("web-a"), x.web("web-b")
	x.issuer(a)
	if got := x.do(a, "GET", issuersPath+"/"+xIssuer, nil).JSON(t)["singleUseSubjectTokens"]; got != false {
		t.Fatalf("a gitlab issuer reads singleUseSubjectTokens %v, want false (Q-CI-4)", got)
	}
	x.addMapping(a, releaseMapping("acme-release-publish", 100))
	jti := "jti-repeated"
	tok := x.mint(oidcstub.MintOptions{Claims: map[string]any{"jti": jti}})

	t.Run("R-CI-018 two parallel exchanges of one ID token on two replicas both succeed", func(t *testing.T) {
		if got := parallel(t, tok, a, b); count(got, 200) != 2 {
			t.Fatalf("statuses %v", got)
		}
		if n := x.jtiCount(jti); n != 2 {
			t.Errorf("count %d, want 2", n)
		}
	})
	t.Run("R-CI-018 past ci.replay.warn_after_exchanges every further exchange records ci.exchange.repeated", func(t *testing.T) {
		if x.events(audit.EventCIExchangeRepeated) != 0 {
			t.Fatal("repeated before the threshold")
		}
		wantToken(t, x.exchange(a, tok))
		x.waitEvents(audit.EventCIExchangeRepeated, 1)
		wantToken(t, x.exchange(b, tok))
		x.waitEvents(audit.EventCIExchangeRepeated, 2)
		if d := x.eventDetails(audit.EventCIExchangeRepeated); d["jti"] != jti || d["exchangeCount"] != float64(4) {
			t.Errorf("ci.exchange.repeated records %v", d)
		}
		// The issuance list shows the count through the replay set; every exchange has its record (R-CI-020).
		if n := x.number(`SELECT count(*) FROM ci_token_issuances WHERE jti = $1`, jti); n != 4 {
			t.Errorf("%d issuance records, want 4", n)
		}
	})
}

// setRestoredAt writes system_state restored_at as the restore paths do (R-BAK-021 step 9, R-HA-025 step 4).
func (x *xenv) setRestoredAt(at time.Time) {
	x.t.Helper()
	x.exec(`INSERT INTO system_state (key, value) VALUES ('restored_at', to_jsonb($1::text))
	        ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, at.UTC().Format(time.RFC3339Nano))
}

func TestR_CI_018_TheRestoreCutOff(t *testing.T) {
	x := newXEnv(t, options{valkey: true})
	a := x.web("web-a")
	x.issuer(a, map[string]any{"singleUseSubjectTokens": true})
	off := startDouble(t, oidcstub.Config{Audience: troveAudience})
	wantStatus(t, x.do(a, "POST", issuersPath, off.body("gitlab-reuse")), 201, "")
	x.addMapping(a, releaseMapping("acme-release-publish", 100))
	wantStatus(t, x.do(a, "POST", mappingsOf("gitlab-reuse"), releaseMapping("reuse-publish", 100)), 201, "")
	x.settled()
	// The restore finished two minutes ago on a replica that was already running (R-HA-026's case): the exchange
	// reads the cut-off in its transaction, nothing at start-up.
	restored := time.Now().Add(-2 * time.Minute)
	x.setRestoredAt(restored)
	iat := func(at time.Time) map[string]any {
		return map[string]any{"iat": at.Unix(), "nbf": at.Unix() - 5, "exp": time.Now().Add(time.Hour).Unix()}
	}

	t.Run("R-CI-018 with single use on, a token issued ten minutes before the restore finished is subject_token_predates_restore", func(t *testing.T) {
		// The exp check runs first (R-CI-004 step 8), so the token's iat is in the past while it is still valid.
		wantOAuth(t, x.exchange(a, x.mint(oidcstub.MintOptions{Claims: iat(restored.Add(-10 * time.Minute))})), 400, "invalid_request", "subject_token_predates_restore")
	})
	t.Run("R-CI-018 with the default 60 s skew, a token issued 30 s before the restore finished is refused", func(t *testing.T) {
		wantOAuth(t, x.exchange(a, x.mint(oidcstub.MintOptions{Claims: iat(restored.Add(-30 * time.Second))})), 400, "invalid_request", "subject_token_predates_restore")
		// Within the skew after the restore the leeway lies on the refusing side.
		wantOAuth(t, x.exchange(a, x.mint(oidcstub.MintOptions{Claims: iat(restored.Add(30 * time.Second))})), 400, "invalid_request", "subject_token_predates_restore")
	})
	t.Run("R-CI-018 a token issued more than the skew after the restore finished is exchanged", func(t *testing.T) {
		wantToken(t, x.exchange(a, x.mint(oidcstub.MintOptions{Claims: iat(restored.Add(90 * time.Second))})))
	})
	t.Run("R-CI-018 with single use off, the older token is exchanged", func(t *testing.T) {
		tok := mint(t, off, oidcstub.MintOptions{Claims: iat(restored.Add(-10 * time.Minute))})
		x.remember(tok)
		wantToken(t, x.exchange(a, tok))
	})
}

// R-CI-018 with Valkey stopped: 1,000 single-use ID tokens, each sent to two replicas at once, yield exactly one 200 each and
// no 5xx. The Sentinel failover and FLUSHALL parts of the criterion need a stack of their own (docs/developer/ci-oidc.md,
// "Pending criteria"); with Valkey stopped no replica can consult it at all, which is the case the replay set must not need.
func TestR_CI_018_OneThousandSingleUseTokensOnTwoReplicasWithValkeyStopped(t *testing.T) {
	x := newXEnv(t, options{valkey: false, env: []string{
		`TROVE_CI_EXCHANGE_RATE_LIMIT_EXEMPT_CIDRS=["127.0.0.0/8"]`,
		"TROVE_CI_EXCHANGE_RATE_LIMIT_PER_SUBJECT_PER_MINUTE=100000", "TROVE_CI_EXCHANGE_RATE_LIMIT_PER_SUBJECT_BURST=100000",
	}})
	a, b := x.web("web-a"), x.web("web-b")
	x.issuer(a, map[string]any{"singleUseSubjectTokens": true})
	x.addMapping(a, releaseMapping("acme-release-publish", 100))
	tokens := make([]string, 1000)
	for i := range tokens {
		tokens[i] = x.mint(oidcstub.MintOptions{})
	}
	results := make([][]int, len(tokens))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, tok := range tokens {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = parallel(t, tok, a, b)
		}()
	}
	wg.Wait()
	for i, r := range results {
		if count(r, 200) != 1 || count(r, 400) != 1 {
			t.Fatalf("token %d answered %v, want one 200 and one 400", i, r)
		}
	}
	if n := x.number(`SELECT count(*) FROM ci_subject_token_uses WHERE exchange_count = 1`); n != 1000 {
		t.Errorf("%d jti rows with one exchange, want 1,000", n)
	}
}
