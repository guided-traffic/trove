package app

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/buildinfo"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// configFacts derives the installation facts from the configuration. The blocks
// that need system_state or the replica registry stay nil until those are wired.
type configFacts struct {
	cfg *config.Config
	a   *App
}

func (f configFacts) Facts(ctx context.Context) (v1.Facts, error) {
	c := f.cfg
	vf := gen.ValkeyFacts{
		Mode: gen.ValkeyFactsMode(c.Valkey.Mode), PrimaryName: c.Valkey.Sentinel.PrimaryName,
		Sentinels: len(c.Valkey.Sentinel.Addresses), Tls: c.Valkey.TLS.Mode, KeyPrefix: c.Valkey.KeyPrefix,
	}
	src := c.Storage.S3.Credentials.Source
	if src == "" {
		src = "default"
	}
	out := v1.Facts{
		Valkey: vf,
		ObjectStore: gen.ObjectStoreFacts{
			Endpoint: c.Storage.S3.Endpoint, Bucket: c.Storage.S3.Bucket, Prefix: c.Storage.S3.Prefix, CredentialSource: src,
		},
		Tracing:     c.Tracing.Enabled,
		MemoryLimit: int64(c.Runtime.MemoryLimitBytes),
	}
	if sf, ok := f.a.systemFacts(); ok {
		out.InstallationID, out.Incarnation = &sf.InstallationID, &sf.Incarnation
		if hex := keyspaceHex(sf.KeyspaceID); len(hex) >= 12 {
			ks := strings.ToLower(hex[:12])
			out.Valkey.Keyspace = &ks
		}
	}
	if db := f.a.database(); db != nil {
		// The reads retry through a failover (postgres.Read); the facts are best
		// effort, so the whole block is bounded and never holds the request longer.
		ctx, cancel := context.WithTimeout(ctx, factsDatabaseBudget)
		defer cancel()
		// The incarnation changes with a restore; read it fresh when the database answers.
		if sf, err := db.SystemFacts(ctx); err == nil {
			out.InstallationID, out.Incarnation = &sf.InstallationID, &sf.Incarnation
		}
		out.Database = databaseFacts(ctx, db)
	}
	return out, nil
}

// factsDatabaseBudget bounds the database part of GET /system/info, which is
// best effort and must not wait out retry.max_elapsed.
const factsDatabaseBudget = 3 * time.Second

// databaseFacts describes the database as this replica sees it (R-OPS-004). A
// part that cannot be read right now stays at its zero value; the call never fails.
func databaseFacts(ctx context.Context, db *postgres.DB) *gen.DatabaseFacts {
	d := &gen.DatabaseFacts{PoolerMode: db.PoolerMode(), Pools: map[string]gen.PoolFacts{}, SupportedSchemaVersions: []int{}}
	if v, err := db.ServerVersion(ctx); err == nil {
		d.ServerVersion = v
	}
	if st, err := db.SchemaState(ctx); err == nil {
		d.SchemaVersion = st.Applied
		for v := postgres.RequiredSchema(); v <= st.Applied; v++ {
			d.SupportedSchemaVersions = append(d.SupportedSchemaVersions, v)
		}
	}
	pool := func(p postgres.PoolStats) gen.PoolFacts {
		return gen.PoolFacts{Max: int(p.Max), InUse: int(p.Acquired), Idle: int(p.Idle)}
	}
	s := db.Stats()
	d.Pools["request"], d.Pools["control"] = pool(s.Main), pool(s.Control)
	if s.Lease.Max > 0 {
		d.Pools["lease"] = pool(s.Lease)
	}
	return d
}

// replicaSource lists the replica registry (R-OPS-003) on the request pool. A
// shell started without infrastructure (tests, the drain fixture) has no
// registry and lists only itself.
type replicaSource struct {
	a *App
}

func (l replicaSource) Replicas(ctx context.Context) ([]gen.Replica, error) {
	db := l.a.database()
	if db == nil {
		return localReplicas(l).Replicas(ctx)
	}
	// Read, not the raw pool: the registry list rides out a failover like a
	// transaction does and ends as database_unavailable (R-ARCH-019).
	var infos []cluster.ReplicaInfo
	err := db.Read(ctx, func(c q.DBTX) (err error) {
		infos, err = cluster.ListReplicas(ctx, c, l.a.cfg.System)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]gen.Replica, 0, len(infos))
	for _, i := range infos {
		out = append(out, replicaOf(i))
	}
	return out, nil
}

