package app

import (
	"context"
	"strings"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/buildinfo"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta/postgres"
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
		// The incarnation changes with a restore; read it fresh when the database answers.
		if sf, err := db.SystemFacts(ctx); err == nil {
			out.InstallationID, out.Incarnation = &sf.InstallationID, &sf.Incarnation
		}
		out.Database = databaseFacts(ctx, db)
	}
	return out, nil
}

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

// localReplicas lists only this replica until the replica registry exists.
// WIRING POINT: the jobs/replica-registry package implements v1.ReplicaLister
// over the replica table; replace this.
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
