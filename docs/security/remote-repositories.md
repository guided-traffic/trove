# Remote repositories

Status: built (M2). The generic engine of the remote repositories ([internal/proxy](../../internal/proxy)) serves every format that has
a remote adapter, today npm and NuGet; [internal/formathost](../../internal/formathost) (`remote.go`) adapts a format to it and computes
the populate check per request. Not built, and refused with `422 not_applicable` when a configuration sets them, so that no setting is
accepted without being enforced ([validate_remote.go](../../internal/repository/validate_remote.go)): the pass-through mode
(`cache.storeArtifacts: false`, R-REPO-044), the cleanup of unused cache content (`cache.unusedRetentionHours` above 0, R-REPO-046),
client certificates (R-REPO-034), custom upstream headers (R-REPO-036) and named outbound proxies (`outboundProxy.mode: named`, R-REPO-037).
The runtime switch of the global offline mode
(`/api/v1/system/proxy-offline`, R-REPO-031) is not built; the configuration key `proxy.global_offline` is.

This page describes what the engine lets a caller, an upstream and a writer of Valkey make trove do: when trove asks an upstream, what it
stores of the answer, what it remembers as missing, how concurrent requests share one fetch, what a failing or offline upstream costs, and
what one repository or one caller can take of the fetch slots. The rules it relies on are on other pages: the address policy every
connection passes on [egress.md](egress.md) (which hosts receive the credential, the addresses taken from upstream documents, the upstream
test), the upstream credentials on [data-and-secret-flow.md](data-and-secret-flow.md#upstream-credentials-of-remote-repositories), the TLS
verification on [transport-encryption.md](transport-encryption.md), the patterns and claims that decide which paths reach the engine at all on
[repository-model.md](repository-model.md), the format specifics on [nuget-remote.md](nuget-remote.md) and [npm.md](npm.md), the background
passes of the mirror mode on [mirror.md](mirror.md), and the gate that evaluates a version before it is fetched on
[vulnerability-scanning.md](vulnerability-scanning.md#download-block). The requirements are R-REPO-024 to R-REPO-047 in
[spec/repositories-and-proxy.md](../spec/repositories-and-proxy.md#r-repo-025--remote-request-path) and
[R-AUTHZ-008](../spec/authorization.md#r-authz-008--remote-repositories-who-may-populate-the-cache).

## Who is trusted with what

| Party | What it can make the engine do | What bounds it |
|---|---|---|
| A caller with `read` on the remote | be served what the cache holds, an expired document included | nothing reaches the upstream for it unless it also passes the populate check |
| A caller who may populate (`read` plus the action of `cacheFillAction`) | make trove ask the upstream for any path the patterns and claims admit, and store the answer | the fetch slots, the circuit, the negative cache, the size limits; nothing per caller (below) |
| The upstream | decide which bytes and documents trove stores, which paths are missing, which hosts its documents name, how fast it answers | the digest check, the content-type block, the size limits, the format's checks, the host rules of the client, the stall bound |
| A writer under trove's Valkey prefix | write negative entries and hold flight locks | a negative entry can only refuse, and a held lock only delays, by at most `proxy.fetch_wait_timeout_seconds` ([H-49](../spec/architecture.md#h-49--write-access-to-valkey-denies-service)) |
| A holder of `repositories:write-config`, or the role `owner` of the repository's project (R-AUTHZ-026) | choose the upstream, its timeouts, its limits and its fetch budget | the address policy; an Owner cannot move a stored credential, the outbound path or the mirror mode ([delegated.go](../../internal/repository/delegated.go)); nothing bounds the budget it sets against other repositories ([H-582](#h-582--one-remote-repository-can-hold-every-fetch-slot-of-a-replica)) |

The engine knows no format and no permission: the dispatcher decides steps 1 to 3 of R-REPO-025 (the repository is enabled, the patterns and
claims admit the coordinate, the caller may read) before the engine is called, the format's gate (`BeforeArtifact`) decides the format's own
marks and names the digests the upstream announced for an artifact, and the engine asks the populate check through the interface
`proxy.Populate` that the format host implements over the decision point ([types.go](../../internal/proxy/types.go),
[formathost/remote.go](../../internal/formathost/remote.go) `Artifact`, `populate`).

## When trove asks the upstream

A request for an artifact passes these checks in this order before anything is sent ([artifact.go](../../internal/proxy/artifact.go)
`Artifact`; a metadata document the same in [metadata.go](../../internal/proxy/metadata.go) `metadata` and `beforeFetch`):

| Step | Check | Answer when it decides |
|---|---|---|
| gone | the package is marked gone upstream (formats that do not track removal per version) | `404`; the mark heals when the package document has no valid negative entry and the upstream serves it again (`goneGate`) |
| 4 | a stored artifact, or a fresh document that no invalidation covers | served from the cache; a stored artifact is served through the format's serve check and the verdict of the scanning |
| 5 | the cache view `<key>~cache`, or `Cache-Control: only-if-cached` | the stored entry, else `404`, or `504 not_cached_only_if_cached` |
| 6 | `availability.mode: offline`, or `proxy.global_offline` | the stored entry (an expired document as stale), else `404 not_cached_offline` |
| 7 | the circuit is open | a stored document as stale within `cache.maxStaleSeconds`, else `503 upstream_assumed_offline` with `Retry-After` |
| 8 | a valid negative entry | `404`, `Cache-Status: trove; hit; detail=negative` |
| 9 | the populate check ([R-AUTHZ-008](../spec/authorization.md#r-authz-008--remote-repositories-who-may-populate-the-cache)) | a stored document as stale, else `403 populate_not_permitted`, audited as `deny.populate`; no negative entry is written |
| scan | the version the path maps to is blocked (R-VULN-015, `Enforce.GateMiss`) | the refusal of the scanning; nothing is fetched |
| 10 | the collapsing protocol, then the fetch | below |

Around these steps:

- A request authenticated by the session cookie never populates, so a link or an image in a page cannot make a browser bring a package into
  the cache; a request on an endpoint with `cache_fill: false` never populates either, and the refusal is counted in
  `trove_endpoint_cache_fill_refused_total{endpoint}` ([endpoints-and-exposure.md](endpoints-and-exposure.md#upstream-contact-from-an-endpoint)).
- A `HEAD` for an uncached artifact passes the same steps and then asks the upstream with `HEAD`, storing nothing (`head`).
- A format whose addressing follows a discovery document (NuGet's service index) resolves that document as an ordinary metadata entry of the
  repository under the caller's populate check before the fetch; a caller who may not populate gets the stored one or the refusal
  ([fetch.go](../../internal/proxy/fetch.go) `discover`).

A fetch that stores something writes the allowed populate event and the event `artifact.cached` in the transaction that commits the cache
entry, so no fill exists without its event; a request that joined another request's fetch writes none
([artifact.go](../../internal/proxy/artifact.go) `artifactEntry`).

## The request trove sends

| Rule | Where | Effect |
|---|---|---|
| One client per repository and configuration version, built from the outbound factory with purpose `remote`: the egress address policy at connect time, the trust store, `connectTimeoutMs`, `readTimeoutMs` as the wait for response headers, `proxy.max_redirects`, the hop policy of the credentials | [upstream/clients.go](../../internal/proxy/upstream/clients.go) `build` | a changed configuration or secret is a new client; a credential that cannot be resolved fails the request with `502 upstream_secret_unavailable` before it is sent (`guard`), remembered for 5 s so a flood costs one resolution per interval |
| The URL is the repository's URL with every canonical segment percent-encoded again; an empty, `.` or `..` segment or one with a NUL is refused | [types.go](../../internal/proxy/types.go) `UpstreamBase.Segments` | no client path decodes to a dot segment upstream (`TestUpstreamURLBuilder`) |
| A URL recorded by a document (the URL map) is used only for a path of the package the document was fetched for | [metadata.go](../../internal/proxy/metadata.go) `recordMappings` | a document of package A cannot steer the fetch of package B |
| The first URL's host is the upstream's or an `extraHosts` entry, and an `https` upstream is never left for `http` | [upstream/client.go](../../internal/proxy/upstream/client.go) `Allowed` | `502 foreign_host_denied`, `502 insecure_redirect`; nothing is sent |
| Redirects: at most `proxy.max_redirects` hops, none from `https` to `http`, each hop through the address policy and the TLS verification | [outbound/factory.go](../../internal/outbound/factory.go) `CheckRedirect` | `502 too_many_redirects`, `502 insecure_redirect`; a redirect may lead to any host the address policy admits, and the credential follows it only with `forwardAuthOnRedirect` ([egress.md](egress.md#which-hosts-receive-credentials)) |
| The caller's `Authorization` is removed and the body is asked for without content coding | [fetch.go](../../internal/proxy/fetch.go) `request` | the client's hop policy alone adds the repository's credential; the caller's identity reaches the upstream log only |
| One retry, 250 ms later, of a connection failure that happened before a request byte was written; no other retry inside a request | [fetch.go](../../internal/proxy/fetch.go) `send`, [classify.go](../../internal/proxy/classify.go) `retryable` | |

**How long a fetch can run.** The clients of remote repositories have no overall timeout ([outbound/factory.go](../../internal/outbound/factory.go)
`defaults`). A body is bounded by its silence, `readTimeoutMs` without a byte (1 to 600 s, 15 s by default), which an idle timer enforces while
a read waits for the upstream ([body.go](../../internal/proxy/body.go) `idleBody`, `TestIdleBody`), and by the size limit. An upstream that keeps
sending a little before each silence ends holds its fetch, its flight and its slot until the limit is reached
([H-582](#h-582--one-remote-repository-can-hold-every-fetch-slot-of-a-replica)). A shadowing-check probe is bounded by
`connectTimeoutMs + readTimeoutMs + 5 s` ([probe.go](../../internal/proxy/probe.go)), and an exchange outside the cache (the advisory request of an
npm audit) by its caller's deadline or 5 minutes ([exchange.go](../../internal/proxy/exchange.go) `boundExchange`).

## What is stored, and what it must pass first

Nothing of an answer is stored or completed to a client before these checks ([fetch.go](../../internal/proxy/fetch.go) `admitBody`,
[artifact.go](../../internal/proxy/artifact.go) `artifact`, [integrity.go](../../internal/proxy/integrity.go)):

| Check | Answer, and what is left behind |
|---|---|
| `checksumPolicy: require` and no announced SHA-256 or stronger digest, before any byte is fetched | `502 upstream_digest_missing` |
| The format's check of status and headers (`AcceptResponse`) | its refusal; nothing stored |
| A media type of `proxy.blocked_content_types` (`text/html`, `application/xhtml+xml` by default) while `integrity.blockUnexpectedContentTypes` is on (the default), unless the format expects the type or the repository allows it | `502 upstream_content_type_blocked`; counts toward the circuit |
| An announced length above the effective limit (`limits.maxArtifactBytes`, else `proxy.max_artifact_bytes`, 10 GiB by default; for a document `limits.maxMetadataBytes`, else `proxy.max_metadata_bytes`, 256 MiB by default, and a format's bound below both), or a body that crosses it | `502 upstream_item_too_large`; no negative entry, so a raised limit applies at once |
| The announced digests against those computed while spooling, under `verify-if-available` (the default) and `require`; under `ignore` and `pass-through` only for a format whose descriptor sets `VerifyAnnouncedDigests`, which neither npm nor NuGet does today, so an npm remote with `ignore` stores a tarball whatever `dist.integrity` says | `502 upstream_integrity_mismatch`, the event `proxy.integrity_mismatch`, `trove_remote_integrity_failures_total`; nothing stored, no negative entry |
| A metadata document: the format's validation outside any transaction (`PrepareFill`) | the previous entry stays |
| An artifact the format inspects (`InspectFill`, NuGet's identity check) | `502` to every request of the fetch; nothing stored; no byte reached a client before the hook ran |
| The cache entry already holds another digest for the path (a duplicate fetch on another replica committed first) | the committed artifact is served to everyone; the leader's own client is aborted; the event `proxy.upstream_digest_changed` |

**The leader's own client.** The request that fetches may receive the body while it arrives, but only when the upstream announced its length,
the body is longer than 64 KiB and the format inspects nothing; the last 64 KiB are withheld until the digest check has passed and the entry has
committed, and a failure after the status line aborts the connection, so no client completes an unverified download
([integrity.go](../../internal/proxy/integrity.go) `stream`, `TestStreamWithholdsTheTail`). A write to a client that stalls is bounded by one
minute, after which that client is dropped and the fetch goes on. Every other request, and the leader in the other cases, is served from the
stored entry.

**Immutable stays immutable.** When refreshed metadata announces another digest for an artifact that is cached, the cached artifact is served
unchanged and the event `proxy.upstream_digest_changed` names both digests, at most once an hour per item and pair of digests on a replica
(`checkAnnounced`). A client then fails its own integrity check instead of receiving other bytes for the same version.

Fetched artifacts become items of the remote through the commit of an upload (origin `fill`, the final URL without user info as
`upstream_url`); documents are cache entries whose bytes are blobs. While the object store is unavailable a verified artifact is streamed to the
leader's client and nothing is stored.

## The negative cache

A negative entry remembers an upstream `404` or `410` for one path of one repository ([negative.go](../../internal/proxy/negative.go)).

- **Who writes one.** Only a fetch that reached the upstream, so only a request that passed the populate check, and only for `404` and `410`,
  before the flight is released (`notFound`). Refusals of the patterns and claims, populate refusals, integrity and size refusals, other statuses,
  a probe of the shadowing check and a pass-through query write none.
- **Where it lives.** In the replica's bounded L1 (`proxy.negative_l1_entries`, 100,000 by default) and in Valkey under the key of
  (repository id, path, variant), with the status and the time the request was sent. It is the same for every caller: it is not keyed by
  the caller.
- **Who sees it.** Every caller whose request reaches step 8, that is every caller the dispatcher let read the path, whether it may populate or
  not; the answer is the `404` of an item that does not exist upstream.
- **What a reader accepts.** An entry counts only while its sent time lies between now minus the repository's current `notFoundTtlSeconds`
  and now plus 5 s, and no invalidation of scope `negative` or `all` covering the path is newer than that time minus 5 s (`valid`,
  `TestNegativeReaders`). The key's own expiry is not trusted. `notFoundTtlSeconds: 0` turns the cache off.
- **What it can do.** Refuse and nothing else: it never serves content and never admits a write; the shadowing check and its probe do not read
  it ([probe.go](../../internal/proxy/probe.go)).
- **Who can poison it.** The upstream, with its own `404`, for up to `notFoundTtlSeconds` (at most 86,400 s), and a writer on Valkey
  ([H-49](../spec/architecture.md#h-49--write-access-to-valkey-denies-service)). A holder of `manage` on the repository clears either with an
  invalidation of scope `negative` (`POST …/cache/invalidate`, audited as `repository.cache_invalidated`,
  [cache_api.go](../../internal/api/v1/cache_api.go)).

**Gone marks.** A `404` or `410` for a stored document marks its entry gone instead of deleting it and writes `proxy.upstream_gone`. When the
document is a package document and the format does not track removal per version (NuGet), every request of that package answers `404`, cached
artifacts included, on the remote, its cache view and in every virtual repository; each replica holds the gone set in memory, reloaded through
the change area `upstream_state` ([engine.go](../../internal/proxy/engine.go) `goneGate`). A version withdrawn from a package that stays is not
covered ([H-5](../spec/repositories-and-proxy.md#h-5--version-removals-reach-cached-artifacts-only-where-the-format-tracks-versions)).

## One fetch for many requests

Concurrent misses of one entry cause one upstream fetch per cluster ([flight.go](../../internal/proxy/flight.go)).

- **The key is the entry, not the caller:** (repository id, path, variant). A request joins a flight only after it has passed steps 1 to 9 and
  the scanning's gate itself, so a caller who may not populate never joins and never waits for a fetch: it gets the stored copy or the `403`.
- **What a request that joined receives.** For an artifact, the stored entry, served through its own serve function, so the format's serve
  check and the scanning's verdict are decided for that request ([artifact.go](../../internal/proxy/artifact.go) `artifactAnswer`,
  `serveStored`); for a document, the stored document, which the format renders for that request; or the flight's error or negative outcome.
  It never receives the leader's stream. Its answer carries `collapsed` in `Cache-Status`.
- **What it never receives.** An outcome that only a mirror's background request understands (a pre-emption, a wait for the circuit or the
  spool, a rate limit) is carried apart (`flightResult.own`): a client that joined such a flight finds the result empty and fetches for itself,
  at most three rounds, then outside any flight (`collapse`, `fetchDirect`).
- **Cancellation.** The fetch runs detached from the client that started it: a client that goes away stops receiving and does not stop a fetch
  others wait for (`detached`). Fetches still running 5 s before the shutdown deadline are cancelled and their flights released with `failed`,
  so waiters on other replicas take over (`Quiesce`); a draining replica leads no new cross-replica flight (`Drain`).
- **Across replicas.** The flight lock in Valkey (`cluster.FlightLock`, [R-ARCH-033](../spec/architecture.md#r-arch-033--cross-replica-request-collapsing-through-valkey-flightlock));
  a waiter repeats the checks of steps 4 to 8 after every wait and fetches without the lock after `proxy.fetch_wait_timeout_seconds` (120 s by
  default), so a stuck or foreign lock delays a request and never fails it. Without Valkey each replica fetches under its own single flight.
- **Duplicates are correct.** A bypass, a lost lock or an unavailable Valkey can fetch one entry twice: blob commits are idempotent by content,
  the metadata upsert keeps the later fetch, and the artifact upsert refuses a different digest (above).

## A failing upstream, the circuit, and offline

The classification of R-REPO-029 decides what counts toward the circuit ([classify.go](../../internal/proxy/classify.go) `classifyStatus`): `429`,
`500`, `502`, `503` and `504`, connection failures, timeouts and a blocked content type count; `401` and `403` (`502 upstream_auth_failed`), every
other status, other `5xx` answers included (`502 upstream_unexpected_status`), certificate failures (`502 tls_untrusted`), egress refusals (`502 egress_denied`), redirect violations, size
and integrity refusals do not. A `429`
opens the circuit at once for its `Retry-After`, capped at `circuitOpenSeconds`. Egress and TLS refusals write the events `proxy.egress_denied`
and `proxy.tls_untrusted`, at most once a minute per repository, kind and host ([engine.go](../../internal/proxy/engine.go) `securityEvent`).

- **The circuit is per repository and shared by all replicas**: a row of `remote_upstream_state`, counted with one statement while it is
  closed, opened at `failureThreshold` (5 by default) for `circuitOpenSeconds` (300 by default; 0 turns it off), and announced through the
  change area `upstream_state` ([state.go](../../internal/proxy/state.go)). It counts per repository, not per path: consecutive counted failures of
  any paths open it for every caller of the remote ([H-583](#h-583--a-caller-who-may-populate-can-open-the-circuit-of-a-remote-for-every-caller)).
  After the period the first request on each replica probes; the others answer as open until it ends. A mirror's background requests never count,
  reset or probe.
- **While it is open** an uncached path answers `503 upstream_assumed_offline` with `Retry-After`, and a stored document is served stale within
  `cache.maxStaleSeconds`.
- **Offline.** With `availability.mode: offline` or `proxy.global_offline`, no request of the repository reaches its upstream, the format's
  exchanges included (`ErrOffline`): stored entries are served whatever their age, an uncached path answers `404 not_cached_offline`
  ([engine.go](../../internal/proxy/engine.go) `offline`).
- **Lost content.** A cached item whose object is missing or damaged is a miss that is filled again; a request that cannot fill it gets
  `503 content_unavailable`, never the `404` of an uncached item (Q-REPO-202 to Q-REPO-205, [lost.go](../../internal/proxy/lost.go)).

## What fills the cache, and what removes from it

| Fill | Stored |
|---|---|
| A client's miss that passed the populate check | the artifact as an item, the document as a cache entry |
| A pass of a remote in mode `mirror`, as the system ([mirror.md](mirror.md)) | the same, through the same fetch |
| A `HEAD` of an uncached artifact | nothing |
| A probe of the shadowing check (R-REPO-048) | nothing, but for the discovery document of a format that has one |
| A pass-through query (NuGet's forwarded search) | the answer in the replica's memory only (`proxy_query_results`, at most one hour, within `proxy.query_cache_bytes`), served to every caller of the repository while current; the format filters it per caller ([query.go](../../internal/proxy/query.go)) |
| An exchange outside the cache (the advisory request of an npm audit forward) | nothing; it takes no fetch slot and neither reads nor moves the circuit ([exchange.go](../../internal/proxy/exchange.go)) |

Nothing is removed on its own: the cleanup of unused content is not built (`unusedRetentionHours` above 0 is refused), so the cache grows with
every path a populating caller asks for, bounded per item by the size limit and not in total. A cached artifact is an item of the remote and is
removed through the item operations, which decide `delete` on every path ([files/delete.go](../../internal/files/delete.go)); its cache entry
goes with it. An invalidation needs `manage`, makes metadata or negative entries count as expired, and deletes nothing.

## The fetch slots

Every upstream request of a fetch, a `HEAD`, a probe or a pass-through query takes a fetch slot of the replica it runs on
([slots.go](../../internal/proxy/slots.go), [R-REPO-040](../spec/repositories-and-proxy.md#r-repo-040--size-and-concurrency-limits)):

- **The replica's limit**, `proxy.max_concurrent_fetches` (256 by default), over all repositories, held until the fetch ends: the
  upstream exchange, the verification, the format's inspection and the commit.
- **The repository's share**, `ceil(limits.maxConcurrentFetches ÷ n)` (64 by default, 1 to 4096), with n the live replicas that fill client
  misses (`cluster.FillCount`, Q-ARCH-297), read at every acquisition (Q-REPO-97). It is held only for the upstream exchange: it goes back
  when the body has been read into the spool, when the answer has no body the fetch reads, or when the request failed
  ([Q-REPO-235](../spec/repositories-and-proxy.md#q-repo-235--the-repositorys-share-of-a-fetch-slot-ends-with-the-upstream-exchange-the-replicas-slot-with-the-fetch),
  `slotHold.exchanged`). The installation-wide total of a repository's upstream exchanges therefore stays within
  `maxConcurrentFetches + n − 1`, with or without Valkey.
- **Waiting.** A request that joined a flight takes none. One that finds no free slot waits up to `proxy.queue_timeout_seconds` (10 s by default)
  and then answers `503 fetch_queue_timeout` with `Retry-After: 5`.
- **The mirror's background share** is `min(mirror.concurrency, max(1, floor(maxConcurrentFetches ÷ 4)))` of the repository
  (`BackgroundShare`, R-REPO-055, Q-REPO-140), returned with the share when its exchange ends. A client fetch that finds no free slot pre-empts
  the youngest background fetch of the repository that is still in its exchange (one that is storing is not pre-empted, Q-REPO-235), or of any
  repository when only the replica's limit is spent, and raises the signal `mirror_yield`, which makes other processes pre-empt theirs
  ([isolation-and-coordination.md](isolation-and-coordination.md#the-signal-that-makes-a-mirror-yield)). Client fetches never pre-empt each
  other.

What one party can take: one caller who may populate can take a repository's whole share with requests for distinct uncached paths, since
nothing counts per caller; and a repository's share is not capped below the replica's limit, so one repository whose share reaches the limit
can hold every slot of the replica ([H-582](#h-582--one-remote-repository-can-hold-every-fetch-slot-of-a-replica)). Forwarded searches take
the same slots as downloads ([H-353](nuget-remote.md#h-353--forwarded-searches-share-the-remotes-circuit-and-fetch-slots-with-its-downloads)).

## The token handshake and the credential hosts

Status: built in the engine ([authenticator.go](../../internal/proxy/authenticator.go), [revalidate.go](../../internal/proxy/revalidate.go),
[formathost/remote.go](../../internal/formathost/remote.go)); no registered format has a remote adapter that implements it yet, so the
generic test adapter of [proxytest](../../internal/proxy/proxytest) is what runs it until the OCI remote adapter (R-OCI-021) registers. This
section extends this page; the OCI format has no page of its own for it.

A container registry answers a request without a token with `401` and a challenge that names a token service, and the upstream chooses that
service's host. The engine therefore applies the host and credential rules of R-REPO-033 to an address that comes from the party it does not
trust, before a byte is sent:

| Rule | Where | Effect |
|---|---|---|
| The token request's host is the upstream origin or an `extraHosts` entry, an `https` upstream is never left for `http`, and the address names no user | [authenticator.go](../../internal/proxy/authenticator.go) `token` calls [upstream/client.go](../../internal/proxy/upstream/client.go) `Allowed` | `502 foreign_host_denied`, `502 insecure_redirect`; the service sees no request (`TestHandshake`, `TestR_OCI_021_RealmOnAnotherHostNeedsAnExtraHostsEntry`) |
| The repository's credential goes to the token service only by the hop policy: to the origin and to `extraHosts` entries with `sendCredentials: true`; an entry without it is asked anonymously, and `authMode: none` sends none; the credential goes to the service's host and to no redirect target of it, also with `forwardAuthOnRedirect` | [outbound/hop.go](../../internal/outbound/hop.go) `CredentialPolicy` | the credential reaches the hosts the operator named for it and no other (`TestHandshake`) |
| The token goes to the upstream origin only, in place of the credential: not to an `extraHosts` entry (with or without `sendCredentials`), not to a redirect target, not over a downgrade to `http`; a request marked without credentials gets none | [outbound/hop.go](../../internal/outbound/hop.go) `Decorate`, `WithAuthorization` | a registry that redirects to a blob host does not hand the token on (`TestR_OCI_021_HandshakeTokenGoesToTheOriginOnly`, `TestHandshake`) |
| At most 64 KiB of the service's answer is read; a larger one is `502 upstream_error` and nothing is kept | [authenticator.go](../../internal/proxy/authenticator.go) `requestToken` | an upstream cannot make the engine buffer an unbounded answer |
| The `Authorization` value the format returns is non-blank, at most 8 KiB and a valid header field value (`httpguts.ValidHeaderFieldValue`: no control character, no DEL) | `requestToken` | a service cannot inject a header, and cannot cache a token the transport refuses |
| A token lives until 10 s before the lifetime the format read (60 s when it read none), at most one hour; it is cached per replica in the registered cache `proxy_upstream_tokens`, never in PostgreSQL or Valkey. The key is the repository, its configuration version and a digest of the format's key (refused above 1 KiB), the token service (scheme, host, port, path, service) and the credential in effect | `registerTokens`, `tokenKeyOf`; [architecture.md](../spec/architecture.md) R-ARCH-032 | a changed credential, `extraHosts` or URL (a new configuration version), a rotated secret behind a reference (a new credential fingerprint) and another token service never meet the old token; a token service cannot seed another service's entry; the cache is bounded by count and each key by 32 bytes |
| The last challenge of a repository is remembered per configuration version (at most 8 KiB) and handed to the format before a request, so that the credential goes to the registry once and a token is sent with the request | `rememberChallenge`, `preemptive` | a challenge is a hint and never the answer: it is learned only from the origin and after its token was accepted, kept 5 minutes, forgotten when the pre-emptive step fails or its token is refused, and then the request is sent without a token so its own `401` is followed; a hostile or retired challenge costs one request, not the repository; the credential no longer goes to the registry on every request ([Q-OCI-82](../spec/formats/oci.md)) |
| A rejected token is dropped by comparing its value, and a token the transport refused is dropped | `dropToken` | a request cannot drop the fresh token another stored |
| The request to the token service runs in a goroutine of its own, ended by the engine's shutdown or after 30 s; a caller that leaves gets `503 unavailable` and counts toward no circuit; a panic of the format is the request's failure | `tokenOnce`, `leadToken` | a stuck or panicking format cannot hold a call forever |
| One request to the token service for the concurrent requests of a replica that need the same token | `tokenOnce` | a burst of pulls is one request to the service |
| `authMode: bearer` runs no handshake: the stored secret is the token | `token` | the secret is sent to the origin as it is |
| A failure of the token service is the answer of the request and counts toward no circuit; an egress or TLS refusal at its host writes `proxy.egress_denied` or `proxy.tls_untrusted` naming that host and counts `trove_remote_egress_denied_total`; for a mirror's request a `429` is the rate limit of R-REPO-054 | [classify.go](../../internal/proxy/classify.go) `classifyError` | see [H-612](#h-612--a-failing-token-service-is-asked-by-every-request) |

**What an upstream can make trove send where.** It can name any host on the repository's own list as the token service, so the repository's
credential can be sent to every `extraHosts` entry that has `sendCredentials: true` and to the origin, and to no other host
([H-610](#h-610--a-hostile-upstream-can-direct-the-credential-to-every-sendcredentials-host)). It cannot make trove send the credential or
the token to a host the repository does not list, to a cleartext address of an `https` upstream, or to the host of a redirect of the token service (also with `forwardAuthOnRedirect`) or of the registry (the token never follows a redirect). The format
builds the token request from the challenge's service and scope, so those strings come from the upstream as well; what the format does with
them is on its own page.

**Revalidation.** A format that gives its own revalidation request ([revalidate.go](../../internal/proxy/revalidate.go)) has that request
sent like any other (the host check, the hop policy, the handshake on a `401`). A `404` or `410` to it marks the document gone and a `429` or
`5xx` follows the stale rules and the circuit, as for a conditional `GET`; only a `2xx` goes to the format, whose verdict renews the
freshness without a body or makes the engine fetch ([Q-OCI-81](../spec/formats/oci.md#q-oci-81--a-head-that-is-not-a-2xx-is-the-engines-answer-and-a-2xx-that-is-not-unchanged-is-a-get)).

## What this does not cover

- [H-4](../spec/repositories-and-proxy.md#h-4--malicious-content-from-a-legitimate-upstream): verification proves only that the bytes are what the upstream announced; a malicious package of a legitimate upstream is fetched and stored like any other.
- [H-5](../spec/repositories-and-proxy.md#h-5--version-removals-reach-cached-artifacts-only-where-the-format-tracks-versions): a version withdrawn from a package that stays upstream remains reachable by exact path where the format does not track versions.
- [H-6](../spec/repositories-and-proxy.md#h-6--remote-repositories-reach-private-networks): a remote repository can be pointed at an internal service, whose answers then come back through the cache.
- [H-11](../spec/storage.md#h-11--object-store-principals-bypass-troves-authorization-and-integrity-checks): cached artifacts and documents are blobs that a principal of the bucket can read or replace past every check above.
- [H-49](../spec/architecture.md#h-49--write-access-to-valkey-denies-service): a writer on Valkey can hide paths with forged negative entries and delay fetches with held flight locks.
- [H-282](egress.md#h-282--the-upstream-test-tells-a-holder-of-repositorieswrite-config-what-it-sees-of-an-address): the upstream test reports what the probing replica sees of an address.
- [H-353](nuget-remote.md#h-353--forwarded-searches-share-the-remotes-circuit-and-fetch-slots-with-its-downloads): pass-through queries share the circuit and the fetch slots of the downloads.
- [H-484](vulnerability-scanning.md#h-484--a-block-that-arrives-while-a-fetch-is-running-does-not-stop-that-fetch): a block that arrives while a fetch runs does not stop that fetch.
- [H-526](isolation-and-coordination.md#h-526--whoever-can-publish-on-the-valkey-signal-channel-can-hold-a-mirror-back): whoever can publish on the signal channel can keep a mirror's background fetches from running.
- [H-610](#h-610--a-hostile-upstream-can-direct-the-credential-to-every-sendcredentials-host): the upstream names the token service, and the credential goes to any `sendCredentials` host it names.
- [H-611](#h-611--handshake-tokens-sit-in-the-memory-of-a-replica-until-they-expire): a token sits in process memory until it expires.
- [H-612](#h-612--a-failing-token-service-is-asked-by-every-request): a failing token service is asked by every request, and no circuit protects it.
- [H-613](#h-613--one-upstream-can-evict-the-tokens-of-other-repositories-from-a-replicas-cache): the token cache is shared, with no cap per repository.
- [H-569](backup-and-restore.md#h-569--upstream-storage-and-format-settings-of-a-repository-come-back-with-a-restore): a restore brings back the upstream URL, the credential's hosts, the integrity settings and the offline mode of the restore point.

### H-582 · One remote repository can hold every fetch slot of a replica

Component: the fetch slots of the engine ([slots.go](../../internal/proxy/slots.go) `fits`, `acquire`) together with the stall bound of a
transfer ([body.go](../../internal/proxy/body.go) `idleBody`) and the clients of purpose `remote`, which have no overall timeout
([outbound/factory.go](../../internal/outbound/factory.go) `defaults`).

Subject: the client misses of every other remote repository whose requests reach the same replica: they wait `proxy.queue_timeout_seconds` and
answer `503 fetch_queue_timeout` while the slots are held. Cached content is served as before.

Live today. A fetch takes a slot when the replica has one free and the repository holds fewer than its share; the share,
`ceil(limits.maxConcurrentFetches ÷ n)`, is not capped below `proxy.max_concurrent_fetches`, and `maxConcurrentFetches` accepts up to 4096. The
share and the replica's slot are both held while the upstream exchange runs (Q-REPO-235 returns the share when it ends, the replica's slot
after the commit), and an exchange ends only at its size limit, at `readTimeoutMs` without a byte (up to 600 s) or at the shutdown; the fetch is detached from its client, so a client that goes away does not free it. Client fetches pre-empt only background fetches.
The setting is an administrator's choice and also the project Owner's: [delegated.go](../../internal/repository/delegated.go) restricts an
Owner's credential, outbound path and mirror settings, not the limits or the timeouts.

What makes it real: an upstream that answers slowly, by fault or by intent (its operator, a compromise, or a project Owner who configures a
remote of their own project against a host they control), behind a remote whose share reaches the replica's limit, and a caller who may
populate it requesting distinct uncached paths. With the defaults (a share of 64 on one replica against a limit of 256) one repository holds a
quarter of the slots, and four such repositories hold all of them.

### H-583 · A caller who may populate can open the circuit of a remote for every caller

Component: the circuit of R-REPO-029 ([state.go](../../internal/proxy/state.go) `circuitFailure`, the classification in
[classify.go](../../internal/proxy/classify.go)).

Subject: every caller of the remote repository: while the circuit is open, every uncached path answers `503 upstream_assumed_offline` and a
stored document is served stale; cached artifacts are served as before.

Live today. The circuit counts consecutive failures per repository, not per path, and a failure is counted for any path that a populating
request sends: a `500`, `502`, `503` or `504` (another `5xx` is `upstream_unexpected_status` and not counted), a timeout, a connection failure and, while `integrity.blockUnexpectedContentTypes` is on (the default), a `200` with a
blocked media type (`text/html` and `application/xhtml+xml` by default). None of them is negatively cached, so the same path can be asked again
at once. A caller who knows one path that the upstream answers that way can send `failureThreshold` requests for it one after another (5 by
default; concurrent ones collapse into one fetch) with no success of another request in between, open the circuit for `circuitOpenSeconds`
(300 s by default), and repeat it after each probe. A `429` opens it at once.

What makes it real: a remote that few other requests keep healthy, a populating caller (anonymous where an anonymous `read` grant meets
`cacheFillAction: read`), and an upstream with one path that fails or answers with an HTML page, which is common for a proxying or
self-hosted registry. The same holds for the searches that share the circuit
([H-353](nuget-remote.md#h-353--forwarded-searches-share-the-remotes-circuit-and-fetch-slots-with-its-downloads)).

### H-610 · A hostile upstream can direct the credential to every `sendCredentials` host

Component: the choice of the token service by the upstream's challenge ([authenticator.go](../../internal/proxy/authenticator.go) `token`),
bounded by `Allowed` and the hop policy ([outbound/hop.go](../../internal/outbound/hop.go)).

Subject: the upstream credential of a repository with `authMode: basic` (a `bearer` repository runs no handshake) and the hosts the operator
listed with `sendCredentials: true`.

Live today in the engine, for a remote whose adapter implements the handshake (the OCI format, [oci-remote.md](oci-remote.md)). The upstream's `401` names the realm;
the engine sends the token request there with the repository's credential when the realm is the origin or a `sendCredentials` entry.
A hostile or compromised upstream can therefore make trove send the credential to any of those hosts, with a path and a query of the
upstream's choosing, as often as it can make trove ask (once per token). Hosts without `sendCredentials`, and every host not on the list,
get no credential.

What makes it real: an operator who lists more than one `sendCredentials` host for an upstream, one of which is not run by the upstream's
operator, and an upstream that is compromised. The default proposal for the well-known OCI upstream is one host, `auth.docker.io`.

### H-611 · Handshake tokens sit in the memory of a replica until they expire

Component: the registered cache `proxy_upstream_tokens` ([authenticator.go](../../internal/proxy/authenticator.go) `registerTokens`).

Subject: the tokens of the repositories' upstream handshakes.

Live today in the engine. A token is an `Authorization` value in a plain string, kept in the process for at most an hour (usually the
lifetime the service named, minus 10 s) and not wiped when it expires or when the cache is flushed; it is in the memory of every replica
that used it. Whoever can read the process memory (a core dump, a debugger, a heap profile exposed by mistake) reads the tokens that are
live, with the rights the repository's credential gave them at the upstream. The credential itself is in the process anyway
([data-and-secret-flow.md](data-and-secret-flow.md#upstream-credentials-of-remote-repositories)); the token adds nothing a holder of the
credential does not have, but it outlives a revocation of the credential at the upstream by its remaining lifetime.

What makes it real: a credential revoked at the upstream and a token issued just before; the token is then valid at the upstream for up to the
rest of its lifetime. A rotation on trove's side does not reach it: the credential in effect is part of the key, so the old token is unreachable at
once (on a replica within the 10 s a changed secret file takes), and so is the token of an older configuration version after a change; an
unreachable token stays in memory until it expires or the cache evicts it, also one that was stored after the change handler had run.

### H-612 · A failing token service is asked by every request

Component: the handshake outside the circuit ([classify.go](../../internal/proxy/classify.go) `classifyError`, [authenticator.go](../../internal/proxy/authenticator.go) `tokenOnce`).

Subject: the token service's host (for the well-known OCI upstream `auth.docker.io`) and the callers whose requests need a token.

Live today in the engine. A failure of the token service is the answer of the request and counts toward no circuit, because the circuit
describes the registry; concurrent requests of a replica share one token request, but requests one after the other each ask again.
While the service fails or is slow, every uncached request of the remote costs one request to it, held for the length of the failure under
the request's fetch slot, and nothing remembers the failure (a failing pre-emptive step forgets the remembered challenge and the request goes out
without a token, which costs the registry one `401` and the service one more request).

What makes it real: an outage or a rate limit of the token service while clients keep pulling. The effect on the service is bounded by the
fetch slots of the replica and the repositories, the effect on callers is a `502` per request instead of `503 upstream_assumed_offline`.

### H-613 · One upstream can evict the tokens of other repositories from a replica's cache

Component: the registered caches `proxy_upstream_tokens` and `proxy_upstream_challenges` ([authenticator.go](../../internal/proxy/authenticator.go)
`registerTokens`).

Subject: the tokens and challenges of every other remote repository on the replica.

Live today in the engine. Each cache is bounded by count (4096 entries, about 33 MiB at the largest entry size, about 65 MiB for both) and
shared by all repositories. The token cache holds one entry per key, and the key holds the scope, which comes from the upstream's requests, and
the token service's path and `service`, which come from its challenge: an upstream whose repository is asked for many distinct scopes (distinct
image names), or one that varies the token service's path or `service` (up to two keys per request), fills `proxy_upstream_tokens` and evicts
the least recently used entries of other repositories. The challenge cache holds one entry per repository and configuration version, so
the eviction applies to the token cache only. The effect is more requests to those repositories' token services, never a wrong token: a
missing token is fetched again.

What makes it real: a repository that is pulled with very many distinct scopes within a token lifetime (a mirror pass over a large registry),
next to repositories whose tokens the replica would otherwise keep. No cap per repository exists.
