//go:build e2e

package e2e

// The CI part of TestBackupJournal (E5.12, R-CI-020, R-CI-018, R-CI-029, R-HA-054): an issuer of the issuer double with single-use
// subject tokens and four mappings of its system tier, a mapping disabled after the snapshot, and a subject token issued before the
// restore finished. The issuer double is started by the set backup (TROVE_E2E_OIDCSTUB=1, deploy/dev/e2e.sh). The restored installation
// has the source's base URL and the dev CA (restore-ns.sh, values-restore.yaml), so it accepts the source's audience and verifies the
// double's certificate.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	// the clock skew of the issuer, in seconds: a token is "predates the restore" up to this long after restored_at (R-CI-018)
	bakCISkew = 5

	// the GitLab project IDs the mappings select on: R is the project of the mapping that stays enabled, P the project of the mappings
	// M1 and M2, Q the project of M3
	bakCIProjectR = "9101"
	bakCIProjectP = "9102"
	bakCIProjectQ = "9103"
)

// bakCI is the issuer and the mappings of one run.
type bakCI struct {
	dbl      *oidcDouble
	issuer   string // the name of the CI issuer
	audience string // the base URL of the installation, the aud of the tokens
	mappings [4]string
	policies []string
}

// bakCISetup registers the issuer and the mappings before the snapshot. M0 (priority 5) selects project R; M1 (10) and M2 (20)
// select project P, M2 granting read and deploy; M3 (30) selects project Q. All four are mappings of the system tier, so a disable of
// M1 lets the jobs of P fall through to M2, which is what R-CI-020 suspends (Q-CI-11).
func bakCISetup(t *testing.T, adm *api, run string) *bakCI {
	t.Helper()
	c := &bakCI{
		dbl:      double(t, "oidcstub", "TROVE_E2E_OIDCSTUB_HOST"),
		issuer:   "bak-ci-" + run,
		audience: "https://localhost:" + edgePort(t),
		mappings: [4]string{"m0", "m1", "m2", "m3"},
		policies: []string{"bak-ci-read-" + run, "bak-ci-deploy-" + run},
	}
	t.Cleanup(func() {
		adm.do(http.MethodDelete, "/api/v1/ci/issuers/"+c.issuer+"?cascade=true", nil)
		for _, p := range c.policies {
			adm.do(http.MethodDelete, "/api/v1/permission-policies/"+p, nil)
		}
	})
	// no entry yet: the save of a mapping that lists a policy adds its own (R-CI-013)
	for _, p := range c.policies {
		adm.do(http.MethodPost, "/api/v1/permission-policies", map[string]any{
			"name": p, "description": "end-to-end tests: the restore drill's CI mappings",
			"resources": map[string]any{"repositories": qualifyAll([]string{bakNPM})}, "entries": []any{},
		}).want(t, "create the policy "+p, 201)
	}
	adm.do(http.MethodPost, "/api/v1/ci/issuers", map[string]any{
		"name": c.issuer, "kind": "gitlab", "issuer": c.dbl.issuer(), "caBundleRef": ciCABundleRef,
		"description": "end-to-end tests: the restore drill", "singleUseSubjectTokens": true, "clockSkewSeconds": bakCISkew,
		"allowedAudiences": []string{c.audience},
	}).want(t, "register the issuer "+c.issuer, 201)
	for i, m := range []struct {
		project  string
		priority int
		policies []string
	}{
		{bakCIProjectR, 5, c.policies[:1]},
		{bakCIProjectP, 10, c.policies[:1]},
		{bakCIProjectP, 20, c.policies},
		{bakCIProjectQ, 30, c.policies[:1]},
	} {
		adm.do(http.MethodPost, c.mappingsPath(), map[string]any{
			"name": c.mappings[i], "priority": m.priority, "maxTtlSeconds": 600,
			"match":  map[string]any{"all": []any{map[string]any{"claim": "project_id", "equals": m.project}}},
			"grants": map[string]any{"policies": m.policies},
		}).want(t, "create the mapping "+c.mappings[i], 201)
	}
	return c
}

func (c *bakCI) mappingsPath() string { return "/api/v1/ci/issuers/" + c.issuer + "/mappings" }

