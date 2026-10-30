package app

import (
	"fmt"
	"slices"
)

// Area names one unit of wiring: the step in steps.go that builds a typed part
// of the replica (R-ARCH-024 steps 3 to 8). A role enables a set of areas as
// data (SubsystemsFor). A step whose area the role does not enable does not run,
// and a step that needs what such an area builds stops the start-up in New
// (planSteps), not at the first request.
type Area string

// The areas of the infrastructure steps. A domain area is added with its
// wire_<area>.go (docs/developer/architecture.md, "How to add a wiring step").
const (
	// AreaKeys is the key ring and the outbound factory: configuration objects
	// that need no infrastructure, so even the shell profile has them.
	AreaKeys Area = "keys"
	// AreaPostgres is the pools, the schema check and the system facts (step 3).
	AreaPostgres Area = "postgres"
	// AreaAudit is the partition preparation and the recorder (step 4).
	AreaAudit Area = "audit"
	// AreaBlob is the object-store client, the blob store and, on a replica that
	// claims jobs, the collector (step 6).
	AreaBlob Area = "blob"
	// AreaValkey is the Valkey client (step 7).
	AreaValkey Area = "valkey"
	// AreaCoordination is signals, change log, caches, replica registry and the
	// job queue or worker (step 7).
	AreaCoordination Area = "coordination"
	// AreaInfra is the typed bundle of the above that every domain step takes.
	AreaInfra Area = "infra"
)

// The domain areas (R-ARCH-024 step 8). A role that serves a listener enables the
// serving areas on top of the ones every role has.
const (
	// AreaKeyRing is the start-up canary of the key ring (R-ARCH-021).
	AreaKeyRing Area = "keyring"
	// AreaExposure is the endpoint exposure ceiling and the rule service (R-EP-013).
	AreaExposure Area = "exposure"
	// AreaRepository is the repository snapshot and the write side (R-ARCH-011).
	AreaRepository Area = "repository"
	// AreaIdentity is the principal, token and settings stores (R-AUTH-*).
	AreaIdentity Area = "identity"
	// AreaAuthz is the decision point (R-AUTHZ-028).
	AreaAuthz Area = "authz"
	// AreaFiles is the item store and the generic layer over it (R-STOR-007).
	AreaFiles Area = "files"
	// AreaLimits is the cluster limiter set of the client listener (R-ARCH-034).
	AreaLimits Area = "limits"
	// AreaSessions is the session store (R-AUTH-001).
	AreaSessions Area = "sessions"
	// AreaLogin is the OIDC and break-glass login with its browser routes.
	AreaLogin Area = "login"
	// AreaAuth is the authentication stage of the chain.
	AreaAuth Area = "auth"
	// AreaEndpoints is the rate-limit classes of the endpoints (R-EP-015).
	AreaEndpoints Area = "endpoints"
	// AreaAPI is stage 7 of the chain: CORS and the request limits (R-API-016).
	AreaAPI Area = "api"
	// AreaShell is the authentication stage of the shell profile, over an empty
	// token store.
	AreaShell Area = "shell"
)

// Subsystems is the set a role enables (R-ARCH-001, INV-2). Every replica of a
// role runs the same set and is interchangeable with any other of that role.
type Subsystems struct {
	// ClientListener serves :8080: the UI, the management API, the package protocols.
	ClientListener bool
	// OpsListener serves :8081: probes and metrics.
	OpsListener bool
	// MainPool is the transaction-scoped PostgreSQL pool.
	MainPool bool
	// LeasePool holds session-scoped connections for leases and singleton jobs.
	LeasePool bool
	// JobLoops are the claim loop, the reaper and the scheduler.
	JobLoops bool
	// Valkey is the Valkey client.
	Valkey bool
	// ChangePoll is the change-log poll and the signal subscriber.
	ChangePoll bool
	// Registry is the replica registry row and its heartbeat.
	Registry bool
	// Areas are the wiring areas the role enables (steps.go).
	Areas []Area
	// Shell marks the shell profile: the HTTP shell without PostgreSQL, Valkey and
	// the object store (Options.SkipInfrastructure, the drain fixture). It has the
	// listeners, the key ring and the authentication stage over an empty token
	// store, and nothing else.
	Shell bool
}

// Has reports whether the role enables an area.
func (s Subsystems) Has(a Area) bool { return slices.Contains(s.Areas, a) }

// shell returns the shell profile of a role: the listeners stay, everything that
// needs PostgreSQL, Valkey or the object store goes, and so do the areas that
// build on them. It is an explicit profile, not what is left when a step finds
// a nil component.
func (s Subsystems) shell() Subsystems {
	return Subsystems{ClientListener: s.ClientListener, OpsListener: s.OpsListener, Areas: areasFor(onShell), Shell: true}
}

// SubsystemsFor returns the subsystems of a serving role.
func SubsystemsFor(role string) (Subsystems, error) {
	switch role {
	case "web":
		return Subsystems{
			ClientListener: true, OpsListener: true, MainPool: true, Valkey: true, ChangePoll: true, Registry: true,
			Areas: areasFor(onWeb),
		}, nil
	case "worker":
		return Subsystems{
			OpsListener: true, MainPool: true, LeasePool: true, JobLoops: true, Valkey: true, ChangePoll: true, Registry: true,
			Areas: areasFor(onWorker),
		}, nil
	case "all":
		return Subsystems{
			ClientListener: true, OpsListener: true, MainPool: true, LeasePool: true, JobLoops: true, Valkey: true, ChangePoll: true, Registry: true,
			Areas: areasFor(onAll),
		}, nil
	}
	return Subsystems{}, fmt.Errorf("process.role must be web, worker or all (got %q)", role)
}
