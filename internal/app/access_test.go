package app

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/api/edge"
	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/qname"
)

// beginAccess is an Access that takes an index of its own, as the access over the
// decision point does.
type beginAccess struct {
	v1.Access
	begun []string
}

func (b *beginAccess) Begin(_ context.Context, permission string, _ ...qname.Repo) (*edge.Access, error) {
	b.begun = append(b.begun, permission)
	return &edge.Access{}, nil
}

func TestR_AUTHZ_015_TheBridgeBeginsThroughTheAccessBehindIt(t *testing.T) {
	t.Run("R-AUTHZ-015 a permission the bridge does not open takes the index of the decision point", func(t *testing.T) {
		inner := &beginAccess{}
		a, err := bridgeAccess{inner: inner}.Begin(t.Context(), "settings:read", qr("generic-local"))
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

// projectInner answers the project questions of the Access behind the bridge.
type projectInner struct {
	v1.Access
	members []authz.Membership
}

func (p projectInner) Memberships(context.Context) ([]authz.Membership, error) {
	return p.members, nil
}

func (p projectInner) Owns(_ context.Context, project string) (bool, error) {
	return project == "web", nil
}

func TestR_AUTHZ_026_TheBridgeForwardsTheProjectQuestions(t *testing.T) {
	t.Run("R-AUTHZ-026 the bridge answers the project questions of the access behind it", func(t *testing.T) {
		b := bridgeAccess{inner: projectInner{members: []authz.Membership{{Key: "web", Role: authz.ProjectRoleOwner}}}}
		got, err := b.Memberships(t.Context())
		if err != nil || len(got) != 1 || got[0].Key != "web" {
			t.Fatalf("%v %v", got, err)
		}
		if ok, err := b.Owns(t.Context(), "web"); err != nil || !ok {
			t.Fatalf("%v %v", ok, err)
		}
	})
	t.Run("R-AUTHZ-026 without a decision point the caller is a member of no project", func(t *testing.T) {
		got, err := bridgeAccess{}.Memberships(t.Context())
		if err != nil || len(got) != 0 {
			t.Fatalf("%v %v", got, err)
		}
		if ok, _ := (bridgeAccess{}).Owns(t.Context(), "web"); ok {
			t.Fatal("owns")
		}
	})
}
