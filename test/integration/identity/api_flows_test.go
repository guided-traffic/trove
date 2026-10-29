//go:build integration

package identity

import (
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"testing"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/auth/token"
)

// assertNoSecret scans every table as text, the stand-in for a dump of the
// database, for a token value and for the material after its prefix (R-AUTH-023).
func assertNoSecret(t *testing.T, e *env, secret string) {
	t.Helper()
	for _, tbl := range scanTables(t, e) {
		for _, needle := range []string{secret, secret[8:]} {
			if n := e.scalar(fmt.Sprintf(`SELECT count(*) FROM %s x WHERE x::text LIKE '%%' || $1 || '%%'`, tbl), needle); n != 0 {
				t.Errorf("R-AUTH-023 table %s contains token material", tbl)
			}
		}
	}
	if n := e.scalar(`SELECT count(*) FROM audit_event WHERE details::text LIKE '%' || $1 || '%'`, secret[8:]); n != 0 {
		t.Errorf("R-AUTH-041 %d audit events carry token material", n)
	}
}

// TestR_AUTH_027_E1_2_TokenEndToEndOverHTTP is E1.2 over the HTTP routes: a PAT
// derived through the API and a service-account token created by an
// administrator are shown once and appear in no table; a revocation through
// replica A is in force on replica B within the revocation bound, with Valkey
// running and with Valkey stopped.
func TestR_AUTH_027_E1_2_TokenEndToEndOverHTTP(t *testing.T) {
	for _, mode := range []vkMode{vkLive, vkDead} {
		t.Run(mode.String(), func(t *testing.T) {
			e := newEnv(t)
			a := e.newReplica("a", replicaOpts{valkey: mode, auditDB: true})
			fx := e.newFixture(a)
			b := e.newReplica("b", replicaOpts{valkey: mode, auditDB: true})
			val := apitest.New(t, v1.EmbeddedYAML())
			sa, sb := a.serve(e, val), b.serve(e, val)
			alice := a.user("alice")
			parent := a.pat(alice)

			// A PAT through the API, shown once.
			rp := sb.call(post, "/api/v1/tokens", parent.Secret, map[string]any{"description": "ci", "ttlSeconds": 3600})
			want(t, rp, 201, "")
			if rp.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control %q", rp.Header.Get("Cache-Control"))
			}
			pat := rp.json(t)
			patSecret := str(pat, "token")

			// A service-account token through the API, by an administrator, on the other replica.
			want(t, sa.call(post, "/api/v1/service-accounts", fx.adminToken, map[string]any{"name": "deploy-bot"}), 201, "")
			rp = sb.call(post, "/api/v1/service-accounts/deploy-bot/tokens", fx.adminToken, map[string]any{"description": "deploy", "ttlSeconds": 7200}, idemHd, "sat-1")
			want(t, rp, 201, "")
			sat := rp.json(t)
			satSecret := str(sat, "token")
			if rp.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control %q", rp.Header.Get("Cache-Control"))
			}
			// The same key again: the value is not shown a second time.
			again := sa.call(post, "/api/v1/service-accounts/deploy-bot/tokens", fx.adminToken, map[string]any{"description": "deploy", "ttlSeconds": 7200}, idemHd, "sat-1")
			want(t, again, 409, "token_already_issued")
			if strings.Contains(string(again.Body), satSecret) {
				t.Fatal("the replay shows the value")
			}

			for _, secret := range []string{patSecret, satSecret} {
				assertNoSecret(t, e, secret)
				for _, s := range []*served{sa, sb} {
					want(t, s.call(get, "/api/v1/auth/whoami", secret, nil), 200, "")
				}
			}

			// Revoke through A (the SAT by an administrator, the PAT by its owner); B rejects within the bound.
			want(t, sa.call(del, "/api/v1/tokens/"+str(sat, "id"), fx.adminToken, nil), 204, "")
			want(t, sa.call(del, "/api/v1/tokens/"+str(pat, "id"), patSecret, nil), 204, "")
			for _, secret := range []string{satSecret, patSecret} {
				took := waitFor(t, "replica B rejecting the revoked token", revocationBound, func() bool {
					return sb.call(get, "/api/v1/auth/whoami", secret, nil).Status == 401
				})
				t.Logf("%s: replica B rejected a revoked token after %s", mode, took)
			}
			want(t, sa.call(get, "/api/v1/auth/whoami", satSecret, nil), 401, "invalid_token")

			// E1.5: the trail has the creations and the revocations, each with its actor.
			for _, action := range []string{"token.created", "token.revoked"} {
				if n := e.scalar(`SELECT count(*) FROM audit_event WHERE action = $1`, action); n < 2 {
					t.Errorf("%s: %d audit events", action, n)
				}
			}
			if n := e.scalar(`SELECT count(*) FROM audit_event WHERE action = 'token.created' AND actor_name = 'root' AND resource_type = 'token' AND details->>'type' = 'sat'`); n != 1 {
				t.Errorf("token.created by the administrator: %d events", n)
			}
			if n := e.scalar(`SELECT count(*) FROM audit_event WHERE action = 'token.revoked' AND reason = 'owner' AND actor_name = 'alice'`); n != 1 {
				t.Errorf("token.revoked by the owner: %d events", n)
			}
			assertNoSecret(t, e, patSecret)
			assertNoSecret(t, e, satSecret)
		})
	}
}

