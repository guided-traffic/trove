package dispatch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// galaxy is a format whose read and publish routes declare the scheme Token the way the Ansible Galaxy format's routes do (R-ANS-006),
// beside a route that does not.
type galaxy struct{ formats.Base }

func (galaxy) Descriptor() formats.Descriptor {
	return formats.Descriptor{
		Facts: catalog.Facts{
			ID: "galaxy", Classes: []formats.Class{formats.Local}, Layout: "galaxy",
			ProtocolBasePath: "/api/galaxy/{project}/{key}/", PatternSubject: formats.SubjectPath,
		},
		ContractVersion: formats.ContractVersion,
	}
}

func (galaxy) Routes() []formats.Route {
	h := func(_ context.Context, _ *formats.Request, w http.ResponseWriter) error {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	return []formats.Route{
		{
			Name: "galaxy.collection.get", Method: http.MethodGet, Template: "/v3/{name}", Examples: []string{"v3/a"},
			Action: formats.ActionRead, Kind: formats.Metadata, Handler: h, TokenScheme: true,
		},
		{
			Name: "galaxy.collection.publish", Method: http.MethodPost, Template: "/v3/{name}", Examples: []string{"v3/a"},
			Action: formats.ActionDeploy, Kind: formats.Upload, Handler: h, TokenScheme: true,
		},
		{
			Name: "galaxy.plain.get", Method: http.MethodGet, Template: "/plain/{name}", Examples: []string{"plain/a"},
			Action: formats.ActionRead, Kind: formats.Metadata, Handler: h,
		},
	}
}

func (g galaxy) Match(m, p string) (*formats.Route, formats.Params, error) {
	return formats.MatchRoutes(g.Routes(), m, p)
}

func (galaxy) Classify(formats.Path) formats.PathInfo { return formats.PathInfo{Kind: formats.Content} }

func (galaxy) PackageDocumentPath(string) (formats.Path, bool) { return formats.Path{}, false }

// TestR_ANS_006_TheDispatcherNamesTheRoutesThatTakeTheTokenScheme is the seam of Q-ANS-4: the authentication stage, which runs before the
// dispatcher, learns from the dispatcher whether a request reaches a route that declares the scheme Token, and the answer is the route
// the dispatcher then serves. A route without the declaration, a format without such a route (the API-key format is one), a path the
// dispatcher refuses before the match and a path outside the format prefixes are no such route.
func TestR_ANS_006_TheDispatcherNamesTheRoutesThatTakeTheTokenScheme(t *testing.T) {
	repos := &fakeRepos{byKey: map[qname.Repo]repository.Repository{
		qr("local"): {ID: "1", Name: qr("local"), Class: repository.Local, Format: "galaxy"},
		qr("keyed"): {ID: "2", Name: qr("keyed"), Class: repository.Local, Format: "keyed"},
	}}
	d, err := New(Options{
		Host: fakeHost{}, Decider: &fakeDecider{}, Repos: repos,
		Formats: func() []formats.Format { return []formats.Format{galaxy{}, keyed{}} },
	})
	if err != nil {
		t.Fatal(err)
	}
	router := httpserver.NewClientRouter()
	d.Register(router)
	var asked, askedKey []bool
	chain := httpserver.Chain{Router: router, Authenticate: func(r *http.Request) (httpserver.Principal, error) {
		asked = append(asked, d.TokenSchemeRoute(r))
		askedKey = append(askedKey, d.APIKeyRoute(r))
		return httpserver.AnonymousPrincipal(), nil
	}}.Handler()
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/galaxy/it/local/v3/a", true},
		{"HEAD", "/api/galaxy/it/local/v3/a", true},
		{"POST", "/api/galaxy/it/local/v3/a", true},
		{"GET", "/api/galaxy/it/local/plain/a", false},
		{"DELETE", "/api/galaxy/it/local/v3/a", false},
		{"GET", "/api/galaxy/it/NOPE/v3/a", false},
		{"GET", "/api/galaxy/it/local//v3/a", false},
		{"GET", "/api/galaxy/it/local/x/%2e%2e/v3/a", false},
		{"GET", "/api/keyed/it/keyed/index.json", false},
		{"PUT", "/api/keyed/it/keyed/package", false},
		{"GET", "/api/nosuch/local/v3/a", false},
		{"GET", "/files/local/v3/a", false},
		{"GET", "/api/v1/tokens", false},
	} {
		asked, askedKey = nil, nil
		chain.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(c.method, c.path, nil))
		if len(asked) != 1 || asked[0] != c.want {
			t.Errorf("%s %s: TokenSchemeRoute %v, want %v", c.method, c.path, asked, c.want)
		}
		if askedKey[0] && strings.HasPrefix(c.path, "/api/galaxy/") {
			t.Errorf("%s %s: APIKeyRoute is true for a route that declares only the Token scheme", c.method, c.path)
		}
	}
}
