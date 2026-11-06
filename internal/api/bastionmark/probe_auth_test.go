package bastionmark

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/api/dispatch"
	"github.com/guided-traffic/trove/internal/api/rawfiles"
	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm"
	"github.com/guided-traffic/trove/internal/formats/nuget"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/repository"
)

// noTokens is a token store that knows no token: the probe carries none, and nothing here may look one up.
type noTokens struct{ t *testing.T }

func (n noTokens) Validate(context.Context, string) (token.Info, error) {
	n.t.Error("a probe made the authentication stage look up a token")
	return token.Info{}, token.ErrUnknown
}

// realStageChain is the chain of the default endpoint with anonymous access off (the default), the real
// authentication stage of internal/auth behind the Bastion's wrapper of the composition root
// (v1.BastionRouteAuth), the Mark stage, and the real format dispatcher and raw file handler. The recognition is
// a stand-in: the request is recognized as a probe or a forward of the Bastion edge-1.
func realStageChain(t *testing.T) (http.Handler, *rig) {
	t.Helper()
	r := &rig{t: t, shapes: &shapes{read: iface.ReadFull}, decider: &fakeDecider{}}
	fs := []formats.Format{npm.New(), nuget.New()}
	d, err := dispatch.New(dispatch.Options{
		Host: fakeHost{}, Decider: r.decider, Formats: func() []formats.Format { return fs },
		Repos: &fakeRepos{byKey: map[string]repository.Repository{
			"npm-all":  {Name: "2", Key: "npm-all", Class: repository.Virtual, Format: "npm"},
			"nuget-v3": {Name: "3", Key: "nuget-v3", Class: repository.Local, Format: "nuget"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	router := httpserver.NewClientRouter()
	d.Register(router)
	rawfiles.New(rawfiles.Options{}).Register(router)
	stage := auth.New(auth.Options{Tokens: noTokens{t}})
	rules := auth.EndpointRules{Name: "default", Anonymous: false, TokenTypes: []token.Type{token.TypePAT, token.TypeSAT}}
	credential := func(req *http.Request) (httpserver.Principal, error) {
		res, err := stage.Authenticate(req, rules)
		if err != nil {
			return httpserver.Principal{}, err
		}
		if res.Anonymous {
			return httpserver.AnonymousPrincipal(), nil
		}
		return httpserver.Principal{Kind: httpserver.PrincipalKind(res.Principal.Kind), ID: res.Principal.ID, Name: res.Principal.Name}, nil
	}
	marker := New(Options{Shapes: r.shapes, MainURL: mainURL})
	recognize := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			rec := bastionmain.Recognition{Bastion: "edge-1", Interface: iface.InterfaceVersion}
			rec.Probe = req.Header.Get(iface.HeaderProbe) == iface.ProbeValue && req.Method != http.MethodGet && req.Method != http.MethodHead
			next.ServeHTTP(w, req.WithContext(bastionmain.WithRecognition(req.Context(), rec)))
		})
	}
	return httpserver.Chain{
		Router: router, Authenticate: v1.BastionRouteAuth(credential, nil),
		Bastion: func(next http.Handler) http.Handler { return recognize(marker.Stage()(next)) },
	}.Handler(), r
}

// TestR_BAS_014_AProbeIsAnsweredThroughTheRealAuthenticationStage: with anonymous access off, a write probe -
// recognized, Trove-Bastion-Probe: 1, no credential of a caller - reaches the route match and is answered there
// (403 bastion_read_only in the format's shape with the main installation's URL, or 204 for a read route that
// takes a body), not refused 401 credentials_required by the authentication stage. The probe is admitted for
// routing only: no handler runs and no decision is asked, and a forwarded request without a credential that is no
// probe is still refused as anonymous (Q-BAS-118).
func TestR_BAS_014_AProbeIsAnsweredThroughTheRealAuthenticationStage(t *testing.T) {
	h, r := realStageChain(t)
	do := func(method, path string, probe bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(""))
		if probe {
			req.Header.Set(iface.HeaderProbe, iface.ProbeValue)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := do(http.MethodPut, "/api/npm/npm-all/lodash", true)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"code":"bastion_read_only"`) ||
		!strings.Contains(rec.Body.String(), "publish to "+mainURL+"/api/npm/npm-all/") {
		t.Fatalf("npm publish as a probe: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodPut, "/api/nuget/nuget-v3/v3/package", true); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "bastion_read_only") {
		t.Fatalf("dotnet nuget push as a probe: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodPost, "/api/npm/npm-all/-/npm/v1/security/advisories/bulk", true); rec.Code != http.StatusNoContent || rec.Header().Get(iface.HeaderRoute) != iface.RouteReadWithBody {
		t.Fatalf("npm audit as a probe: %d %v", rec.Code, rec.Header())
	}
	if rec := do(http.MethodPut, "/files/files-local/a/b.zip", true); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "bastion_read_only") {
		t.Fatalf("a raw file upload as a probe: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodGet, "/api/npm/npm-all/lodash", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a forwarded read without a credential with anonymous access off: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodPut, "/api/npm/npm-all/lodash", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a forwarded write without a credential that is no probe: %d %s", rec.Code, rec.Body.String())
	}
	if len(r.reached) != 0 {
		t.Fatalf("a handler ran: %v", r.reached)
	}
	if len(r.shapes.asked) != 0 {
		t.Fatalf("a decision was observed for a probe: %v", r.shapes.asked)
	}
}