// TestR_API_014_ConcurrentCreatesWithOneKeyMakeOneObject sends the same creates
// with one Idempotency-Key to three replicas at once: one object exists, the other
// answers are replays or a 409 while the first is in progress.
func TestR_API_014_ConcurrentCreatesWithOneKeyMakeOneObject(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkLive})
	fx := e.newFixture(a)
	nodes := []*served{a.serve(e, nil), e.newReplica("b", replicaOpts{valkey: vkLive}).serve(e, nil), e.newReplica("c", replicaOpts{valkey: vkLive}).serve(e, nil)}
	for name, req := range map[string]struct {
		path string
		body map[string]any
		sql  string
	}{
		"user":            {"/api/v1/users", map[string]any{"username": "dana"}, `SELECT count(*) FROM principal WHERE kind = 'user' AND name = 'dana'`},
		"group":           {"/api/v1/groups", map[string]any{"name": "devs"}, `SELECT count(*) FROM principal WHERE kind = 'group' AND name = 'devs'`},
		"service account": {"/api/v1/service-accounts", map[string]any{"name": "bot-one"}, `SELECT count(*) FROM principal WHERE kind = 'service_account' AND name = 'bot-one'`},
	} {
		t.Run(name, func(t *testing.T) {
			var wg sync.WaitGroup
			statuses := make([]int, 6)
			for i := range statuses {
				wg.Add(1)
				go func() {
					defer wg.Done()
					statuses[i] = nodes[i%3].call(post, req.path, fx.adminToken, req.body, idemHd, "key-"+name).Status
				}()
			}
			wg.Wait()
			created := 0
			for _, s := range statuses {
				switch s {
				case 201:
					created++
				case 409:
				default:
					t.Errorf("statuses %v", statuses)
				}
			}
			if created < 1 {
				t.Fatalf("statuses %v", statuses)
			}
			if n := e.scalar(req.sql); n != 1 {
				t.Fatalf("%d objects exist, statuses %v", n, statuses)
			}
		})
	}
}

func basic(secret string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("any:"+secret))
}

