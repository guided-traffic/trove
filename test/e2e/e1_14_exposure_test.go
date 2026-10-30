//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/token"
)

// E1.14: exposure on a customer endpoint. The endpoint `customers` (listener :8443, host packages.trove.localhost,
// its own Ingress; deploy/dev/values-dev.yaml) serves the surface packages, files and api, accepts only
// service-account tokens bound to it, no anonymous request, and exposes what its rules say (exposure: rules).

const (
	custHost = "packages.trove.localhost"
	appZip1  = "/files/generic-local/releases/1.0/app.zip"
	appZip2  = "/files/generic-local/releases/2.0/app.zip"
	internal = "/files/generic-local/internal/x.bin"
)

// customersAPI is a client of the endpoint through the edge (its own Ingress).
func customersAPI(t testing.TB, token string) *api {
	t.Helper()
	a := newAPI(t)
	a.base = "https://" + custHost + ":" + edgePort(t)
	a.token = token
	return a
}

// customersOnPods returns one client per web pod that talks to that pod's listener of the endpoint directly
// (port-forward to :8443), so a request provably reaches the replica and not another one.
func customersOnPods(t testing.TB, token string) ([]*api, func()) {
	t.Helper()
	addrs, stop := podForwardsPort(t, 8443)
	var out []*api
	for _, a := range addrs {
		c := customersAPI(t, token)
		c.rt.rr = []string{a}
		out = append(out, c)
	}
	return out, stop
}

// problemOf is the problem+json of an answer without the members that differ per request.
func problemOf(t testing.TB, r reply) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("no problem body in %s", r)
	}
	delete(m, "instance")
	delete(m, "requestId")
	b, _ := json.Marshal(m)
	return string(b)
}

func clearRules(t testing.TB, adm *api) {
	t.Helper()
	items, _ := walkRetry(t, adm, "/api/v1/endpoints/customers/exposure-rules", 100)
	for _, n := range items {
		g := adm.get("/api/v1/endpoints/customers/exposure-rules/" + n)
		if g.Status == 200 {
			adm.do(http.MethodDelete, "/api/v1/endpoints/customers/exposure-rules/"+n, nil, "If-Match", g.Header.Get("ETag")).want(t, "delete rule "+n, 204)
		}
	}
}

// auditCount counts the events of an action for a rule of the endpoint customers.
func auditCount(t testing.TB, adm *api, action, rule string) int {
	t.Helper()
	n := 0
	cursor := ""
	for {
		u := "/api/v1/audit/events?limit=500&action=" + action
		if cursor != "" {
			u += "&cursor=" + urlEscape(cursor)
		}
		r := adm.get(u).want(t, "audit events "+action, 200)
		var p struct {
			Items []struct {
				Details struct{ Name, Endpoint string } `json:"details"`
			}
			NextCursor *string `json:"nextCursor"`
		}
		r.JSON(t, &p)
		for _, e := range p.Items {
			if e.Details.Name == rule && e.Details.Endpoint == "customers" {
				n++
			}
		}
		if p.NextCursor == nil || *p.NextCursor == "" {
			return n
		}
		cursor = *p.NextCursor
	}
}

// metricAcrossPods is a series summed over the web pods.
func seriesNow(t testing.TB, prefix string, parts ...string) float64 {
	return sumSeries(t, prefix, parts...)
}

// ensureRule creates the rule `releases` (paths releases/**) and waits until every web pod serves the exposed file: a
// rule reaches the other replicas within the bound of R-AUTHZ-028, so one pod answering is not the cluster answering.
func ensureRule(t testing.TB, adm *api, token string) {
	t.Helper()
	adm.do(http.MethodPost, "/api/v1/endpoints/customers/exposure-rules", map[string]any{
		"name": "releases", "repository": "generic-local", "paths": map[string]any{"include": []string{"releases/**"}},
	}).want(t, "create the rule", 201, 409)
	pods, stop := customersOnPods(t, token)
	defer stop()
	for i, c := range pods {
		waitFor(t, fmt.Sprintf("the rule on replica %d", i), 10*time.Second, 50*time.Millisecond, func() bool { return c.get(appZip1).Status == 200 })
	}
}

