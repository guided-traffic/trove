package app

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/api/login"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/auth/oidc"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/files"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/secrets"
)

// The typed results of the wiring steps (steps.go). Each is the product of one
// area and is handed by value to the steps that need it; none is read by a step
// that did not list it as an input.

// keysArea is the key ring and the outbound factory (R-ARCH-021). Both are pure
// configuration: the shell profile has them too.
type keysArea struct {
	ring *secrets.KeyRing
	out  *outbound.Factory
}

// postgresArea is the connected database and the system facts read from it once
// (R-ARCH-030).
type postgresArea struct {
	db    *postgres.DB
	facts postgres.SystemFacts
}

// auditArea is the recorder every writer records through and the store that reads
// and maintains the trail.
type auditArea struct {
	rec   audit.Recorder
	store *audit.Store
}

// blobArea is the blob store; gc is the collector and nil on a replica that
// claims no jobs.
type blobArea struct {
	store *blob.Store
	gc    *blob.Collector
}

// valkeyArea is the Valkey client; it never blocks on an unreachable Valkey
// (degraded mode, Q-HA-13).
type valkeyArea struct{ vk *cluster.Valkey }

// coordArea is the coordination of the replica: signals, the change log, the
// registry of in-process caches, the job queue and, on a replica that claims
// jobs, the lease manager (nil elsewhere). vk is the client the bus runs on.
type coordArea struct {
	signals *cluster.Signals
	changes *cluster.Changes
	caches  *cache.Registry
	queue   *jobs.Queue
	leases  *cluster.Leases
	vk      *cluster.Valkey
}

// infra is what every domain area takes: the shared components, all non-nil on a
// replica that has infrastructure. A role without the infra area (the shell
// profile) builds no domain area, so a domain step never sees a nil member.
type infra struct {
	db      *postgres.DB
	facts   postgres.SystemFacts
	changes *cluster.Changes
	caches  *cache.Registry
	signals *cluster.Signals
	queue   *jobs.Queue
	blobs   *blob.Store
	vk      *cluster.Valkey
	ring    *secrets.KeyRing
	out     *outbound.Factory
	rec     audit.Recorder
}

// The results of the domain steps. A member is nil only in a type that a role
// does not build at all; inside a built area every member is set.

// keyRingArea is the result of the key-ring canary: the ring matches the
// database. It carries nothing; a step that needs the ring checked takes it.
type keyRingArea struct{}

// exposureArea is the exposure ceiling the decision point applies and the rule
// service of the endpoint operations (R-EP-013, R-EP-016).
type exposureArea struct {
	ceiling *authz.ExposureCeiling
	rules   *authz.ExposureService
}

// repositoryArea is the snapshot reader (R-ARCH-011) and the write side
// (R-REPO-009 to R-REPO-013).
type repositoryArea struct {
	reader repository.Reader
	// snapshot is the same reader as the concrete type the routing is built on.
	snapshot *repository.SnapshotReader
	service  *repository.Service
}

// identityArea is the stores and services of the identity area. anonymousAccess
// is the runtime switch of R-AUTH-037 that the decision point reads.
type identityArea struct {
	metrics         *principal.Metrics
	principals      principal.Store
	manager         *principal.Manager
	provisioner     principal.Provisioner
	tokens          *token.Service
	settings        *token.SettingsStore
	anonymousAccess func(ctx context.Context) (bool, error)
}

// authzArea is the decision point and the policy service. The engine is the
// authz.Decider the request paths decide on.
type authzArea struct {
	engine  *authz.Engine
	service *authz.Service
}

// filesArea is the item store, the generic layer over it and the decider the
// entry points of the files transfer begin their requests' indexes on.
type filesArea struct {
	items   *items.Store
	service *files.Service
	decider authz.Decider
}

// ownershipArea is the package ownership service behind the owner operations of
// the management API (R-AUTHZ-018, R-AUTHZ-019).
type ownershipArea struct{ service *authz.Ownership }

// limitsArea is the cluster limiter set of a replica with a client listener, the
// live-replica count its fallback divides by and the two limiters of the
// identity area (R-AUTH-038).
type limitsArea struct {
	set        *cluster.LimiterSet
	replicas   *replicaCount
	failures   auth.FailureLimiter
	flowStarts *cluster.Limiter
}

// sessionsArea is the session store: the session.Validator of the authentication
// stage and EndForPrincipal for disabling a user (R-AUTH-016).
type sessionsArea struct{ store *session.Store }

// loginArea is the OIDC and break-glass login and the handler of its browser
// routes, which every endpoint's router mounts.
type loginArea struct {
	service *oidc.Service
	handler *login.Handler
}

// authArea is the authentication stage of the client listeners' chains and the
// patterns of the management API operations that are public.
type authArea struct {
	stage     auth.Authenticator
	publicOps map[string]bool
}

// shellArea is the authentication stage of the shell profile over an empty token
// store, so the shell has one authentication code path as well.
type shellArea struct{ auth authArea }

// endpointsArea is the rate-limit stage of each endpoint that names a class
// (R-EP-015), by endpoint name.
type endpointsArea struct {
	limits map[string]func(http.Handler) http.Handler
}

// apiArea is stage 7 of the chain and the CORS preflight (R-API-016, R-API-018).
type apiArea struct {
	rateLimit func(http.Handler) http.Handler
	preflight func(http.Handler) http.Handler
}
