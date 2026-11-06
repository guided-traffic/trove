//go:build integration

package identity

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/errs"
)

// TestR_AUTH_024_ASelectorOfAScopeNamesTheProject: the selectors of a token scope are written as in every
// instance context (R-AUTHZ-004, Q-AUTHZ-216): project and key, a pattern of both, or a class. A bare key is
// 400 invalid_scope, never a selector that silently matches nothing.
func TestR_AUTH_024_ASelectorOfAScopeNamesTheProject(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead})
	alice := a.user("alice")
	ctx := context.Background()
	create := func(selector string) error {
		_, err := a.tokens.Create(ctx, token.CreateRequest{
			Caller: admin, Type: token.TypePAT, OwnerID: alice.ID, Description: "scoped", Via: "api",
			Scope: []byte(`{"repositories":[{"repositories":["` + selector + `"],"actions":["read"]}]}`),
		})
		return err
	}
	for _, ok := range []string{"web/npm-local", "web/npm-*", "*/npm-remote", "w*/n?m-*", "@any", "@any-local", "@any-remote:npm", "web/npm-*:npm"} {
		if err := create(ok); err != nil {
			t.Errorf("R-AUTH-024 %q: %v", ok, err)
		}
	}
	for _, bad := range []string{"npm-local", "npm-*", "*", "web/", "/npm-local", "web/npm/local", "WEB/npm-local", "web/npm-local:npm", "@any-thing"} {
		err := create(bad)
		if e, ok := errs.As(err); !ok || e.Status() != 400 || e.Code != errs.CodeInvalidScope {
			t.Errorf("R-AUTH-024 %q: %v, want 400 invalid_scope", bad, err)
		}
	}
}
