package app

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/api/edge"
	v1 "github.com/guided-traffic/trove/internal/api/v1"
)

// beginAccess is an Access that takes an index of its own, as the access over the
// decision point does.
type beginAccess struct {
	v1.Access
	begun []string
}

func (b *beginAccess) Begin(_ context.Context, permission string, _ ...string) (*edge.Access, error) {
	b.begun = append(b.begun, permission)
	return &edge.Access{}, nil
}

func TestR_AUTHZ_015_TheBridgeBeginsThroughTheAccessBehindIt(t *testing.T) {
	t.Run("R-AUTHZ-015 a permission the bridge does not open takes the index of the decision point", func(t *testing.T) {
		inner := &beginAccess{}
		a, err := bridgeAccess{inner: inner}.Begin(t.Context(), "settings:read", "generic-local")
		if err != nil || a == nil || len(inner.begun) != 1 || inner.begun[0] != "settings:read" {
			t.Fatalf("holder %v, begun %v, %v", a, inner.begun, err)
		}
	})
	t.Run("R-AUTHZ-015 without a decision point there is nothing to begin and the step refuses by itself", func(t *testing.T) {
		a, err := bridgeAccess{}.Begin(t.Context(), "settings:read")
		if err != nil || a != nil {
			t.Fatalf("holder %v, %v", a, err)
		}
	})
	t.Run("R-AUTHZ-015 a missing decision point is not wrapped, and a replica without the authz area has none", func(t *testing.T) {
		if b := newBridgeAccess(nil, false); b.inner != nil {
			t.Fatal("the bridge wrapped a missing decision point")
		}
		// A nil *authz.Engine in the interface would not be nil: decider answers the untyped nil.
		if d := (&App{parts: &wiring{}}).decider(); d != nil {
			t.Fatalf("decider = %v on a replica without the authz area", d)
		}
	})
}
