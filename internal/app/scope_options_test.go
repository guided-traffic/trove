package app

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
)

func TestR_AUTH_024_ScopePatternsUseTheAuthorizationValidators(t *testing.T) {
	opts := TokenScopeOptions(&config.Config{})
	grant := func(repo, pkg, path string) string {
		return `{"repositories":[{"repositories":[` + repo + `],"packages":{"include":[` + pkg + `]},"paths":{"include":[` + path + `]},"actions":["read"]}]}`
	}
	ok := map[string]string{
		"a key, a key pattern and a class selector": grant(`"web/npm-local","w*/npm-team-*","@any-local:npm"`, `"@acme/*"`, `"releases/**"`),
		"every capability of R-AUTHZ-010":           `{"system":["content:bypass","endpoints:write","audit:read"]}`,
		"the triage capability of R-VULN-019":       `{"inherit":false,"system":["vulnerabilities:triage"]}`,
	}
	for name, doc := range ok {
		t.Run("R-AUTH-024 accepts "+name, func(t *testing.T) {
			if _, err := token.ParseScope([]byte(doc), opts); err != nil {
				t.Fatal(err)
			}
		})
	}
	bad := map[string]string{
		"a bare key outside a project":         grant(`"npm-local"`, `"a"`, `"b"`),
		"a repository selector that is no key": grant(`"web/Not A Key"`, `"a"`, `"b"`),
		"a class selector that does not exist": grant(`"@any-local-ish"`, `"a"`, `"b"`),
		"a path pattern with an empty segment": grant(`"npm"`, `"a"`, `"a//b"`),
		"a pattern longer than the limit":      grant(`"npm"`, `"`+strings.Repeat("a", 1025)+`"`, `"b"`),
		"an action that is not one of the six": `{"repositories":[{"repositories":["a"],"actions":["publish"]}]}`,
		"a capability that does not exist":     `{"system":["everything:write"]}`,
	}
	for name, doc := range bad {
		t.Run("R-AUTH-024 refuses "+name+" with 400 invalid_scope", func(t *testing.T) {
			_, err := token.ParseScope([]byte(doc), opts)
			if e, isErr := errs.As(err); !isErr || e.Code != errs.CodeInvalidScope || e.Status() != 400 {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestR_EP_011_ScopeOptionsKnowTheTokenTypesOfAnEndpoint(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.Endpoints = map[string]config.Endpoint{
		"customers": {Listen: ":8443", BaseURL: "https://packages.example.net"},
		"partners":  {Listen: ":8444", BaseURL: "https://partners.example.net", Auth: config.EndpointAuth{TokenTypes: []string{"pat", "sat"}}},
	}
	o := TokenScopeOptions(cfg)
	for _, tc := range []struct {
		endpoint, typ string
		want          bool
	}{
		{"default", "pat", true},
		{"default", "sat", true},
		{"customers", "sat", true},
		{"customers", "pat", false},
		{"partners", "pat", true},
	} {
		if got := o.EndpointAccepts(tc.endpoint, tc.typ); got != tc.want {
			t.Errorf("R-EP-011 %s accepts %s = %v, want %v", tc.endpoint, tc.typ, got, tc.want)
		}
	}
}
