package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// denyCapability refuses one capability and allows every other, as a caller without it.
type denyCapability string

func (d denyCapability) Require(_ context.Context, permission string) error {
	if permission == string(d) {
		return errs.E(errs.Forbidden, errs.CodeForbidden, "forbidden")
	}
	return nil
}

func (denyCapability) RequireAction(context.Context, authz.Coordinate) error { return nil }

// suggestProblem is the part of a problem answer that does not change between two requests.
type suggestProblem struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Errors []struct {
		Pointer string `json:"pointer"`
		Code    string `json:"code"`
	} `json:"errors"`
}

func readSuggestProblem(t *testing.T, w *httptest.ResponseRecorder) suggestProblem {
	t.Helper()
	var p suggestProblem
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("%d %s: %v", w.Code, w.Body.String(), err)
	}
	return p
}

// TestR_API_060_TheSuggestionsCheckTheirParameters: q of 2 to 100 characters after trimming, the known kinds, a limit of 1 to 10;
// every other value answers 400 malformed_request with the parameter as the pointer.
func TestR_API_060_TheSuggestionsCheckTheirParameters(t *testing.T) {
	s := newSetup(t, nil, authed)
	long := strings.Repeat("a", 101)
	cases := []struct{ name, query, pointer string }{
		{"a text of one character", "q=n", "q"},
		{"a text of one character after trimming", "q=%20n%20", "q"},
		{"a text of 101 characters", "q=" + long, "q"},
		{"no text", "kind=repository", "q"},
		{"an unknown kind", "q=npm&kind=file", "kind"},
		{"an unknown kind in a comma-separated list", "q=npm&kind=repository,file", "kind"},
		{"a limit of 11", "q=npm&limit=11", "limit"},
		{"a limit of 0", "q=npm&limit=0", "limit"},
		{"a limit that is no number", "q=npm&limit=five", "limit"},
	}
	for _, c := range cases {
		t.Run("R-API-060 "+c.name+" answers 400", func(t *testing.T) {
			w := s.get("/api/v1/search/suggestions?" + c.query)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			p := readSuggestProblem(t, w)
			if p.Code != errs.CodeInvalid || len(p.Errors) != 1 || p.Errors[0].Pointer != c.pointer {
				t.Fatalf("%+v", p)
			}
		})
	}
	t.Run("R-API-060 a text of 100 characters is accepted", func(t *testing.T) {
		if w := s.get("/api/v1/search/suggestions?q=" + strings.Repeat("a", 100)); w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	})
}

// TestR_API_060_TheAnswerHoldsAMemberPerKindAsked: one member per kind asked and none for a kind not asked, never paged and never
// stored by a cache.
func TestR_API_060_TheAnswerHoldsAMemberPerKindAsked(t *testing.T) {
	s := newSetup(t, nil, authed)
	cases := []struct {
		query string
		want  []string
	}{
		{"q=npm", []string{"packages", "projects", "repositories"}},
		{"q=npm&kind=repository", []string{"repositories"}},
		{"q=npm&kind=package&kind=project", []string{"packages", "projects"}},
		{"q=npm&kind=user,group,repository", []string{"groups", "repositories", "users"}},
	}
	for _, c := range cases {
		t.Run("R-API-060 "+c.query, func(t *testing.T) {
			w := s.get("/api/v1/search/suggestions?" + c.query)
			if w.Code != http.StatusOK {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
				t.Errorf("Cache-Control %q", cc)
			}
			if w.Header().Get("Link") != "" {
				t.Errorf("Link %q", w.Header().Get("Link"))
			}
			var body map[string]struct {
				Items []json.RawMessage `json:"items"`
				More  *bool             `json:"more"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body) != len(c.want) {
				t.Fatalf("members %v, want %v: %s", body, c.want, w.Body.String())
			}
			for _, m := range c.want {
				if got, ok := body[m]; !ok || got.Items == nil || got.More == nil {
					t.Fatalf("member %s: %s", m, w.Body.String())
				}
			}
		})
	}
}

// TestR_API_061_TheAdminKindsAreRefusedBeforeAnyLookup: kind user or group without identities:read answers 403 before any lookup,
// with the same body for every text (Q-API-272); a caller without a credential gets 401 while nothing is public.
func TestR_API_061_TheAdminKindsAreRefusedBeforeAnyLookup(t *testing.T) {
	s := newSetup(t, func(o *Options) { o.Access = denyCapability(authz.IdentitiesRead) }, authed)
	for _, kind := range []string{"user", "group", "repository,user"} {
		t.Run("R-API-061 kind="+kind+" without identities:read answers 403, the same for two texts", func(t *testing.T) {
			a := s.get("/api/v1/search/suggestions?q=fr&kind=" + kind)
			b := s.get("/api/v1/search/suggestions?q=zz&kind=" + kind)
			if a.Code != http.StatusForbidden || b.Code != http.StatusForbidden {
				t.Fatalf("%d %d", a.Code, b.Code)
			}
			if pa, pb := readSuggestProblem(t, a), readSuggestProblem(t, b); pa.Detail != pb.Detail || pa.Code != pb.Code {
				t.Fatalf("%+v %+v", pa, pb)
			}
		})
	}
	t.Run("R-API-061 the main kinds need no capability", func(t *testing.T) {
		if w := s.get("/api/v1/search/suggestions?q=fr"); w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	})
	closed := newSetup(t, func(o *Options) {
		o.Repositories.PublicContent = func(context.Context) bool { return false }
	}, func(*http.Request) (httpserver.Principal, error) { return httpserver.Principal{Anonymous: true}, nil })
	t.Run("R-API-060 a caller without a credential gets 401 while nothing is public", func(t *testing.T) {
		if w := closed.get("/api/v1/search/suggestions?q=npm"); w.Code != http.StatusUnauthorized {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	})
}
