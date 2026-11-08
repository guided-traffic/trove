package app

import (
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/registrytoken"
	"github.com/guided-traffic/trove/internal/qname"
)

// R-OCI-005: named in the test name by WP3 once /v2/ is mounted and the grant check runs.
func TestTheRegistryGrantCrossesTheSeam(t *testing.T) {
	repo := qname.Repo{Project: "web", Key: "docker-local"}
	exp := time.Date(2026, 11, 8, 10, 5, 0, 0, time.UTC)
	grant := &registrytoken.Grant{Entries: []registrytoken.Entry{{Repo: repo, Name: "app", Actions: registrytoken.Pull | registrytoken.Delete}}, Catalog: true}

	t.Run("a registry token of a principal keeps its trove token's facts and gains the grant", func(t *testing.T) {
		p := principalFromResult(auth.Result{
			Principal:  principal.Principal{ID: "u1", Kind: principal.KindUser, Name: "alice"},
			Credential: auth.Credential{Method: auth.MethodBearer, TokenID: "t1", TokenType: "pat", ExpiresAt: &exp, Registry: grant},
		})
		g := p.Credential.Registry
		if p.Credential.TokenID != "t1" || g == nil || !g.Catalog || !g.Allows(repo, "app", "pull") || !g.Allows(repo, "app", "delete") ||
			g.Allows(repo, "app", "push") || g.Allows(repo, "other", "pull") {
			t.Fatalf("principal %+v grant %+v", p, g)
		}
	})
	t.Run("an anonymous registry token stays anonymous and keeps its grant", func(t *testing.T) {
		res := auth.Result{Anonymous: true, Credential: auth.Credential{Method: auth.MethodBearer, ExpiresAt: &exp, Registry: &registrytoken.Grant{}}}
		p := principalFromResult(res)
		if !p.Anonymous || p.Credential.Registry == nil || p.Credential.Registry.Allows(repo, "app", "pull") {
			t.Fatalf("principal %+v", p)
		}
	})
	t.Run("every other credential has no grant: nil, not an empty one", func(t *testing.T) {
		if p := principalFromResult(auth.Result{Credential: auth.Credential{Method: auth.MethodBasic, TokenID: "t1"}}); p.Credential.Registry != nil {
			t.Fatal("a direct credential carries a grant")
		}
		if p := principalFromResult(auth.Result{Anonymous: true}); p.Credential.Registry != nil {
			t.Fatal("a request without a credential carries a grant")
		}
		var none *registrytoken.Grant
		if registryGrant(none) != nil {
			t.Fatal("a nil grant converted to one")
		}
	})
}
