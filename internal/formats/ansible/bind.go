package ansible

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/observability"
)

// A format is registered once per process, and a process may hold several replicas (the integration tests start them in one), each
// with its own settings and its own metrics registry. What belongs to a replica is therefore kept per Host, the one object a request
// carries that is the replica's: the composition root calls [Format.Bind] with the Host it built, before the replica serves, and a
// handler finds its replica by rq.Host. A request on a Host that was never bound (the conformance kit's) runs on the defaults.

// replica is what the composition root bound for one Host.
type replica struct {
	cfg config.Ansible
	// publish counts the outcomes of a publish; nil on a Host nobody bound with a metrics registry.
	publish *prometheus.CounterVec
	// ext holds what the class files bind per replica (the remote class's metrics, the virtual class's), keyed by a value of their own.
	ext map[any]any
}

// The outcomes of trove_ansible_publish_total (R-ANS-030).
const (
	outcomeCompleted  = "completed"
	outcomeFailed     = "failed"
	outcomeExists     = "exists"
	outcomeTombstoned = "tombstoned"
)

// binders are the functions the class files register to bind their own per-replica state (metrics) when the replica is bound.
var binders = struct {
	fns []func(r *replica, m *observability.Metrics)
}{}

// registerBinder registers a function run by [Format.Bind] for every replica; it must be called from init.
func registerBinder(fn func(r *replica, m *observability.Metrics)) {
	binders.fns = append(binders.fns, fn)
}

func init() {
	registerBinder(func(r *replica, m *observability.Metrics) {
		r.publish = m.NewCounterVec("trove_ansible_publish_total",
			"Publishes of collections to local Ansible Galaxy repositories by outcome: completed, failed, exists or tombstoned (R-ANS-030).", "outcome")
	})
}

// Bind gives the format the ansible section of the configuration of the replica that owns the Host h (R-ANS-031 bounds) and registers
// the metric families of R-ANS-030 on m. It must be called once per Host, before the replica serves.
//
// It is the one place a class binds per-replica state: the local class's publish counter is registered above, the remote class's metrics
// in remote_bind.go, and the virtual class adds its own with registerBinder from its class file; no class defines a second Bind.
func (f *Format) Bind(h formats.Host, cfg *config.Config, m *observability.Metrics) {
	r := &replica{cfg: cfg.Ansible, ext: map[any]any{}}
	if m != nil {
		for _, fn := range binders.fns {
			fn(r, m)
		}
	}
	roleMax.Store(int64(cfg.Ansible.RoleArchiveMaxBytes))
	f.replicas.Store(h, r)
	if d := h.FormatData(ID); d != nil {
		f.ports.Store(d, r)
	}
}

// replicaOfPort finds the replica a data port was bound for: the storage hooks and the remote fill meet the format without a Host.
func (f *Format) replicaOfPort(port any) *replica {
	if port != nil {
		if r, ok := f.ports.Load(port); ok {
			return r.(*replica)
		}
	}
	return defaultReplica()
}

// replicaOf finds the replica of a request's Host.
func (f *Format) replicaOf(h formats.Host) *replica {
	if r, ok := f.replicas.Load(h); ok {
		return r.(*replica)
	}
	return defaultReplica()
}

// defaultReplica is the replica of a Host nobody bound: the defaults of the ansible section of the configuration.
func defaultReplica() *replica {
	return &replica{
		cfg: config.Ansible{MaxArchiveBytes: config.ByteSize(1 << 30), RoleArchiveMaxBytes: config.ByteSize(256 << 20), ImportTaskKeep: 720 * time.Hour},
		ext: map[any]any{},
	}
}

// published counts the outcome of a publish.
func (r *replica) published(outcome string) {
	if r != nil && r.publish != nil {
		r.publish.WithLabelValues(outcome).Inc()
	}
}
