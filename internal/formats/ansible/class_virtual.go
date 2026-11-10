package ansible

import (
	"github.com/guided-traffic/trove/internal/formats"
)

// The virtual class (R-ANS-022 to R-ANS-024). The virtual engine plans a request (the repository's patterns, the pin rules and claims of
// R-REPO-023, the members each caller may read and fill) and caches the merged document by the members that took part; the format
// contributes the document of a local member, the merge and the strategy per path (virtual.go), and the handlers that render the answer
// for the repository the client addressed (handlers_virtual.go).
//
// Two documents of a collection are merged, each at the canonical path the remote class stores it at (R-ANS-002):
//
//   - the version list, at collections/<ns>/<name>/versions.json, variant [variantFirstPage]. A local member's document is the neutral
//     list of its rows; a remote member's is the first stored page of the upstream's list, and the adapter reads its further pages
//     through the member (Q-ANS-12, the pattern of Q-NUGET-82).
//   - the collection document, at collections/<ns>/<name>/collection.json: only its times are merged.
//
// The version document and the archive are resolved to the first member that holds them (the tiers of R-REPO-019 step 6). The merged
// bytes hold no address: the handler renders the answer for the repository the client addressed after the cache.
func init() { registerClass(formats.Virtual, func(f *Format) { f.virtual = virtualAdapter{f: f} }) }
