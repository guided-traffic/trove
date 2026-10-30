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

func (withExposure) Exposed(_ context.Context, sub authz.Subject, keys []string) ([]string, error) {
	if sub.Endpoint != "customers" {
		return keys, nil
	}
	return slices.DeleteFunc(slices.Clone(keys), func(k string) bool { return k != "npm-customers" }), nil
}

func TestR_EP_014_ConfigurationReadersAreBoundByTheCeiling(t *testing.T) {
	keys := []string{"generic-local", "npm-customers", "npm-internal"}
	ctx := context.Background()
	customers := authz.Subject{Endpoint: "customers"} // the subject edge.SubjectFrom builds for a request of the endpoint
	t.Run("R-EP-014 Q-EP-12 a holder of repositories:read-config sees what the endpoint exposes", func(t *testing.T) {
		s := New(Options{Repositories: RepositoryServices{Authz: withExposure{}}})
		got, err := s.visibleKeys(ctx, customers, keys, true)
		if err != nil || !slices.Equal(got, []string{"npm-customers"}) {
			t.Fatalf("%v %v", got, err)
		}
		got, err = s.visibleKeys(ctx, authz.Subject{}, keys, true)
		if err != nil || !slices.Equal(got, keys) {
			t.Fatalf("without an endpoint: %v %v", got, err)
		}
	})
	t.Run("R-AUTHZ-016 a caller without it sees what it may read, the decision point narrows that by exposure", func(t *testing.T) {
		s := New(Options{Repositories: RepositoryServices{Authz: withExposure{}}})
		got, err := s.visibleKeys(ctx, customers, keys, false)
		if err != nil || !slices.Equal(got, []string{"generic-local"}) {
			t.Fatalf("%v %v", got, err)
		}
	})
	t.Run("R-EP-013 a request of an endpoint without a decision point that applies the ceiling fails closed", func(t *testing.T) {
		s := New(Options{Repositories: RepositoryServices{Authz: ceilingOnly{}}})
		_, err := s.visibleKeys(ctx, customers, keys, true)
		if e, ok := errs.As(err); !ok || e.Kind != errs.Unavailable {
			t.Fatalf("%v", err)
		}
	})
}
