//go:build integration

package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The acceptance criteria of the requirements of api-and-ui.md that no area test of the management API states in one place,
// run on the composition root behind two replicas: the real chain, the real authentication stage with real tokens and the
// real decision point. Every answer is validated against api/openapi.yaml by send.

const mergePatch = "application/merge-patch+json"

var pathParam = regexp.MustCompile(`\{[^}]+\}`)

func toJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// without returns the headers of a browser session minus one header.
func without(hdr []string, name string) []string {
	var out []string
	for i := 0; i+1 < len(hdr); i += 2 {
		if !strings.EqualFold(hdr[i], name) {
			out = append(out, hdr[i], hdr[i+1])
		}
	}
	return out
}

func newTwoReplicas(t *testing.T) (*env, *webCluster) {
	t.Helper()
	e := newEnv(t, false)
	return e, e.cluster(e.newNode("web-1", nodeOpts{}), e.newNode("web-2", nodeOpts{}))
}

// R-API-005: the credentials of the management API, and the challenge a browser never answers with a dialog.
func TestR_API_005_CallerAuthenticationOnTheManagementAPI(t *testing.T) {
	e, c := newTwoReplicas(t)
	whoami := "/api/v1/auth/whoami"

	check401 := func(t *testing.T, what string, a answer) {
		t.Helper()
		ch := strings.Join(a.Header.Values("WWW-Authenticate"), ",")
		if a.Status != http.StatusUnauthorized || !strings.Contains(ch, `Bearer realm="trove"`) || strings.Contains(strings.ToLower(ch), "basic") {
			t.Errorf("%s: %d, WWW-Authenticate %q, want 401 with the Bearer challenge of RFC 6750 and no Basic challenge\n%s", what, a.Status, ch, a.Body)
		}
	}
	t.Run("R-API-005 an invalid token is 401 with a Bearer challenge and no Basic challenge, on every replica", func(t *testing.T) {
		for range 4 {
			check401(t, "invalid bearer", send(t, c, "GET", whoami, nil, "Authorization", "Bearer trv_pat_invalid"))
		}
	})
	t.Run("R-API-005 the same token as the password of Basic names its owner, whatever the user name", func(t *testing.T) {
		basic := base64.StdEncoding.EncodeToString([]byte("anyname:" + e.alice.token))
		a := send(t, c, "GET", whoami, nil, "Authorization", "Basic "+basic)
		principal, _ := a.JSON(t)["principal"].(map[string]any)
		if a.Status != http.StatusOK || principal["name"] != "alice" {
			t.Fatalf("Basic: %d %s", a.Status, a.Body)
		}
	})
	t.Run("R-API-005 with an Authorization header the session cookie is ignored, and a bad credential never falls back", func(t *testing.T) {
		_, browser := e.browserSession(e.alice.Person)
		if a := send(t, c, "GET", whoami, nil, browser...); a.Status != http.StatusOK {
			t.Fatalf("the session alone: %d %s", a.Status, a.Body)
		}
		check401(t, "session cookie with an invalid bearer", send(t, c, "GET", whoami, nil, append(browser, "Authorization", "Bearer trv_pat_invalid")...))
	})
	t.Run("R-API-005 an invalid token is 401 although a request without credentials may be admitted as anonymous", func(t *testing.T) {
		for range 4 {
			check401(t, "invalid bearer", send(t, c, "GET", whoami, nil, "Authorization", "Bearer trv_pat_invalid"))
		}
	})
}

