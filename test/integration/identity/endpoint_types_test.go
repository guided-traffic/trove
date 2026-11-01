//go:build integration

package identity

import (
	"context"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/test/integration/idapi"
)

// endpointRules adds the endpoint rules of the harness (idapi.ScopeOptions) to the
// scope options of a replica's token service: the service refuses a binding to an
// endpoint that does not accept the token's type (R-EP-011).
func endpointRules(o scope.Options) scope.Options {
	e := idapi.ScopeOptions()
	o.Endpoint, o.EndpointAccepts = e.Endpoint, e.EndpointAccepts
	return o
}

const (
	boundToCustomers = `{"inherit":true,"endpoints":["customers"]}` // accepts service-account tokens only
	boundToInternal  = `{"inherit":true,"endpoints":["internal"]}`  // accepts personal tokens only
)

// TestR_EP_011_TheServiceRefusesABindingTheTypeCannotUse: the check is the token
// service's, so every way to create a token (the API, the command, a test) has it,
// and so has a rotation, which would otherwise copy a binding that works nowhere.
func TestR_EP_011_TheServiceRefusesABindingTheTypeCannotUse(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{})
	alice := a.user("alice")
	sa := a.serviceAccount("ci-bot")
	ctx := context.Background()

	t.Run("R-EP-011 a create refuses it, naming the endpoint, and accepts the types the endpoint takes", func(t *testing.T) {
		_, err := a.tokens.Create(ctx, token.CreateRequest{Caller: admin, Type: token.TypePAT, OwnerID: alice.ID, Description: "x", Scope: []byte(boundToCustomers), Via: "api"})
		wantCode(t, err, 400, "invalid_scope")
		if !strings.Contains(err.Error(), "customers") {
			t.Fatalf("the refusal does not name the endpoint: %v", err)
		}
		_, err = a.tokens.Create(ctx, token.CreateRequest{Caller: admin, Type: token.TypeSAT, OwnerID: sa.ID, Description: "x", Scope: []byte(boundToInternal), Via: "admin-cli", Admin: true})
		wantCode(t, err, 400, "invalid_scope")
		if _, err = a.tokens.Create(ctx, token.CreateRequest{Caller: admin, Type: token.TypeSAT, OwnerID: sa.ID, Description: "x", Scope: []byte(boundToCustomers), Via: "admin-cli", Admin: true}); err != nil {
			t.Fatalf("a service-account token on customers: %v", err)
		}
	})

	t.Run("R-EP-011 Q-AUTH-182 a rotation refuses a binding the type cannot use and leaves the old token as it was", func(t *testing.T) {
		// A personal token bound to customers cannot be created any more; one made before the check existed can be rotated no better.
		old := a.pat(alice)
		e.exec(`UPDATE api_token SET scope = $2::text::jsonb WHERE id = $1::uuid`, old.Record.ID, boundToCustomers)
		_, err := a.tokens.Rotate(ctx, token.RotateRequest{Caller: admin, ID: old.Record.ID, OverlapSeconds: 0})
		wantCode(t, err, 400, "invalid_scope")
		if !strings.Contains(err.Error(), "customers") {
			t.Fatalf("the refusal does not name the endpoint: %v", err)
		}
		recs, err := a.tokens.List(ctx, token.ListFilter{OwnerID: alice.ID})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range recs {
			found = found || r.ID == old.Record.ID
		}
		if !found {
			t.Fatal("the token a refused rotation was asked for is no longer active")
		}
		// A binding the type can use rotates.
		fine := a.pat(alice)
		e.exec(`UPDATE api_token SET scope = $2::text::jsonb WHERE id = $1::uuid`, fine.Record.ID, boundToInternal)
		if _, err := a.tokens.Rotate(ctx, token.RotateRequest{Caller: admin, ID: fine.Record.ID, OverlapSeconds: 0}); err != nil {
			t.Fatalf("rotating a personal token on an endpoint that takes them: %v", err)
		}
	})
}
