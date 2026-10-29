package app

import (
	"net/http"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
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
	principals       principal.Store
	principalManager *principal.Manager
	// provisioner is the seam the login area calls after an OIDC callback.
	provisioner   principal.Provisioner
	tokens        *token.Service
	authSettings  *token.SettingsStore
	authenticator auth.Authenticator
	// sessions resolves the session cookie; nil means no sessions.
	// WIRING POINT: the login area sets it before wireIdentity runs (its
	// marker precedes none of identity's; the development agent orders them).
	sessions session.Validator
	// failureLimiter is the count-only limiter auth.credential_failures; nil
	// means the per-replica one (WIRING POINT: the api area's cluster limiter).
	failureLimiter auth.FailureLimiter
	// publicOps are the patterns of the management API operations whose
	// declaration is public, or public by a switch.
	publicOps map[string]bool
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
// httpserver.Principal. The authenticator is read when a request arrives, so a
// chain built before step 8 of the start-up still uses the wired one.
func (a *App) authenticate(rt *endpointRuntime) func(*http.Request) (httpserver.Principal, error) {
	rules := endpointRules(rt)
	rules.PublicRoute = a.publicRoute(rt)
	return func(r *http.Request) (httpserver.Principal, error) {
		a.mu.Lock()
		stage := a.m1.authenticator
		a.mu.Unlock()
		if stage == nil {
			// Before step 8 of the start-up no request is served (the listener
			// opens at step 9); a request that arrives anyway is refused.
			return httpserver.Principal{}, errs.E(errs.Unavailable, errs.CodeUnavailable, "The replica is starting.", errs.WithRetryAfter(time.Second))
		}
		res, err := stage.Authenticate(r, rules)
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
