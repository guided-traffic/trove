package v1

import (
	"context"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
)

// ceilingOnly is a decision point whose endpoint "customers" exposes only
// npm-customers and whose subject may read only generic-local.
type ceilingOnly struct {
	authz.Decider // unimplemented forms panic: a test names what it uses
}

func (ceilingOnly) Begin(context.Context, authz.Subject, ...string) (*authz.Index, error) {
	return authz.BuildIndex(&authz.Snapshot{}, nil, nil), nil
}

func (ceilingOnly) ReadableOn(_ context.Context, _ *authz.Index, _ authz.Subject, keys []string) ([]string, error) {
	return slices.DeleteFunc(slices.Clone(keys), func(k string) bool { return k != "generic-local" }), nil
}

func (ceilingOnly) ExposedOn(_ *authz.Index, sub authz.Subject, keys []string) []string {
	if sub.Endpoint != "customers" {
		return keys
	}
	return slices.DeleteFunc(slices.Clone(keys), func(k string) bool { return k != "npm-customers" })
}

func TestR_EP_014_ConfigurationReadersAreBoundByTheCeiling(t *testing.T) {
	keys := []string{"generic-local", "npm-customers", "npm-internal"}
	ctx := context.Background()
	holder := func(t *testing.T, sub authz.Subject) *edge.Access {
		t.Helper()
		acc, err := edge.Begin(ctx, ceilingOnly{}, sub)
		if err != nil {
			t.Fatal(err)
		}
		return acc
	}
	customers := authz.Subject{Endpoint: "customers"} // the subject edge.SubjectFrom builds for a request of the endpoint
	s := New(Options{})
	t.Run("R-EP-014 Q-EP-12 a holder of repositories:read-config sees what the endpoint exposes", func(t *testing.T) {
		got, err := s.visibleKeys(ctx, holder(t, customers), keys, true)
		if err != nil || !slices.Equal(got, []string{"npm-customers"}) {
			t.Fatalf("%v %v", got, err)
		}
		got, err = s.visibleKeys(ctx, holder(t, authz.Subject{}), keys, true)
		if err != nil || !slices.Equal(got, keys) {
			t.Fatalf("without an endpoint: %v %v", got, err)
		}
	})
	t.Run("R-AUTHZ-016 a caller without it sees what it may read, the decision point narrows that by exposure", func(t *testing.T) {
		got, err := s.visibleKeys(ctx, holder(t, customers), keys, false)
		if err != nil || !slices.Equal(got, []string{"generic-local"}) {
			t.Fatalf("%v %v", got, err)
		}
	})
	t.Run("R-EP-013 a request without a decision point sees nothing, whoever it is", func(t *testing.T) {
		for _, cfg := range []bool{false, true} {
			_, err := s.visibleKeys(ctx, nil, keys, cfg)
			if e, ok := errs.As(err); !ok || e.Kind != errs.Unavailable {
				t.Errorf("cfg %v: %v", cfg, err)
			}
		}
	})
}