// token mints a subject token of a job of the project, with the audience of the installation.
func (c *bakCI) token(t *testing.T, project string) string {
	t.Helper()
	return c.dbl.mint(t, map[string]any{"aud": c.audience, "project_id": project, "job_project_id": project}, nil)
}

// disable disables a mapping the way an administrator does: the full representation again with enabled false (there is no PATCH,
// R-CI-019).
func (c *bakCI) disable(t *testing.T, adm *api, name string) {
	t.Helper()
	g := adm.get(c.mappingsPath()+"/"+name).want(t, "read the mapping "+name, 200)
	var body map[string]any
	g.JSON(t, &body)
	body["enabled"] = false
	adm.do(http.MethodPut, c.mappingsPath()+"/"+name, body, "If-Match", g.Header.Get("ETag")).want(t, "disable the mapping "+name, 200)
}

// enabled reads which mappings of the issuer are enabled, by name.
func (c *bakCI) enabled(t *testing.T, a *api) map[string]bool {
	t.Helper()
	var l struct {
		Items []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"items"`
	}
	a.get(c.mappingsPath()).want(t, "list the mappings of "+c.issuer, 200).JSON(t, &l)
	out := map[string]bool{}
	for _, m := range l.Items {
		out[m.Name] = m.Enabled
	}
	return out
}

// ciExchangeAt posts the subject token to the token exchange endpoint of the installation a talks to, without a credential of its own
// (the form of (*ciFixture).exchange).
func ciExchangeAt(t testing.TB, a *api, idToken string) reply {
	t.Helper()
	form := "grant_type=" + formEscape("urn:ietf:params:oauth:grant-type:token-exchange") +
		"&subject_token_type=" + formEscape("urn:ietf:params:oauth:token-type:id_token") + "&subject_token=" + formEscape(idToken)
	r, err := a.withToken("").send(http.MethodPost, "/api/v1/auth/oidc/token", form, "Content-Type", "application/x-www-form-urlencoded")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	return r
}

// ciOAuthError reads the error code and the reason of an exchange that was refused.
func ciOAuthError(t testing.TB, r reply) (code, reason string) {
	t.Helper()
	var e struct {
		Error  string `json:"error"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(r.Body, &e); err != nil {
		t.Fatalf("the exchange answered %s, which is no OAuth error: %v", r, err)
	}
	return e.Error, e.Reason
}

// ciGranted reads the mappings an exchange that succeeded names.
func ciGranted(t testing.TB, r reply) []string {
	t.Helper()
	var g struct {
		Token    string   `json:"access_token"`
		Mappings []string `json:"mappings"`
	}
	if r.Status != 200 {
		t.Fatalf("the exchange answered %s, want 200", r)
	}
	if err := json.Unmarshal(r.Body, &g); err != nil || !strings.HasPrefix(g.Token, "trv_cit_") {
		t.Fatalf("the exchange's answer is no token: %v %s", err, r)
	}
	return g.Mappings
}

// replayLine returns the outcome the restore's output gives the subject (a mapping's principal name) and the event, "" if the output has
// no such line. A line is `"<event>" "<subject>" <id> <outcome>` (journal.Result.Line; event and subject are quoted).
func replayLine(logs, event, subject string) string {
	for _, l := range strings.Split(logs, "\n") {
		f := strings.Fields(l)
		if len(f) >= 4 && strings.Trim(f[len(f)-4], `"`) == event && strings.Trim(f[len(f)-3], `"`) == subject {
			return f[len(f)-1]
		}
	}
	return ""
}

// bakCIWaitPastSkew waits until the restored installation's clock is more than the issuer's skew and a second past restored_at: from then
// on a token minted now is not refused as one that predates the restore (R-CI-018).
func bakCIWaitPastSkew(t *testing.T) {
	t.Helper()
	waitFor(t, "the restored installation's clock to pass restored_at plus the clock skew", 2*time.Minute, time.Second, func() bool {
		return krSQL(t, fmt.Sprintf(`SELECT coalesce((SELECT extract(epoch FROM now() - (value #>> '{}')::timestamptz) > %d
			FROM trove.system_state WHERE key = 'restored_at'), false)`, bakCISkew+1)) == "t"
	})
}
