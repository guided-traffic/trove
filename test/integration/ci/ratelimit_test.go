//go:build integration

package ci

// R-CI-021: the cluster-wide budgets of the exchange per address and per subject, with Valkey running and stopped,
// exempt networks, a flood of forged tokens beside a legitimate project, garbage that costs no outbound request and
// few audit rows; R-CI-029: what a Valkey outage changes, and the Valkey keys the exchange leaves.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/oidcstub"
)

// flood sends n requests of subject from addr, spread round robin over the replicas by workers goroutines, and
// returns the statuses and whether every 429 carried Retry-After.
func flood(t *testing.T, reps []*testenv.Replica, addr string, n, workers int, subject func(i int) string) (map[int]int, bool) {
	t.Helper()
	var mu sync.Mutex
	got := map[int]int{}
	retry := true
	var next atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				st, ra := rawExchange(t, reps[i%len(reps)], addr, subject(i))
				mu.Lock()
				got[st]++
				if st == 429 && ra == "" {
					retry = false
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return got, retry
}

// garbage is a subject token that does not parse.
func garbage(int) string { return "not-a-token" }

func TestR_CI_021_TheAddressBudgetIsOneAcrossReplicas(t *testing.T) {
	for _, c := range []struct {
		name     string
		valkey   bool
		replicas int
	}{
		{"three web replicas with Valkey", true, 3},
		{"one web replica", true, 1},
		{"three web replicas with Valkey stopped", false, 3},
	} {
		t.Run("R-CI-021 "+c.name+": 1,000 requests from one address in 5 s get at most 200 answers that are not 429", func(t *testing.T) {
			x := newXEnv(t, options{valkey: c.valkey})
			var reps []*testenv.Replica
			for i := range c.replicas {
				reps = append(reps, x.web(fmt.Sprintf("web-%d", i)))
			}
			if !c.valkey {
				// The fallback divides each budget by the live web replicas (R-ARCH-034): wait until every replica
				// counts the others.
				within(t, "the replicas seeing each other", 10*time.Second, func() bool {
					return x.number(`SELECT count(*) FROM replica WHERE 'web' = ANY(roles) AND state = 'ready' AND heartbeat_at > now() - interval '3 seconds'`) == int64(c.replicas)
				})
				time.Sleep(1500 * time.Millisecond) // one heartbeat interval: every replica's count reads the others
			}
			start := time.Now()
			got, retry := flood(t, reps, "203.0.113.1", 1000, 25, garbage)
			took := time.Since(start)
			if took > 5*time.Second {
				t.Fatalf("the 1,000 requests took %s, longer than the criterion's 5 s", took)
			}
			if passed := got[200] + got[400]; passed > 200 || passed == 0 {
				t.Errorf("%d requests passed the address limiter (%v), want 1 to 200", passed, got)
			}
			if got[429] < 800 || !retry {
				t.Errorf("429s %d, every one with Retry-After %v (%v)", got[429], retry, got)
			}
		})
	}
}

func TestR_CI_021_AnExemptAddressIsBoundedByTheSubjectBudget(t *testing.T) {
	x := newXEnv(t, options{valkey: true, env: []string{
		`TROVE_CI_EXCHANGE_RATE_LIMIT_EXEMPT_CIDRS=["198.51.100.0/24"]`,
		"TROVE_CI_EXCHANGE_RATE_LIMIT_PER_SUBJECT_PER_MINUTE=6", "TROVE_CI_EXCHANGE_RATE_LIMIT_PER_SUBJECT_BURST=6",
	}})
	a := x.web("web-a")
	x.issuer(a)
	x.addMapping(a, releaseMapping("acme-release-publish", 100))
	x.addMapping(a, mapping("second-read", 200, all(eq("namespace_id", "4712")), grants(inline("npm-internal", []string{"*"}, "read"))))
	const runner = "198.51.100.44"

	t.Run("R-CI-021 the exempt address is never refused by the address limiter", func(t *testing.T) {
		got, _ := flood(t, []*testenv.Replica{a}, runner, 300, 10, garbage)
		if got[429] != 0 || got[400] != 300 {
			t.Errorf("statuses %v", got)
		}
	})
	t.Run("R-CI-021 its exchanges of one project stop at the project's budget, a second project's go on", func(t *testing.T) {
		var last testenv.Answer
		ok := 0
		for range 10 {
			last = x.exchangeFrom(a, runner, x.mint(oidcstub.MintOptions{}))
			if last.Status == 200 {
				ok++
			}
		}
		if ok < 5 || ok > 6 {
			t.Errorf("%d exchanges of the first project succeeded, want the budget of 6 (with the sliding window, 5 or 6)", ok)
		}
		wantOAuth(t, last, 429, "slow_down", "rate_limited")
		if d := last.JSON(t)["error_description"].(string); !strings.Contains(d, "project") {
			t.Errorf("the refusal is not the subject's: %q", d)
		}
		second := x.mint(oidcstub.MintOptions{Claims: map[string]any{"project_id": "2222", "job_project_id": "2222", "namespace_id": "4712"}})
		wantToken(t, x.exchangeFrom(a, runner, second))
	})
}

// forge replaces the signature of a valid token by another one of the same length.
func forge(tok string) string {
	parts := strings.Split(tok, ".")
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	for i := range sig {
		sig[i] ^= 0x5a
	}
	return parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestR_CI_021_ForgedTokensLeaveALegitimateProjectUnaffected(t *testing.T) {
	x := newXEnv(t, options{valkey: true})
	a, b := x.web("web-a"), x.web("web-b")
	x.issuer(a)
	x.addMapping(a, releaseMapping("acme-release-publish", 100))
	forged := make([]string, 100)
	for i := range forged {
		forged[i] = forge(x.mint(oidcstub.MintOptions{}))
	}
	var wg sync.WaitGroup
	results := make([]map[int]int, 20)
	for k := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[k], _ = flood(t, []*testenv.Replica{a, b}, fmt.Sprintf("192.0.2.%d", 10+k), 100, 2, func(i int) string { return forged[i] })
		}()
	}
	// The legitimate project exchanges from its runner while the flood runs: every exchange succeeds.
	legit := 0
	for range 20 {
		if r := x.exchangeFrom(b, "198.51.100.77", x.mint(oidcstub.MintOptions{})); r.Status == 200 {
			legit++
		}
	}
	wg.Wait()
	if legit != 20 {
		t.Errorf("%d of 20 legitimate exchanges succeeded during the flood", legit)
	}
	refused := 0
	for _, r := range results {
		refused += r[400] + r[429]
		if r[200] != 0 {
			t.Fatalf("a forged token was accepted: %v", r)
		}
	}
	if refused != 2000 {
		t.Errorf("%d of 2,000 forged tokens were refused", refused)
	}
	if v := metric(t, a, exchanges(xIssuer, "denied", "signature_invalid")) + metric(t, b, exchanges(xIssuer, "denied", "signature_invalid")); v < 1 {
		t.Error("the forged tokens are not counted as signature_invalid")
	}
}