// TestR_AUTH_038_E1_8_ThrottleAcrossThreeReplicas is E1.8 over HTTP: rejected
// credentials presented as Bearer and as Basic count in one cluster-wide budget
// per address, so the address is throttled on a replica that saw none of them.
// With Valkey stopped each replica counts its own share of the budget.
func TestR_AUTH_038_E1_8_ThrottleAcrossThreeReplicas(t *testing.T) {
	bad := func(t *testing.T) string {
		t.Helper()
		s, err := token.Generate(token.TypePAT)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	t.Run("Valkey running: one budget for the cluster", func(t *testing.T) {
		e := newEnv(t)
		opts := replicaOpts{valkey: vkLive, failureBudget: 9, liveReplicas: 3}
		reps := []*replica{e.newReplica("a", opts), e.newReplica("b", opts), e.newReplica("c", opts)}
		for _, r := range reps {
			waitFor(t, "Valkey on "+r.name, 20e9, r.vk.Available)
		}
		fx := e.newFixture(reps[0])
		nodes := make([]*served, 3)
		for i, r := range reps {
			nodes[i] = r.serve(e, nil)
		}
		want(t, nodes[0].call(get, "/api/v1/auth/whoami", fx.adminToken, nil), 200, "") // cached on replica a
		// Nine rejected credentials, three per replica, Bearer on two replicas and Basic on one.
		for i := range 9 {
			n := nodes[i%3]
			var rp reply
			if i%3 == 1 {
				rp = n.call(get, "/api/v1/auth/whoami", "", nil, "Authorization", basic(bad(t)))
			} else {
				rp = n.call(get, "/api/v1/auth/whoami", bad(t), nil)
			}
			want(t, rp, 401, "invalid_token")
		}
		// The budget is used up: an unknown credential gets 429 on every replica, and a
		// replica that counted 3 of the 9 does not wait for its own share.
		for _, n := range nodes {
			rp := n.call(get, "/api/v1/auth/whoami", bad(t), nil)
			want(t, rp, 429, "rate_limited")
			if rp.Header.Get("Retry-After") == "" {
				t.Error("429 without Retry-After")
			}
			rp = n.call(get, "/api/v1/auth/whoami", "", nil, "Authorization", basic(bad(t)))
			want(t, rp, 429, "rate_limited")
		}
		// A credential that already validated on a replica still works there (R-AUTH-038 exemption).
		want(t, nodes[0].call(get, "/api/v1/auth/whoami", fx.adminToken, nil), 200, "")
	})
	t.Run("Valkey stopped: each replica enforces its share", func(t *testing.T) {
		e := newEnv(t)
		opts := replicaOpts{valkey: vkDead, failureBudget: 9, liveReplicas: 3}
		reps := []*replica{e.newReplica("a", opts), e.newReplica("b", opts), e.newReplica("c", opts)}
		nodes := make([]*served, 3)
		for i, r := range reps {
			nodes[i] = r.serve(e, nil)
		}
		// Three rejections fill a replica's share of 9/3; its fourth credential is throttled
		// while another replica, which saw none, still validates.
		for range 3 {
			want(t, nodes[0].call(get, "/api/v1/auth/whoami", bad(t), nil), 401, "invalid_token")
		}
		want(t, nodes[0].call(get, "/api/v1/auth/whoami", bad(t), nil), 429, "rate_limited")
		want(t, nodes[1].call(get, "/api/v1/auth/whoami", bad(t), nil), 401, "invalid_token")
	})
}

// TestR_AUTH_038_RevocationByValueCountsInTheFlowStartBudget: POST
// /api/v1/tokens/revoke is one of the unauthenticated endpoints that share the
// cluster-wide budget auth.flow_starts per address (R-AUTH-038).
func TestR_AUTH_038_RevocationByValueCountsInTheFlowStartBudget(t *testing.T) {
	e := newEnv(t)
	opts := replicaOpts{valkey: vkLive, flowStarts: 4, liveReplicas: 3}
	reps := []*replica{e.newReplica("a", opts), e.newReplica("b", opts), e.newReplica("c", opts)}
	for _, r := range reps {
		waitFor(t, "Valkey on "+r.name, 20e9, r.vk.Available)
	}
	nodes := make([]*served, 3)
	for i, r := range reps {
		nodes[i] = r.serve(e, nil)
	}
	for i := range 4 {
		want(t, nodes[i%3].call(post, "/api/v1/tokens/revoke", "", map[string]any{"token": unknownToken(t)}), 202, "")
	}
	for _, n := range nodes {
		rp := n.call(post, "/api/v1/tokens/revoke", "", map[string]any{"token": unknownToken(t)})
		want(t, rp, 429, "rate_limited")
		if rp.Header.Get("Retry-After") == "" {
			t.Error("429 without Retry-After")
		}
	}
}
