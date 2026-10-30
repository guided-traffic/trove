//go:build integration

package npm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_NPM_001_TheRegistryURL: the registry URL answers 200 {}, a repository that does
// not exist or has another format answers 404 in the native error shape, an operation
// npm defines and trove does not implement answers 404 (501 for a POST).
func TestR_NPM_001_TheRegistryURL(t *testing.T) {
	e := newEnv(t)
	e.st.Repository("gen-local", "local", "generic", "{}")
	a := e.replica("web-a")
	t.Run("R-NPM-001 the registry URL answers 200 {} to GET and HEAD", func(t *testing.T) {
		for _, m := range []string{"GET", "HEAD"} {
			ans := e.st.Do(a, m, "/api/npm/npm-local/", e.tokC, nil)
			if ans.Status != 200 || (m == "GET" && strings.TrimSpace(string(ans.Body)) != "{}") {
				t.Fatalf("%s: %d %q", m, ans.Status, ans.Body)
			}
		}
		if ans := e.st.Do(a, "GET", "/api/npm/npm-local", e.tokC, nil); ans.Status != 200 {
			t.Fatalf("without the slash: %d", ans.Status)
		}
	})
	t.Run("R-NPM-001 a repository that does not exist is 404 in the native shape", func(t *testing.T) {
		ans := e.st.Do(a, "GET", "/api/npm/does-not-exist/lodash", e.tokA, nil)
		if ans.Status != 404 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		code(t, ans)
	})
	t.Run("R-NPM-001 a repository of another format is 404 as well", func(t *testing.T) {
		ans := e.st.Do(a, "GET", "/api/npm/gen-local/lodash", e.tokA, nil)
		if ans.Status != 404 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		code(t, ans)
	})
	t.Run("R-NPM-043 the endpoints npm defines and trove does not implement", func(t *testing.T) {
		for _, c := range []struct {
			method, path string
			status       int
		}{
			{"GET", "-/v1/search?text=x", 404},
			{"GET", "-/npm/v1/keys", 404},
			{"GET", "-/package/lodash/access", 404},
			{"PUT", "-/user/org.couchdb.user:alice", 404},
			{"DELETE", "-/user/token/abc", 404},
			{"POST", "-/v1/login", 501},
			{"POST", "-/npm/v1/security/advisories/bulk", 501},
			{"POST", "-/stage/package/lodash", 501},
		} {
			ans := e.st.Do(a, c.method, "/api/npm/npm-local/"+c.path, e.tokA, nil)
			if ans.Status != c.status {
				t.Errorf("%s %s: %d %s", c.method, c.path, ans.Status, ans.Body)
				continue
			}
			code(t, ans)
		}
	})
	t.Run("R-NPM-001 an operation of the wrong method is 405 with Allow", func(t *testing.T) {
		ans := e.st.Do(a, "DELETE", "/api/npm/npm-local/-/ping", e.tokA, nil)
		if ans.Status != 405 || ans.Header.Get("Allow") != "GET, HEAD" {
			t.Fatalf("%d Allow %q", ans.Status, ans.Header.Get("Allow"))
		}
		code(t, ans)
	})
	t.Run("R-NPM-002 a second layer of escapes never reaches a route", func(t *testing.T) {
		ans := e.st.Do(a, "GET", "/api/npm/npm-local/a%2525b", e.tokA, nil)
		if ans.Status != 400 || code(t, ans) != "npm_invalid_name" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
}

// TestR_NPM_005_EveryRouteAnswersInTheNativeShape is the contract test of R-NPM-005 and
// R-NPM-004 over all routes of the format, with and without a credential and with a
// reader's: every error response has the three members and Content-Type
// application/json, and no WWW-Authenticate holds an otp element.
func TestR_NPM_005_EveryRouteAnswersInTheNativeShape(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	f, ok := formats.Lookup("npm")
	if !ok {
		t.Fatal("npm is not registered")
	}
	var n int
	for _, r := range f.Routes() {
		for _, ex := range r.Examples {
			for who, tok := range map[string]string{"anonymous": "", "reader": e.tokC, "writer": e.tokA} {
				ans := e.st.Do(a, r.Method, "/api/npm/npm-local/"+ex, tok, []byte(`{}`))
				n++
				if h := strings.ToLower(ans.Header.Get("WWW-Authenticate")); strings.Contains(h, "otp") || strings.Contains(h, "ipaddress") {
					t.Errorf("%s %s as %s: WWW-Authenticate %q", r.Method, ex, who, h)
				}
				if ans.Status >= 400 {
					if !errorShapeOK(ans) {
						t.Errorf("%s %s as %s: %d is no npm error body: %s", r.Method, ex, who, ans.Status, ans.Body)
					}
				}
				if who == "anonymous" && ans.Status != 401 {
					t.Errorf("%s %s without a credential: %d", r.Method, ex, ans.Status)
				}
				if ans.Status == 401 && ans.Header.Get("WWW-Authenticate") == "" {
					t.Errorf("%s %s: a 401 without a challenge", r.Method, ex)
				}
				if ans.Status >= 500 && ans.Status != 501 {
					t.Errorf("%s %s as %s: %d %s", r.Method, ex, who, ans.Status, ans.Body)
				}
			}
		}
	}
	if n < 100 {
		t.Fatalf("only %d requests were made", n)
	}
}

// TestR_NPM_005_AccessLogAndMetrics: npm-command and npm-session reach the access log of
// the request, and the protocol metrics count it.
func TestR_NPM_005_AccessLogAndMetrics(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "logged"})
	ans := e.publish(a, "npm-local", e.tokA, pkg{Name: "logged", Version: "1.0.1"}, "npm-command", "publish", "npm-session", "0123456789abcdef")
	if ans.Status != 201 {
		t.Fatalf("%d %s", ans.Status, ans.Body)
	}
	e.st.Do(a, "GET", "/api/npm/npm-local/-/v1/search", e.tokA, nil, "npm-command", "search", "npm-session", "feedface")
	logs := a.Logs.String()
	for _, want := range []string{
		`"handler":"npm.publish"`, `"npm-command":"publish"`, `"npm-session":"0123456789abcdef"`,
		`"handler":"npm.deferred.get"`, `"npm-command":"search"`, `"npm-session":"feedface"`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("the access log lacks %s", want)
		}
	}
	m := a.Metrics(t)
	for _, series := range []string{
		`trove_npm_requests_total{class="local",operation="publish",status="201"}`,
		`trove_npm_requests_total{class="local",operation="deferred.get",status="404"}`,
	} {
		if v := testenv.MetricValue(m, series); v < 1 {
			t.Errorf("%s = %v", series, v)
		}
	}
	if !strings.Contains(m, `trove_npm_request_duration_seconds_count{class="local",operation="publish"}`) {
		t.Error("no duration histogram for publish")
	}
}

// errorShapeOK reports whether an answer is the native error body of npm (R-NPM-005).
func errorShapeOK(a testenv.Answer) bool {
	var b map[string]string
	if json.Unmarshal(a.Body, &b) != nil {
		return false
	}
	return b["error"] != "" && b["error"] == b["message"] && b["code"] != "" && a.Header.Get("Content-Type") == "application/json"
}
