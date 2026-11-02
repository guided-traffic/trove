package nuget

import (
	"sync"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/nupkg"
	"github.com/guided-traffic/trove/internal/observability"
)

// A format is registered once per process, and a process may hold several replicas (the
// integration tests start them in one), each with its own settings and its own metrics
// registry. What belongs to a replica is therefore kept per Host, the one object a request
// carries that is the replica's: the composition root calls [Format.Bind] with the Host it
// built, before the replica serves, and a handler finds its replica by rq.Host. A request
// on a Host that was never bound (the conformance kit's) runs on the defaults.

// replica is what the composition root bound for one Host.
type replica struct {
	cfg config.NuGet
	// writes counts the pushes, nil on a Host nobody bound with a metrics registry.
	writes *writeMetrics
	// remote counts what the remote class meets, nil on a Host nobody bound with a registry.
	remote *remoteMetrics
	// virt counts what a virtual repository leaves out, nil likewise.
	virt *virtualMetrics
	// search counts the withheld and the failed forwarded searches, nil likewise.
	search *searchMetrics
}

// Bind gives the format the nuget section of the configuration of the replica that owns the
// Host h (R-NUGET-005 archive bounds, R-NUGET-009 paging, R-NUGET-010 search limits). It
// must be called once per Host, before the replica serves. The metric families of
// R-NUGET-028 are registered on m by the work packages that count what they measure.
func (f *Format) Bind(h formats.Host, cfg *config.Config, m *observability.Metrics) {
	r := &replica{cfg: cfg.NuGet}
	if m != nil {
		r.writes = newWriteMetrics(m)
		r.remote = newRemoteMetrics(m)
		r.virt = newVirtualMetrics(m)
		r.search = newSearchMetrics(m)
	}
	f.replicas.Store(h, r)
	if d := h.FormatData(ID); d != nil {
		f.ports.Store(d, r)
	}
}

// replicaOf finds the replica of a request's Host.
func (f *Format) replicaOf(h formats.Host) *replica {
	if r, ok := f.replicas.Load(h); ok {
		return r.(*replica)
	}
	return defaultReplica()
}

// replicaOfPort finds the replica a data port was bound for: the storage hooks meet the
// format without a request and so without a Host, and reach their replica by the port.
func (f *Format) replicaOfPort(port any) *replica {
	if port != nil {
		if r, ok := f.ports.Load(port); ok {
			return r.(*replica)
		}
	}
	return defaultReplica()
}

// defaultReplica is the replica of a Host nobody bound: the defaults of the nuget section
// of the configuration (internal/config/nuget.go).
func defaultReplica() *replica {
	return &replica{cfg: config.NuGet{
		Archive: config.NuGetArchive{
			MaxNuspecBytes: config.MiB, MaxEntries: nupkg.DefaultMaxEntries,
			MaxUncompressedBytes: config.ByteSize(nupkg.DefaultMaxUncompressedBytes), MaxCompressionRatio: nupkg.DefaultMaxRatio,
		},
		Registration: config.NuGetRegistration{PageSize: 64, InlineBelow: 128},
		Search:       config.NuGetSearch{DefaultTake: 20, MaxTake: 1000, MaxSkip: 3000, VirtualMergeWindow: 1000, RemoteResultTTL: defaultRemoteResultTTL},
	}}
}

// archiveLimits maps the nuget.archive settings to the limits of the package reader
// (R-NUGET-005 step 1, Q-NUGET-23). The bounds on what is read into memory can only be
// tightened by the reader, which clamps them.
func (r *replica) archiveLimits() nupkg.Limits {
	a := r.cfg.Archive
	return nupkg.Limits{
		MaxEntries: a.MaxEntries, MaxUncompressedBytes: int64(a.MaxUncompressedBytes),
		MaxRatio: a.MaxCompressionRatio, MaxNuspecBytes: int64(a.MaxNuspecBytes),
	}
}

// replicas is the registry of bound hosts, a field of the format and not a package
// variable. The format keeps a second one by the data port of the replica.
type replicas struct{ sync.Map }
