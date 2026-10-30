// Package proxy is the engine of the remote repositories: the request path of
// R-REPO-025, the collapse of concurrent misses of R-REPO-026, the freshness,
// negative cache, failure handling and offline modes of R-REPO-027 to R-REPO-031,
// the cache view of R-REPO-032, the limits and the verification of R-REPO-040 to
// R-REPO-042 and the observability of R-REPO-047.
//
// A format reaches it through the RemoteFetcher of internal/formathost, which adapts
// the format's formats.RemoteAdapter to [Adapter] and computes the populate check
// ([Populate]) from the request's subject, index and endpoint. The engine itself
// knows no format: it moves bytes, keeps the cache entries and decides when the
// upstream may be asked.
//
// The packages beside it are internal/proxy/upstream (the HTTP client of each
// remote repository and the upstream test) and internal/proxy/invalidation (the
// invalidations the engine's freshness and negative-cache decisions ask).
//
// The engine holds no state between requests that another replica would have to
// share (R-ARCH-032): its per-replica state is a registered cache each (the circuit
// and gone packages of a repository, the negative L1, the bytes of metadata by
// digest), the flights in progress and the fetch slots. Every cross-replica bound
// holds without Valkey.
package proxy
