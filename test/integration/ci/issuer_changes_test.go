//go:build integration

package ci

import (
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/oidcstub"
)

// The issuer changes that move trust (R-CI-001, R-CI-002, R-CI-003, R-CI-005, Q-CI-50, Q-CI-51): a new key location, a
// public switch, fewer pin claims, and what a replica whose copy lags does with an issuer it does not know yet.

// replaceIssuer replaces the issuer with the members of change applied and returns the answer.
func (e *env) replaceIssuer(r *testenv.Replica, name string, change map[string]any) testenv.Answer {
	e.t.Helper()
	g := e.do(r, "GET", issuersPath+"/"+name, nil)
	wantStatus(e.t, g, 200, "")
	body := g.JSON(e.t)
	for k, v := range change {
		body[k] = v
	}
	return e.do(r, "PUT", issuersPath+"/"+name, body, "If-Match", etag(g))
}

// R-CI-003, Q-CI-50: a changed jwks_uri or discovery_url replaces the stored set with the fail-closed placeholder in
// the write's transaction and fetches from the new location: no key of the old location validates a token after the
// commit, and the set of the new one is used as soon as its fetch stored it.
func TestR_CI_003_AChangedKeyLocationFailsClosedUntilItsSetIsFetched(t *testing.T) {
	x := newXEnv(t, options{valkey: true})
	a := x.web("web-a")
	x.issuer(a)
	x.addMapping(a, releaseMapping("acme-release-publish", 100))
	wantToken(t, x.exchange(a, x.mint(oidcstub.MintOptions{})))

	wantStatus(t, x.replaceIssuer(a, xIssuer, map[string]any{"jwksUri": "https://127.0.0.1:1/keys"}), 200, "")
	if got := x.number("SELECT extract(epoch FROM fetched_at)::bigint FROM ci_issuer_keys"); got != 0 {
		t.Fatalf("the stored set of the old location survived the change (fetched_at %d)", got)
	}
	if got := x.text("SELECT jwks::text FROM ci_issuer_keys"); got != `{"keys": []}` {
		t.Errorf("the placeholder holds %s", got)
	}
	r := x.exchange(a, x.mint(oidcstub.MintOptions{}))
	wantOAuth(t, r, 503, "temporarily_unavailable", "signing_keys_unavailable")

	// Back to the location of the discovery document: the change fetches the set, and the exchange works again.
	wantStatus(t, x.replaceIssuer(a, xIssuer, map[string]any{"jwksUri": nil}), 200, "")
	within(t, "the set of the discovery document's location stored", 10*time.Second, func() bool {
		return x.number("SELECT extract(epoch FROM fetched_at)::bigint FROM ci_issuer_keys") > 0
	})
	wantToken(t, x.exchange(a, x.mint(oidcstub.MintOptions{})))
}

// R-CI-005, Q-CI-50: switching an issuer public, or removing a claim from its pin_claims, is refused with 409 while a
// mapping of the issuer would fail the pin rule its save would apply now; the answer lists those mappings.
func TestR_CI_005_AnIssuerChangeThatUnpinsMappingsIsRefused(t *testing.T) {
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	e.issuer(t, a, "gitlab-main", false)
	e.stack.Repository("npm-internal", "local", "npm", "{}")
	wantStatus(t, e.do(a, "POST", mappingsOf("gitlab-main"), mapping("path-read", 10, all(eq("namespace_path", "acme")),
		grants(inline("it/npm-internal", []string{"*"}, "read")))), 201, "")
	wantStatus(t, e.do(a, "POST", mappingsOf("gitlab-main"), mapping("ns-write", 20, all(eq("namespace_id", "4711"), eq("ref_protected", true)),
		grants(map[string]any{"repositories": []string{"it/npm-internal"}, "packages": map[string]any{"include": []string{"@a/*"}}, "actions": []string{"deploy"}, "ownership": "none"}))), 201, "")

	t.Run("R-CI-005 public_issuer true with an unpinned mapping", func(t *testing.T) {
		r := e.replaceIssuer(a, "gitlab-main", map[string]any{"publicIssuer": true})
		wantStatus(t, r, 409, "mappings_unpinned")
		if body := string(r.Body); !strings.Contains(body, "path-read") || strings.Contains(body, "ns-write") {
			t.Errorf("the answer does not list exactly path-read: %s", body)
		}
	})
	t.Run("R-CI-010 pin_claims without namespace_id unpins a writing mapping of a private issuer", func(t *testing.T) {
		r := e.replaceIssuer(a, "gitlab-main", map[string]any{"pinClaims": []string{"project_id"}})
		wantStatus(t, r, 409, "mappings_unpinned")
		if body := string(r.Body); !strings.Contains(body, "ns-write") || strings.Contains(body, "path-read") {
			t.Errorf("the answer does not list exactly ns-write: %s", body)
		}
	})
	t.Run("a change that unpins nothing is applied", func(t *testing.T) {
		wantStatus(t, e.replaceIssuer(a, "gitlab-main", map[string]any{"pinClaims": []string{"namespace_id", "project_id"}}), 200, "")
		wantStatus(t, e.replaceIssuer(a, "gitlab-main", map[string]any{"description": "the internal instance"}), 200, "")
	})
}

// R-CI-003, Q-CI-51: a replica whose copy of the issuers lags (its change log stopped, Valkey stopped) re-reads the
// issuers, at most once per cluster.epoch_poll_interval, for a token whose iss its copy does not hold or holds as
// disabled, so a just-created or re-enabled issuer is served within that interval, not after the copy's SafetyTTL.
func TestR_CI_003_ALaggingReplicaLearnsANewIssuerWithinThePollInterval(t *testing.T) {
	x := newXEnv(t, options{valkey: false})
	a, b := x.web("web-a"), x.web("web-b")
	// B's copy holds no issuer, then B stops listening.
	wantOAuth(t, x.exchange(b, x.mint(oidcstub.MintOptions{})), 400, "invalid_request", "unknown_issuer")
	if err := b.App.Changes().Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	x.issuer(a)
	wantStatus(t, x.do(a, "POST", mappingsOf(xIssuer), releaseMapping("acme-release-publish", 100)), 201, "")
	within(t, "B serving the issuer created on A", 5*time.Second, func() bool {
		return x.exchange(b, x.mint(oidcstub.MintOptions{})).Status == 200
	})

	t.Run("R-CI-002 a re-enabled issuer is served on B within the interval", func(t *testing.T) {
		wantStatus(t, x.replaceIssuer(a, xIssuer, map[string]any{"enabled": false}), 200, "")
		within(t, "B refusing the disabled issuer", 5*time.Second, func() bool {
			return x.exchange(b, x.mint(oidcstub.MintOptions{})).Status == 400
		})
		wantStatus(t, x.replaceIssuer(a, xIssuer, map[string]any{"enabled": true}), 200, "")
		within(t, "B serving the re-enabled issuer", 5*time.Second, func() bool {
			return x.exchange(b, x.mint(oidcstub.MintOptions{})).Status == 200
		})
	})
}
