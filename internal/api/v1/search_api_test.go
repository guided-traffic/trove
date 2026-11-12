package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
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
	t.Run("R-API-060 a text of 100 characters and a limit of 10 are accepted", func(t *testing.T) {
		sr, err := parseSuggest(gen.GetSearchSuggestionsParams{Q: "  " + strings.Repeat("A", 100) + " ", Limit: ptr(10)})
		if err != nil || sr.text != strings.Repeat("a", 100) || sr.limit != 10 {
			t.Fatalf("%+v %v", sr, err)
		}
	})
}

// TestR_API_060_TheAnswerHoldsAMemberPerKindAsked: one member per kind asked and none for a kind not asked, each with items and
// more; over HTTP never paged and never stored by a cache.
func TestR_API_060_TheAnswerHoldsAMemberPerKindAsked(t *testing.T) {
	kinds := func(vs ...gen.GetSearchSuggestionsParamsKind) *[]gen.GetSearchSuggestionsParamsKind { return &vs }
	cases := []struct {
		name string
		kind *[]gen.GetSearchSuggestionsParamsKind
		want []string
	}{
		{"no kind", nil, []string{"packages", "projects", "repositories"}},
		{"kind=repository", kinds("repository"), []string{"repositories"}},
		{"kind=package&kind=project", kinds("package", "project"), []string{"packages", "projects"}},
		{"kind=user,group,repository", kinds("user,group,repository"), []string{"groups", "repositories", "users"}},
	}
	for _, c := range cases {
		t.Run("R-API-060 "+c.name, func(t *testing.T) {
			sr, err := parseSuggest(gen.GetSearchSuggestionsParams{Q: "npm", Kind: c.kind})
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(emptySuggestions(sr))
			var body map[string]struct {
				Items []json.RawMessage `json:"items"`
				More  *bool             `json:"more"`
			}
			if err := json.Unmarshal(b, &body); err != nil {
				t.Fatal(err)
			}
			var got []string
			for m, v := range body {
				if v.Items == nil || v.More == nil {
					t.Fatalf("member %s: %s", m, b)
				}
				got = append(got, m)
			}
			if sort.Strings(got); !slices.Equal(got, c.want) {
				t.Fatalf("members %v, want %v", got, c.want)
			}
		})
	}
	s := newSetup(t, func(o *Options) { o.Repositories.Authz = &indexDecider{} }, authed)
	t.Run("R-API-060 kind=repository answers only the member repositories, never paged and never stored", func(t *testing.T) {
		w := s.get("/api/v1/search/suggestions?q=npm&kind=repository")
		if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"repositories":{"items":[],"more":false}}` {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		if cc := w.Header().Get("Cache-Control"); cc != "no-store" || w.Header().Get("Link") != "" {
			t.Fatalf("Cache-Control %q Link %q", cc, w.Header().Get("Link"))
		}
	})
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
		if w := s.get("/api/v1/search/suggestions?q=fr"); w.Code == http.StatusForbidden {
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

// suggestOf is the request of a text with the limit.
func suggestOf(t *testing.T, text string, limit int) suggestRequest {
	t.Helper()
	sr, err := parseSuggest(gen.GetSearchSuggestionsParams{Q: text, Limit: &limit})
	if err != nil {
		t.Fatal(err)
	}
	return sr
}

// TestR_API_061_TheSuggestionsAreFilteredBeforeTheyAreCut: a repository or project the caller cannot see is decided away before
// the ranking and the cut, so it neither takes the place of a readable match nor sets `more` (Q-API-276).
func TestR_API_061_TheSuggestionsAreFilteredBeforeTheyAreCut(t *testing.T) {
	var facts []authz.RepositoryFacts
	for _, k := range []string{"button-1", "button-2", "button-3", "button-4", "button-5", "button-6", "button-7"} {
		facts = append(facts, authz.RepositoryFacts{Name: qname.Repo{Project: "web", Key: k}, Class: repository.Local, Format: "npm"})
	}
	facts = append(facts, authz.RepositoryFacts{Name: qname.Repo{Project: "web", Key: "button"}, Class: repository.Virtual, Format: "npm"})
	// The decider of the test reads every repository whose key does not end in an odd digit; button-1, -3, -5 and -7 are hidden.
	asked := 0
	visible := func(names []qname.Repo) ([]qname.Repo, error) {
		asked += len(names)
		var out []qname.Repo
		for _, n := range names {
			if last := n.Key[len(n.Key)-1]; !strings.ContainsRune("1357", rune(last)) {
				out = append(out, n)
			}
		}
		return out, nil
	}
	keys := func(r *gen.RepositorySuggestions) []string {
		var out []string
		for _, i := range r.Items {
			out = append(out, i.Project+"/"+i.Key)
		}
		return out
	}
	t.Run("R-API-061 the cut keeps readable matches only, and more counts readable matches only", func(t *testing.T) {
		got, err := repositorySuggestions(suggestOf(t, "button", 3), facts, visible)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"web/button", "web/button-2", "web/button-4"}; !slices.Equal(keys(got), want) || !got.More {
			t.Fatalf("%v more=%v, want %v more=true", keys(got), got.More, want)
		}
		if asked != len(facts) {
			t.Errorf("the decider was asked about %d repositories, want every match (%d) before the cut", asked, len(facts))
		}
	})
	t.Run("R-API-061 more is false when the readable matches fit, however many hidden ones match", func(t *testing.T) {
		got, err := repositorySuggestions(suggestOf(t, "button", 4), facts, visible)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Items) != 4 || got.More {
			t.Fatalf("%v more=%v, want the four readable ones and more=false", keys(got), got.More)
		}
	})
	t.Run("R-API-061 a hidden project is neither suggested nor counted", func(t *testing.T) {
		rows := []q.SearchProjectsRow{
			{Key: "web", DisplayName: "Web platform", Visibility: "public"},
			{Key: "webshop", DisplayName: "Shop", Visibility: "private"},
			{Key: "web-tools", DisplayName: "Tools", Visibility: "internal"},
		}
		listed := func(key, visibility string) bool { return visibility != "private" }
		got := projectSuggestions(suggestOf(t, "web", 2), rows, listed)
		if len(got.Items) != 2 || got.Items[0].Key != "web" || got.Items[1].Key != "web-tools" || got.More {
			t.Fatalf("%+v", got)
		}
	})
}
