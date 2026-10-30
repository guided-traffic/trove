package app

import (
	"context"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/repository"
)

// AreaOwnership is package ownership.
const AreaOwnership Area = "ownership"

// The wiring of package ownership (R-AUTHZ-018, R-AUTHZ-019): the ownership
// service behind the owner operations of the management API, and the repository
// hook that removes the ownership rows of a deleted repository in the transaction
// that removes its row (wire_repository.go, the hook list).

// wireOwnership builds the ownership service over the decision point. It records
// its events with the engine's recorder and holds nothing but the engine and the
// database handle.
func (a *App) wireOwnership(_ context.Context, in infra, az authzArea) (ownershipArea, error) {
	if err := requireInputs(AreaOwnership, given("decision point", az.engine)); err != nil {
		return ownershipArea{}, err
	}
	svc, err := authz.NewOwnership(authz.OwnershipOptions{Engine: az.engine, DB: in.db})
	if err != nil {
		return ownershipArea{}, err
	}
	return ownershipArea{service: svc}, nil
}

// ownershipHook is the repository hook of package ownership (R-AUTHZ-018 rule 8,
// Q-AUTHZ-156): the deletion of a repository removes the ownership rows of its
// domain, which carry no foreign key to the row. It depends on nothing, so the
// repository area adds it itself.
func ownershipHook() repository.Hooks { return authz.NewOwnershipHook() }
