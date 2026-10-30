package app

import (
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/repository"
)

// The wiring of package ownership (R-AUTHZ-018, R-AUTHZ-019): the ownership
// service behind the owner operations of the management API, which the authz
// services hand to the handlers (wire_authz.go, authzServices), and the
// repository hook that removes the ownership rows of a deleted repository in the
// transaction that removes its row (wire_repository.go, the hook list).

// ownershipService returns the ownership service over the decision point of step
// 8, nil on a replica without PostgreSQL or before the authz wiring ran; the
// owner operations then answer 503. It records its events with the engine's
// recorder. The service holds nothing but the engine and the database handle,
// so a value built per call is the same service. It takes the app lock itself,
// so a caller must not hold it.
func (a *App) ownershipService() *authz.Ownership {
	a.mu.Lock()
	eng := a.m1.authzEngine
	a.mu.Unlock()
	db := a.database()
	if db == nil || eng == nil {
		return nil
	}
	svc, err := authz.NewOwnership(authz.OwnershipOptions{Engine: eng, DB: db})
	if err != nil {
		a.log.Error("ownership: the service could not be built", "err", err)
		return nil
	}
	return svc
}

// ownershipHook is the repository hook of package ownership (R-AUTHZ-018 rule 8,
// Q-AUTHZ-156): the deletion of a repository removes the ownership rows of its
// domain, which carry no foreign key to the row.
func ownershipHook() repository.Hooks { return authz.NewOwnershipHook() }
