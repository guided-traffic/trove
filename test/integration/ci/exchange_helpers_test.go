//go:build integration

package ci

// The helpers of the exchange tests (work package W4): an installation with the repositories of the reference
// pipeline, an issuer of the issuer double, the reference mappings, the requests of RFC 8693 and the check that no
// answer and no log line holds a subject token or a CI token (R-CI-008).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/ci"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/oidcstub"
)

const (
	exchangePath = "/api/v1/auth/oidc/token"
	// troveAudience is the base URL of the test installation (testenv), the default audience of an issuer.
	troveAudience = "http://trove.test"
	xIssuer       = "gitlab-main"
	formType      = "application/x-www-form-urlencoded"
)

// xenv is an installation for the exchange: npm-internal (ownership enforced), npm-all (virtual over npm-internal,
// deploying to it), nuget-internal, the policy ci-read-internal over npm-all and npm-internal, and the issuer double
// whose tokens carry trove's audience. Every subject token the test sends is remembered, and the cleanup checks that
// no error answer and no log line of a replica holds one or a trv_cit_ value (R-CI-008).
type xenv struct {
	*env
	d *double

	mu      sync.Mutex
	sent    []string
	answers []testenv.Answer
	reps    []*testenv.Replica
}

func newXEnv(t *testing.T, o options) *xenv {
	t.Helper()
	// The tests choose their client address through X-Forwarded-For (R-ARCH-006), so the per-address limiter of
	// one test does not share a bucket with the requests of another part of it.
	o.env = append([]string{`TROVE_SERVER_TRUSTED_PROXIES=["127.0.0.0/8"]`}, o.env...)
	e := newEnv(t, o)
	st := e.stack
	st.Repository("npm-internal", "local", "npm", `{"local":{"packageOwnership":"enforce"}}`)
	st.Repository("npm-all", "virtual", "npm", "")
	e.exec(`INSERT INTO virtual_members (virtual_id, position, member_id) SELECT v.id, 0, m.id FROM repositories v, repositories m WHERE v.key = 'npm-all' AND m.key = 'npm-internal'`)
	e.exec(`INSERT INTO virtual_deploy_targets (virtual_id, target_id) SELECT v.id, m.id FROM repositories v, repositories m WHERE v.key = 'npm-all' AND m.key = 'npm-internal'`)
	st.Repository("nuget-internal", "local", "nuget", `{"local":{"packageOwnership":"enforce"}}`)
	st.Policy("ci-read-internal", nil, []string{"npm-all", "npm-internal"})
	x := &xenv{env: e, d: startDouble(t, oidcstub.Config{Audience: troveAudience})}
	t.Cleanup(func() { x.checkNoSecrets(t) })
	return x
}

func (x *xenv) web(name string, env ...string) *testenv.Replica {
	x.t.Helper()
	r := x.stack.Start(x.t, testenv.ReplicaOptions{Name: name, Role: "web", Env: env})
	x.mu.Lock()
	x.reps = append(x.reps, r)
	x.mu.Unlock()
	return r
}

// issuer creates the issuer of the double through a replica; its keys are fetched at the create.
func (x *xenv) issuer(r *testenv.Replica, extra ...map[string]any) {
	x.t.Helper()
	wantStatus(x.t, x.do(r, "POST", issuersPath, x.d.body(xIssuer, extra...)), 201, "")
}

// addMapping creates a mapping of the issuer through a replica.
func (x *xenv) addMapping(r *testenv.Replica, body map[string]any) {
	x.t.Helper()
	wantStatus(x.t, x.do(r, "POST", mappingsOf(xIssuer), body), 201, "")
	x.settled()
}

// settled waits until every replica's grant index holds the authorization epoch of the database: a mapping save
// writes the mapping into its policies' principal lists (Q-CI-35), and the exchange answers the policy grants from
// the replica's index as every request does (R-AUTHZ-028), so a test that exchanges at once waits for it.
func (x *xenv) settled() {
	x.t.Helper()
	want := x.epoch(cluster.AreaAuthz)
	x.mu.Lock()
	reps := append([]*testenv.Replica(nil), x.reps...)
	x.mu.Unlock()
	for _, r := range reps {
		e, _ := r.App.Authorizer().(*authz.Engine)
		if e == nil {
			x.t.Fatalf("%s has no grant index", r.Name)
		}
		within(x.t, r.Name+"'s grant index at epoch "+fmt.Sprint(want), 10*time.Second, func() bool { return e.Epoch() >= want })
	}
}

