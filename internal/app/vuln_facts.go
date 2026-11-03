package app

import (
	"context"
	"sort"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// vulnerabilityFacts builds the block vulnerabilityData of GET /api/v1/system/info (R-OPS-004, R-VULN-024) from
// vuln_source_state and the configuration. It is nil where the scanning is not wired on this replica or the table cannot be
// read right now (the facts are best effort and never fail the call). It never affects readiness.
//
// One entry per source and ecosystem the configuration names (the effective ecosystems of the OSV source and the KEV
// catalog), whatever its mode, so that an installation whose rows a restore removed shows its sources with state empty;
// a stored row of an ecosystem the configuration no longer names is listed with mode off. With the scanning disabled
// every mode is off. A source that never ran has no syncedAt and is not stale: its state says empty.
func (a *App) vulnerabilityFacts(ctx context.Context, now time.Time) *gen.VulnerabilityDataFacts {
	if _, wired := lookup[vulnArea](a.parts); !wired {
		return nil
	}
	db := a.database()
	if db == nil {
		return nil
	}
	rows, err := store.SourceStates(ctx, db)
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

// vulnFactsFrom is the pure part of vulnerabilityFacts.
func vulnFactsFrom(v config.Vulnerabilities, rows []store.SourceState, now time.Time) *gen.VulnerabilityDataFacts {
	out := &gen.VulnerabilityDataFacts{Enforcement: gen.VulnerabilityDataFactsEnforcement(v.Enforcement)}
	mode := func(m string) gen.VulnerabilityDataFactsSourcesMode {
		if !v.Enabled {
			return gen.VulnerabilityDataFactsSourcesModeOff
		}
		return gen.VulnerabilityDataFactsSourcesMode(m)
	}
	byKey := map[store.ExpectedSource]store.SourceState{}
	for _, r := range rows {
		byKey[store.ExpectedSource{Source: r.Source, Ecosystem: r.Ecosystem}] = r
	}
	type entry struct {
		key  store.ExpectedSource
		mode gen.VulnerabilityDataFactsSourcesMode
	}
	var entries []entry
	seen := map[store.ExpectedSource]bool{}
	add := func(k store.ExpectedSource, m gen.VulnerabilityDataFactsSourcesMode) {
		seen[k] = true
		entries = append(entries, entry{k, m})
	}
	for _, e := range v.Sources.OSV.EffectiveEcosystems() {
		add(store.ExpectedSource{Source: "osv", Ecosystem: e}, mode(v.Sources.OSV.Mode))
	}
	add(store.ExpectedSource{Source: "kev"}, mode(v.Sources.KEV.Mode))
	for _, r := range rows {
		k := store.ExpectedSource{Source: r.Source, Ecosystem: r.Ecosystem}
		if !seen[k] {
			add(k, gen.VulnerabilityDataFactsSourcesModeOff)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].key.Source != entries[j].key.Source {
			return entries[i].key.Source < entries[j].key.Source
		}
		return entries[i].key.Ecosystem < entries[j].key.Ecosystem
	})
	for _, e := range entries {
		r, ok := byKey[e.key]
		s := vulnFactSource{Ecosystem: e.key.Ecosystem, Mode: e.mode, Source: gen.VulnerabilityDataFactsSourcesSource(e.key.Source), State: "empty"}
		if ok {
			s.Failures, s.State = int(r.Failures), gen.VulnerabilityDataFactsSourcesState(r.State)
			if !r.SyncedAt.IsZero() {
				t := r.SyncedAt
				s.SyncedAt = &t
				s.Stale = now.Sub(t) > v.StaleAfter
			}
		}
		out.Sources = append(out.Sources, s)
	}
	return out
}
