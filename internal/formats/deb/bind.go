package deb

import (
	"context"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/observability"
)

// replica is what the format keeps per replica: the metric families the remote class exports (R-DEB-033). The format is one value for
// the process; the composition root binds it to each Host it builds (Bind), and a request finds its replica by its Host.
type replica struct {
	met *metrics
}

// metrics are the families the remote class exports. The label that R-OPS-007 calls repository is "repo", capped by the repository
// limit of the metrics section.
type metrics struct {
	repos       *observability.CappedLabels
	rollbacks   *prometheus.CounterVec
	sigFailures *prometheus.CounterVec
	unverified  *prometheus.CounterVec
}

func newMetrics(m *observability.Metrics) *metrics {
	met := &metrics{
		repos: m.Repos(),
		rollbacks: m.NewCounterVec("trove_deb_release_rollbacks_total",
			"Fetched release documents older than the newest release verified for the directory, which were not stored (R-DEB-019).", "repo"),
		sigFailures: m.NewCounterVec("trove_deb_upstream_signature_failures_total",
			"Release documents of a remote that failed the verification against deb.upstreamKeys, by reason (R-DEB-020).", "repo", "reason"),
		unverified: m.NewCounterVec("trove_deb_unverified_fills_total",
			"Package files stored without a digest to verify them against because no parsed index lists the path (R-DEB-022).", "repo"),
	}
	met.repos.DeleteOnForget(met.rollbacks, met.sigFailures, met.unverified)
	return met
}

func (m *metrics) rollback(repo string) {
	if m != nil {
		m.rollbacks.WithLabelValues(repo).Inc()
	}
}

func (m *metrics) signatureFailed(repo, reason string) {
	if m != nil {
		m.sigFailures.WithLabelValues(repo, reason).Inc()
	}
}

func (m *metrics) unverifiedFill(repo string) {
	if m != nil {
		m.unverified.WithLabelValues(repo).Inc()
	}
}

// Bind binds the format to the Host a replica built and to the metrics registry of the replica. It registers the metric families on m
// and must be called once per Host, before the replica serves. The data port of the Host is remembered for the parts of a fill that
// have no Host (the revalidation of a release, which asks whether the stored one is verified).
func (f *Format) Bind(h formats.Host, _ *config.Config, m *observability.Metrics) {
	r := &replica{}
	if m != nil {
		r.met = newMetrics(m)
	}
	f.replicas.Store(h, r)
	if d := h.FormatData(ID); d != nil {
		f.ports.Store(d, r)
		f.port.Store(&portBox{d})
	}
}

type portBox struct{ v any }

// replicaOf finds the replica of a request's Host; a Host nobody bound has no metrics.
func (f *Format) replicaOf(h formats.Host) *replica {
	if r, ok := f.replicas.Load(h); ok {
		return r.(*replica) //nolint:forcetypeassert // only Bind stores, and it stores a *replica
	}
	return &replica{}
}

// replicaOfPort finds the replica a data port was bound for.
func (f *Format) replicaOfPort(port any) *replica {
	if port != nil {
		if r, ok := f.ports.Load(port); ok {
			return r.(*replica) //nolint:forcetypeassert // only Bind stores, and it stores a *replica
		}
	}
	return &replica{}
}

// remoteStoreOfBound is the remote part of the data port bound to this process, nil before Bind.
func (f *Format) remoteStoreOfBound() RemoteStore {
	if b := f.port.Load(); b != nil {
		if st, ok := b.v.(Store); ok {
			return st
		}
	}
	return nil
}

// unverified says that the release document at p is stored and no key of the remote's set verifies it now (R-DEB-020, Q-DEB-42).
func (f *Format) unverified(ctx context.Context, rc formats.RemoteContext, p formats.Path) bool {
	rp := analyzeRemote(p.Path)
	if rp.kind != rkRelease || rp.doc == FileReleaseGPG || SettingsOf(rc.Settings).UpstreamKeys == "" {
		return false
	}
	st := f.remoteStoreOfBound()
	if st == nil || rc.Repo == nil {
		return false
	}
	rel, found, err := st.Release(ctx, rc.Repo.NumericID(), rp.dir)
	return err == nil && found && rel.VerifiedBy == "" && strings.EqualFold(rel.Document, rp.doc)
}