// releaseCondition matches the double's default claims: a protected tag of namespace 4711 pushed or run by hand.
func releaseCondition() map[string]any {
	return all(eq("namespace_id", "4711"), eq("ref_type", "tag"), eq("ref_protected", true),
		map[string]any{"claim": "pipeline_source", "in": []string{"push", "web"}})
}

// releaseMapping is the reference mapping of the specification's examples for the double's default claims: the
// policy ci-read-internal and read and deploy of the namespace's own npm scope in npm-internal (bind_project).
func releaseMapping(name string, priority int, extra ...map[string]any) map[string]any {
	return mapping(name, priority, releaseCondition(), map[string]any{
		"policies": []string{"ci-read-internal"},
		"inline":   []any{inline("npm-internal", []string{"@{{ namespace_path | slug }}/*"}, "read", "deploy")},
	}, extra...)
}

// mint mints a subject token of the double and remembers it for the secrets check.
func (x *xenv) mint(o oidcstub.MintOptions) string {
	x.t.Helper()
	tok := mint(x.t, x.d, o)
	x.remember(tok)
	return tok
}

func (x *xenv) remember(tok string) {
	x.mu.Lock()
	x.sent = append(x.sent, tok)
	x.mu.Unlock()
}

// form is the RFC 8693 request of a subject token; kv sets further parameters.
func form(subject string, kv ...string) url.Values {
	v := url.Values{
		"grant_type": {ci.GrantTypeTokenExchange}, "subject_token_type": {ci.TokenTypeIDToken}, "subject_token": {subject},
	}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

// post sends a body to the exchange of a replica; the answer is validated against the API document by the stack.
func (x *xenv) post(r *testenv.Replica, contentType string, body []byte, hdr ...string) testenv.Answer {
	x.t.Helper()
	a := x.stack.Do(r, "POST", exchangePath, "", body, append([]string{"Content-Type", contentType}, hdr...)...)
	x.mu.Lock()
	x.answers = append(x.answers, a)
	x.mu.Unlock()
	return a
}

// exchange sends the form request of a subject token.
func (x *xenv) exchange(r *testenv.Replica, subject string, kv ...string) testenv.Answer {
	x.t.Helper()
	x.remember(subject)
	return x.post(r, formType, []byte(form(subject, kv...).Encode()))
}

// exchangeFrom is exchange from a client address (X-Forwarded-For through the trusted loopback proxy).
func (x *xenv) exchangeFrom(r *testenv.Replica, addr, subject string, kv ...string) testenv.Answer {
	x.t.Helper()
	x.remember(subject)
	return x.post(r, formType, []byte(form(subject, kv...).Encode()), "X-Forwarded-For", addr)
}

// rawExchange is one exchange without the stack's helpers, safe for concurrent use: the status and the
// Retry-After of the answer. A transport failure is reported with Errorf, never Fatal, so a goroutine may call it.
func rawExchange(t *testing.T, r *testenv.Replica, addr, subject string) (int, string) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+r.Addr("default")+exchangePath,
		strings.NewReader(form(subject).Encode()))
	if err != nil {
		t.Errorf("request: %v", err)
		return 0, ""
	}
	req.Host = testenv.DefaultHost
	req.Header.Set("Content-Type", formType)
	if addr != "" {
		req.Header.Set("X-Forwarded-For", addr)
	}
	res, err := testenv.Client.Do(req)
	if err != nil {
		t.Errorf("exchange: %v", err)
		return 0, ""
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if bytes.Contains(body, []byte(subject)) || bytes.Contains(body, []byte("trv_cit_")) && res.StatusCode != 200 {
		t.Errorf("an answer %d holds token material: %s", res.StatusCode, body)
	}
	return res.StatusCode, res.Header.Get("Retry-After")
}

// wantOAuth checks an OAuth error answer (R-CI-008): status, `error`, `reason`, requestId equal to X-Request-Id, the
// cache headers.
func wantOAuth(t *testing.T, a testenv.Answer, status int, oauthError, reason string) {
	t.Helper()
	if a.Status != status {
		t.Fatalf("answered %d, want %d %s %s\n%s", a.Status, status, oauthError, reason, a.Body)
	}
	m := a.JSON(t)
	if m["error"] != oauthError || (reason != "" && m["reason"] != reason) {
		t.Fatalf("answered %v/%v, want %s/%s\n%s", m["error"], m["reason"], oauthError, reason, a.Body)
	}
	if id := a.Header.Get("X-Request-Id"); id == "" || m["requestId"] != id {
		t.Errorf("requestId %v, X-Request-Id %q", m["requestId"], id)
	}
	if a.Header.Get("Cache-Control") != "no-store" || a.Header.Get("Pragma") != "no-cache" {
		t.Errorf("cache headers %q %q", a.Header.Get("Cache-Control"), a.Header.Get("Pragma"))
	}
	if ct := a.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type %q", ct)
	}
}

