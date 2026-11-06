//go:build integration

package ci

// R-CI-008: every reason of the exchange's error table that the MVP can reach, triggered through the endpoint and
// asserted by status, `error` and `reason`; the grep that no answer and no log line holds a subject token or a CI
// token (checkNoSecrets, the cleanup of every xenv); expose_reason false; the 404 of a switched-off exchange and
// the 405 of another method (R-CI-007).
//
// Not reached here, and why (Q-CI-43): job_token_rejected, job_not_running and gitlab_unreachable belong to the
// job-token fallback, which is P2 and answers unsupported_subject_token_type (R-CI-022, Q-CI-5);
// configuration_changed is the second mismatch inside one transaction, which the lock protocol of R-CI-007 and
// R-CI-009 rules out against a consistent database (TestConfigurationChangedIsTheSecondMismatch in internal/ci
// proves the answer with a store that disagrees with itself).

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/oidcstub"
)

func TestR_CI_008_EveryReasonOfTheErrorTable(t *testing.T) {
	x := newXEnv(t, options{valkey: true, env: []string{
		"TROVE_DATABASE_SESSION_LOCK_TIMEOUT=1s", "TROVE_CI_SIGNING_KEYS_MAX_STALE=2m", "TROVE_CI_SIGNING_KEYS_MAX_REFRESH_INTERVAL=1m",
		"TROVE_CI_SIGNING_KEYS_DEFAULT_REFRESH_INTERVAL=1m", "TROVE_CI_SIGNING_KEYS_MIN_REFRESH_INTERVAL=30s",
	}})
	a := x.web("web-a")
	x.issuer(a)
	x.addMapping(a, releaseMapping("acme-release-publish", 100))
	// A mapping whose grant renders the environment claim: a token without one fails to render (R-CI-014).
	x.addMapping(a, mapping("env-read", 200, all(eq("project_id", "5555")), grants(inline("it/npm-internal", []string{"@{{ environment | slug }}/*"}, "read"))))
	// The other issuers of the destructive cases, each of a double of its own.
	other := startDouble(t, oidcstub.Config{Audience: troveAudience})
	stale := startDouble(t, oidcstub.Config{Audience: troveAudience})
	broken := startDouble(t, oidcstub.Config{Audience: troveAudience})
	for name, d := range map[string]*double{"gitlab-other": other, "gitlab-stale": stale, "gitlab-broken": broken} {
		wantStatus(t, x.do(a, "POST", issuersPath, d.body(name)), 201, "")
		wantStatus(t, x.do(a, "POST", mappingsOf(name), readOnlyMapping("all-read", 1, releaseCondition(), "it/npm-internal")), 201, "")
	}
	x.settled()
	now := time.Now().Unix()
	good := func() string { return x.mint(oidcstub.MintOptions{}) }
	mintOf := func(d *double, o oidcstub.MintOptions) string { tok := mint(t, d, o); x.remember(tok); return tok }

	cases := []struct {
		name, oauth, reason string
		status              int
		send                func() testenv.Answer
	}{
		{"a parameter sent twice", "invalid_request", "malformed_request", 400, func() testenv.Answer {
			return x.post(a, formType, []byte(form(good()).Encode()+"&grant_type="+"urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Atoken-exchange"))
		}},
		{"an empty subject_token (Q-CI-41)", "invalid_request", "malformed_request", 400, func() testenv.Answer { return x.exchange(a, "") }},
		{"no grant_type", "invalid_request", "malformed_request", 400, func() testenv.Answer {
			v := form(good())
			v.Del("grant_type")
			return x.post(a, formType, []byte(v.Encode()))
		}},
		{"a ttl that is no number", "invalid_request", "malformed_request", 400, func() testenv.Answer { return x.exchange(a, good(), "ttl", "ten") }},
		{"a JSON member that is no string", "invalid_request", "malformed_request", 400, func() testenv.Answer {
			b, _ := json.Marshal(map[string]any{"grant_type": "urn:ietf:params:oauth:grant-type:token-exchange", "subject_token": good(), "subject_token_type": true})
			return x.post(a, "application/json", b)
		}},
		{"a JSON body with a member twice", "invalid_request", "malformed_request", 400, func() testenv.Answer {
			return x.post(a, "application/json", []byte(`{"grant_type":"a","grant_type":"b"}`))
		}},
		{"a body over ci.exchange.max_body_bytes", "invalid_request", "malformed_request", 400, func() testenv.Answer {
			return x.post(a, formType, []byte("scope="+strings.Repeat("a", 70000)))
		}},
		{"a text body", "invalid_request", "unsupported_content_type", 400, func() testenv.Answer {
			return x.post(a, "text/plain", []byte(form(good()).Encode()))
		}},
		{"the job-token subject type (P2)", "invalid_request", "unsupported_subject_token_type", 400, func() testenv.Answer {
			return x.exchange(a, good(), "subject_token_type", "urn:trove:token-type:gitlab-job-token")
		}},
		{"another subject type", "invalid_request", "unsupported_subject_token_type", 400, func() testenv.Answer {
			return x.exchange(a, good(), "subject_token_type", "urn:ietf:params:oauth:token-type:saml2")
		}},
		{"a refresh token requested", "invalid_request", "unsupported_requested_token_type", 400, func() testenv.Answer {
			return x.exchange(a, good(), "requested_token_type", "urn:ietf:params:oauth:token-type:refresh_token")
		}},
		{"an actor token", "invalid_request", "actor_token_not_supported", 400, func() testenv.Answer {
			return x.exchange(a, good(), "actor_token", good(), "actor_token_type", "urn:ietf:params:oauth:token-type:jwt")
		}},
		{"another grant type", "unsupported_grant_type", "grant_type_unsupported", 400, func() testenv.Answer {
			return x.exchange(a, good(), "grant_type", "client_credentials")
		}},
		{"an audience of another server", "invalid_target", "audience_not_this_server", 400, func() testenv.Answer {
			return x.exchange(a, good(), "audience", "https://other.example.com")
		}},
		{"a resource of another server", "invalid_target", "audience_not_this_server", 400, func() testenv.Answer {
			return x.exchange(a, good(), "resource", "https://other.example.com/api")
		}},
		{"manage in the scope", "invalid_scope", "scope_malformed", 400, func() testenv.Answer {
			return x.exchange(a, good(), "scope", "manage:npm-internal")
		}},
		// H-418, Q-CI-51: a key that names no repository answers as one that exists and is not granted, so the scope
		// tells a matched job nothing of which repositories exist.
		{"a scope key that names no repository", "invalid_scope", "scope_not_granted", 400, func() testenv.Answer {
			return x.exchange(a, good(), "scope", "read:it/npm-nowhere")
		}},
		{"a bare key in the scope names no project (R-CI-007, Q-AUTHZ-216)", "invalid_scope", "scope_malformed", 400, func() testenv.Answer {
			return x.exchange(a, good(), "scope", "read:npm-internal")
		}},
		{"a scope the grants do not hold", "invalid_scope", "scope_not_granted", 400, func() testenv.Answer {
			return x.exchange(a, good(), "scope", "deploy:it/nuget-internal")
		}},
		{"a requested ttl below ci.token.min_ttl (Q-CI-41)", "invalid_request", "ttl_below_minimum", 400, func() testenv.Answer {
			return x.exchange(a, good(), "ttl", "5")
		}},
		{"not a JWS", "invalid_request", "subject_token_malformed", 400, func() testenv.Answer { return x.exchange(a, "not-a-token") }},
		{"a JWE", "invalid_request", "subject_token_malformed", 400, func() testenv.Answer { return x.exchange(a, x.mint(oidcstub.MintOptions{JWE: true})) }},
		{"a subject token over ci.exchange.max_subject_token_bytes", "invalid_request", "subject_token_malformed", 400, func() testenv.Answer {
			return x.exchange(a, x.mint(oidcstub.MintOptions{PadTo: 20000}))
		}},
		{"a GitLab job token", "invalid_request", "wrong_subject_token_type", 400, func() testenv.Answer { return x.exchange(a, x.mint(oidcstub.MintOptions{JobToken: true})) }},
		{"an iss of no issuer", "invalid_request", "unknown_issuer", 400, func() testenv.Answer {
			return x.exchange(a, x.mint(oidcstub.MintOptions{Claims: map[string]any{"iss": "https://gitlab.nowhere.example"}}))
		}},
		{"ci_issuer naming another issuer", "invalid_request", "issuer_mismatch", 400, func() testenv.Answer {
			return x.exchange(a, good(), "ci_issuer", "gitlab-other")
		}},
		{"HS256 with the public key as the secret (Q-CI-24)", "invalid_request", "algorithm_not_allowed", 400, func() testenv.Answer {
			return x.exchange(a, x.mint(oidcstub.MintOptions{Alg: "HS256"}))
		}},
		{"a typ the issuer does not allow", "invalid_request", "token_type_not_allowed", 400, func() testenv.Answer {
			return x.exchange(a, x.mint(oidcstub.MintOptions{Typ: "at+jwt"}))
		}},
		{"a kid the issuer does not publish", "invalid_request", "unknown_signing_key", 400, func() testenv.Answer {
			return x.exchange(a, x.mint(oidcstub.MintOptions{HeaderKID: "no-such-key"}))
		}},
		{"a signature of another key", "invalid_request", "signature_invalid", 400, func() testenv.Answer {
			return x.exchange(a, x.mint(oidcstub.MintOptions{ForeignSignature: true}))
		}},
		{"an aud of another service", "invalid_request", "audience_invalid", 400, func() testenv.Answer {
			return x.exchange(a, x.mint(oidcstub.MintOptions{Claims: map[string]any{"aud": "https://other.example.com"}}))
		}},
		{"an aud that also names another service", "invalid_request", "audience_not_exclusive", 400, func() testenv.Answer {
			return x.exchange(a, x.mint(oidcstub.MintOptions{Claims: map[string]any{"aud": []string{troveAudience, "https://vault.example.com"}}}))
		}},
		{"exp 61 s ago", "invalid_request", "token_expired", 400, func() testenv.Answer {
			return x.exchange(a, x.mint(oidcstub.MintOptions{Claims: map[string]any{"exp": now - 61, "iat": now - 900, "nbf": now - 905}}))
		}},
		{"nbf two minutes ahead", "invalid_request", "token_not_yet_valid", 400, func() testenv.Answer {
			return x.exchange(a, x.mint(oidcstub.MintOptions{Claims: map[string]any{"nbf": now + 120}}))
		}},
		{"iat two minutes ahead", "invalid_request", "issued_in_future", 400, func() testenv.Answer {
			return x.exchange(a, x.mint(oidcstub.MintOptions{Claims: map[string]any{"iat": now + 120}, Remove: []string{"nbf"}}))
		}},
		{"a required claim missing", "invalid_request", "required_claim_missing", 400, func() testenv.Answer {
			return x.exchange(a, x.mint(oidcstub.MintOptions{Remove: []string{"user_login"}}))
		}},
		{"a fork pipeline in the parent (R-CI-012)", "invalid_request", "cross_project_source_denied", 400, func() testenv.Answer {
			return x.exchange(a, x.mint(oidcstub.MintOptions{Claims: map[string]any{"project_id": "900", "job_project_id": "42"}}))
		}},
		{"claims no mapping matches", "invalid_request", "no_matching_mapping", 400, func() testenv.Answer {
			return x.exchange(a, x.mint(oidcstub.MintOptions{Claims: map[string]any{"namespace_id": "9999"}}))
		}},
		{"a grant that renders an absent claim", "invalid_request", "grant_render_failed", 400, func() testenv.Answer {
			return x.exchange(a, x.mint(oidcstub.MintOptions{Claims: map[string]any{"project_id": "5555", "job_project_id": "5555", "namespace_id": "5555"}, Remove: []string{"environment"}}))
		}},
		{"an ID token with 20 s left", "invalid_request", "subject_token_expires_too_soon", 400, func() testenv.Answer {
			return x.exchange(a, x.mint(oidcstub.MintOptions{Claims: map[string]any{"exp": time.Now().Unix() + 20}}))
		}},
	}
	for _, c := range cases {
		t.Run("R-CI-008 "+c.reason+": "+c.name, func(t *testing.T) {
			wantOAuth(t, c.send(), c.status, c.oauth, c.reason)
		})
	}

	t.Run("R-CI-008 issuer_disabled: the issuer of the token is disabled", func(t *testing.T) {
		g := x.do(a, "GET", issuersPath+"/gitlab-other", nil)
		body := g.JSON(t)
		body["enabled"] = false
		wantStatus(t, x.do(a, "PUT", issuersPath+"/gitlab-other", body, "If-Match", etag(g)), 200, "")
		within(t, "the copy of the issuers", 5*time.Second, func() bool {
			is, ok, _ := a.App.CIIssuers().Resolve(t.Context(), other.stub.Issuer())
			return ok && !is.Enabled
		})
		wantOAuth(t, x.exchange(a, mintOf(other, oidcstub.MintOptions{})), 400, "invalid_request", "issuer_disabled")
	})

	t.Run("R-CI-008 rate_limited: the per-address budget is spent, with Retry-After", func(t *testing.T) {
		var last testenv.Answer
		for i := 0; i < 130 && last.Status != 429; i++ {
			last = x.exchangeFrom(a, "198.51.100.8", "garbage")
		}
		wantOAuth(t, last, 429, "slow_down", "rate_limited")
		if last.Header.Get("Retry-After") == "" {
			t.Error("a 429 without Retry-After")
		}
	})

	t.Run("R-CI-008 signing_keys_unavailable: the stored key set is older than max_stale and the issuer is unreachable", func(t *testing.T) {
		if err := stale.stub.SetFault(oidcstub.Fault{Unreachable: true}); err != nil {
			t.Fatal(err)
		}
		id := x.text("SELECT id::text FROM ci_issuers WHERE name = 'gitlab-stale'")
		x.exec("UPDATE ci_issuer_keys SET fetched_at = now() - interval '3 minutes' WHERE issuer_id = $1::uuid", id)
		x.bump(cluster.AreaCIIssuerKeys, map[string]string{"issuer": id})
		var last testenv.Answer
		within(t, "the replica reading the aged row", 5*time.Second, func() bool {
			last = x.exchange(a, mintOf(stale, oidcstub.MintOptions{}))
			return last.Status == 503
		})
		wantOAuth(t, last, 503, "temporarily_unavailable", "signing_keys_unavailable")
		if last.Header.Get("Retry-After") == "" {
			t.Error("a 503 without Retry-After")
		}
	})

	t.Run("R-CI-008 database_unavailable: the issuer row stays locked beyond the lock timeout", func(t *testing.T) {
		tok := good()
		wantToken(t, x.exchange(a, tok)) // the copies are filled, the next exchange reads only in its transaction
		c := x.conn(t)
		tx, err := c.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err := tx.Exec(t.Context(), "SELECT 1 FROM ci_issuers WHERE name = $1 FOR UPDATE", xIssuer); err != nil {
			t.Fatal(err)
		}
		r := x.exchange(a, tok)
		wantOAuth(t, r, 503, "temporarily_unavailable", "database_unavailable")
		if r.Header.Get("Retry-After") == "" {
			t.Error("a 503 without Retry-After")
		}
		_ = tx.Rollback(context.Background())
		wantToken(t, x.exchange(a, tok))
	})

	t.Run("R-CI-008 internal: a stored mapping that does not decode", func(t *testing.T) {
		x.exec(`UPDATE ci_mappings SET match = '{"claim": 5}'::jsonb WHERE issuer_id = (SELECT id FROM ci_issuers WHERE name = 'gitlab-broken')`)
		wantOAuth(t, x.exchange(a, mintOf(broken, oidcstub.MintOptions{})), 500, "server_error", "internal")
	})

	t.Run("R-CI-008 a refusal writes ci.exchange.denied with its reason; before the signature only iss and kid", func(t *testing.T) {
		// A forged token with a 10 kB project_path from an address of its own, so its denial is the first of its
		// address and reason in this minute (R-CI-020, R-CI-021).
		forged := x.mint(oidcstub.MintOptions{ForeignSignature: true, Claims: map[string]any{"project_path": strings.Repeat("p", 10240)}})
		wantOAuth(t, x.exchangeFrom(a, "198.51.100.9", forged), 400, "invalid_request", "signature_invalid")
		var det map[string]any
		within(t, "the denial event", 5*time.Second, func() bool {
			raw := x.text(`SELECT coalesce((SELECT details::text FROM audit_event WHERE action = $1 AND source_ip = '198.51.100.9' LIMIT 1), '')`, audit.EventCIExchangeDenied)
			return raw != "" && json.Unmarshal([]byte(raw), &det) == nil
		})
		if det["reason"] != "signature_invalid" || det["issuer"] != xIssuer || det["iss"] != x.d.stub.Issuer() || det["kid"] == nil {
			t.Errorf("the denial records %v", det)
		}
		if _, ok := det["claims"]; ok {
			t.Error("a denial before the signature records claims")
		}
		raw := x.text(`SELECT details::text FROM audit_event WHERE action = $1 AND source_ip = '198.51.100.9' LIMIT 1`, audit.EventCIExchangeDenied)
		if strings.Contains(raw, "pppppppp") {
			t.Error("the denial event holds the forged project_path")
		}
		// After the signature the claim snapshot, without user_email.
		wantOAuth(t, x.exchangeFrom(a, "198.51.100.10", x.mint(oidcstub.MintOptions{Claims: map[string]any{"namespace_id": "9999"}})), 400, "invalid_request", "no_matching_mapping")
		within(t, "the verified denial", 5*time.Second, func() bool {
			raw = x.text(`SELECT coalesce((SELECT details::text FROM audit_event WHERE action = $1 AND source_ip = '198.51.100.10' LIMIT 1), '')`, audit.EventCIExchangeDenied)
			return raw != ""
		})
		var verified struct {
			Claims map[string]any `json:"claims"`
		}
		if err := json.Unmarshal([]byte(raw), &verified); err != nil || verified.Claims["namespace_id"] != "9999" || strings.Contains(raw, "user_email") {
			t.Errorf("the verified denial records %s", raw)
		}
	})

	t.Run("R-CI-020 the refusals are counted by issuer, outcome and reason", func(t *testing.T) {
		m := a.Metrics(t)
		for _, s := range []string{
			exchanges(xIssuer, "denied", "audience_invalid"), exchanges("", "denied", "subject_token_malformed"),
			exchanges("gitlab-broken", "error", "internal"), exchanges(xIssuer, "error", "database_unavailable"),
		} {
			if testenv.MetricValue(m, s) < 1 {
				t.Errorf("%s is not counted", s)
			}
		}
		if testenv.MetricValue(m, `trove_ci_exchange_duration_seconds_count{issuer="`+xIssuer+`"}`) < 1 {
			t.Error("the duration of the exchanges is not observed")
		}
	})
}

