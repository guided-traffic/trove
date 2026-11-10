//go:build integration

package ansiblelocal

import (
	"encoding/base64"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection/collectiontest"
)

// TestR_ANS_006_TheCredentialsTheGalaxyClientsSend: Authorization Token (token= in ansible.cfg, --token, the environment variable),
// Basic with the token as the password (username= and password=, ~/.netrc) and Bearer all authenticate, the audit event names the
// principal and the method galaxy_token, and the scheme Token is a credential on the Galaxy routes only (R-ANS-006, R-AUTH-033).
func TestR_ANS_006_TheCredentialsTheGalaxyClientsSend(t *testing.T) {
	e := newEnv(t)
	basic := func(user, tok string) string {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+tok))
	}

	if a := e.publishWith("Token "+e.tokA, collectiontest.Build(spec("1.0.0"))); a.Status != 202 {
		t.Fatalf("publish with Token: %d %s", a.Status, a.Body)
	}
	if got := e.st.Text(`SELECT auth_method FROM audit_event WHERE action = 'package.published' ORDER BY occurred_at LIMIT 1`); got != "galaxy_token" {
		t.Errorf("the event names the method %q, want galaxy_token", got)
	}
	if got := e.st.Text(`SELECT actor_id::text FROM audit_event WHERE action = 'package.published' LIMIT 1`); got != e.alice.ID {
		t.Errorf("the event names %s, want alice %s", got, e.alice.ID)
	}
	if a := e.publishWith(basic("anything", e.tokA), collectiontest.Build(spec("1.1.0"))); a.Status != 202 {
		t.Errorf("publish with Basic: %d %s", a.Status, a.Body)
	}
	if a := e.publishWith("Bearer "+e.tokA, collectiontest.Build(spec("1.2.0"))); a.Status != 202 {
		t.Errorf("publish with Bearer: %d %s", a.Status, a.Body)
	}
	if got := e.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'package.published' AND auth_method = 'basic'`); got != 1 {
		t.Errorf("the Basic publish is recorded as basic: %d", got)
	}
	read := func(auth string) int {
		return e.st.Do(e.r, "GET", base+"v3/collections/acme/tools/", "", nil, "Authorization", auth).Status
	}
	if s := read("Token " + e.tokC); s != 200 {
		t.Errorf("GET with Token: %d", s)
	}
	if s := read("Token not-a-trove-token"); s != 401 {
		t.Errorf("GET with a Token that is none: %d", s)
	}
	// The scheme is a credential on the routes of the format only.
	if a := e.st.Do(e.r, "GET", "/api/v1/projects", "", nil, "Authorization", "Token "+e.tokA); a.Status != 401 {
		t.Errorf("Token on the management API: %d", a.Status)
	}

	// Without a credential: 401 in the Galaxy shape with the challenge of a client that waits for one.
	a := e.get("", "v3/collections/acme/tools/")
	if a.Status != 401 || code(t, a) != "not_authenticated" || a.Header.Get("WWW-Authenticate") != `Basic realm="trove"` {
		t.Errorf("anonymous: %d %s %q", a.Status, a.Body, a.Header.Get("WWW-Authenticate"))
	}
	if a := e.get("", ""); a.Status != 401 {
		t.Errorf("the discovery document without a credential: %d", a.Status)
	}
}

// TestR_ANS_007_PermissionsPerOperation: read reads, deploy publishes, a token that may only read is refused with permission_denied
// in the shape ansible-galaxy prints, and a caller who may not read the repository learns nothing of it (R-ANS-007, R-AUTHZ-016).
func TestR_ANS_007_PermissionsPerOperation(t *testing.T) {
	e := newEnvOpts(t, envOpts{seed: func(e *env) {
		blind := e.st.User("blind")
		e.st.Policy("ansible-blind", []string{"deploy", "delete", "manage"}, []string{repo}, blind)
		e.tokBlind = e.st.Token(blind, scope.Default())
	}})
	e.mustPublish("1.0.0")
	if a := e.publish(e.tokC, collectiontest.Build(spec("1.1.0")), true); a.Status != 403 || code(t, a) != "permission_denied" {
		t.Errorf("a reader publishes: %d %s", a.Status, a.Body)
	}
	if a := e.publish(e.tokB, collectiontest.Build(spec("1.1.0")), true); a.Status != 202 {
		t.Errorf("a deployer publishes: %d %s", a.Status, a.Body)
	}
	for _, p := range []string{"", "v3/collections/acme/tools/", "v3/collections/acme/tools/versions/", "v3/collections/acme/tools/versions/1.0.0/", "v3/artifacts/collections/acme-tools-1.0.0.tar.gz"} {
		if a := e.get(e.tokC, p); a.Status != 200 {
			t.Errorf("GET %q as a reader: %d", p, a.Status)
		}
		if a := e.get(e.tokBlind, p); a.Status != 403 && a.Status != 404 {
			t.Errorf("GET %q without read: %d", p, a.Status)
		}
	}
	// The action of a publish is deploy and nothing else: a caller who holds it and cannot read publishes.
	if a := e.publish(e.tokBlind, collectiontest.Build(spec("1.2.0")), true); a.Status != 202 {
		t.Errorf("a publish with deploy alone: %d %s", a.Status, a.Body)
	}
}

// TestR_ANS_007_OwnershipOfAName: the first publish of a name claims it, and another principal who holds deploy only is refused
// (R-ANS-008 step 3, R-AUTHZ-018).
func TestR_ANS_007_OwnershipOfAName(t *testing.T) {
	e := newEnvOpts(t, envOpts{cfg: `{"local":{"packageOwnership":"enforce"}}`})
	e.mustPublish("1.0.0")
	if a := e.publish(e.tokB, collectiontest.Build(spec("1.1.0")), true); a.Status != 403 {
		t.Errorf("a second principal publishes a name that is owned: %d %s", a.Status, a.Body)
	}
	if a := e.publish(e.tokA, collectiontest.Build(spec("1.1.0")), true); a.Status != 202 {
		t.Errorf("the owner publishes: %d %s", a.Status, a.Body)
	}
	if e.versionRows() != 2 {
		t.Errorf("rows = %d", e.versionRows())
	}
}
