package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// ReplicaLease is a lease in the `leases` column.
type ReplicaLease struct {
	Name  string    `json:"name"`
	Since time.Time `json:"since"`
}

// ReplicaInfo is one row of the replica registry as GET /api/v1/system/replicas
// shows it (R-OPS-003).
type ReplicaInfo struct {
	InstanceUID       string
	Name              string
	Version           string
	Revision          string
	Roles             []string
	State             string
	Lost              bool
	StartedAt         time.Time
	HeartbeatAt       time.Time
	Leases            []ReplicaLease
	Node              string // empty outside Kubernetes
	Pool              string
	Checks            map[string]ReplicaCheck
	Degraded          []string // reported checks that fail
	Valkey            json.RawMessage
	SecretKeyIDs      []string
	ActiveSecretKeyID string
	// Runtime is the replica's measurement of its last heartbeat period
	// (Q-OPS-175); nil while the column holds '{}' (a release before it, or a row
	// not yet written by the new release) or a value this release cannot read.
	Runtime *ReplicaRuntime
}

// ListReplicas reads the registry. Lost is decided by PostgreSQL's clock:
// heartbeat_at older than system.heartbeat_stale_after (R-HA-003). Replica
// clocks are never compared. It is the read side behind the ReplicaLister of
// the API; it needs no Registry, so every role can serve it, and it reads on
// the request pool (the caller passes DB.Main). The leases of a lost replica
// are not listed.
func ListReplicas(ctx context.Context, db q.DBTX, sys config.System) ([]ReplicaInfo, error) {
	rows, err := q.New(db).ListReplicas(ctx, sys.HeartbeatStaleAfter.Seconds())
	if err != nil {
		return nil, fmt.Errorf("reading the replica registry: %w", err)
	}
	out := make([]ReplicaInfo, 0, len(rows))
	for _, r := range rows {
		ri, err := replicaInfoOf(r)
		if err != nil {
			return nil, err
		}
		out = append(out, ri)
	}
	return out, nil
}

// replicaInfoOf reads one row of the registry.
func replicaInfoOf(r q.ListReplicasRow) (ReplicaInfo, error) {
	ri := ReplicaInfo{
		InstanceUID: r.InstanceUid.String(), Name: r.Name, Version: r.Version, Revision: r.Revision,
		Roles: r.Roles, State: r.State, Lost: r.Lost,
		StartedAt: r.StartedAt.Time.UTC(), HeartbeatAt: r.HeartbeatAt.Time.UTC(),
		Node: r.Node.String, Pool: r.Pool.String, Valkey: json.RawMessage(r.Valkey),
		SecretKeyIDs: r.SecretKeyIds, ActiveSecretKeyID: r.ActiveSecretKeyID,
		Runtime: runtimeOf(r.Runtime),
	}
	if err := json.Unmarshal(r.Leases, &ri.Leases); err != nil {
		return ReplicaInfo{}, fmt.Errorf("replica %s: leases: %w", r.Name, err)
	}
	if err := json.Unmarshal(r.Checks, &ri.Checks); err != nil {
		return ReplicaInfo{}, fmt.Errorf("replica %s: checks: %w", r.Name, err)
	}
	for name, c := range ri.Checks {
		if c.Status == "degraded" {
			ri.Degraded = append(ri.Degraded, name)
		}
	}
	sort.Strings(ri.Degraded)
	if ri.Degraded == nil {
		ri.Degraded = []string{}
	}
	if ri.Leases == nil || ri.Lost {
		// A lost replica holds nothing any more: its session ended with the
		// process, and the lock may already belong to another replica, which
		// must not share the lease name with a stale row.
		ri.Leases = []ReplicaLease{}
	}
	return ri, nil
}

// runtimeOf reads the runtime column. '{}' (the default a release before
// Q-OPS-175 leaves) is no runtime. A value this release cannot read is no
// runtime either, not an error: the figures are for display, and the list of
// the registry must not fail for them.
func runtimeOf(raw []byte) *ReplicaRuntime {
	var rt ReplicaRuntime
	if len(raw) <= 2 || json.Unmarshal(raw, &rt) != nil || rt.SampledAt.IsZero() {
		return nil
	}
	rt.SampledAt = rt.SampledAt.UTC()
	return &rt
}

// PruneReplicas is the body of the replica-registry-prune task (R-OPS-003): it
// deletes rows whose heartbeat is older than staleFor (24 h by the spec).
func PruneReplicas(ctx context.Context, db q.DBTX, staleFor time.Duration) (int64, error) {
	return q.New(db).PruneReplicas(ctx, staleFor.Seconds())
}
