package bastionmain

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/bastion/iface"
)

// reachResolver implements [ShapeResolver] over the decision point's reach classification (R-BAS-012). The
// rules are authz.Engine.ReachOn's; this type maps them to the wire's shapes and to the status a refused read
// of the repository gets.
type reachResolver struct{ r authz.Reacher }

// NewShapeResolver returns the [ShapeResolver] over the decision point.
func NewShapeResolver(r authz.Reacher) ShapeResolver { return reachResolver{r: r} }

// Shapes implements [ShapeResolver].
func (s reachResolver) Shapes(ctx context.Context, idx *authz.Index, subject authz.Subject, keys []qname.Repo) (map[qname.Repo]Shape, error) {
	got, err := s.r.ReachOn(ctx, idx, subject, keys)
	if err != nil {
		return nil, err
	}
	out := make(map[qname.Repo]Shape, len(got))
	for k, res := range got {
		out[k] = shapeOf(res)
	}
	return out, nil
}

func shapeOf(res authz.ReachResult) Shape {
	switch res.Reach {
	case authz.ReachFull:
		return Shape{Read: iface.ReadFull}
	case authz.ReachPartial:
		return Shape{Read: iface.ReadPartial}
	}
	switch res.Denial {
	case authz.DenyUnauthenticated:
		return Shape{Read: iface.ReadNone, Status: http.StatusUnauthorized}
	case authz.DenyVisible:
		return Shape{Read: iface.ReadNone, Status: http.StatusForbidden}
	}
	return Shape{Read: iface.ReadNone, Status: http.StatusNotFound}
}
