package app

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/vuln/report"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// vulnerabilityFacts builds the block vulnerabilityData of GET /api/v1/system/info (R-OPS-004, R-VULN-024) from
// vuln_source_state and the configuration. It is nil where the scanning is not wired on this replica or the table cannot be
// read right now (the facts are best effort and never fail the call). It never affects readiness.
//
// One entry per source and ecosystem the configuration names (the effective ecosystems of the OSV source and the KEV
// catalog), whatever its mode, so that an installation whose rows a restore removed shows its sources with state empty;
// a stored row of an ecosystem the configuration no longer names is listed with mode off, as the sources operation lists it. With the scanning disabled
// every mode is off. A source that never ran has no syncedAt and is not stale: its state says empty.
func (a *App) vulnerabilityFacts(ctx context.Context, now time.Time) *gen.VulnerabilityDataFacts {
	if _, wired := lookup[vulnArea](a.parts); !wired {
		return nil
	}
	db := a.database()
	if db == nil {
		return nil
	}
	rows, err := store.ReadSources(ctx, db)
	if err != nil {
		return nil
	}
	return vulnFactsFrom(a.cfg.Vulnerabilities, rows, now)
}

// vulnFactSource is one element of VulnerabilityDataFacts.Sources, whose generated type is anonymous.
type vulnFactSource = struct {
	Ecosystem string                                  `json:"ecosystem"`
	Failures  int                                     `json:"failures"`
	Mode      gen.VulnerabilityDataFactsSourcesMode   `json:"mode"`
	Source    gen.VulnerabilityDataFactsSourcesSource `json:"source"`
	Stale     bool                                    `json:"stale"`
	State     gen.VulnerabilityDataFactsSourcesState  `json:"state"`
	SyncedAt  *time.Time                              `json:"syncedAt"`
}

// vulnFactsFrom is the pure part of vulnerabilityFacts: the view of the sources operation (report.SourceViews), which this
// block and GET /api/v1/vulnerabilities/sources share (A6), in the shape of the system information.
func vulnFactsFrom(v config.Vulnerabilities, rows []store.SourceRow, now time.Time) *gen.VulnerabilityDataFacts {
	out := &gen.VulnerabilityDataFacts{Enforcement: gen.VulnerabilityDataFactsEnforcement(v.Enforcement)}
	views := report.SourceViews(report.SourceConfig{
		Enabled: v.Enabled, OSVMode: v.Sources.OSV.Mode, KEVMode: v.Sources.KEV.Mode,
		Ecosystems: v.Sources.OSV.EffectiveEcosystems(), StaleAfter: v.StaleAfter,
	}, rows, now)
	for _, e := range views {
		out.Sources = append(out.Sources, vulnFactSource{
			Ecosystem: e.Ecosystem, Failures: e.Failures, Mode: gen.VulnerabilityDataFactsSourcesMode(e.Mode),
			Source: gen.VulnerabilityDataFactsSourcesSource(e.Source), Stale: e.Stale,
			State: gen.VulnerabilityDataFactsSourcesState(e.State), SyncedAt: e.SyncedAt,
		})
	}
	return out
}
