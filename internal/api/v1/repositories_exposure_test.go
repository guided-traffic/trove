package v1

import (
	"context"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
)

// ceilingOnly is a decision point whose endpoint "customers" exposes only
// npm-customers and whose subject may read only generic-local.
type ceilingOnly struct{}

func (ceilingOnly) Decide(context.Context, authz.Subject, authz.Coordinate) authz.Decision {
	return authz.Decision{Outcome: authz.DenyHidden}
}

func (ceilingOnly) Readable(_ context.Context, _ authz.Subject, keys []string) ([]string, error) {
	return slices.DeleteFunc(slices.Clone(keys), func(k string) bool { return k != "generic-local" }), nil
}

func (ceilingOnly) Require(context.Context, authz.Subject, string) error { return nil }

type withExposure struct{ ceilingOnly }

func (withExposure) Exposed(ctx context.Context, _ authz.Subject, keys []string) ([]string, error) {
	if authz.EndpointOf(ctx) != "customers" {
		return keys, nil
	}
	return slices.DeleteFunc(slices.Clone(keys), func(k string) bool { return k != "npm-customers" }), nil
}

func TestR_EP_014_ConfigurationReadersAreBoundByTheCeiling(t *testing.T) {
	keys := []string{"generic-local", "npm-customers", "npm-internal"}
	customers := authz.WithEndpoint(context.Background(), "customers")
	t.Run("R-EP-014 Q-EP-12 a holder of repositories:read-config sees what the endpoint exposes", func(t *testing.T) {
		s := New(Options{Repositories: RepositoryServices{Authz: withExposure{}}})
		got, err := s.visibleKeys(customers, keys, true)
		if err != nil || !slices.Equal(got, []string{"npm-customers"}) {
			t.Fatalf("%v %v", got, err)
		}
		got, err = s.visibleKeys(context.Background(), keys, true)
		if err != nil || !slices.Equal(got, keys) {
			t.Fatalf("without an endpoint: %v %v", got, err)
		}
	})
	t.Run("R-AUTHZ-016 a caller without it sees what it may read, the decision point narrows that by exposure", func(t *testing.T) {
		s := New(Options{Repositories: RepositoryServices{Authz: withExposure{}}})
		got, err := s.visibleKeys(customers, keys, false)
		if err != nil || !slices.Equal(got, []string{"generic-local"}) {
			t.Fatalf("%v %v", got, err)
		}
	})
	t.Run("R-EP-013 a request of an endpoint without a decision point that applies the ceiling fails closed", func(t *testing.T) {
		s := New(Options{Repositories: RepositoryServices{Authz: ceilingOnly{}}})
		_, err := s.visibleKeys(customers, keys, true)
		if e, ok := errs.As(err); !ok || e.Kind != errs.Unavailable {
			t.Fatalf("%v", err)
		}
	})
}
