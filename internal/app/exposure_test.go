package app

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/config"
)

func TestR_EP_010_EndpointRulesFromConfiguration(t *testing.T) {
	srv := config.Server{
		BaseURL: "https://trove.example.com",
		Endpoints: map[string]config.Endpoint{
			"customers": {Listen: ":8443", BaseURL: "https://packages.example.com"},
			"internal": {
				Listen: ":8444", BaseURL: "https://ci.example.com", Surface: []string{"packages", "api"},
				Auth: config.EndpointAuth{TokenTypes: []string{"pat", "cit"}, BoundTokensOnly: ptrTo(false), Anonymous: ptrTo(true)},
			},
		},
	}
	eps := srv.ResolveEndpoints()
	rules := map[string]func() bool{}
	for _, e := range eps {
		r := endpointRules(&endpointRuntime{cfg: e})
		switch e.Name {
		case "default":
			rules[e.Name] = func() bool {
				return r.AcceptsSession && slices.Equal(r.TokenTypes, []token.Type{token.TypePAT, token.TypeSAT, token.TypeCIT}) && !r.BoundTokensOnly && r.Anonymous
			}
		case "customers":
			rules[e.Name] = func() bool {
				return !r.AcceptsSession && slices.Equal(r.TokenTypes, []token.Type{token.TypeSAT}) && r.BoundTokensOnly && !r.Anonymous
			}
		case "internal":
			rules[e.Name] = func() bool {
				return !r.AcceptsSession && slices.Equal(r.TokenTypes, []token.Type{token.TypePAT, token.TypeCIT}) && !r.BoundTokensOnly && r.Anonymous
			}
		}
		if r.Name != e.Name {
			t.Errorf("%s: name %q", e.Name, r.Name)
		}
	}
	for name, ok := range rules {
		t.Run("R-EP-010 the rules of "+name+" follow its configuration", func(t *testing.T) {
			if !ok() {
				t.Fatal("rules differ from the configuration")
			}
		})
	}
}

func ptrTo[T any](v T) *T { return &v }

func TestR_EP_013_EndpointStagePutsTheEndpointIntoTheContext(t *testing.T) {
	a := &App{}
	var seen string
	h := a.endpointStage(&endpointRuntime{cfg: config.ResolvedEndpoint{Name: "customers"}})(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = authz.EndpointOf(r.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/files/generic-local/x", nil))
	if seen != "customers" {
		t.Fatalf("the decision point would see endpoint %q", seen)
	}
}

func TestR_EP_013_ExposureCeilingFromConfiguration(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.Endpoints = map[string]config.Endpoint{
		"customers": {Listen: ":8443", BaseURL: "https://packages.example.com"},
		"mirror":    {Listen: ":8444", BaseURL: "https://mirror.example.com", Exposure: "all"},
	}
	a := &App{cfg: cfg}
	cl := a.exposureCeiling()
	for name, want := range map[string]string{"default": authz.ExposureModeAll, "customers": authz.ExposureModeRules, "mirror": authz.ExposureModeAll, "unknown": authz.ExposureModeRules} {
		if got := cl.Mode(name); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
	if a.exposureCeiling() != cl {
		t.Fatal("the ceiling is built twice")
	}
}