func TestR_CI_021_GarbageCausesNoFetchAndFewAuditRows(t *testing.T) {
	x := newXEnv(t, options{valkey: true, env: []string{`TROVE_CI_EXCHANGE_RATE_LIMIT_EXEMPT_CIDRS=["198.51.100.0/24"]`}})
	a := x.web("web-a")
	x.issuer(a)
	x.d.stub.Reset()
	before := x.d.stub.Counts()
	// From an exempt address every request reaches the parse; from another one most stop at the address limiter.
	exempt, _ := flood(t, []*testenv.Replica{a}, "198.51.100.5", 9000, 25, garbage)
	limited, _ := flood(t, []*testenv.Replica{a}, "203.0.113.9", 1000, 10, garbage)
	if exempt[400] != 9000 || limited[400]+limited[429] != 1000 {
		t.Fatalf("statuses %v, %v", exempt, limited)
	}
	if after := x.d.stub.Counts(); after.Discovery != before.Discovery || after.JWKS != before.JWKS {
		t.Errorf("garbage made the replica contact the issuer: %+v -> %+v", before, after)
	}
	// The denials are aggregated per address, reason and minute (R-CI-021): the first at once, the rest as one event
	// with its count when the minute ends or the replica stops.
	if err := a.Stop(); err != nil {
		t.Fatal(err)
	}
	within(t, "the audit buffer written", 10*time.Second, func() bool {
		return x.number(`SELECT coalesce(sum(coalesce((details->>'count')::bigint, 1)), 0)::bigint FROM audit_event WHERE action = $1`, audit.EventCIExchangeDenied) >= 10000
	})
	if rows := x.number(`SELECT count(*) FROM audit_event`); rows >= 100 {
		t.Errorf("10,000 garbage requests wrote %d audit rows", rows)
	}
	if n := x.number(`SELECT coalesce(sum(coalesce((details->>'count')::bigint, 1)), 0)::bigint FROM audit_event WHERE action = $1`, audit.EventCIExchangeDenied); n != 10000 {
		t.Errorf("the denial events count %d requests, want 10,000", n)
	}
}

// keyspace is the Valkey keyspace of the installation (R-ARCH-030): system_state keyspace_id, 12 lowercase hex digits.
func (x *xenv) keyspace() string {
	x.t.Helper()
	id := strings.ReplaceAll(x.text(`SELECT value #>> '{}' FROM system_state WHERE key = 'keyspace_id'`), "-", "")
	return strings.ToLower(id[:12])
}