func TestR_CI_008_ExposeReasonFalseHidesTheReasonButNotTheRequestID(t *testing.T) {
	x := newXEnv(t, options{valkey: true, env: []string{"TROVE_CI_EXCHANGE_EXPOSE_REASON=false"}})
	a := x.web("web-a")
	x.issuer(a)
	r := x.exchangeFrom(a, "198.51.100.20", x.mint(oidcstub.MintOptions{Claims: map[string]any{"aud": "https://other.example.com"}}))
	wantOAuth(t, r, 400, "invalid_request", "")
	m := r.JSON(t)
	if _, has := m["reason"]; has || strings.Contains(m["error_description"].(string), "audience") {
		t.Errorf("the answer names its reason: %s", r.Body)
	}
	// The scope refusal is invalid_request too, and the log and the audit trail keep the reason.
	wantOAuth(t, x.exchange(a, x.mint(oidcstub.MintOptions{}), "scope", "manage:npm-internal"), 400, "invalid_request", "")
	within(t, "the denial event", 5*time.Second, func() bool {
		return x.number(`SELECT count(*) FROM audit_event WHERE action = $1 AND reason = 'audience_invalid'`, audit.EventCIExchangeDenied) == 1
	})
}

func TestR_CI_007_TheEndpointsOtherAnswers(t *testing.T) {
	t.Run("R-CI-007 another method answers 405 with Allow: POST", func(t *testing.T) {
		x := newXEnv(t, options{valkey: true})
		a := x.web("web-a")
		// Not through the stack's helper: it validates an answer against the document's operations, and a method
		// other than POST has none on this path.
		for _, m := range []string{"GET", "PUT", "DELETE"} {
			req, _ := http.NewRequestWithContext(t.Context(), m, "http://"+a.Addr("default")+exchangePath, nil)
			req.Host = testenv.DefaultHost
			res, err := testenv.Client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = res.Body.Close()
			if res.StatusCode != 405 || !strings.Contains(res.Header.Get("Allow"), "POST") {
				t.Errorf("%s answered %d, Allow %q", m, res.StatusCode, res.Header.Get("Allow"))
			}
		}
	})
	t.Run("R-CI-007 ci.exchange.enabled false answers 404 and counts nothing", func(t *testing.T) {
		x := newXEnv(t, options{valkey: true, env: []string{"TROVE_CI_EXCHANGE_ENABLED=false"}})
		a := x.web("web-a")
		x.issuer(a)
		r := x.exchange(a, x.mint(oidcstub.MintOptions{}))
		wantStatus(t, r, 404, "not_found")
		if v := metric(t, a, exchanges("", "denied", "rate_limited")); v != 0 {
			t.Errorf("a switched-off exchange counted %v", v)
		}
	})
}
