package app

import (
	"net/http"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/repository"
)

// m1Wiring holds the components the M1 areas build in bootstrap (the WIRING
// POINTs of app.go) and other areas read. A field is set once, before the
// client listener starts, and never changes afterwards, so it needs no lock.
// Each area declares its fields only inside its own markers
// (docs/developer/m1-contracts.md). A nil field means the area is not wired and
// the M0 stand-in of access.go applies.
type m1Wiring struct {
	// >>> M1 audit
	// recorder writes audit events (R-OPS-015); set at step 4 on every role with
	// a main pool, nil elsewhere. auditStore reads the trail (R-OPS-017) and runs
	// the partition maintenance of the audit-retention task.
	recorder   audit.Recorder
	auditStore *audit.Store
	// <<< M1 audit

	// >>> M1 api
	// rateLimit is stage 7 of the chain (R-ARCH-005, R-API-016); nil: none.
	rateLimit func(http.Handler) http.Handler
	// <<< M1 api

	// >>> M1 identity
	// principals principal.Store (declare it with the first use)
	authenticator auth.Authenticator
	// <<< M1 identity

	// >>> M1 login
	// <<< M1 login

	// >>> M1 authz
	authorizer authz.Authorizer
	// <<< M1 authz

	// >>> M1 repository
	// repositories answers the per-request lookup of a repository from the
	// replica's snapshot (R-ARCH-011). repositoryService is the write side
	// (R-REPO-009 to R-REPO-013); it is read by the deletion job under a.mu, because
	// a job can run while the bootstrap still wires.
	repositories      repository.Reader
	repositoryService *repository.Service
	// <<< M1 repository

	// >>> M1 files
	// <<< M1 files

	// >>> M1 endpoints
	// <<< M1 endpoints
}

// authenticate returns the authentication stage of an endpoint's chain: the
// authenticator of the identity area behind the conversion to
// httpserver.Principal, or the M0 stand-in while none is wired.
func (a *App) authenticate(rt *endpointRuntime) func(*http.Request) (httpserver.Principal, error) {
	if a.m1.authenticator == nil {
		return bootstrapAuthenticate
	}
	return func(r *http.Request) (httpserver.Principal, error) {
		res, err := a.m1.authenticator.Authenticate(r, endpointRules(rt))
		if err != nil {
			return httpserver.Principal{}, err
		}
		return principalFromResult(res), nil
	}
}

// access returns the permission check of the management API: the decision point
// of the authz area, or the M0 stand-in while it is not wired.
func (a *App) access() v1.Access {
	if a.m1.authorizer == nil {
		return bootstrapAccess{anonymousRead: a.cfg.System.BootstrapAnonymousRead}
	}
	return v1.NewAccess(a.m1.authorizer)
}