// valkeyKeys lists the keys of the installation's keyspace on the stack's Valkey (valkey-cli --scan with the
// administrator of the stack), from the first of the primary and the replica containers that answers.
func (x *xenv) valkeyKeys() []string {
	x.t.Helper()
	pattern := testenv.Get(x.t, "TROVE_TEST_VALKEY_KEY_PREFIX") + x.keyspace() + ":*"
	user, pass := testenv.Get(x.t, "TROVE_TEST_VALKEY_ADMIN_USERNAME"), testenv.Get(x.t, "TROVE_TEST_VALKEY_ADMIN_PASSWORD")
	nodes := strings.Split(testenv.Get(x.t, "TROVE_TEST_VALKEY_NODES"), ",")
	for i, c := range strings.Split(testenv.Get(x.t, "TROVE_TEST_VALKEY_CONTAINERS"), ",")[:len(nodes)] {
		_, port, _ := strings.Cut(nodes[i], ":")
		out, err := exec.Command("docker", "exec", c, "valkey-cli", "--tls", "--cacert", "/certs/ca.crt", "-p", port, "--user", user, "--pass", pass,
			"--no-auth-warning", "--scan", "--pattern", pattern).Output()
		if err == nil {
			return strings.Fields(string(out))
		}
	}
	x.t.Fatal("no Valkey container of the stack answers valkey-cli")
	return nil
}

func TestR_CI_029_TheExchangeLeavesOnlyItsLimitersInValkey(t *testing.T) {
	x := newXEnv(t, options{valkey: true})
	a, b := x.web("web-a"), x.web("web-b")
	x.issuer(a, map[string]any{"singleUseSubjectTokens": true})
	x.addMapping(a, releaseMapping("acme-release-publish", 100))
	var jtis []string
	for i := range 6 {
		jti := fmt.Sprintf("jti-valkey-%d", i)
		jtis = append(jtis, jti)
		tok := x.mint(oidcstub.MintOptions{Claims: map[string]any{"jti": jti}})
		wantToken(t, x.exchange([]*testenv.Replica{a, b}[i%2], tok))
		wantOAuth(t, x.exchange([]*testenv.Replica{a, b}[(i+1)%2], tok), 400, "invalid_request", "subject_token_replayed")
	}
	wantOAuth(t, x.exchange(a, x.mint(oidcstub.MintOptions{ForeignSignature: true})), 400, "invalid_request", "signature_invalid")
	keys := x.valkeyKeys()
	seen := map[string]bool{}
	for _, k := range keys {
		for _, jti := range jtis {
			if strings.Contains(k, jti) {
				t.Errorf("a Valkey key names a jti: %s", k)
			}
		}
		_, rest, _ := strings.Cut(k, x.keyspace()+":")
		fields := strings.Split(rest, ":")
		if len(fields) >= 3 && fields[0] == "rl" && strings.HasPrefix(fields[2], "ci") {
			seen[fields[2]] = true
			if fields[2] != "ci_exchange_address" && fields[2] != "ci_exchange_subject" {
				t.Errorf("a rate-limit key of another CI limiter: %s", k)
			}
			continue
		}
		if strings.Contains(strings.ToLower(rest), "ci") {
			t.Errorf("a Valkey key of the CI federation that is no limiter counter: %s", k)
		}
	}
	if !seen["ci_exchange_address"] || !seen["ci_exchange_subject"] {
		t.Errorf("the counters of both limiters are expected among %d keys: %v", len(keys), seen)
	}
}

// outcome is what a scenario of the exchange answered: the status and the reason.
func outcome(a testenv.Answer) string {
	var m map[string]any
	_ = json.Unmarshal(a.Body, &m)
	return fmt.Sprintf("%d %v", a.Status, m["reason"])
}

func TestR_CI_029_AValkeyOutageChangesNoAnswerButThe429Budget(t *testing.T) {
	answers := map[bool][]string{}
	for _, valkey := range []bool{true, false} {
		x := newXEnv(t, options{valkey: valkey})
		a, b := x.web("web-a"), x.web("web-b")
		x.issuer(a, map[string]any{"singleUseSubjectTokens": true})
		x.addMapping(a, releaseMapping("acme-release-publish", 100))
		once := x.mint(oidcstub.MintOptions{})
		var got []string
		for _, s := range []testenv.Answer{
			x.exchange(a, x.mint(oidcstub.MintOptions{})),
			x.exchange(b, x.mint(oidcstub.MintOptions{}), "scope", "read:npm-internal"),
			x.exchange(b, x.mint(oidcstub.MintOptions{}), "scope", "deploy:nuget-internal"),
			x.exchange(a, x.mint(oidcstub.MintOptions{Claims: map[string]any{"namespace_id": "9999"}})),
			x.exchange(a, x.mint(oidcstub.MintOptions{ForeignSignature: true})),
			x.exchange(a, once), x.exchange(b, once),
			x.exchange(a, x.mint(oidcstub.MintOptions{Claims: map[string]any{"project_id": "900", "job_project_id": "42"}})),
		} {
			got = append(got, outcome(s))
		}
		answers[valkey] = got
	}
	if strings.Join(answers[true], ", ") != strings.Join(answers[false], ", ") {
		t.Errorf("with Valkey: %v\nwithout:     %v", answers[true], answers[false])
	}
}
