package ansible

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/observability"
)

// The remote class's per-replica state: its metric families, registered by the binder below when [Format.Bind] (bind.go, the one Bind
// of the format) binds a replica. The fill meets the format without a Host, so it finds the state through the replica of the data port.

// remoteKey is the key of the remote class's metrics in replica.ext.
type remoteKey struct{}

// remoteMetrics are the families of R-ANS-030 that the remote class counts; the zero value counts nothing.
type remoteMetrics struct {
	repos *observability.CappedLabels
	// dropped counts the role versions the host rule removed.
	dropped *prometheus.CounterVec
	// docBytes observes the size of the documents a fill stores.
	docBytes *prometheus.HistogramVec
}

// roleMax is ansible.role_archive_max_bytes of the replica bound last: the response check of a fetch has no Host to ask, and the
// configuration of the replicas of one process is one (H-858). Bind stores it.
var roleMax atomic.Int64

func init() {
	roleMax.Store(int64(256 << 20))
	registerBinder(func(r *replica, m *observability.Metrics) {
		rm := &remoteMetrics{repos: m.Repos()}
		rm.dropped = m.NewCounterVec("trove_ansible_role_versions_dropped_total",
			"Role versions a remote repository left out of the list it serves, because their download_url is not an https URL on an extra host (R-ANS-020).", "repo")
		rm.repos.DeleteOnForget(rm.dropped) // the series leave with the repository (R-OPS-007)
		rm.docBytes = m.NewHistogramVec("trove_ansible_document_bytes",
			"Size of the upstream documents a remote repository stored (R-ANS-030).", []float64{1 << 10, 4 << 10, 16 << 10, 64 << 10, 256 << 10, 1 << 20, 4 << 20, 16 << 20}, "kind")
		r.ext[remoteKey{}] = rm
	})
}

// roleArchiveLimit is the largest role archive a remote keeps (R-ANS-020).
func roleArchiveLimit() int64 { return roleMax.Load() }

// remoteMetricsOfPort finds the remote metrics of the replica a data port was bound for; a port nobody bound has none.
func (f *Format) remoteMetricsOfPort(port any) *remoteMetrics {
	if rm, ok := f.replicaOfPort(port).ext[remoteKey{}].(*remoteMetrics); ok {
		return rm
	}
	return &remoteMetrics{}
}

// observe records the size of a stored document.
func (r *remoteMetrics) observe(d fillData) {
	if r.docBytes == nil {
		return
	}
	r.docBytes.WithLabelValues(d.kind.label()).Observe(float64(d.size))
}

// rolesDropped counts role versions the host rule removed.
func (r *remoteMetrics) rolesDropped(repo string, n int) {
	if r.dropped == nil || n == 0 {
		return
	}
	r.dropped.WithLabelValues(r.repos.Value("trove_ansible_role_versions_dropped_total", repo)).Add(float64(n))
}

func (k docKind) label() string {
	switch k {
	case kindRoot:
		return "root"
	case kindCollection:
		return "collection"
	case kindVersions:
		return "versions"
	case kindVersion:
		return "version"
	case kindRoleLookup, kindRoleVersions:
		return "role"
	}
	return "other"
}