func TestE1_14Exposure(t *testing.T) {
	useScatter(t)
	adm := adminAPI(t)
	ensureRepo(t, adm, "generic-local")
	ensureSA(t, adm, "e2e-cust")
	// e2e-cust may read three more repositories than the rules expose, so the listing under exposure shows a difference.
	for i := 1; i <= 3; i++ {
		ensureRepo(t, adm, fmt.Sprintf("e2e-list-%d", i))
	}
	ensurePolicy(t, adm, "e2e-cust-read", []string{"generic-local", "e2e-list-*"}, [2]any{"sa:e2e-cust", []string{"read"}})
	adm.do(http.MethodDelete, "/api/v1/service-accounts/e2e-cust/tokens", nil)
	clearRules(t, adm)
	t.Cleanup(func() { clearRules(t, adm) })

	nonce := storeNonce(t) // see storeNonce: the bytes depend on the object store instance
	for path, body := range map[string]string{appZip1: "app 1.0 " + nonce, appZip2: "app 2.0 " + nonce, internal: "internal only " + nonce} {
		adm.do(http.MethodPut, path, body).want(t, "upload "+path, 200, 201)
	}

	boundSAT := newSAToken(t, adm, "e2e-cust", 3600, map[string]any{"inherit": true, "endpoints": []string{"customers"}})
	unbound := newSAToken(t, adm, "e2e-cust", 3600, map[string]any{"inherit": true})
	both := newSAToken(t, adm, "e2e-cust", 3600, map[string]any{"inherit": true, "endpoints": []string{"customers", "default"}})
	boundAdmin := newSAToken(t, adm, "e2e-admin", 3600, map[string]any{"inherit": true, "admin": true, "endpoints": []string{"customers"}})
	cust := customersAPI(t, boundSAT.Token)

	t.Run("R-EP-012 R-EP-013 E1.14 an endpoint with exposure rules and no rule exposes nothing, writes are refused as read-only", func(t *testing.T) {
		cust.get(appZip1).want(t, "no rule", 404)
		w := cust.do(http.MethodPut, appZip1, "overwrite")
		if w.Status != 403 || w.Code() != "endpoint_read_only" {
			t.Errorf("a write through the endpoint = %s, want 403 endpoint_read_only", w)
		}
		dim := adm.do(http.MethodPost, "/api/v1/endpoints/customers/exposure-rules", map[string]any{
			"name": "bad", "repository": "generic-local", "packages": map[string]any{"include": []string{"@acme/*"}},
		})
		if dim.Status != 422 || dim.Code() != "validation_failed" || dim.FieldCode() != "dimension_not_supported" {
			t.Errorf("a packages rule on a generic repository = %s, want 422 validation_failed with dimension_not_supported", dim)
		}
	})

	// The rule lifecycle on every replica, within the bound of R-AUTHZ-028: 1 s with Valkey, epoch_poll_interval
	// (1 s) plus 1 s without; the tests allow one more second for the poll and the request.
	lifecycle := func(t *testing.T, within time.Duration, label string) {
		pods, stop := customersOnPods(t, boundSAT.Token)
		defer stop()
		reach := func(what string, within time.Duration, want func(c *api) bool) {
			t.Helper()
			for i, c := range pods {
				start := time.Now()
				for !want(c) {
					if time.Since(start) > within {
						t.Fatalf("%s: replica %d did not show %q within %s", label, i, what, within)
					}
					time.Sleep(50 * time.Millisecond)
				}
				t.Logf("%s: %s on replica %d after %s", label, what, i, time.Since(start).Round(10*time.Millisecond))
			}
		}
		status := func(path string) func(c *api) bool {
			return func(c *api) bool { r := c.get(path); return r.Status == 200 }
		}
		hidden := func(path string) func(c *api) bool {
			return func(c *api) bool { r := c.get(path); return r.Status == 404 }
		}
		created0 := auditCount(t, adm, "endpoint.exposure.created", "releases")
		updated0 := auditCount(t, adm, "endpoint.exposure.updated", "releases")
		deleted0 := auditCount(t, adm, "endpoint.exposure.deleted", "releases")

		adm.do(http.MethodPost, "/api/v1/endpoints/customers/exposure-rules", map[string]any{
			"name": "releases", "repository": "generic-local", "paths": map[string]any{"include": []string{"releases/**"}},
		}).want(t, "create the rule", 201)
		reach("releases/1.0/app.zip downloadable", within, status(appZip1))
		reach("releases/2.0/app.zip downloadable", within, status(appZip2))
		for i, c := range pods {
			if r := c.get(internal); r.Status != 404 {
				t.Errorf("%s: internal/x.bin on replica %d = %s, want 404", label, i, r)
			}
		}

		g := adm.get("/api/v1/endpoints/customers/exposure-rules/releases").want(t, "read the rule", 200)
		adm.do(http.MethodPatch, "/api/v1/endpoints/customers/exposure-rules/releases", `{"paths":{"include":["releases/2.0/**"]}}`,
			"Content-Type", "application/merge-patch+json", "If-Match", g.Header.Get("ETag")).want(t, "narrow the rule", 200)
		reach("releases/1.0/app.zip hidden after the rule was narrowed", within, hidden(appZip1))
		reach("releases/2.0/app.zip still downloadable", within, status(appZip2))

		g = adm.get("/api/v1/endpoints/customers/exposure-rules/releases").want(t, "read the rule", 200)
		adm.do(http.MethodDelete, "/api/v1/endpoints/customers/exposure-rules/releases", nil, "If-Match", g.Header.Get("ETag")).want(t, "delete the rule", 204)
		reach("releases/2.0/app.zip hidden after the rule was deleted", within, hidden(appZip2))

		for action, before := range map[string]int{"endpoint.exposure.created": created0, "endpoint.exposure.updated": updated0, "endpoint.exposure.deleted": deleted0} {
			var after int
			waitFor(t, "the audit event "+action, 20*time.Second, time.Second, func() bool {
				after = auditCount(t, adm, action, "releases")
				return after > before
			})
			if after != before+1 {
				t.Errorf("%s: %d events of %s after one change, want %d", label, after-before, action, 1)
			}
		}
	}

	t.Run("R-EP-010 R-EP-011 R-EP-013 R-EP-014 E1.14 a rule makes releases/1.0/app.zip downloadable with a bound token while internal/x.bin answers the 404 of a missing file", func(t *testing.T) {
		ensureRule(t, adm, boundSAT.Token)

		got := cust.get(appZip1).want(t, "the exposed file", 200)
		if string(got.Body) != "app 1.0 "+nonce {
			t.Errorf("body %q", got.Body)
		}
		hiddenR := cust.get(internal)
		if hiddenR.Status != 404 {
			t.Fatalf("internal/x.bin = %s, want 404", hiddenR)
		}
		for _, p := range []string{"/files/generic-local/releases/9.9/none.zip", "/files/generic-local/internal/none.bin", "/files/ghost/releases/1.0/app.zip", "/files/generic-local/zzz"} {
			m := cust.get(p)
			if m.Status != 404 || problemOf(t, m) != problemOf(t, hiddenR) {
				t.Errorf("%s = %s, want the bytes of %s", p, m, hiddenR)
			}
			for _, h := range []string{"Content-Type", "Content-Length", "Cache-Control"} {
				if m.Header.Get(h) != hiddenR.Header.Get(h) && h != "Content-Length" {
					t.Errorf("%s: header %s %q differs from the hidden file's %q", p, h, m.Header.Get(h), hiddenR.Header.Get(h))
				}
			}
		}

		// the same bound token on the default endpoint, and credentials the endpoint does not accept
		onDefault := newAPI(t).withToken(boundSAT.Token)
		for _, p := range []string{"/api/v1/auth/whoami", appZip1} {
			if r := onDefault.get(p); r.Status != 401 || r.Code() != "invalid_token" {
				t.Errorf("the bound token on the default endpoint, %s = %s, want 401 invalid_token", p, r)
			}
		}

		pw := secretValue(t, "trove-keycloak", "alice-password")
		b := newBrowser(t)
		b.oidcLogin("alice", pw)
		pat := b.do(http.MethodPost, "/api/v1/tokens", map[string]any{"description": "e2e customers", "ttlSeconds": 600, "scope": map[string]any{"inherit": true}}).want(t, "alice's PAT", 201)
		var pc createdToken
		pat.JSON(t, &pc)
		t.Cleanup(func() { b.do(http.MethodDelete, "/api/v1/tokens/"+pc.ID, nil) })

		// "No query of the token table": the table is locked (ACCESS EXCLUSIVE, in a transaction that sleeps) and
		// PATs are sent to customers. A lookup would wait for the lock; the answers are immediate. The control is an
		// unknown PAT on the default endpoint, which needs the table and waits. (The statistics views are no
		// evidence: an idle backend flushes its counters up to a minute late.)
		const n = 40
		randomPAT := func() string { raw, _ := token.Generate(token.TypePAT); return raw }
		lockDone := make(chan error, 1)
		go func() {
			_, err := kubectl("-n", cfg.namespace, "exec", pgPrimary(t), "-c", "postgres", "--", "psql", "-U", "postgres", "-d", "trove", "-At", "-c",
				"BEGIN; LOCK TABLE trove.api_token IN ACCESS EXCLUSIVE MODE; SELECT pg_sleep(14); COMMIT;")
			lockDone <- err
		}()
		waitFor(t, "the lock on api_token", 20*time.Second, 200*time.Millisecond, func() bool {
			return sql(t, `SELECT count(*) FROM pg_locks l JOIN pg_class c ON c.oid = l.relation WHERE c.relname = 'api_token' AND l.mode = 'AccessExclusiveLock' AND l.granted`) == "1"
		})
		lockedAt := time.Now()
		control := make(chan reply, 1)
		go func() {
			r, err := newAPI(t).withToken(randomPAT()).send(http.MethodGet, "/api/v1/auth/whoami", nil)
			if err != nil {
				r = reply{Status: 0, Dur: time.Since(lockedAt), Body: []byte(err.Error())}
			}
			control <- r
		}()
		refused0 := seriesNow(t, "trove_endpoint_credentials_refused_total{", `endpoint="customers"`, `reason="token_type"`)
		slowest := time.Duration(0)
		for i := 0; i <= n; i++ {
			tok := pc.Token
			if i < n {
				tok = randomPAT()
			}
			r := customersAPI(t, tok).get(appZip1)
			if r.Status != 401 || r.Code() != "invalid_token" {
				t.Errorf("a PAT on customers = %s, want 401 invalid_token", r)
			}
			slowest = max(slowest, r.Dur)
		}
		t.Logf("%d PATs on customers while api_token is locked: all answered, the slowest in %s", n+1, slowest.Round(time.Millisecond))
		if slowest > 2*time.Second {
			t.Errorf("a PAT on customers took %s with api_token locked: the endpoint looked the token up", slowest)
		}
		if d := seriesNow(t, "trove_endpoint_credentials_refused_total{", `endpoint="customers"`, `reason="token_type"`) - refused0; d != float64(n+1) {
			t.Errorf("trove_endpoint_credentials_refused_total{reason=token_type} rose by %v, want %d", d, n+1)
		}
		select {
		case c := <-control:
			t.Logf("the control, an unknown PAT on the default endpoint, was answered after %s: %d", c.Dur.Round(100*time.Millisecond), c.Status)
			if c.Dur < 3*time.Second {
				t.Errorf("the control request was answered after %s: the lock did not hold the token table (%s)", c.Dur, c)
			}
		case <-time.After(60 * time.Second):
			t.Error("the control request never ended")
		}
		if err := <-lockDone; err != nil {
			t.Errorf("the locking session: %v", err)
		}

		binding0 := seriesNow(t, "trove_endpoint_credentials_refused_total{", `endpoint="customers"`, `reason="token_binding"`)
		if r := customersAPI(t, unbound.Token).get(appZip1); r.Status != 401 || r.Code() != "invalid_token" {
			t.Errorf("an unbound SAT on customers = %s, want 401 invalid_token", r)
		}
		if d := seriesNow(t, "trove_endpoint_credentials_refused_total{", `endpoint="customers"`, `reason="token_binding"`) - binding0; d != 1 {
			t.Errorf("reason=token_binding rose by %v, want 1", d)
		}
		if r := customersAPI(t, "").get(appZip1); r.Status != 401 || r.Code() != "credentials_required" {
			t.Errorf("no credential on customers = %s, want 401 credentials_required", r)
		}
		// the session cookie is ignored on an endpoint without ui (R-EP-010 item 1)
		sess := b.cookie("__Host-trv_session")
		if r := customersAPI(t, "").get(appZip1, "Cookie", "__Host-trv_session="+sess); r.Status != 401 || r.Code() != "credentials_required" {
			t.Errorf("a valid session cookie on customers = %s, want 401 credentials_required", r)
		}
		// a token bound to both endpoints reads on both
		if r := customersAPI(t, both.Token).get(appZip1); r.Status != 200 {
			t.Errorf("a token bound to customers and default on customers = %s", r)
		}
		if r := newAPI(t).withToken(both.Token).get(appZip1); r.Status != 200 {
			t.Errorf("a token bound to customers and default on the default endpoint = %s", r)
		}
	})

	t.Run("R-EP-010 R-EP-011 R-EP-013 R-EP-014 E1.14 an administrator token bound to customers sees no more than the rules expose", func(t *testing.T) {
		a := customersAPI(t, boundAdmin.Token)
		a.get(appZip1).want(t, "exposed file with the bound administrator token", 200)
		if r := a.get(internal); r.Status != 404 {
			t.Errorf("internal/x.bin with an administrator token bound to customers = %s, want 404", r)
		}
		if r := a.do(http.MethodPut, appZip1, "x"); r.Status != 403 || r.Code() != "endpoint_read_only" {
			t.Errorf("a write with the bound administrator token = %s, want 403 endpoint_read_only", r)
		}
		// The listing of the management API under exposure (R-EP-014): a token that may read four repositories sees
		// the one with an enabled rule on customers and all four on the default endpoint. (The configuration list of an
		// administrator is a capability operation, not content, and is not narrowed.)
		repos, _ := walkRetry(t, cust, "/api/v1/repositories", 100)
		if len(repos) != 1 || repos[0] != "generic-local" {
			t.Errorf("GET /api/v1/repositories on customers with a token that reads four repositories lists %v, want [generic-local]", repos)
		}
		all, _ := walkRetry(t, newAPI(t).withToken(both.Token), "/api/v1/repositories", 100)
		if len(all) < 4 {
			t.Errorf("the default endpoint lists %v: the comparison needs more repositories than the rules expose", all)
		}
		if r := a.get("/api/v1/repositories/generic-local/items?path=internal/x.bin"); r.Status != 404 {
			t.Errorf("GET items of internal/x.bin with the bound administrator token = %s, want 404", r)
		}
		// Capabilities of the administrator stay what the endpoint's credentials allow: the users list is not content.
		if r := a.get("/api/v1/users?limit=1"); r.Status != 200 {
			t.Logf("GET /api/v1/users on customers with the bound administrator token = %d (a capability operation, R-EP-014)", r.Status)
		}
		children := a.get("/api/v1/repositories/generic-local/children?path=").want(t, "children of the root", 200)
		if strings.Contains(string(children.Body), "internal") {
			t.Errorf("the listing of generic-local on customers shows the folder internal: %s", children.Body)
		}
	})

	t.Run("R-EP-012 R-EP-016 R-EP-017 R-AUTHZ-028 E1.14 a rule created, narrowed and deleted through the API takes effect on every replica within the bound, and each change is an audit event", func(t *testing.T) {
		adm.do(http.MethodDelete, "/api/v1/endpoints/customers/exposure-rules/releases", nil, "If-Match", "*")
		lifecycle(t, 3*time.Second, "Valkey up")
	})

	t.Run("R-EP-012 R-EP-017 R-AUTHZ-028 E1.14 the same with Valkey stopped: within epoch_poll_interval plus 1 s", func(t *testing.T) {
		withValkeyStopped(t, func() {
			lifecycle(t, 4*time.Second, "Valkey stopped")
		})
	})

	t.Run("R-EP-015 E1.14 the endpoint's rate-limit class: 429 with Retry-After after the burst, a second token and another endpoint are not slowed", func(t *testing.T) {
		ensureRule(t, adm, boundSAT.Token)

		a := customersAPI(t, both.Token)
		rs, dur := blast(a, 400, 32, func(int) (string, string, any, []string) { return http.MethodGet, appZip1, nil, nil })
		st := countStatus(rs)
		t.Logf("400 reads on customers with one token in %s: %v", dur.Round(time.Millisecond), st)
		if st[429] == 0 || st[200] > admittedBound(readBurst, readRate, dur) {
			t.Fatalf("400 reads: %v, want a 429 after the burst of 200", st)
		}
		for _, r := range rs {
			if r.Status == 429 {
				if secs, err := strconv.Atoi(r.Header.Get("Retry-After")); err != nil || secs < 1 || r.Code() != "rate_limited" || !strings.Contains(r.Header.Get("RateLimit-Policy"), `"endpoint:external"`) {
					t.Fatalf("a 429 of the endpoint class: Retry-After %q, code %q, policy %q", r.Header.Get("Retry-After"), r.Code(), r.Header.Get("RateLimit-Policy"))
				}
				break
			}
		}
		other := newToken2(t, adm, "customers")
		customersAPI(t, other).get(appZip1).want(t, "a second bound token right after", 200)
		// the same token on the default endpoint is not held by the class of customers
		r := newAPI(t).withToken(both.Token).get("/api/v1/auth/whoami")
		if r.Status != 200 && (r.Status != 429 || !strings.Contains(r.Header.Get("RateLimit-Policy"), `"read"`)) {
			t.Errorf("the token on the default endpoint = %s: the class of customers must not hold it", r)
		}
	})

	t.Run("R-EP-015 R-ARCH-034 E1.14 1000 requests with one token on customers within about 2 s are admitted at most burst plus decay times, also with Valkey stopped, and the fallback is 1 on every replica that serves customers", func(t *testing.T) {
		ensureRule(t, adm, boundSAT.Token)
		run := func(t *testing.T, label string) {
			tok := newToken2(t, adm, "customers")
			pods, stop := customersOnPods(t, tok)
			defer stop()
			addrs := make([]string, 0, len(pods))
			for _, p := range pods {
				addrs = append(addrs, p.rt.rr...)
			}
			rr := customersAPI(t, tok)
			rr.rt.rr = addrs
			rs, dur := blast(rr, 1000, 12, func(int) (string, string, any, []string) { return http.MethodGet, appZip1, nil, nil })
			st := countStatus(rs)
			t.Logf("%s: 1000 requests with one token on customers in %s over three replicas: %v", label, dur.Round(time.Millisecond), st)
			if st[200] > admittedBound(readBurst, readRate, dur) {
				t.Errorf("%s: %d admitted in %s, want at most %d", label, st[200], dur, admittedBound(readBurst, readRate, dur))
			}
			if st[200] < 3 {
				t.Errorf("%s: only %d admitted", label, st[200])
			}
		}
		t.Run("Valkey up", func(t *testing.T) { run(t, "Valkey up") })
		t.Run("Valkey stopped", func(t *testing.T) {
			withValkeyStopped(t, func() {
				run(t, "Valkey stopped")
				for _, p := range readyWebPods(t) {
					if v := metricsOf(t, p)[`trove_ratelimit_fallback{limiter="endpoint_external"}`]; v != 1 {
						t.Errorf("%s trove_ratelimit_fallback{limiter=endpoint_external} = %v, want 1", p, v)
					}
				}
			})
		})
	})
}

// newToken2 mints a token of e2e-cust bound to the endpoint.
func newToken2(t testing.TB, adm *api, endpoint string) string {
	t.Helper()
	return newSAToken(t, adm, "e2e-cust", 3600, map[string]any{"inherit": true, "endpoints": []string{endpoint}}).Token
}
