# trove Bastion

Status: specified for phase 5b of the [project plan](../projektplan/BETA.md#phase-5b--trove-bastion-m13) and built. The Bastion's own side is
wired into one role ([developer page](../developer/bastion.md#how-the-role-is-wired-package-4c)): the request flow
([internal/bastion/engine](../../internal/bastion/engine)), the guard, the cache backends, the Valkey state and the link to the main
installation. On the main installation's side the registrations and the Bastion token
([below](#the-registration-and-its-token-on-the-main-installation)) and the interface (recognition, check, feed, download intake,
contact; [its section](#the-main-installations-side-of-the-interface)) are built. This page describes the code, with links to it, and
the threat model of [spec/bastion.md](../spec/bastion.md).

This page says what the internet-facing Bastion can do, what an attacker who reaches it can do, what the Bastion's own token
can do, and where abuse is stopped. The roles and boundaries of the main installation are in
[roles-and-trust-boundaries.md](roles-and-trust-boundaries.md), its tokens in [tokens-and-credentials.md](tokens-and-credentials.md)
and its permissions in [permissions.md](permissions.md); the rules are in the spec page.

## What the Bastion is trusted with

```text
 internet ──► HAProxy Ingress ──► Bastion replicas ──(Bastion token + caller's credential)──► main endpoint
 (anyone)     stage 1: coarse     stage 2: blocks, limits,                                    decides every
              per-address limit   failed-auth timeout, budget                                 answer, renders
                                  │            │                                              every byte
                                  ▼            ▼
                      own bucket or volume   Valkey (optional)
                      (bytes, signed entries) (blocks, counters, signed answers)
```

| The Bastion holds | It does not hold |
|---|---|
| The Bastion token (`trv_bst_`), read from a mounted Secret and re-read on change (R-BAS-006) | Any user, group, policy, token hash or session of the main installation (R-BAS-020) |
| The main installation's answers about callers, keyed by the SHA-256 of the presented token, for at most `bastion.answers.period` (plus the grace period while the main installation is unreachable) | A caller's token beyond the request: never on disk, never in Valkey (R-BAS-019) |
| Cached artifacts under their SHA-256, shared documents, negative entries, and per-caller documents in memory only (R-BAS-022 to R-BAS-024) | Any credential for the main installation's object store, PostgreSQL or upstream registries (R-BAS-030); the chart does not limit where its pods connect ([H-522](#h-522--the-chart-restricts-no-egress-of-the-bastion-pods)) |
| The signing key for its records in Valkey and the bucket (R-BAS-032) | A right to write Kubernetes Secrets or to issue tokens (R-BAS-006, R-BAS-045) |

Every decision about a caller is the main installation's. A miss is forwarded with the caller's own credential and decided by
the main installation's own route; a cached artifact is served only after a `full` read answer or a conditional request with the
caller's credential answered `304` through the real route (R-BAS-022, Q-BAS-27); a shared document only to a caller whose `full`
read the main installation confirmed (R-BAS-023). The Bastion carries no format knowledge, so there is no second classifier
whose disagreement with the main installation could serve bytes.

## What an attacker who reaches the Bastion can do

- **Without a credential.** Read what `anonymous` may read at the main installation, exactly when the main installation admits
  anonymous requests (R-BAS-019); otherwise every request answers `401` from the Bastion without reaching the main installation.
  Misses cost the attacker nothing, so they are absorbed: names the main installation does not have are answered from the
  negative cache to callers who read the whole repository (R-BAS-024), anonymous requests have stricter per-address limits and a
  per-address miss budget (R-BAS-035), and anonymous misses can hold only a share of the concurrency budget towards the main
  installation (R-BAS-025). Spread over many addresses, they still take that share (H-388).
- **With guessed or stolen tokens.** A malformed token is refused without a request (R-AUTH-022's checksum). More than 20 failed
  authentications from one address put it into a timeout in which unverified credentials are refused without a request, while
  known credentials from that address keep working (R-BAS-036). Behind the Bastion, the main installation still applies its own
  failed-credential throttle to the real client (R-BAS-017).
- **With a valid token.** Read what the token may read at the main installation, no more: the shape of the answer is the decision
  point's (R-BAS-012), a narrowed reader never receives a shared document (R-BAS-023), and a revocation at the main installation
  reaches every Bastion replica within the feed's poll interval and one round trip (R-BAS-026). During an outage of the main
  installation a revoked token reads on for the grace period (H-383).
- **Writes.** None reach the main installation as writes: the Bastion sends a bodiless probe, the main installation answers `403
  bastion_read_only` in the protocol's shape, and no upload body leaves the Bastion (R-BAS-014).
- **Learn about others.** The Bastion sends no cache state (Q-BAS-40), and negative entries go only to callers who read the whole
  repository, and a `404` that is not `absent` is never kept for anybody, so a narrowed reader cannot time hidden names (Q-BAS-42, Q-BAS-124). The time of a hit still tells whether a readable package
  was fetched recently (H-390).
- **Flood it.** See [Where abuse is stopped](#where-abuse-is-stopped).

## What the Bastion token can do

The token has the type `bst` and belongs to a Bastion registration, a principal that holds no grants (R-BAS-004). Every route of
the main installation except the Bastion interface refuses it by type, before any lookup, as `Authorization`, Basic password or
API key (R-BAS-005).

| On the interface it can | It cannot |
|---|---|
| Run the handshake: learn the interface version, its own expiry, whether anonymous access and download recording are on | Read a package, a document or a file: on a forwarded request only the caller's credential decides |
| Check any credential and read its shape on named repositories, for a client address it names (R-BAS-011) | Write content, change a setting, read the audit trail, issue or rotate a token |
| Read the revocation feed: token IDs, principal IDs, qualified repository names `<projectKey>/<repoKey>` (R-BAS-015) | Reach any route on a further endpoint |
| Submit download events for its cached downloads, marked with its registration (R-BAS-016) | Submit any other audit event type |
| Make the main installation mark its forwarded responses and refuse writes (R-BAS-013, R-BAS-014) | Turn an anonymous forwarded request into anything but anonymous |

What its holder can still do with those rights is H-386. It is revoked like every token, journaled, and renewed by hand with an
overlap (R-BAS-006); the Bastion warns 30 days before it expires and alerts when the main installation refuses it (R-BAS-041).

## The registration and its token on the main installation

Built ([R-BAS-004](../spec/bastion.md#r-bas-004--bastion-registrations-and-the-bastion-token-on-the-main-installation),
[R-BAS-005](../spec/bastion.md#r-bas-005--what-the-bastion-token-can-do)). The token's whole security is in the type and in the registration
that owns it:

- **A registration holds no grant.** Every write that would give it a policy entry, a role, an ownership or a membership answers `422
  principal_not_allowed`, and the database refuses the rows when they are written around the API
  ([permissions.md](permissions.md#a-bastion-registration-holds-nothing)).
- **The token is refused everywhere but on the interface.** As `Authorization: Bearer`, as the Basic password and as `X-NuGet-ApiKey` it
  is `401 invalid_token` before any lookup; listing it in an endpoint's `auth.token_types` stops start-up
  ([tokens-and-credentials.md](tokens-and-credentials.md#bastion-tokens)).
- **It always expires and is limited in number.** At most three unrevoked tokens per registration by default, so that an overlap fits and
  a forgotten token does not accumulate; the value is shown once and is in no event, no log line and no list.
- **Who manages it.** `endpoints:write` registers, issues, disables and deletes; `tokens:revoke` alone may revoke a token, so a
  security administrator can end a leaked token without managing endpoints. Every operation is an audit event with `resource_type`
  `bastion`, a revocation is journaled, and deleting a registration revokes its tokens first.
- **Disabling is not revoking.** A disabled registration refuses its tokens within `auth.revocation_bound` on every replica and enabling
  it restores them; nothing is deleted.

## The main installation's side of the interface

What the main installation does with a request that claims to come from a Bastion, in the order the request meets it
([R-BAS-008](../spec/bastion.md#r-bas-008--interface-routes-recognition-and-headers) to
[R-BAS-017](../spec/bastion.md#r-bas-017--the-real-client-and-the-bastions-host-at-the-main-installation)):

| Mechanism | What it guarantees | Code |
|---|---|---|
| Recognition | A request is a Bastion's only with a valid, enabled `bst` token in `Trove-Bastion-Authorization` (a forward) or `Authorization` (an interface call, which carries no `Trove-Bastion-Authorization`); every other `Trove-Bastion-*` header is ignored. The stages behind it get a copy of the request without the Bastion token, so neither the dispatcher nor a remote repository's outbound client can pass it on. | [recognize.go](../../internal/bastionmain/recognize.go) |
| Route-family backstop | A forward reaches only `/api/<format>/` and `/files/`, judged on the path as the router decodes it segment by segment (`/api/%76%31/tokens` is `/api/v1/tokens`); anything else answers `404 not_found` before authentication, so a forward runs no route of the management API, the login tree or the UI with the caller's credential, and is never answered `bastion_token_invalid` for the caller's credential ([Q-BAS-115](../spec/bastion.md#q-bas-115--a-request-with-trove-bastion-authorization-is-a-forward-and-a-forward-reaches-only-the-package-families)) | [recognize.go](../../internal/bastionmain/recognize.go) |
| Forwarded headers | The Bastion's egress is listed in `bastions.trusted_proxies`, not in `server.trusted_proxies`. Its forwarded client address and host count only for a request whose Bastion token was recognized and that came from a listed address; any other request from that address - a workload sharing the egress - cannot choose its client address (rate limits, throttle, audit) or its response base URL ([Q-BAS-116](../spec/bastion.md#q-bas-116--the-bastions-forwarded-headers-count-only-for-a-recognized-request-from-an-address-of-bastionstrusted_proxies), [H-467](../spec/bastion.md#h-467--behind-the-main-installations-own-ingress-the-forwarded-host-is-what-the-ingress-reports)) | [recognize.go](../../internal/bastionmain/recognize.go), [wire_bastion.go](../../internal/app/wire_bastion.go) |
| Throttle of rejected Bastion tokens | The stage runs before the authentication stage, so it counts rejected `trv_bst_` tokens against the address that sent them (a budget of its own, the size of R-AUTH-038's); the token service asks the throttle after its cache, so a throttled address is refused a token the replica does not hold without a lookup, and a known one passes ([Q-BAS-65](../spec/bastion.md#q-bas-65--the-recognition-admits-an-interface-route-on-its-own-proof-throttles-tokens-it-cannot-place-and-counts-the-bastion-in-the-read-class), H-440). `token.rejected` names the request's source; the token's last use records the Bastion's own address | [recognize.go](../../internal/bastionmain/recognize.go), [authenticator.go](../../internal/bastionmain/authenticator.go), [bastion.go](../../internal/auth/token/bastion.go) |
| Interface routes | Admitted on the recognition alone, as the registration, which holds no grant and is no member of `authenticated` in the decision point; the registration's requests are charged to the read class under its token ([H-438](../spec/bastion.md#h-438--the-interface-calls-of-a-bastion-share-the-read-budget-of-its-token)) | [bastion_services.go](../../internal/api/v1/bastion_services.go), [index.go](../../internal/authz/index.go) |
| Write probe | Admitted as the anonymous caller for routing only, whatever the anonymous switches say; answered at the route match (`403 bastion_read_only` or `204`) before any decision, handler or body read ([Q-BAS-118](../spec/bastion.md#q-bas-118--a-write-probe-is-admitted-as-the-anonymous-caller-for-routing-only), [H-469](../spec/bastion.md#h-469--a-probe-admitted-for-routing-runs-any-handler-below-the-package-families-that-does-not-tell-the-marking-its-route)) | [bastion_services.go](../../internal/api/v1/bastion_services.go) |
| Check | The caller's credential is validated by the authentication stage of the default endpoint as a direct request from the caller's address: the endpoint's token types, the failed-credential throttle and `token.rejected` apply to that address, never to the Bastion's. A subject that is not usable gets no shapes. No `authz.denied` is written, and the request log line stays the registration's ([Q-BAS-119](../spec/bastion.md#q-bas-119--the-check-runs-the-credential-through-a-stage-that-records-nothing-in-the-request-log)). | [bastion_check_api.go](../../internal/api/v1/bastion_check_api.go) |
| Feed | Entries carry IDs and qualified repository names only. A cursor that is gone, from the future beyond the lookback or older than the audit retention, or too far behind, answers `reset`, never a truncated list. An event whose target cannot be read drops every answer. Every journaled event is either mapped or listed with the reason it narrows nothing a Bastion keeps, held to the audit registry by a test; moving a repository to another project and removing or re-roling a project member drop every permission answer ([Q-BAS-120](../spec/bastion.md#q-bas-120--unassigning-a-repository-from-a-project-drops-every-permission-answer-and-every-journaled-event-is-classified)). A restore makes the feed answer reset to a Bastion whose cursor lies before it, because the journal replay re-applies narrowings without writing audit events (R-REPO-061). A narrowed repository and a new claim end the cached answers of the repository and of the virtual repositories over it (`content`), a `global` claim every answer (`grants`), so a name that a member removal, a pattern or a claim took away is not served from a Bastion's cache for the entry's period ([Q-BAS-146](../spec/bastion.md#q-bas-146--a-content-narrowing-and-a-claim-of-scope-members-yield-a-content-entry-a-global-claim-yields-grants)). | [feed.go](../../internal/bastionmain/feed.go) |
| Download intake | Events are recorded as `artifact.downloaded` marked `origin: bastion` with the registration's name, a time in the future at the time of receipt; a lost, expired or malformed event is counted in `bastion.downloads.dropped`. A batch is charged to its registration by its events (`bastions.downloads.events_per_second`, in units of 100); beyond it the answer is `429` and nothing is recorded ([Q-BAS-117](../spec/bastion.md#q-bas-117--download-events-are-charged-to-their-registration-by-their-count), [H-468](../spec/bastion.md#h-468--the-download-budget-is-per-registration-so-many-registrations-at-their-budget-still-fill-the-audit-buffer)). The time the Bastion reports is trusted ([H-439](../spec/bastion.md#h-439--download-events-enter-the-audit-trail-with-the-time-the-bastion-reports)). | [downloads.go](../../internal/bastionmain/downloads.go) |
| Last contact | Written at most once a minute per replica, at most 256 rows per registration; a change of a reported value is `bastion.contact`. The values are what the Bastion says about itself, except the address, which is the one the main installation saw. | [contact.go](../../internal/bastionmain/contact.go) |

`Trove-Bastion-Authorization` is redacted like `Authorization` in every log, span and support bundle: the key rule of R-OPS-013 matches
the word `authorization` in a header name (tested in [redact_bastion_test.go](../../internal/observability/redact_bastion_test.go)). No
`Trove-Bastion-*` request header is echoed; the one header on an answer to a Bastion request is the interface version.

## Where abuse is stopped

| Stage | What it knows | What it does | Requirement |
|---|---|---|---|
| Network provider | bandwidth | absorbs volumetric attacks, or does not (H-382) | outside trove |
| HAProxy Ingress (stage 1, switchable) | the client address | a per-address request and connection limit, set wider than the Bastion's | R-BAS-038 |
| Bastion block list (stage 2) | refusals per address, shared through Valkey | blocks an address that collects too many refusals, with durations doubling to 24 h; IPv6 per `/64` | R-BAS-037 |
| Bastion limits | credential or not, misses, failures | per-address request rates (anonymous stricter), per-address miss budgets, the failed-authentication timeout | R-BAS-035, R-BAS-036 |
| Concurrency budget | requests to the main installation | at most `max_concurrent` across replicas, anonymous at most a share; collapsed misses | R-BAS-025 |
| Main installation | the real client, through `bastions.trusted_proxies` for a recognized Bastion request | its own rate limits, throttle and audit, applied to the client, not to the Bastion | R-BAS-017 |

Without Valkey every counter and block holds per replica at the configured value divided by the chart's replica count (H-389).

**What the guard does with an address** ([guard](../../internal/bastion/guard/instance.go), R-BAS-035 to R-BAS-037):

- The address is the one R-ARCH-006 yields with the Bastion's own `server.trusted_proxies`; an IPv6 address counts as its `/64`, so one
  host that owns a whole prefix is one address. `exempt_cidrs` are never limited, timed out or blocked: an entry is a promise that nothing
  behind that network abuses the Bastion.
- Each refusal of a request limit, a miss limit or a timeout counts toward one threshold. Above it the address is blocked, for 15 minutes
  and twice as long for each further block within 24 h, up to `max_duration`. A blocked address costs the Bastion one comparison in memory
  and is answered `429 address_blocked`; its requests are not logged one by one, the block is, once.
- The failed-authentication timeout refuses only a credential for which the Bastion holds no usable answer; a partner's known token
  behind the same address keeps working, and the guesser keeps anonymous access (Q-BAS-36). It cannot be used to cut a colleague off: the
  count is of the unverified credentials the address sent.
- A limit admits up to its rate and a refused request or miss consumes nothing, so a client slightly over its limit is not starved and does not
  collect the refusals that block (Q-BAS-127; before, at 330 misses a minute against 300 no miss was admitted from the second minute and the
  address was blocked for 15 minutes, doubling to 24 hours, with its hits, which cut off every partner behind a shared address). A flood of several
  times the limit still reaches the threshold.
- The address the guard counts is only the client's when the chart's `config.server.trusted_proxies` names the ingress controller's pods; the
  chart refuses a render without it while its Ingress is on (Q-BAS-130). Before, every caller shared the controller's address by default: one
  client's bad tokens put every unverified credential into the timeout and a flood blocked the Bastion for everyone.
- Without Valkey the maps of the counters, buckets and blocks are bounded at 100,000 keys each, sharded, and evict the least recently used entry in
  constant time; a flood of addresses from an IPv6 /47 neither scans a map under a lock every request takes nor holds a lock across Valkey round
  trips (Q-BAS-128).
- The guard never refuses on its own fault. If the shared state fails, the request is admitted; with Valkey unavailable the
  counters and blocks work from memory at the configured value divided by `bastion.replicas`, and `trove_ratelimit_fallback` and
  `trove_valkey_available` say so (Q-BAS-94).
- With Valkey the block list, the timeouts and the counters are shared: a block set by one replica holds on every replica at its next
  request. What Valkey holds is chosen by whoever can write to it; the answers are signed (below), the blocks and counters are not.
  Write access to the Bastion's Valkey therefore lets its holder block an address or reset a counter, which is a denial or a missing limit
  and no grant of access; the ACL rule of the Bastion's user confines it to `trove-bastion:*`.
## What the main installation tells the Bastion about a response

The main installation marks the response of a recognized Bastion request (R-BAS-013) and refuses writes (R-BAS-014). The code is
[internal/api/bastionmark](../../internal/api/bastionmark/mark.go); the lookups that feed `absent` are in `internal/formathost` and
`internal/files`.

- **Only a recognized Bastion sees the marking.** The headers are added to the response of a request that carries a valid Bastion token
  and stripped from every other response, a header a handler set included. A caller learns its own shape from nobody. This is held for the
  default endpoint's chain, the only one that carries the stage; the other endpoints add no such header (nothing sets one) and
  the remote engine copies a fixed list of upstream headers.
- **`absent` is never a hidden item.** A negative entry is replayed to full readers only (R-BAS-024). The class `absent` therefore
  needs a `full` shape, a lookup that found the item missing for every subject, and no refusal, pattern refusal, serve-time check or
  remote member that could not tell on the way; every other `404` is `pass` ([Q-BAS-70](../spec/bastion.md#q-bas-70--absent-is-derived-from-the-notes-of-the-cores-lookups-and-only-for-a-full-reader)).
  The residual risk is [H-441](../spec/bastion.md#h-441--absent-rests-on-the-notes-of-the-cores-lookups-not-on-the-routes).
- **`full` is checked against the decision point.** A full shape never meets a refused read: `TestR_BAS_012_ShapeAgreesWithTheDecisionPoint`
  asks the decision point for every read of its fixture, and `content:metadata-read` alone, a scope that narrows, exposure rules and a
  missing populate right are all `partial`.
- **A shared document is the same for every full reader.** Kit check 12 sends each `Metadata` and `Computed` document as a user, a
  service account and the anonymous caller, each with its own request context and its own credential, client, session and forwarded
  headers except those the answer names in `Vary`; npm's `-/whoami` and `-/ping`, which differed, are `Volatile`.
- **A refused read says nothing about the caller's shape.** The marking learns the decision only once a read was allowed, on `/files/` as
  on the format routes, so a refusal carries no `Trove-Bastion-Read`.
- **The Bastion token cannot write.** A write route (kind `Upload`, `Delete`, `Admin`, or an action other than `read`) is refused before any
  handler runs and before a body is read, as a probe and as a forwarded request; the refusal names the main installation's URL of the
  repository and no secret. `GET` and `HEAD` on those routes are decided as the caller's own request. The probe gets there with anonymous
  access off as well: it is admitted for routing only.
- **The subject header carries IDs and an expiry.** `token=<token ID>; principal=<principal ID>; expires=<unix seconds>`, `anonymous` or
  `invalid` (every `401`, a session cookie): no name, no group. [H-442](../spec/bastion.md#h-442--a-401-states-the-subject-as-invalid-whether-the-credential-was-rejected-or-none-was-sent)
  records what `invalid` does not say.

## What the request flow enforces

These are properties of [internal/bastion/engine](../../internal/bastion/engine); each has a test named for its requirement.

| Property | Where | Test |
|---|---|---|
| A path outside `/api/<format>/<projectKey>/<repoKey>/…` and `/files/<projectKey>/<repoKey>/…`, a project or key that fails INV-5's rule, and a path with one segment after the format, is refused before any request | `stepPath`, `classifyPath` | `TestR_BAS_003_OnlyThePackagePathFamiliesReachTheReadFlow` |
| The same repository key in two projects is two repositories: the read-shape answer is keyed by credential and the qualified name `<projectKey>/<repoKey>`, so a caller's answer for one is never used for the other | `shapeKey`, `classifyPath` | `TestR_BAS_018_TheAddressedRepositoryIsTheQualifiedName`, `TestR_BAS_018_ARepositoryOfAnotherProjectIsAnotherRepository` |
| A malformed token, a `bst` token and an unknown prefix are refused without a request and counted as failed authentications; `X-NuGet-Apikey` and cookies are never read as credentials | `parseCredential` | `TestR_BAS_019_AMalformedTokenAnswers401WithoutARequest`, `TestR_BAS_019_TheNuGetAPIKeyIsNeverAReadCredential` |
| No answer is keyed by a token: the key is the SHA-256 of the token string, and the string lives in memory for the request | `credentialKey`, H-447 | `TestR_BAS_019_TheCredentialKeyIsTheSHA256AndNoTokenIsKept` |
| A cached artifact or document is served only to a caller with a usable `full` answer or a usable URL answer `allowed`; every answer is judged at each use by the four rules of R-BAS-020, so one obtained before a revocation is never used after it, in memory or from Valkey | `fromEntry`, `answerStore.get` | `TestR_BAS_020_AnAnswerObtainedBeforeARevocationIsNeverUsedAfterIt` |
| A forged or moved answer or negative record in Valkey is ignored and deleted by the store that signs and verifies them (the engine hands over plain records and never sees one that failed); an entry rewritten in the bucket is ignored and deleted by the cache | the Valkey `AnswerStore` (Q-BAS-93), `Cache.LookupEntry` through `loadEntry` | `TestR_BAS_034_*` in `test/integration/bastionshared`, `TestR_BAS_032_AnEntryRewrittenToPointAtOtherBytesIsIgnored`, `TestR_BAS_020_AnswersAreSharedThroughTheSharedStoreAndAnUnreadableRecordIsIgnored` |
| A document filtered for a caller is kept in memory only, under that caller's key, and validated with a conditional request on every use; a narrowed reader never receives the shared document and never gets a negative entry; the one exception is a reader the main installation itself validated: its `304` to the shared entry's validator proves that the caller's document is those bytes, and they are served without renewing the entry (Q-BAS-107) | `onPrivate`, `fromNegative` | `TestR_BAS_023_APartialReaderNeverReceivesTheSharedDocumentAndTheSharedEntryStaysUntouched`, `TestR_BAS_024_APartialReaderAsksTheMainInstallationEveryTime` |
| The headers of the interface, the replica header, `Cache-Status`, cookies and the hop-by-hop headers never reach the caller, and the caller's cookie, conditionals, range and forwarded-for headers never reach the main installation | `replayHeader`, `stripRequest` | `TestR_BAS_021_AMissIsForwardedWithTheCallersCredentialAndTheListedHeaders`, `TestR_BAS_021_TheCallerReceivesTheMainInstallationsAnswerWithoutTheInternalHeaders` |
| A write sends the main installation a probe without body and credential; the upload is not read | `write` | `TestR_BAS_014_AWriteIsProbedWithoutBodyAndCredentialAndTheRefusalIsReplayed` |
| The main installation unreachable: cached content for the answers the grace period allows, `503` for everything else, nothing after the grace period; a token past its expiry is refused throughout | `answers.State`, `unreachable` | `TestR_BAS_027_AnswersHeldServeCachedContentForThePeriodAndTheGraceThenNothing`, `TestR_BAS_020_ATokenIsRefusedFromItsExpiryOnAlsoWhileTheMainInstallationIsUnreachable` |
| Requests to the main installation are bounded: the budget per replica with a share for anonymous callers, held until the response body ends or is closed, the per-address miss limit, one fetch per key | `mainclient.SlotBudget`, `forward`, `miss`, every `resp.Body.Close` | `TestR_BAS_025_MissesNeverPutMoreThanTheBudgetOntoTheMainInstallation`, `TestR_BAS_025_EveryPathClosesTheResponseBodyAndSoReleasesItsSlot`, `TestR_BAS_035_AMissLimitedAddressKeepsGettingItsHits` |
| Before the first handshake the replica does not know whether anonymous requests are admitted, and answers them `503` rather than admit them or call the access off (Q-BAS-99) | `stepCredentials` | `TestR_BAS_019_AnAnonymousRequestBeforeTheFirstHandshakeIs503` |
| A request from a blocked address is counted and refused, and kept out of the request log, so a flood does not fill the log; the guard logs one line per block | `Verdict.Quiet`, `engine.WithQuietSlot`, `server.requestLog` | `TestR_BAS_037_ARequestFromABlockedAddressIsCountedButNotLoggedOneByOne` |

## Integrity of what it serves

Artifacts are stored under their SHA-256, verified when they are filled (the withheld tail of R-REPO-041) and again while they are
served (R-STOR-013's verifying reader): a replaced object aborts the transfer and is removed (R-BAS-032). Entries, negative entries
and answers in the bucket and in Valkey are signed with HMAC-SHA-256; write access to either without the key only deletes. With the
key, it forges (H-387).

What each layer of the cache holds, and what protects it:

| Held | Where | Protected by | What write access without the key does |
|---|---|---|---|
| Entries (content address, size, headers, repository, `validatedAt`) | `entries/` | HMAC-SHA-256 over every field and the cache key (`cache.Signer`); a record that fails is ignored, deleted and counted as `stale`. **Without a key** (the volume backend with no Valkey, Q-BAS-98) the entry is plain JSON and nothing protects it (H-460) | Deletes; cannot point an entry at other bytes, move it to another key, or extend its validity. Without a key it can do all three |
| Artifact and document bytes | `sha256/`, `documents/` | the content address, checked while served by a verifying reader that withholds the last 64 KiB until the SHA-256 and the length match | Replaces: the next whole read aborts, deletes the object and the entry, and counts `trove_bastion_cache_corrupt_total`; the next request refetches. A range is answered through the verifying read of the whole object (Q-BAS-82, H-449), so it is verified too; the replacement stays until read (H-453) |
| Access markers, modification times | `access/` | nothing | Steers the order of eviction (H-453) |
| The audit spool | `audit-spool/` | nothing; the files are events the main installation receives as reports of the Bastion's own token | Deletes events or adds events marked with the Bastion's registration (H-386's capability, from the bucket) |

A fill is spooled to a temporary directory of the volume or the pod's `tmp` and committed under its address only after its digest
was checked (`cache.Fill.Commit`), and the engine releases the last 64 KiB of the transfer to the caller only after that, so a reader never sees a partial
object and a caller never receives a complete corrupt one; a spool that fails during the transfer cuts it, so bytes are never passed on unverified (Q-BAS-123); the backend checks the digest and size of every `PutObject` once more. Every
name that becomes a path or a key (content address, key hash, replica, spool file) is validated as hex or a fixed form before use.
The bucket is reached over `https` with the trust bundle of the chart, and its credential is a file the replica re-reads.
An answer record that Valkey returns is verified by the store (not by the engine) against its kind and key before anything else sees it: a record that is altered,
unsigned, signed with another key or moved to another key is not returned, is deleted and is counted as `bad_signature` (Q-BAS-93). The
engine still judges the content with the validity rules of R-BAS-020, so a valid old record cannot outlive the revocations received after it.

## The link to the main installation

The client of the main installation, the standing link and the download buffer are built ([developer/bastion.md](../developer/bastion.md#the-link-to-the-main-installation-package-4a));
this is what they do about the security of the connection and of what flows over it.

| Concern | Mechanism | Code |
|---|---|---|
| Where the token and the callers' credentials go | `https` only; the trust is the system roots plus `bastion.main.ca_file`, read again without a restart; a redirect is returned, never followed, because the Bastion token travels in a header that no redirect policy strips; the address policy of upstream clients is not applied to the main installation (H-446) | [client.go](../../internal/bastion/mainclient/client.go) |
| What a forward sends | the caller's `Authorization` as it came; the Bastion token in `Trove-Bastion-Authorization`; `Cookie`, `Range`, `If-*`, `Forwarded`, `X-Forwarded-*`, `Trove-*` and trace headers of the caller removed; exactly one client address in both families, replacing the caller's; a write probe carries no credential of the caller and no body | [client.go](../../internal/bastion/mainclient/client.go) |
| What is logged | no header is logged; errors of the client name no URL query and no header; the token file's content is in no error; a token appears as its ID (`token_id`) and as a hint that the log scrubber already reduces to `trv_bst_[REDACTED]` (R-OPS-013) | [reload.go](../../internal/bastion/mainclient/reload.go), [link.go](../../internal/bastion/link/link.go) |
| Rotation of the Bastion token | the file is re-read every `token_reload_interval` and when its identity changes; a malformed or vanished file keeps the token in use, is logged as an error and counted as refused; a revoked token in use makes the replica unreachable and sets `trove_bastion_main_token_refused`, when the answer to a call of the interface's own says so; the same answer to a forward moves nothing (Q-BAS-121) | [reload.go](../../internal/bastion/mainclient/reload.go) |
| Revocations | every replica polls the feed and records each entry with its receipt time; an answer is refused if a matching entry was received after it was asked for, whatever the order in which the answer and the entry reached the replica (R-BAS-020 rule 3); a reset or a start without history refuses every older answer | [link.go](../../internal/bastion/link/link.go), [answers](../../internal/bastion/answers/answers.go) |
| The main installation cannot be reached | the replica is unreachable after a failed handshake, a refused token, a version mismatch, a base URL the main installation does not honour, or `unreachable_after` without a successful poll; only then do held answers live on for the grace period (H-383), a token the replica has not seen gets a `503`, and nothing is served after `period + grace_period` | [link.go](../../internal/bastion/link/link.go) |
| A main installation that does not know the Bastion's host | the handshake's response base URL must equal `server.base_url`, so no client receives a document with the main installation's URLs | [link.go](../../internal/bastion/link/link.go) |
| The concurrency budget | `ceil(max_concurrent / replicas)` slots per replica, anonymous callers at most their share; a slot is held until the response body ends, and no client's speed decides when that is: a fill reads at the main installation's speed, a pass-through is cut below 64 KiB per second after 30 s or when a chunk takes 10 s (Q-BAS-123, H-471) | [budget.go](../../internal/bastion/mainclient/budget.go), [fill.go](../../internal/bastion/engine/fill.go) |
| Download events | a bounded memory buffer; the excess is counted and reported in the audit; at a drain the buffer is sent or written to the cache backend's audit spool, which any replica delivers (H-444) | [reporter.go](../../internal/bastion/downloads/reporter.go) |

## Findings of the review of the engine, the client and the link

A security review with proofs of concept found the defects below on the Bastion's own side; each is closed by a decision of the spec page and a test that goes through the code path of the proof. The
gaps that remain are the H-items in [What this does not cover](#what-this-does-not-cover).

| Finding | What it allowed | Closed by | Test |
|---|---|---|---|
| P1: an encoded `v1` in the path | `GET /api/%761/users` reached the management API through the Bastion; `GET /api/%761/bastion/handshake` made the main installation answer `401 bastion_token_invalid`, which the Bastion read as a refusal of its own token: one request from anyone made the replica unreachable | the path is classified by what the main installation's router will read, an encoded family, format or key segment and a dot segment are refused ([Q-BAS-121](../spec/bastion.md#q-bas-121--a-path-is-classified-as-the-main-installations-router-will-read-it-and-a-token-refusal-on-a-forward-is-no-statement-about-the-bastions-token)); a token refusal counts only on a call of the interface's own | `TestR_BAS_003_R_BAS_018_APathIsClassifiedAsTheMainInstallationsRouterWillRouteIt`, `TestR_BAS_003_R_BAS_006_AnEncodedSpellingOfTheInterfaceRoutesCannotTurnTheReplicaUnreachable` |
| P2: a withdrawal while unreachable | an artifact withdrawn through the feed was served on while the replica was unreachable and its link still read the feed | a `content` entry ends the service of an entry also then ([Q-BAS-122](../spec/bastion.md#q-bas-122--a-content-entry-ends-the-service-of-a-cached-entry-also-while-the-main-installation-is-unreachable)) | `TestR_BAS_022_R_BAS_027_AWithdrawnArtifactIsNotServedWhileTheMainInstallationIsUnreachable` |
| P3, B4: slots and waiters held by slow clients | seventeen connections that took their tarball one buffer at a time held every anonymous slot; a second caller of the key waited without bound | the fill does not wait for the client, the flight ends at the commit, a waiter is bounded, a pass-through has a floor ([Q-BAS-123](../spec/bastion.md#q-bas-123--the-fill-reads-the-main-installation-at-its-own-speed-and-no-client-holds-a-slot-or-a-waiter)) | `TestR_BAS_025_ClientsThatReadSlowlyDoNotHoldTheBudgetSlots`, `TestR_BAS_025_AWaiterOfACollapsedFetchIsNotHeldByTheLeadersClient` |
| P4: the `404` oracle | a narrowed reader's second request for a name was answered from memory when the name is cached and hidden, and from the main installation when it does not exist | a `404` that is not `absent` is never kept ([Q-BAS-124](../spec/bastion.md#q-bas-124--a-404-that-is-not-marked-absent-is-never-kept-as-a-url-answer)) | `TestR_BAS_024_ANarrowedReaderCannotTellACachedHiddenNameFromAMissingOne` |
| B2: the probe's slot | with `anonymous_share: 0` every write refusal answered `503` | the probe takes the class of the caller | `TestR_BAS_014_R_BAS_025_AProbeTakesTheSlotClassOfTheCaller` |
| B10: a mark of one caller | a caller without rights could keep collapsing off for a popular key | a mark that depends on the caller's rights is the caller's ([Q-BAS-125](../spec/bastion.md#q-bas-125--a-result-that-depends-on-the-callers-rights-switches-collapsing-off-for-that-caller-only)) | `TestR_BAS_025_ARefusalOfOneCallerDoesNotSwitchCollapsingOffForOthers` |
| B3, B7, B8, B11: the link | a half-read start counted as the start of the history; a burst page of 1,000 entries cost 1,000 passes over the answers; a black-holed call held the tick for `response_header_timeout`; IDs forgotten before the feed stopped re-sending ([Q-BAS-126](../spec/bastion.md#q-bas-126--the-link-applies-a-page-as-a-unit-ends-the-start-read-at-the-end-of-the-feed-and-bounds-each-call), H-470) | see the decision | `TestR_BAS_026_TheHistoryStartsOnlyWhenTheStartReadReachedItsEnd`, `TestR_BAS_026_AFeedPageOfAThousandEntriesIsAppliedAgainstTwoHundredThousandAnswersWithinTheBound`, `TestR_BAS_027_ACallThatTheMainInstallationNeverAnswersIsCutAtUnreachableAfter`, `TestR_BAS_026_AnAppliedIDIsKeptForAtLeastTheFeedLookback` |
| B1 (mirror): a `304` marked `shared` against the caller's private copy | the caller, who sent no condition, received an empty `304` | the copy's bytes answer as a `200` | `TestR_BAS_023_A304SharedAgainstThePrivateCopyAnswersTheCallerWithTheCopysBytes` |

## What this does not cover

The gaps of the Bastion's mechanisms are recorded on the spec page, where the mechanisms are, and only linked here. The one gap
that no mechanism of the Bastion holds, because the chart leaves it to the cluster operator, is recorded on this page:
[H-522](#h-522--the-chart-restricts-no-egress-of-the-bastion-pods).

- [H-382](../spec/bastion.md#h-382--a-volumetric-attack-on-bandwidth-is-not-absorbed-by-the-bastion): volumetric attacks on
  bandwidth;
- [H-383](../spec/bastion.md#h-383--during-the-grace-period-a-token-revoked-at-the-main-installation-reads-on-at-the-bastion):
  revocations during an outage of the main installation;
- [H-384](../spec/bastion.md#h-384--a-version-newly-published-appears-at-the-bastion-only-after-the-metadata-period): new versions
  and security fixes after the metadata period;
- [H-385](../spec/bastion.md#h-385--a-withdrawal-the-feed-does-not-carry-reaches-the-bastion-within-the-artifact-revalidation-period):
  withdrawals and automatic blocks the feed does not carry;
- [H-386](../spec/bastion.md#h-386--whoever-holds-the-bastion-token-can-test-credentials-name-client-addresses-and-add-download-events):
  what the holder of the Bastion token can do;
- [H-387](../spec/bastion.md#h-387--the-signing-key-and-write-access-to-the-bastions-valkey-or-bucket-together-forge-answers-and-entries):
  the signing key together with write access to Valkey or the bucket;
- [H-388](../spec/bastion.md#h-388--anonymous-misses-from-many-addresses-take-the-anonymous-share-of-the-budget): distributed
  anonymous miss floods;
- [H-389](../spec/bastion.md#h-389--without-valkey-a-block-and-a-counter-hold-on-one-replica-only): blocks and counters without
  Valkey;
- [H-390](../spec/bastion.md#h-390--the-time-of-an-answer-tells-a-caller-whether-others-fetched-a-package-recently): what the
  timing of a hit tells;
- [H-391](../spec/bastion.md#h-391--download-events-lost-to-a-full-buffer-a-crash-or-an-ephemeral-spool-are-missing-from-the-audit):
  download events lost before they reach the audit;
- [H-428](../spec/bastion.md#h-428--the-ops-listener-of-a-bastion-serves-its-metrics-without-authentication): the ops listener's
  metrics carry no authentication;
- [H-432](../spec/bastion.md#h-432--the-edge-limit-counts-per-tcp-peer-address-and-has-no-burst): the edge limit keys on the TCP
  peer address and has no burst;
- [H-434](../spec/bastion.md#h-434--a-controller-that-ignores-the-annotations-turns-the-edge-limit-and-routing-off-silently): a
  controller that ignores the Ingress annotations turns the edge limit and routing off silently;
- [H-436](../spec/bastion.md#h-436--deleting-a-registration-deletes-its-token-rows-so-a-later-use-of-a-deleted-token-is-unknown-and-not-logged):
  a deleted registration's token leaves no rejection log;
- [H-437](../spec/bastion.md#h-437--a-bastion-tokens-last-use-is-one-value-per-token-at-the-granularity-of-the-last-used-buffer): the last
  use of a Bastion token is one value per token;
- [H-438](../spec/bastion.md#h-438--the-interface-calls-of-a-bastion-share-the-read-budget-of-its-token): the interface calls of a
  Bastion share the read budget of its token;
- [H-439](../spec/bastion.md#h-439--download-events-enter-the-audit-trail-with-the-time-the-bastion-reports): download events enter
  the audit trail with the time the Bastion reports, behind the position of a tailing consumer;
- [H-440](../spec/bastion.md#h-440--an-address-that-sends-rejected-bastion-tokens-is-refused-its-valid-bastion-token-for-a-minute): an
  address that sends rejected Bastion tokens is refused its valid one for a minute;
- [H-441](../spec/bastion.md#h-441--absent-rests-on-the-notes-of-the-cores-lookups-not-on-the-routes): the class `absent` rests on the
  notes of the core's lookups;
- [H-442](../spec/bastion.md#h-442--a-401-states-the-subject-as-invalid-whether-the-credential-was-rejected-or-none-was-sent): a `401`
  states the subject as `invalid` whatever the reason;
- [H-444](../spec/bastion.md#h-444--download-events-spooled-by-a-draining-replica-can-be-delivered-twice): spooled download
  events can be delivered twice;
- [H-445](../spec/bastion.md#h-445--a-bastion-clock-ahead-of-the-main-installations-shortens-the-feed-lookback): a Bastion
  clock ahead of the main installation's shortens the feed lookback;
- [H-446](../spec/bastion.md#h-446--the-connection-to-the-main-installation-is-not-under-the-egress-address-policy): the
  connection to the main installation is not under the egress address policy;
- [H-447](../spec/bastion.md#h-447--a-background-refresh-keeps-the-token-string-beyond-its-request): a background refresh keeps the
  token string beyond its request;
- [H-448](../spec/bastion.md#h-448--a-chunked-body-of-a-read-route-that-takes-a-body-is-held-in-memory): a chunked body of a read
  route that takes a body is held in memory;
- [H-449](../spec/bastion.md#h-449--a-range-request-costs-a-read-of-the-whole-object): a range request costs a read of the whole
  object;
- [H-450](../spec/bastion.md#h-450--a-fill-that-does-not-fit-the-spool-is-passed-through-unkept-and-a-request-answered-from-the-spool-fails):
  a fill that does not fit the spool is passed through unkept, and a request answered from the spool fails;
- [H-451](../spec/bastion.md#h-451--a-ranged-read-of-a-cached-artifact-is-not-verified): a ranged read of a cached artifact is not
  verified;
- [H-452](../spec/bastion.md#h-452--the-entries-of-evicted-bytes-stay-until-they-are-read-and-are-not-counted-in-the-cache-size):
  entries of evicted bytes stay until read and are not counted in the size;
- [H-453](../spec/bastion.md#h-453--objects-and-markers-are-not-signed-a-replaced-object-is-found-only-when-it-is-read-and-markers-steer-eviction):
  objects and markers are not signed;
- [H-454](../spec/bastion.md#h-454--the-gauge-of-blocked-addresses-counts-the-blocks-a-replica-knows): the gauge of blocked addresses
  is per replica;
- [H-455](../spec/bastion.md#h-455--every-request-costs-the-bastions-valkey-round-trips-and-an-unblocked-flood-from-many-addresses-costs-it-per-request):
  the load a flood from many addresses puts on the Bastion's Valkey;
- [H-456](../spec/bastion.md#h-456--a-block-or-a-timeout-set-without-valkey-or-while-it-is-unavailable-is-forgotten-when-the-replica-restarts):
  blocks set without Valkey are forgotten at a restart;
- [H-457](../spec/bastion.md#h-457--a-reader-without-endpointsread-is-not-offered-the-bastion-as-a-target): a reader without
  `endpoints:read` is not offered the Bastion as a target;
- [H-458](../spec/bastion.md#h-458--the-address-a-registration-reports-chooses-among-the-listed-addresses-and-the-name-is-testable):
  the reported address chooses among the listed addresses, and the name is testable;
- [H-460](../spec/bastion.md#h-460--without-a-signing-key-the-entries-on-the-volume-are-not-authenticated): without a signing key the entries on the
  volume are not authenticated;
- [H-461](../spec/bastion.md#h-461--the-suites-front-reaches-the-main-installations-replica-in-plain-http): the suite's front reaches the main installation's replica in plain HTTP;
- [H-462](../spec/bastion.md#h-462--a-main-installation-that-reports-a-higher-interface-version-in-its-own-problem-body-is-not-exercised): a main installation that reports a higher interface version in its own problem body is not exercised;
- [H-463](../spec/bastion.md#h-463--the-bound-on-the-upload-body-a-bastion-reads-is-shown-by-the-answer-not-by-a-counter): the bound on the upload body a Bastion reads is shown by the answer, not by a counter;
- [H-467](../spec/bastion.md#h-467--behind-the-main-installations-own-ingress-the-forwarded-host-is-what-the-ingress-reports): behind the main installation's own ingress the forwarded host is what the ingress reports;
- [H-468](../spec/bastion.md#h-468--the-download-budget-is-per-registration-so-many-registrations-at-their-budget-still-fill-the-audit-buffer): the download budget is per registration;
- [H-469](../spec/bastion.md#h-469--a-probe-admitted-for-routing-runs-any-handler-below-the-package-families-that-does-not-tell-the-marking-its-route): a probe admitted for routing runs any handler below the package families that does not tell the marking its
  route;
- [H-470](../spec/bastion.md#h-470--the-bastion-assumes-the-feeds-default-lookback-when-it-keeps-the-ids-of-applied-entries): the Bastion assumes the
  feed's default lookback when it keeps the IDs of applied entries;
- [H-471](../spec/bastion.md#h-471--a-pass-through-body-at-the-floor-rate-holds-its-budget-slot-for-the-length-of-the-transfer): a pass-through body at
  the floor rate holds its budget slot for the length of the transfer;
- [H-472](../spec/bastion.md#h-472--a-fill-goes-on-after-its-client-has-left): a fill goes on after its client has left;
- [H-473](../spec/bastion.md#h-473--the-roles-binary-contains-packages-of-the-main-installation-it-never-starts): the role's binary contains
  packages of the main installation it never starts;
- [H-474](../spec/bastion.md#h-474--the-sliding-estimate-of-a-limit-admits-more-than-the-limit-in-a-minute-that-spans-a-boundary): the sliding
  estimate of a limit admits more than the limit in a minute that spans a boundary;
- [H-464](../spec/bastion.md#h-464--a-client-that-sends-accept-encoding-gzip-was-asked-about-at-the-main-installation-on-every-artifact-read-cause-not-found): a client that sends
  `Accept-Encoding: gzip` was asked about at the main installation on every artifact read, cause not found.

This page does not describe the main installation's own mechanisms that the Bastion relies on (the decision point, token
validation, the revocation journal); their pages and gaps are linked above.

### H-522 · The chart restricts no egress of the Bastion pods

Stage: the pod network of the Bastion pods. The chart's NetworkPolicy holds ingress rules only
([networkpolicy.yaml](../../deploy/helm/trove-bastion/templates/networkpolicy.yaml),
[R-BAS-047](../spec/bastion.md#r-bas-047--network-policy)), by the owner's decision of 2026-11-05 that
[ADR-154](../decisions.md#adr-154--network-policies-are-ingress-rules-between-the-charts-pods-each-with-its-port) governs this
chart as well. Until then the chart rendered an egress policy, on by default, that let the pods reach DNS, the main installation,
Valkey, the cache bucket and the span collector and nothing else.

Live wherever a Bastion shares a cluster or a routed network with the main installation and the cluster operator sets no egress
policy of their own; dormant where the Bastion's network reaches nothing but the main installation's public address. Subject:
every address the pod network reaches from a Bastion pod, reached by a process that took over a Bastion pod, the component of
trove built to face the internet. Per case, in the kind profile, where the Bastions run in the namespace `trove-bastion` next to the
main installation in `trove`:

- the main installation's PostgreSQL Service `trove-db-rw:5432` and `registry.npmjs.org:443`. Until 2026-11-05
  `TestBastion_R_BAS_049_NetworkPolicy` ([bastion_scenarios_test.go](../../test/e2e/bastion_scenarios_test.go)) asserted that the
  chart's former egress policy refused a Bastion pod's connections to both, and that a client pod of `trove`, which no egress
  policy restricts, reached both. No NetworkPolicy of the profile selects the PostgreSQL pods, so without the chart's policy a
  Bastion pod reaches them as well;
- the profile's Keycloak (`trove-keycloak:8443`), to which the same test found a Bastion pod's connection refused while that
  policy was in force; no NetworkPolicy of the profile selects Keycloak either;
- not the main installation's Valkey: its own ingress policy `trove-valkey-clients` admits only trove's serving pods of `trove`
  ([deploy/dev/valkey.yaml](../../deploy/dev/valkey.yaml)). A target that fences itself with an ingress policy stays fenced.

What does not change: the Bastion holds no credential for PostgreSQL, the main installation's object store or upstream registries
(R-BAS-030), so a connection to them is a network path, not access; the Bastion token and the callers' credentials in transit are
what a taken-over pod holds (H-386). Beyond the targets that fence themselves, the in-cluster services, on a cloud node its
metadata endpoint (`169.254.169.254`), and the internet are reachable, so the taken-over pod is a foothold for scanning and for
attacking services that trust the pod network.

What makes it real: a flaw that lets an attacker run code in a Bastion process, in a cluster or network without an egress policy
for the Bastion pods. What closes it is the cluster operator's own egress policy for the Bastion pods, admitting what R-BAS-047
lists: DNS; the main installation's addresses (`bastion.main.url`, and `bastion.main.packages_url` where set) on their ports;
Valkey and its Sentinels while Valkey is configured; the cache bucket with the bucket backend, and its credential endpoint with
`credentials.source` `webIdentity` or `default`; the span collector while tracing is enabled. The chart's counterpart for the
main installation's pods is [H-516](egress.md#h-516--the-chart-restricts-no-egress-of-its-pods).
