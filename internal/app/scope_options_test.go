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
		"a key, a key pattern and a class selector": grant(`"npm-local","npm-team-*","@any-local:npm"`, `"@acme/*"`, `"releases/**"`),
		"every capability of R-AUTHZ-010":           `{"system":["content:bypass","endpoints:write","audit:read"]}`,
	}
	for name, doc := range ok {
		t.Run("R-AUTH-024 accepts "+name, func(t *testing.T) {
			if _, err := token.ParseScope([]byte(doc), opts); err != nil {
				t.Fatal(err)
			}
		})
	}
	bad := map[string]string{
		"a repository selector that is no key": grant(`"Not A Key"`, `"a"`, `"b"`),
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
