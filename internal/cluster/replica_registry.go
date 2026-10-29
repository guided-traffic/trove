package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Replica states of the registry row (R-OPS-003).
const (
	ReplicaStarting = "starting"
	ReplicaReady    = "ready"
	ReplicaUnready  = "unready"
	ReplicaDraining = "draining"
)

// ReplicaIdentity is what a replica says about itself once, at registration.
// Backup pods, the restore Job and the migration Job never register
// (R-HA-003).
type ReplicaIdentity struct {
	// UID is random per process start (replica.instance_uid), a UUID.
	UID string
	// Name is the pod name, else the host name.
	Name     string
	Version  string
	Revision string
	// Role is web, worker or all.
	Role string
	// Node is the Kubernetes node name; empty outside Kubernetes.
	Node string
	// Pool is the R-HA-062 pool name; empty for a role's main Deployment.
	Pool      string
	StartedAt time.Time
	// SecretKeyIDs and ActiveSecretKeyID are the key IDs of the replica's
	// stored-secret key ring (R-ARCH-021): IDs only, never key material.
	SecretKeyIDs      []string
	ActiveSecretKeyID string
}

// ReplicaCheck is the last result of one check as the row stores it.
type ReplicaCheck struct {
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	ConfigError bool   `json:"configError,omitempty"`
}

// ReplicaSnapshot is what each heartbeat writes besides the identity.
type ReplicaSnapshot struct {
	// State is one of the Replica* constants.
	State string
	// Checks holds every check of R-OPS-001, gating and reported.
	Checks map[string]ReplicaCheck
	// Valkey is the replica's view of Valkey (R-OPS-003), rendered by the
	// caller; opaque here. No credential, ACL user name or key appears.
	Valkey json.RawMessage
}

// RegistryOptions configure [NewRegistry].
type RegistryOptions struct {
	// DB is the replica's control connection (DB.ControlPool): a request pool
	// exhausted under load must not make a live replica look lost.
	DB       q.DBTX
	Identity ReplicaIdentity
	// Snapshot returns the current state, checks and Valkey view; called on
	// every heartbeat. Nil reports "ready" with no checks.
	Snapshot func() ReplicaSnapshot
	// Leases returns the leases the replica holds (the `leases` column); nil
	// for none.
	Leases          func() []HeldInfo
	Config          config.System
	WatchdogTimeout time.Duration
	Watchdog        Watchdog
	Log             *slog.Logger
}

// Registry is the replica registry of one replica (R-OPS-003): it registers the
// replica at start with its role, refreshes the row every
// system.heartbeat_interval, marks it draining at SIGTERM and deletes it on a
// clean shutdown. A row whose heartbeat is older than system.heartbeat_stale_after
// is listed as lost, on PostgreSQL's clock.
type Registry struct {
	o   RegistryOptions
	log *slog.Logger

	mu       sync.Mutex
	draining bool
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// NewRegistry creates the registry.
func NewRegistry(o RegistryOptions) (*Registry, error) {
	switch {
	case o.DB == nil:
		return nil, errors.New("cluster: the replica registry needs the control connection")
	case o.Identity.UID == "" || o.Identity.Name == "" || o.Identity.Role == "":
		return nil, errors.New("cluster: the replica identity needs UID, Name and Role")
	}
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	return &Registry{o: o, log: log}, nil
}

// Name implements the composition root's Component.
func (r *Registry) Name() string { return "registry" }

// Start registers the replica and begins the heartbeat. The registration
// retries until PostgreSQL answers or ctx ends (R-ARCH-024 step 7).
func (r *Registry) Start(ctx context.Context) error {
	for {
		err := r.beat(ctx)
		if err == nil {
			break
		}
		r.log.Warn("replica registration failed; retrying", "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.o.Config.HeartbeatInterval):
		}
	}
	lctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	r.cancel = cancel
	r.wg.Add(1)
	go r.loop(lctx)
	return nil
}

func (r *Registry) loop(ctx context.Context) {
	defer r.wg.Done()
	loop := WatchLoop(r.o.Watchdog, "cluster.registry", r.o.WatchdogTimeout)
	defer loop.Stop()
	t := time.NewTicker(r.o.Config.HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := r.beat(ctx); err != nil && ctx.Err() == nil {
			r.log.Warn("replica heartbeat failed", "error", err)
		}
		loop.Beat()
	}
}

// beat writes the row: registration and heartbeat are one upsert, so a row
// removed by the prune or a restore comes back with the next heartbeat.
func (r *Registry) beat(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout(r.o.WatchdogTimeout))
	defer cancel()
	id := r.o.Identity
	snap := ReplicaSnapshot{State: ReplicaReady}
	if r.o.Snapshot != nil {
		snap = r.o.Snapshot()
	}
	r.mu.Lock()
	if r.draining {
		snap.State = ReplicaDraining
	}
	r.mu.Unlock()
	checks, err := json.Marshal(nonNilChecks(snap.Checks))
	if err != nil {
		return err
	}
	leases := []byte("[]")
	if r.o.Leases != nil {
		if held := r.o.Leases(); len(held) > 0 {
			sort.Slice(held, func(i, j int) bool { return held[i].Name < held[j].Name })
			if leases, err = json.Marshal(held); err != nil {
				return err
			}
		}
	}
	valkey := string(snap.Valkey)
	if valkey == "" {
		valkey = "{}"
	}
	keyIDs := id.SecretKeyIDs
	if keyIDs == nil {
		keyIDs = []string{}
	}
	return q.New(r.o.DB).UpsertReplica(ctx, q.UpsertReplicaParams{
		InstanceUid: id.UID, Name: id.Name, Version: id.Version, Revision: id.Revision,
		Roles: []string{id.Role}, State: snap.State, Checks: string(checks), Leases: string(leases),
		SecretKeyIds: keyIDs, ActiveSecretKeyID: id.ActiveSecretKeyID, Node: id.Node,
		Valkey: valkey, Pool: id.Pool,
		StartedAt: pgtype.Timestamptz{Time: id.StartedAt, Valid: true},
	})
}

func nonNilChecks(m map[string]ReplicaCheck) map[string]ReplicaCheck {
	if m == nil {
		return map[string]ReplicaCheck{}
	}
	return m
}

// Drain turns the row draining at once and keeps it draining in later
// heartbeats (R-ARCH-024 step 1).
func (r *Registry) Drain(ctx context.Context) {
	r.mu.Lock()
	r.draining = true
	r.mu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, callTimeout(r.o.WatchdogTimeout))
	defer cancel()
	if _, err := q.New(r.o.DB).SetReplicaState(cctx, q.SetReplicaStateParams{InstanceUid: r.o.Identity.UID, State: ReplicaDraining}); err != nil {
		r.log.Warn("replica row not marked draining", "error", err)
	}
}

// StopHeartbeat ends the heartbeat and leaves the row, as a process that was
// killed does. Fault-injection tests use it to show a replica turning lost.
func (r *Registry) StopHeartbeat() {
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
}

// Stop ends the heartbeat and deletes the replica's own row (a clean shutdown,
// R-OPS-003). A replica that is killed leaves the row to go stale.
func (r *Registry) Stop(ctx context.Context) error {
	r.StopHeartbeat()
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return q.New(r.o.DB).DeleteReplica(dctx, r.o.Identity.UID)
}
