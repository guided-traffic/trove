package login

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/oidc"
	"github.com/guided-traffic/trove/internal/errs"
)

// The HTTP level of the browser login without a database: what the edge parses,
// checks and writes. The flows that need the providers, the session store and
// PostgreSQL are in test/integration/login.

// handler is the edge over a login service without providers or break-glass
// account, which answers what the edge asks of it for an unknown provider and a
// disabled local login.
func handler() *Handler {
	return New(&oidc.Service{}, nil, Options{
		ClientAddr: func(*http.Request) string { return "192.0.2.1" },
		Origin:     func(*http.Request) string { return "https://trove.test" },
	})
}

func problemOf(t *testing.T, rec *httptest.ResponseRecorder) (int, string) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("content type %q, want application/problem+json (body %s)", ct, rec.Body)
	}
	var p struct {
		Status int    `json:"status"`
		Code   string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p.Status, p.Code
}

func TestR_AUTH_005_UnknownProviderIsA404Problem(t *testing.T) {
	h := handler()
	mux := http.NewServeMux()
	mux.HandleFunc(RouteStart, h.HandleStart)
	mux.HandleFunc(RouteCallback, h.HandleCallback)
	for _, path := range []string{"/auth/oidc/nowhere/start", "/auth/oidc/nowhere/callback?state=x&code=y"} {
		t.Run("R-AUTH-005 R-AUTH-006 GET "+path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			status, code := problemOf(t, rec)
			if rec.Code != http.StatusNotFound || status != http.StatusNotFound || code != errs.CodeNotFound {
				t.Fatalf("%d %d %q", rec.Code, status, code)
			}
			if rec.Header().Get("Set-Cookie") != "" {
				t.Fatalf("a request for an unknown provider set a cookie: %q", rec.Header().Get("Set-Cookie"))
			}
		})
	}
}

func TestR_AUTH_014_LocalLoginEdge(t *testing.T) {
	h := handler()
	post := func(mut func(*http.Request), body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/auth/local/login", strings.NewReader(body))
		if mut != nil {
			mut(r)
		}
		rec := httptest.NewRecorder()
		h.HandleLocalLogin(rec, r)
		return rec
	}

	t.Run("R-AUTH-012 R-AUTH-014 a request from another origin is refused with csrf_failed", func(t *testing.T) {
		rec := post(func(r *http.Request) { r.Header.Set("Origin", "https://evil.test") }, `{}`)
		if status, code := problemOf(t, rec); rec.Code != http.StatusForbidden || status != 403 || code != errs.CodeCSRFFailed {
			t.Fatalf("%d %d %q", rec.Code, status, code)
		}
	})

	t.Run("R-AUTH-012 R-AUTH-014 a request with neither Origin nor Sec-Fetch-Site is refused with csrf_failed", func(t *testing.T) {
		rec := post(nil, `{}`)
		if _, code := problemOf(t, rec); rec.Code != http.StatusForbidden || code != errs.CodeCSRFFailed {
			t.Fatalf("%d %q", rec.Code, code)
		}
	})

	t.Run("R-AUTH-014 a disabled local login answers 403 local_login_disabled before the body is read", func(t *testing.T) {
		rec := post(func(r *http.Request) { r.Header.Set("Origin", "https://trove.test") }, `not json`)
		if _, code := problemOf(t, rec); rec.Code != http.StatusForbidden || code != errs.CodeLocalLoginDisabled {
			t.Fatalf("%d %q", rec.Code, code)
		}
		if rec.Header().Get("Set-Cookie") != "" {
			t.Fatalf("a refused login set a cookie: %q", rec.Header().Get("Set-Cookie"))
		}
	})
}

func TestR_AUTH_006_ErrorPage(t *testing.T) {
	cases := []struct {
		name string
		f    oidc.LoginFailure
		want int
	}{
		{"a failure of the browser or the user is 400", oidc.LoginFailure{Reason: oidc.ReasonFlowExpired}, http.StatusBadRequest},
		{"a failure of the provider is 502", oidc.LoginFailure{Reason: oidc.ReasonTokenExchange, Provider: true}, http.StatusBadGateway},
		{"a dependency of trove that did not answer is 503", oidc.LoginFailure{Reason: oidc.ReasonUnavailable, Unavailable: true}, http.StatusServiceUnavailable},
		{"a failed provisioning is 500", oidc.LoginFailure{Reason: oidc.ReasonProvisioningFailed}, http.StatusInternalServerError},
	}
	for _, c := range cases {
		t.Run("R-AUTH-006 "+c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeErrorPage(rec, &c.f)
			if rec.Code != c.want {
				t.Fatalf("status %d, want %d", rec.Code, c.want)
			}
			if !strings.Contains(rec.Body.String(), `<code id="reason">`+c.f.Reason+`</code>`) {
				t.Fatalf("the page does not show the reason: %s", rec.Body)
			}
		})
	}

	t.Run("R-AUTH-006 R-AUTH-012 the page escapes the provider's text and keeps out of frames and caches", func(t *testing.T) {
		rec := httptest.NewRecorder()
		writeErrorPage(rec, &oidc.LoginFailure{Reason: oidc.ReasonIDPError, Provider: true, Detail: `<script>alert(1)</script>`})
		body := rec.Body.String()
		if strings.Contains(body, "<script>") || !strings.Contains(body, "&lt;script&gt;") {
			t.Fatalf("the provider's text is not escaped: %s", body)
		}
		h := rec.Header()
		if h.Get("X-Frame-Options") != "DENY" || h.Get("Cache-Control") != "no-store" ||
			!strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") ||
			!strings.HasPrefix(h.Get("Content-Type"), "text/html") {
			t.Fatalf("headers %v", h)
		}
	})
}