// granted is a successful exchange's answer.
type granted struct {
	Token     string   `json:"access_token"`
	Issued    string   `json:"issued_token_type"`
	Type      string   `json:"token_type"`
	ExpiresIn int64    `json:"expires_in"`
	Scope     string   `json:"scope"`
	Issuer    string   `json:"ci_issuer"`
	Mappings  []string `json:"mappings"`
}

func wantToken(t *testing.T, a testenv.Answer) granted {
	t.Helper()
	if a.Status != 200 {
		t.Fatalf("answered %d, want 200\n%s", a.Status, a.Body)
	}
	var g granted
	if err := json.Unmarshal(a.Body, &g); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(g.Token, "trv_cit_") || g.Issued != ci.TokenTypeAccessToken || g.Type != "Bearer" || g.ExpiresIn <= 0 {
		t.Fatalf("the answer %+v", g)
	}
	if a.Header.Get("Cache-Control") != "no-store" || a.Header.Get("Pragma") != "no-cache" {
		t.Errorf("cache headers %q %q", a.Header.Get("Cache-Control"), a.Header.Get("Pragma"))
	}
	return g
}

// citValue is a CI token value: the prefix and the token's own characters. A token hint (trv_cit_…ab12, R-AUTH-022)
// names a token without being one and does not match.
var citValue = regexp.MustCompile(`trv_cit_[A-Za-z0-9]{8,}`)

// checkNoSecrets is R-CI-008's grep: no error answer and no log line of a replica holds a subject token the test
// sent or a trv_cit_ value; a 200 holds its own CI token and no subject token, and no log line holds a CI token an
// answer issued.
func (x *xenv) checkNoSecrets(t *testing.T) {
	x.mu.Lock()
	defer x.mu.Unlock()
	var issued []string
	for _, a := range x.answers {
		for _, tok := range x.sent {
			if len(tok) > 16 && bytes.Contains(a.Body, []byte(tok)) {
				t.Errorf("an answer %d holds a subject token", a.Status)
			}
		}
		if a.Status != 200 && citValue.Match(a.Body) {
			t.Errorf("an answer %d holds a trv_cit_ value", a.Status)
		}
		if a.Status == 200 {
			var g granted
			if json.Unmarshal(a.Body, &g) == nil && g.Token != "" {
				issued = append(issued, g.Token)
			}
		}
	}
	for _, r := range x.reps {
		logs := r.Logs.String()
		for _, tok := range x.sent {
			// A token's signature part is the part no other text shares; the whole value would pass a grep that a
			// log line cut short.
			parts := strings.Split(tok, ".")
			if len(tok) > 16 && strings.Contains(logs, parts[len(parts)-1]) {
				t.Errorf("the log of %s holds a subject token", r.Name)
			}
		}
		for _, tok := range issued {
			if strings.Contains(logs, tok[len("trv_cit_"):]) {
				t.Errorf("the log of %s holds an issued CI token", r.Name)
			}
		}
		if citValue.MatchString(logs) {
			t.Errorf("the log of %s holds a trv_cit_ value", r.Name)
		}
	}
}

// metric reads a series of a replica's /metrics; 0 when absent.
func metric(t *testing.T, r *testenv.Replica, series string) float64 {
	t.Helper()
	v := testenv.MetricValue(r.Metrics(t), series)
	if v < 0 {
		return 0
	}
	return v
}

// exchanges is the series of trove_ci_exchanges_total.
func exchanges(issuer, outcome, reason string) string {
	return fmt.Sprintf(`trove_ci_exchanges_total{issuer=%q,outcome=%q,reason=%q}`, issuer, outcome, reason)
}

// waitEvents waits until the audit buffer wrote n events of an action.
func (x *xenv) waitEvents(action string, n int64) {
	x.t.Helper()
	within(x.t, fmt.Sprintf("%d events %s", n, action), 10*time.Second, func() bool { return x.events(action) >= n })
}
