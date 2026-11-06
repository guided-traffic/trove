package bastionmain

import (
	"context"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/bastion/iface"
)

// Shape is a subject's read shape on one repository and, for none, the status the subject would receive for a
// read of it (R-BAS-011, R-BAS-012).
type Shape struct {
	Read iface.Read
	// Status is 401 (anonymous), 403 or 404 (R-AUTHZ-016, R-AUTHZ-017) when Read is none, and 0 otherwise.
	Status int
}

// ShapeResolver computes the read shapes of a subject (R-BAS-012, Q-BAS-28, Q-BAS-51). Work package 3
// implements it, because the marking asks it on the hot path of every forwarded read; the check operation of
// work package 2 asks it for a credential it has just validated. Both ask on the index of their request or
// their check, and both pass the subject with the endpoint of the default endpoint, so the exposure ceiling of
// R-EP-013 applies (an exposure rule makes every shape on the exposed repository partial at most).
//
// The rules, in terms of the decision point:
//
//   - full: for a local or remote repository, authz.ReadAll of the subject's authz.ReadScoper and, for a
//     remote repository, the populate right of R-AUTHZ-008; for a virtual repository, the gate read on the
//     repository itself (R-AUTHZ-009 rule 1) and full on every member, recursively.
//   - none: no gate, ReadNone on every member, an anonymous subject on a repository that requires
//     authentication or with anonymous access off, an unknown key.
//   - partial: every other case.
//
// TestR_BAS_012_ShapeAgreesWithTheDecisionPoint is the acceptance test of the implementation.
type ShapeResolver interface {
	// Shapes returns one entry per key, in the keys' terms; an unknown key is none with the status of a missing
	// repository for that subject. The answer is computed on idx, so it agrees with every other decision of
	// the request.
	Shapes(ctx context.Context, idx *authz.Index, subject authz.Subject, keys []qname.Repo) (map[qname.Repo]Shape, error)
}
