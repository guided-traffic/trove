package v1

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/ci/reason"
)

// The decoding of the exchange's body (R-CI-007): form and JSON, a parameter sent twice, the body limit, the content
// type, the parameters the exchange does not know. A description names a parameter of the exchange, never a value
// and never the caller's other member names (R-CI-008).
func TestR_CI_007_TheBodyOfAnExchange(t *testing.T) {
	decode := func(contentType, body string, limit int) (string, string, *int64, string) {
		r := httptest.NewRequest("POST", ExchangePath, strings.NewReader(body))
		r.Header.Set("Content-Type", contentType)
		req, ee := decodeExchange(httptest.NewRecorder(), r, limit)
		if ee != nil {
			return "", "", nil, string(ee.Reason) + " " + ee.Description
		}
		return req.GrantType, req.SubjectToken, req.TTLSeconds, ""
	}
	t.Run("R-CI-007 a form body with a charset parameter", func(t *testing.T) {
		g, s, ttl, err := decode("application/x-www-form-urlencoded; charset=utf-8", "grant_type=g&subject_token=a.b.c&ttl=600&other=1&other=2", 1024)
		if err != "" || g != "g" || s != "a.b.c" || ttl == nil || *ttl != 600 {
			t.Fatalf("%q %q %v %q", g, s, ttl, err)
		}
	})
	t.Run("R-CI-007 a JSON body with string values, ttl as a number", func(t *testing.T) {
		g, s, ttl, err := decode("application/json", `{"grant_type":"g","subject_token":"a.b.c","ttl":60,"extra":{"x":1}}`, 1024)
		if err != "" || g != "g" || s != "a.b.c" || ttl == nil || *ttl != 60 {
			t.Fatalf("%q %q %v %q", g, s, ttl, err)
		}
	})
	for _, c := range []struct {
		name, contentType, body string
		want                    reason.Reason
	}{
		{"a parameter sent twice", "application/x-www-form-urlencoded", "grant_type=a&grant_type=b", reason.MalformedRequest},
		{"a form that does not decode", "application/x-www-form-urlencoded", "grant_type=%zz", reason.MalformedRequest},
		{"a JSON member twice", "application/json", `{"scope":"a","scope":"b"}`, reason.MalformedRequest},
		{"a JSON member that is no string", "application/json", `{"subject_token":7}`, reason.MalformedRequest},
		{"a JSON array", "application/json", `["grant_type"]`, reason.MalformedRequest},
		{"a ttl that is no number", "application/x-www-form-urlencoded", "ttl=1h", reason.MalformedRequest},
		{"a body over the limit", "application/x-www-form-urlencoded", "scope=" + strings.Repeat("a", 2000), reason.MalformedRequest},
		{"no content type", "", "grant_type=a", reason.UnsupportedContentType},
		{"another content type", "text/plain", "grant_type=a", reason.UnsupportedContentType},
	} {
		t.Run("R-CI-008 "+c.name, func(t *testing.T) {
			_, _, _, err := decode(c.contentType, c.body, 1024)
			if !strings.HasPrefix(err, string(c.want)+" ") {
				t.Fatalf("refusal %q, want %s", err, c.want)
			}
			if strings.Contains(err, "zz") || strings.Contains(err, "1h") {
				t.Errorf("the description quotes a value: %q", err)
			}
		})
	}
	t.Run("R-CI-008 an unknown member is ignored and never quoted", func(t *testing.T) {
		_, _, _, err := decode("application/json", `{"eyJhbGciOiJSUzI1NiJ9":5,"grant_type":"g"}`, 1024)
		if err != "" {
			t.Errorf("an unknown member is refused: %q", err)
		}
	})
}

// R-CI-021: the exchange spends its own limiters, never the general API rate limit.
func TestR_CI_021_TheExchangeIsNotUnderTheGeneralRateLimit(t *testing.T) {
	if limitedPath(ExchangePath) {
		t.Error("the exchange is under the general API rate limit")
	}
	for _, p := range []string{"/api/v1/auth/oidc/token/x", "/api/v1/tokens/current", "/api/npm/npm-internal/x"} {
		if !limitedPath(p) {
			t.Errorf("%s is not limited", p)
		}
	}
}
