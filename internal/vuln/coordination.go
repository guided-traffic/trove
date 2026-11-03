package vuln

import "time"

// The caches of the scanning (R-VULN-027): each is a per-replica cache of the cache registry (R-ARCH-018) that the change
// area vuln_data (cluster.AreaVulnData) invalidates. The registry's names are declared here once; the work package that
// owns the cache registers it.
const (
	// CacheSourceState is the per-replica copy of vuln_source_state that States reads (work package A): SafetyTTL
	// [SourceStateSafetyTTL], one entry per source and ecosystem.
	CacheSourceState = "vuln_source_state"
	// CacheNuGetPage is the rendered NuGet vulnerability page (work package D), keyed by the vuln_data epoch of NuGet and
	// the applied epochs of the claims and repo_config areas, with the fill check of R-ARCH-018.
	CacheNuGetPage = "vuln_nuget_page"
)

// SourceStateSafetyTTL is the safety TTL of the source-state cache (R-VULN-027): the bound on how long a replica that
// missed every change message keeps a state.
const SourceStateSafetyTTL = 60 * time.Second

// DataChange is the payload (version 1) of a change of the area vuln_data: the ecosystem whose data changed and the
// data epoch (vuln_source_state.data_epoch) it changed to.
type DataChange struct {
	Ecosystem string `json:"ecosystem"`
	Epoch     int64  `json:"epoch"`
}

// DataChangePayloadVersion is the payload version a build writes and understands for vuln_data.
const DataChangePayloadVersion = 1