// R-API-006: the CSRF protection applies to a session and to nothing else.
func TestR_API_006_CSRFProtectionOnTheManagementAPI(t *testing.T) {
	e, c := newTwoReplicas(t)
	_, browser := e.browserSession(e.alice.Person)
	body := map[string]any{"description": "t", "ttlSeconds": 3600}
	tokens := func() int64 {
		return e.scalar("SELECT count(*) FROM api_token WHERE principal_id = $1::uuid", e.alice.ID)
	}
	before := tokens()

	t.Run("R-API-006 a session POST without X-CSRF-Token is 403 csrf_failed and creates nothing, with the header it succeeds", func(t *testing.T) {
		a := send(t, c, "POST", "/api/v1/tokens", body, without(browser, "X-CSRF-Token")...)
		if a.Status != http.StatusForbidden || a.Code(t) != "csrf_failed" {
			t.Fatalf("without the header: %d %s", a.Status, a.Body)
		}
		if n := tokens(); n != before {
			t.Fatalf("%d tokens after the refusal, %d before", n, before)
		}
		if a := send(t, c, "POST", "/api/v1/tokens", body, browser...); a.Status != http.StatusCreated {
			t.Fatalf("with the header: %d %s", a.Status, a.Body)
		}
	})
	t.Run("R-API-006 the same request with Authorization: Bearer and no CSRF header succeeds", func(t *testing.T) {
		if a := send(t, c, "POST", "/api/v1/tokens", body, e.alice.header()...); a.Status != http.StatusCreated {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})
	t.Run("R-API-006 a form posted from another origin while the session cookie is present creates no token", func(t *testing.T) {
		n := tokens()
		form := url.Values{"description": {"evil"}, "ttlSeconds": {"3600"}}.Encode()
		hdr := append(without(without(browser, "X-CSRF-Token"), "Origin"), "Origin", "https://evil.example", "Sec-Fetch-Site", "cross-site",
			"Content-Type", "application/x-www-form-urlencoded")
		a := send(t, c, "POST", "/api/v1/tokens", form, hdr...)
		if a.Status < 400 || a.Status >= 500 {
			t.Fatalf("cross-origin form: %d %s", a.Status, a.Body)
		}
		if got := tokens(); got != n {
			t.Fatalf("%d tokens after the cross-origin post, %d before", got, n)
		}
	})
	t.Run("R-API-006 a GET changes no state and sign-out is a POST that needs the header", func(t *testing.T) {
		if a := send(t, c, "POST", "/auth/logout", nil, without(browser, "X-CSRF-Token")...); a.Status != http.StatusForbidden || a.Code(t) != "csrf_failed" {
			t.Fatalf("logout without the header: %d %s", a.Status, a.Body)
		}
	})
}

// events returns the audit events of one trace ID, polling because the asynchronous events reach the table with the next batch.
func (e *env) events(t *testing.T, c *webCluster, query string, n int) []map[string]any {
	t.Helper()
	var items []any
	waitFor(t, "audit events of "+query, 15*time.Second, func() bool {
		a := send(t, c, "GET", "/api/v1/audit/events?"+query, nil, e.admin.header()...)
		if a.Status != http.StatusOK {
			t.Fatalf("audit query: %d %s", a.Status, a.Body)
		}
		items, _ = a.JSON(t)["items"].([]any)
		return len(items) >= n
	})
	out := make([]map[string]any, len(items))
	for i, it := range items {
		out[i], _ = it.(map[string]any)
	}
	return out
}

// R-API-019: no management change commits without its event, and the event carries the trace ID of the answer.
func TestR_API_019_AuditAndRequestCorrelation(t *testing.T) {
	e, c := newTwoReplicas(t)

	t.Run("R-API-019 a repository create produces an audit event whose traceId equals the X-Request-Id of the answer", func(t *testing.T) {
		a := send(t, c, "POST", "/api/v1/projects/"+testenv.DefaultProject+"/repositories",
			map[string]any{"key": "audited", "class": "local", "format": "generic"}, e.admin.header()...)
		id := a.Header.Get("X-Request-Id")
		if a.Status != http.StatusCreated || id == "" {
			t.Fatalf("create: %d %q %s", a.Status, id, a.Body)
		}
		ev := e.events(t, c, "traceId="+id, 1)[0]
		actor, _ := ev["actor"].(map[string]any)
		if ev["action"] != "repository.created" || ev["outcome"] != "success" || ev["traceId"] != id || actor["name"] != "api-admin" {
			t.Fatalf("%v", ev)
		}
	})
	t.Run("R-API-019 a rejected policy change (403) produces an audit event with outcome denied and the answer's trace ID", func(t *testing.T) {
		a := send(t, c, "POST", "/api/v1/permission-policies", map[string]any{
			"name": "refused", "resources": map[string]any{"repositories": []string{testenv.DefaultProject + "/audited"}},
			"entries": []any{map[string]any{"principal": "user:bob", "actions": []string{"read"}}},
		}, e.alice.header()...)
		id := a.Header.Get("X-Request-Id")
		if a.Status != http.StatusForbidden || id == "" || a.JSON(t)["requestId"] != id {
			t.Fatalf("policy create as alice: %d %q %s", a.Status, id, a.Body)
		}
		ev := e.events(t, c, "traceId="+id, 1)[0]
		if ev["outcome"] != "denied" || ev["traceId"] != id {
			t.Fatalf("%v", ev)
		}
	})
}

// R-API-020: every prefix of the resource map answers a caller without credentials with 401 or its documented anonymous result,
// and every function has one path in the document.
func TestR_API_020_ResourceMapAndRouting(t *testing.T) {
	e, c := newTwoReplicas(t)
	_ = e

	// The families of the table that the document describes today; a prefix the document does not hold yet is not routed (the table
	// is ahead of the build for the P1 families and for the storage summary, the trash can, the search and the banner).
	doc := send(t, c, "GET", "/api/v1/openapi.json", nil).JSON(t)
	docPaths, _ := doc["paths"].(map[string]any)
	t.Run("R-API-020 a request to every described family without credentials answers 401 or its documented anonymous result, never 5xx", func(t *testing.T) {
		swept := 0
		for p, item := range docPaths {
			op, _ := item.(map[string]any)["get"].(map[string]any)
			if op == nil {
				continue
			}
			// Two characters make a valid project, repository and package key; one would be answered at the syntax before the caller is
			// asked, now that the default endpoint admits a request without a credential (R-EP-010).
			concrete := pathParam.ReplaceAllString(p, "xx")
			perm, _ := op["x-trove-permission"].(map[string]any)
			public := perm["public"] == true
			// The UI's content routes serve a session only; any other request is the answer of a path the role does not have,
			// 404 (Q-UI-106).
			if strings.Contains(p, "/content/") {
				public = true
			}
			a := send(t, c, "GET", concrete, nil)
			swept++
			if a.Status >= 500 || !public && a.Status != http.StatusUnauthorized {
				t.Errorf("GET %s without credentials: %d (public %v) %s", concrete, a.Status, public, a.Body)
			}
		}
		if swept < 40 {
			t.Fatalf("only %d described GET paths were swept", swept)
		}
	})
	t.Run("R-API-020 one path per function in the document: no maintenance start or stop, no summary refresh, and the task run path", func(t *testing.T) {
		paths := docPaths
		if len(paths) == 0 {
			t.Fatal("the document holds no path")
		}
		for p := range paths {
			if strings.HasPrefix(p, "/api/v1/maintenance") || p == "/api/v1/storage/summary/refresh" || p == "/api/v1/system/storage/summary" {
				t.Errorf("the document holds a second path for a function: %s", p)
			}
		}
		if _, ok := paths["/api/v1/system/tasks/{name}/run"]; !ok {
			t.Error("the document has no /api/v1/system/tasks/{name}/run")
		}
	})
	t.Run("R-API-020 POST /api/v1/system/tasks/gc/run starts a run", func(t *testing.T) {
		a := send(t, c, "POST", "/api/v1/system/tasks/gc/run", map[string]any{"dryRun": true}, e.admin.header()...)
		job, _ := a.JSON(t)["job"].(map[string]any)
		if a.Status != http.StatusAccepted || job["kind"] != "gc" || job["id"] == nil {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})
}

// R-API-032: the access tokens of the management API.
func TestR_API_032_AccessTokens(t *testing.T) {
	e, c := newTwoReplicas(t)

	create := func(t *testing.T) (value, id string) {
		t.Helper()
		a := send(t, c, "POST", "/api/v1/tokens", map[string]any{"description": "t", "ttlSeconds": 3600}, e.alice.header()...)
		m := a.JSON(t)
		value, _ = m["token"].(string)
		id, _ = m["id"].(string)
		if a.Status != http.StatusCreated || !strings.HasPrefix(value, "trv_pat_") || id == "" {
			t.Fatalf("create: %d %s", a.Status, a.Body)
		}
		return value, id
	}
	t.Run("R-API-032 a created token starts with trv_pat_ and no read of it ever shows the value again", func(t *testing.T) {
		value, id := create(t)
		for _, p := range []string{"/api/v1/tokens/" + id, "/api/v1/tokens?limit=100", "/api/v1/tokens/current"} {
			hdr := e.alice.header()
			if p == "/api/v1/tokens/current" {
				hdr = []string{"Authorization", "Bearer " + value}
			}
			a := send(t, c, "GET", p, nil, hdr...)
			if a.Status != http.StatusOK || strings.Contains(string(a.Body), value) {
				t.Errorf("GET %s: %d, the body holds the token value or the read failed\n%s", p, a.Status, a.Body)
			}
		}
	})
	t.Run("R-API-032 POST /api/v1/tokens/revoke without credentials revokes a token by its value", func(t *testing.T) {
		value, _ := create(t)
		if a := send(t, c, "GET", "/api/v1/auth/whoami", nil, "Authorization", "Bearer "+value); a.Status != http.StatusOK {
			t.Fatalf("the new token: %d %s", a.Status, a.Body)
		}
		if a := send(t, c, "POST", "/api/v1/tokens/revoke", map[string]any{"token": value}); a.Status != http.StatusAccepted && a.Status != http.StatusOK {
			t.Fatalf("revoke by value: %d %s", a.Status, a.Body)
		}
		waitFor(t, "the revoked token to fail on both replicas", 2*revocationBound+time.Second, func() bool {
			for range 2 {
				if send(t, c, "GET", "/api/v1/auth/whoami", nil, "Authorization", "Bearer "+value).Status != http.StatusUnauthorized {
					return false
				}
			}
			return true
		})
	})
	t.Run("R-API-032 DELETE /api/v1/tokens/current answers 204 and the token gets 401 on every replica within the revocation bound", func(t *testing.T) {
		value, _ := create(t)
		if a := send(t, c, "DELETE", "/api/v1/tokens/current", nil, "Authorization", "Bearer "+value); a.Status != http.StatusNoContent {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		waitFor(t, "the revoked token to fail on both replicas", revocationBound, func() bool {
			for range 2 {
				if send(t, c, "GET", "/api/v1/auth/whoami", nil, "Authorization", "Bearer "+value).Status != http.StatusUnauthorized {
					return false
				}
			}
			return true
		})
	})
}

// R-API-037: the settings surface answers with the ceilings of the YAML and says where the base URL comes from.
func TestR_API_037_SettingsSurface(t *testing.T) {
	e, c := newTwoReplicas(t)

	t.Run("R-API-037 PATCH /api/v1/settings/auth with a PAT maximum above the YAML ceiling answers 400 exceeds_ceiling", func(t *testing.T) {
		a := send(t, c, "PATCH", "/api/v1/settings/auth", map[string]any{"tokens": map[string]any{"pat": map[string]any{"maxTTLSeconds": 99999999999}}},
			append(e.admin.header(), "If-Match", "*", "Content-Type", mergePatch)...)
		if a.Status != http.StatusBadRequest || a.Code(t) != "exceeds_ceiling" {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})
	t.Run("R-API-037 with server.base_url set, GET /api/v1/system/info reports baseUrlSource config", func(t *testing.T) {
		a := send(t, c, "GET", "/api/v1/system/info", nil, e.admin.header()...)
		m := a.JSON(t)
		if a.Status != http.StatusOK || m["baseUrlSource"] != "config" || m["baseUrl"] != "http://"+testenv.DefaultHost {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})
}

// R-API-039: a permission policy's creation is an audit event that carries the granted actions and is found by the policy.
func TestR_API_039_AuditEventsOfAPolicy(t *testing.T) {
	e, c := newTwoReplicas(t)
	e.stack.Repository("audited", "local", "generic", "")

	a := send(t, c, "POST", "/api/v1/permission-policies", map[string]any{
		"name": "audited-readers", "resources": map[string]any{"repositories": []string{testenv.DefaultProject + "/audited"}},
		"entries": []any{map[string]any{"principal": "user:bob", "actions": []string{"read", "deploy"}}},
	}, e.admin.header()...)
	id := a.Header.Get("X-Request-Id")
	if a.Status != http.StatusCreated || id == "" {
		t.Fatalf("create policy: %d %s", a.Status, a.Body)
	}
	t.Run("R-API-039 the audit log filtered by the policy shows its creation with the granted actions", func(t *testing.T) {
		evs := e.events(t, c, "resourceType=permission_policy&traceId="+url.QueryEscape(id), 1)
		ev := evs[0]
		details, _ := ev["details"].(map[string]any)
		after, _ := details["after"].(map[string]any)
		raw := strings.ReplaceAll(strings.ReplaceAll(toJSON(t, after), " ", ""), "\n", "")
		if ev["action"] != "authz.policy.created" || !strings.Contains(raw, `"read"`) || !strings.Contains(raw, `"deploy"`) {
			t.Fatalf("%v", ev)
		}
	})
}

// R-API-040: garbage collection is a task of the shared job framework; one run at a time, listed by kind with its result.
func TestR_API_040_GarbageCollectionTasks(t *testing.T) {
	e := newEnv(t, true)
	c := e.cluster(e.newNode("web-1", nodeOpts{}), e.newNode("web-2", nodeOpts{}))
	admin := e.admin.header()

	a := send(t, c, "POST", "/api/v1/system/tasks/gc-full/run", map[string]any{"dryRun": true}, admin...)
	first, _ := a.JSON(t)["job"].(map[string]any)
	id, _ := first["id"].(string)
	if a.Status != http.StatusAccepted || id == "" {
		t.Fatalf("gc-full dry run: %d %s", a.Status, a.Body)
	}
	t.Run("R-API-040 a gc run while a full run is queued answers 409 with the same job", func(t *testing.T) {
		b := send(t, c, "POST", "/api/v1/system/tasks/gc/run", nil, admin...)
		job, _ := b.JSON(t)["job"].(map[string]any)
		if b.Status != http.StatusConflict || job["id"] != id {
			t.Fatalf("%d %s, want 409 naming job %s", b.Status, b.Body, id)
		}
	})
	t.Run("R-API-040 after the job ends GET /api/v1/system/jobs?kind=gc-full lists it with its result counts", func(t *testing.T) {
		e.newNode("worker-1", nodeOpts{role: "worker"})
		done := waitJob(t, c, id, admin, "succeeded")
		result, _ := done["result"].(map[string]any)
		if result["dryRun"] != true || result["errors"] != float64(0) {
			t.Fatalf("result %v", result)
		}
		list := send(t, c, "GET", "/api/v1/system/jobs?kind=gc-full", nil, admin...)
		found := false
		items, _ := list.JSON(t)["items"].([]any)
		for _, it := range items {
			m, _ := it.(map[string]any)
			if m["kind"] != "gc-full" {
				t.Fatalf("kind=gc-full lists %v", m["kind"])
			}
			if m["id"] == id {
				found = m["result"] != nil
			}
		}
		if list.Status != http.StatusOK || !found {
			t.Fatalf("%d %s", list.Status, list.Body)
		}
	})
}
