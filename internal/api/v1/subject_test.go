package v1

import (
	"testing"

	"github.com/guided-traffic/trove/internal/httpserver"
)

func TestR_AUTHZ_002_SubjectFrom(t *testing.T) {
	t.Run("R-AUTHZ-002 anonymous", func(t *testing.T) {
		s := SubjectFrom(httpserver.AnonymousPrincipal())
		if !s.Anonymous || s.Scope != nil || s.ID != "" {
			t.Fatalf("subject = %+v", s)
		}
	})
	t.Run("R-AUTHZ-013 scope is a ceiling and carries the binding", func(t *testing.T) {
		s := SubjectFrom(httpserver.Principal{
			Kind: httpserver.KindServiceAccount, ID: "id1", Name: "sa1",
			Groups: []httpserver.GroupRef{{ID: "g1", Name: "devs"}},
			Credential: httpserver.Credential{
				Method: httpserver.MethodBearer, TokenID: "t1", TokenType: httpserver.TokenSAT,
				Endpoints: []string{"customers"},
				Scope: &httpserver.Scope{Repositories: []httpserver.ScopeGrant{
					{Repositories: []string{"generic-local"}, Actions: []string{"read"}, Paths: httpserver.Patterns{Include: []string{"releases/**"}}},
				}},
			},
		})
		if s.Scope == nil || s.Scope.Inherit || len(s.Scope.Repositories) != 1 ||
			s.Scope.Repositories[0].Paths.Include[0] != "releases/**" || s.Scope.Endpoints[0] != "customers" {
			t.Fatalf("scope = %+v", s.Scope)
		}
		if s.TokenID != "t1" || len(s.Groups) != 1 || s.Groups[0].Name != "devs" {
			t.Fatalf("subject = %+v", s)
		}
	})
}

func TestQ_OPS_49_ActorFrom(t *testing.T) {
	a := ActorFrom(httpserver.Principal{
		Kind: httpserver.KindServiceAccount, ID: "id1", Name: "sa1",
		Credential: httpserver.Credential{Method: httpserver.MethodBearer, TokenID: "t1"},
	})
	if a.Kind != "service_account" || a.ID != "id1" || a.Name != "sa1" || a.TokenID != "t1" || a.AuthMethod != "bearer" {
		t.Fatalf("actor = %+v", a)
	}
	if got := ActorFrom(httpserver.AnonymousPrincipal()); got.Kind != "anonymous" || got.ID != "" {
		t.Fatalf("anonymous actor = %+v", got)
	}
}