func replicaOf(i cluster.ReplicaInfo) gen.Replica {
	r := gen.Replica{
		Name: i.Name, InstanceUid: i.InstanceUID, Version: i.Version, Revision: i.Revision,
		State: gen.ReplicaState(i.State), Lost: i.Lost, StartedAt: i.StartedAt.UTC(), HeartbeatAt: i.HeartbeatAt.UTC(),
		Degraded: i.Degraded, Checks: map[string]gen.CheckResult{}, Roles: []gen.ReplicaRoles{},
		Leases: []struct {
			Name  string    `json:"name"`
			Since time.Time `json:"since"`
		}{},
	}
	if r.Degraded == nil {
		r.Degraded = []string{}
	}
	for _, role := range i.Roles {
		r.Roles = append(r.Roles, gen.ReplicaRoles(role))
	}
	if i.Node != "" {
		r.Node = &i.Node
	}
	for name, c := range i.Checks {
		cr := gen.CheckResult{Status: gen.CheckResultStatus(c.Status)}
		if c.Reason != "" {
			cr.Reason = &c.Reason
		}
		if c.ConfigError {
			t := true
			cr.ConfigError = &t
		}
		r.Checks[name] = cr
	}
	for _, ls := range i.Leases {
		r.Leases = append(r.Leases, struct {
			Name  string    `json:"name"`
			Since time.Time `json:"since"`
		}{ls.Name, ls.Since.UTC()})
	}
	var vk gen.ValkeyView
	if len(i.Valkey) > 2 && json.Unmarshal(i.Valkey, &vk) == nil {
		r.Valkey = &vk
	}
	return r
}

// localReplicas lists only this replica, for a shell without a registry.
type localReplicas struct {
	a *App
}

func (l localReplicas) Replicas(context.Context) ([]gen.Replica, error) {
	a := l.a
	snap := a.health.Snapshot()
	bi := buildinfo.Get()
	checks := make(map[string]gen.CheckResult, len(snap.Checks))
	for _, c := range snap.Checks {
		r := gen.CheckResult{Status: gen.CheckResultStatus(c.Status)}
		if c.Reason != "" {
			r.Reason = &c.Reason
		}
		if c.ConfigError {
			t := true
			r.ConfigError = &t
		}
		checks[c.Name] = r
	}
	var node *string
	if a.id.Node != "" {
		node = &a.id.Node
	}
	degraded := snap.Degraded
	if degraded == nil {
		degraded = []string{}
	}
	rep := gen.Replica{
		Name: a.id.Instance, InstanceUid: a.id.UID, Version: bi.Version, Revision: bi.Commit,
		Roles: []gen.ReplicaRoles{gen.ReplicaRoles(a.id.Role)}, State: gen.ReplicaState(snap.State),
		StartedAt: a.startedAt.UTC(), HeartbeatAt: time.Now().UTC(),
		Node: node, Checks: checks, Degraded: degraded, Valkey: a.valkeyView(),
		Leases: []struct {
			Name  string    `json:"name"`
			Since time.Time `json:"since"`
		}{},
	}
	return []gen.Replica{rep}, nil
}

// valkeyView renders this replica's view of Valkey (R-OPS-003); nil without a client.
func (a *App) valkeyView() *gen.ValkeyView {
	vk := a.valkey()
	if vk == nil {
		return nil
	}
	return valkeyViewOf(vk.Status())
}

func valkeyViewOf(st cluster.ValkeyStatus) *gen.ValkeyView {
	v := &gen.ValkeyView{
		State: gen.ValkeyViewStateAvailable, Circuit: "closed",
		SentinelsReachable: st.SentinelsReachable, SentinelsConfigured: st.SentinelsConfigured,
	}
	if !st.Available {
		v.State = gen.ValkeyViewStateDegraded
		r := st.DegradedReason
		v.Reason = &r
	}
	if st.CircuitOpen {
		v.Circuit = "open"
	}
	if st.Primary != "" {
		v.Primary = &st.Primary
	}
	if st.Version != "" {
		v.ServerVersion = &st.Version
	}
	return v
}
