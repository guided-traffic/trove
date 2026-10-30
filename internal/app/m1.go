package app

import (
	"context"
	"net/http"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/auth/oidc"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/files"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/items"
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
	// preflight answers the CORS preflight of an allowed origin before the
	// authentication stage (R-API-018); nil: none.
	preflight func(http.Handler) http.Handler
	// limiters is the replica's cluster limiter set (R-ARCH-034), built once by
	// limiterSet; the identity stage takes its failure throttle from it.
	limiters *cluster.LimiterSet
	// replicas is the count n of the pool's live replicas that every limiter of
	// the client listener divides its fallback share by (replicaCount).
	replicas *replicaCount
	// <<< M1 api

	// >>> M1 identity
	// authMetrics are the identity metrics, shared with the login (authMetrics).
	authMetrics      *principal.Metrics
	principals       principal.Store
	principalManager *principal.Manager
	// provisioner is the seam the login area calls after an OIDC callback.
	provisioner   principal.Provisioner
	tokens        *token.Service
	authSettings  *token.SettingsStore
	authenticator auth.Authenticator
	// failureLimiter is the count-only limiter auth.credential_failures; nil
	// means the per-replica one.
	failureLimiter auth.FailureLimiter
	// flowStarts is the limiter auth.flow_starts of the unauthenticated
	// row-creating endpoints (R-AUTH-038); nil on a replica without a client
	// listener.
	flowStarts *cluster.Limiter
	// publicOps are the patterns of the management API operations whose
	// declaration is public, or public by a switch.
	publicOps map[string]bool
	// <<< M1 identity

	// >>> M1 login
	// sessions is the session store of web and all replicas: the
	// session.Validator the authentication stage of the identity area takes
	// (wireSessions runs before wireIdentity), and EndForPrincipal for disabling
	// a user (R-AUTH-016). Nil on a worker.
	sessions *session.Store
	// login is the OIDC and break-glass login whose routes startClient registers
	// (wire_login.go). Nil on a worker.
	login *oidc.Service
	// <<< M1 login

	// >>> M1 authz
	authorizer authz.Authorizer
	// authzEngine is the same component with the methods beyond the contract
	// (Check, Effective, RecordAllowed); the policy service of wave 2 reads it.
	authzEngine *authz.Engine
	// authzService is the policy and role-binding service behind the permission
	// operations (R-AUTHZ-021 to R-AUTHZ-023). It is also the principal.AdminBinder
	// of `service-account create --admin`. Nil until the authz wiring ran, and on a
	// replica without PostgreSQL or without the identity wiring.
	authzService *authz.Service
	// anonymousAccess is the runtime switch of R-AUTH-037 (YAML ceiling and
	// auth_settings.anonymous_access); the identity area sets it before the authz
	// wiring runs. Nil means anonymous access is off.
	anonymousAccess func(ctx context.Context) (bool, error)
	// <<< M1 authz

	// >>> M1 repository
	// repositories answers the per-request lookup of a repository from the
	// replica's snapshot (R-ARCH-011). repositoryService is the write side
	// (R-REPO-009 to R-REPO-013); it is read by the deletion job under a.mu, because
	// a job can run while the bootstrap still wires.
	repositories      repository.Reader
	repositoryService *repository.Service
	// <<< M1 repository

	// >>> M2 virtual
	// routing is the caller-independent routing of virtual repositories (members,
	// pin rules, claims; R-REPO-019 steps 2 and 3, R-REPO-023). The virtual engine
	// and the formats' publish transaction read it under a.mu; nil on a replica
	// without PostgreSQL.
	routing *repository.Routing
	// <<< M2 virtual

	// >>> M1 files
	// files is the files transfer service (R-STOR-007 and the items operations);
	// nil on a replica without PostgreSQL or the object store, where /files/
	// answers 503. It is read under a.mu, because a job can run while the
	// bootstrap still wires.
	files *files.Service
	// items is the item store the files service and the delete job run on; nil
	// where files is.
	items *items.Store
	// <<< M1 files

	// >>> WP2.1 formats
	// formats is the format wiring (wire_formats.go): the shared dependencies of the
	// storage hooks and the Host, and the Host and the dispatcher once built. Nil
	// where the item store is.
	formats *formatsWiring
	// <<< WP2.1 formats

	// >>> M1 endpoints
	// exposureCeiling is the endpoint exposure of R-EP-013 that the decision point
	// applies, built once from the configured endpoints (exposureCeiling).
	exposureCeiling *authz.ExposureCeiling
	// exposure is the exposure-rule service of the endpoint operations
	// (R-EP-016); nil on a replica without PostgreSQL.
	exposure *authz.ExposureService
	// endpointLimits are the rate-limit stages of the endpoints' classes by
	// endpoint name (R-EP-015); an endpoint without a class has none.
	endpointLimits map[string]func(http.Handler) http.Handler
	// <<< M1 endpoints
}

// authenticate returns the authentication stage of an endpoint's chain: the
// authenticator of the identity area behind the conversion to
// httpserver.Principal. The authenticator is read when a request arrives, so a
// chain built before step 8 of the start-up still uses the wired one.
func (a *App) authenticate(rt *endpointRuntime) func(*http.Request) (httpserver.Principal, error) {
	rules := endpointRules(rt)
	rules.PublicRoute = a.publicRoute(rt)
	rules.Refused = a.credentialRefused(rt)
	return withMinEpoch(func(r *http.Request) (httpserver.Principal, error) {
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
		p := principalFromResult(res)
		recordPrincipal(r, p)
		return p, nil
	})
}

// access returns the permission check of the management API: the decision point
// of the authz area behind the dev-profile bridge of system.bootstrap_anonymous_read
// (access.go). While the decision point is not wired (a replica without
// PostgreSQL), nobody holds a permission.
func (a *App) access() v1.Access {
	return newBridgeAccess(a.m1.authorizer, a.cfg.System.BootstrapAnonymousRead)
}
