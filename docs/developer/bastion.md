# The trove Bastion: contracts

Status: the contracts of phase 5b are laid (the shared types, interfaces, schema, routes, configuration and the skeleton of the
role); the logic of the work packages is built package by package. Built: package 6 ([shared state and guard](#shared-state-and-the-guard)). The specification is [spec/bastion.md](../spec/bastion.md)
(R-BAS-001 to R-BAS-049, decisions Q-BAS-1 to Q-BAS-54), its security perspective [security/bastion.md](../security/bastion.md), the plan
[projektplan/BETA.md](../projektplan/BETA.md#phase-5b--trove-bastion-m13).

The Bastion is a role of the one image, `trove serve --role bastion`, with a configuration tree of its own, no PostgreSQL, no UI and a
client listener that mirrors the main installation's package URL space. This page is for the eight developers who build it in
parallel: it says which package owns which file, which seams join them, how a request travels, and how to run the role.

## What to have in your head first

- **Two sides, one wire.** The Bastion's code is [internal/bastion/](../../internal/bastion); the main installation's side is
  [internal/bastionmain](../../internal/bastionmain) (seams), [internal/api/v1](../../internal/api/v1) (the operations) and the chain stage.
  The wire contract both import is [internal/bastion/iface](../../internal/bastion/iface); it changes only with `InterfaceVersion`
  ([the guard](#the-interface-guard)).
- **The Bastion imports no subsystem of the main installation.** An allow-list rule of the archtest ([bastion.go](../../internal/archtest/bastion.go),
  R-BAS-001) holds `internal/bastion/**` to the platform packages. `internal/cluster` (the Valkey client) is allowed for `shared`
  only and `internal/blob/s3` for `cache` only.
- **Stubs are obviously stubs.** `mainclient.Stub`, `cache.Stub`, `downloads.Stub` and the placeholder engine answer
  [ErrNotBuilt](../../internal/bastion/mainclient/mainclient.go) or `404 bastion_not_served`; the operations of the management API
  answer `501 bastion_not_implemented` ([bastion_stub.go](../../internal/api/v1/bastion_stub.go)). `server` logs the placeholders at
  start. Replace a stub, never wrap it.
- **Every file has one owner.** The tables below name it. Shared files carry `>>> package n` markers
  ([wire_bastion.go](../../internal/app/wire_bastion.go)); edit your block only.
- **Tests carry the requirement ID** in their name (`TestR_BAS_0nn_<What>`), or in a comment for a contract or refusal test.

## Work packages and what they own

| # | Package | Owns (new files unless said) | Fills the seams |
|---|---|---|---|
| 1 | Registrations and the `bst` token type (R-BAS-004, R-BAS-005) | [bastion_registrations_api.go](../../internal/api/v1/bastion_registrations_api.go) (replace the stubs), a registration service and the `trove admin bastion …` commands, the token issuance for the type `bst` in `internal/auth/token`, the `bastion.*` audit events | `bastionmain.TokenAuthenticator` |
| 2 | Interface handlers, recognition (R-BAS-008 to R-BAS-012, R-BAS-015 to R-BAS-017) | [bastion_handshake_api.go](../../internal/api/v1/bastion_handshake_api.go), [bastion_check_api.go](../../internal/api/v1/bastion_check_api.go), [bastion_feed_api.go](../../internal/api/v1/bastion_feed_api.go), [bastion_downloads_api.go](../../internal/api/v1/bastion_downloads_api.go), the Recognize stage, the feed mapping over [bastion_feed.sql](../../internal/meta/postgres/queries/bastion_feed.sql), the contact upsert | `Stages.Recognize`; uses `ShapeResolver` |
| 3 | Marking and the write probe (R-BAS-013, R-BAS-014) | the Mark stage and the `RouteObserver`, the `ShapeResolver` (R-BAS-012), the calls in `internal/api/rawfiles`, the conformance-kit check for shared documents | `Stages.Mark`, `RouteObserver`, `ShapeResolver` |
| 4 | The engine (R-BAS-006, R-BAS-018 to R-BAS-029, R-BAS-042 to R-BAS-044) | [internal/bastion/engine](../../internal/bastion/engine) (replace `New`), the body of `mainclient.Client`, the token reloader, the feed application over `answers.FeedLog` | `engine.Engine`, `mainclient.Client`, `mainclient.Budget` |
| 5 | Cache backends, eviction, integrity (R-BAS-030 to R-BAS-032) | the two implementations of `cache.Backend` in `internal/bastion/cache/…`, the sweep | `cache.Backend` |
| 6 | Valkey, rate limits, failed-auth timeout, blocking (R-BAS-034 to R-BAS-037) — **built** | the Valkey implementation of `shared.*` in `internal/bastion/shared/…`, `guard.New` and its `Instance`, the Valkey mechanism in `internal/cluster/valkey_bastion.go` | `shared.State`, `guard.Guard` |
| 7 | Download buffer and metrics (R-BAS-039, R-BAS-040) | [internal/bastion/downloads](../../internal/bastion/downloads), the PrometheusRule's metric inputs (the names are defined) | `downloads.Buffer` |
| 8 | UI, client setup, manual (R-BAS-007) | the Bastions screen, the `bastion=` parameter of `getClientSetup` ([packages_api.go](../../internal/api/v1/packages_api.go)), `docs/manual/admin/bastion.md` | none |

The contract work (this page's author) owns [internal/bastion/iface](../../internal/bastion/iface), the schema, the OpenAPI document,
the configuration trees and the skeleton. A change to one of those in a work package is a contract change: say so in the review.

## Package tree

```text
internal/bastion/
├── iface/            the wire contract of both sides; stdlib only; guarded by testdata/interface.txt
├── answers/          the validity rules of R-BAS-020 and the feed log; a leaf
├── metrics/          every metric of R-BAS-040, defined once; a leaf
├── mainclient/       the client of the main installation: Client, Budget, TokenSource, errors; token file and holder
├── cache/            Backend, Entry, the HMAC Signer; the volume and bucket backends, Cache, Fill, the sweep
├── shared/           the shared state (limiters, block list, timeouts, flight lock, sweep lease, answer records): the local form and the Valkey form
├── guard/            Guard (R-BAS-035 to R-BAS-037): Instance, the verdicts, and the address key (an IPv6 address counts as its /64)
├── downloads/        Buffer of download events (R-BAS-039)
├── engine/           Deps, Engine; New returns the placeholder that answers bastion_not_served
└── server/           the process of the role: telemetry, token, TLS, listeners, health, drain
internal/bastionmain/ the main installation's seams (a domain package)
```

<details>
<summary>File → responsibility</summary>

| File | Responsibility |
|---|---|
| [iface/version.go](../../internal/bastion/iface/version.go) | `InterfaceVersion` |
| [iface/routes.go](../../internal/bastion/iface/routes.go) | the four route paths, the feed query parameters, the operation names, the mirrored path families |
| [iface/headers.go](../../internal/bastion/iface/headers.go) | every header name of R-BAS-008, R-BAS-013, R-BAS-014, R-BAS-017, the probe and route values, the token prefix |
| [iface/marking.go](../../internal/bastion/iface/marking.go) | `Class`, `Read`, `Marking` (Set, Parse, Strip), `SubjectKind`, `Subject` and its header form |
| [iface/cursor.go](../../internal/bastion/iface/cursor.go) | `Cursor` and the synthetic `t:<RFC 3339>` form |
| [iface/wire.go](../../internal/bastion/iface/wire.go) | the bodies of the handshake, check, feed and downloads, `FeedKind`, `DecodeStrict`, `Marshal` |
| [iface/problems.go](../../internal/bastion/iface/problems.go) | the problem codes of R-BAS-008, R-BAS-009, R-BAS-014 and R-BAS-029 |
| [iface/surface_test.go](../../internal/bastion/iface/surface_test.go) | the generator of the surface and the guard of R-BAS-009 |
| [answers/answers.go](../../internal/bastion/answers/answers.go) | `Answer`, `State.Check` (the four rules), `FeedLog` (Record, Prune, Reset) |
| [metrics/metrics.go](../../internal/bastion/metrics/metrics.go) | names, label values, `Metrics`, `New`, `Names` |
| [mainclient/mainclient.go](../../internal/bastion/mainclient/mainclient.go) | `Client`, `Forward`, `Response`, `Budget`, `Token`, `TokenSource`, the errors, `Stub` |
| [mainclient/token.go](../../internal/bastion/mainclient/token.go) | `ParseToken` (prefix, length, base62, CRC), `ReadTokenFile`, `Holder` |
| [cache/cache.go](../../internal/bastion/cache/cache.go) | the layout, `Backend`, `Entry`, `Usage`, `Stub` |
| [cache/sign.go](../../internal/bastion/cache/sign.go) | `Signer` (Sign, Verify, SignEntry, VerifyEntry), `KeyHash`, the signature domains |
| [cache/layout.go](../../internal/bastion/cache/layout.go) | the names of the layout, `WithArea`, validation of every name that becomes a path or a key |
| [cache/core.go](../../internal/bastion/cache/core.go) | `backend`, the one implementation of `Backend` over a `store`: probe, spool, eviction sweep, audit spool, error counting |
| [cache/volume.go](../../internal/bastion/cache/volume.go), [cache/bucket.go](../../internal/bastion/cache/bucket.go) | the two stores: a directory, and S3 through `internal/blob/s3` |
| [cache/verify.go](../../internal/bastion/cache/verify.go), [cache/fill.go](../../internal/bastion/cache/fill.go) | the verifying reader with its hold-back, the `Fill` spool |
| [cache/store.go](../../internal/bastion/cache/store.go), [cache/sweep.go](../../internal/bastion/cache/sweep.go) | `Cache` (what the engine uses), `New`, `NewVolume`, `NewBucket`, `Options`; `SweepOnce`, `RunSweeper` |
| [shared/shared.go](../../internal/bastion/shared/shared.go) | `Limiter`, `BlockList`, `FlightLock`, `SweepLease`, `AnswerStore`, `State`, `NewLocal` |
| [shared/local.go](../../internal/bastion/shared/local.go) | the local forms: token buckets, sliding-window counters, block list, flights, answers in memory, all bounded |
| [shared/valkey.go](../../internal/bastion/shared/valkey.go) | `Open`, `New`, `Options`: the Valkey forms over `cluster.BastionStore`, the limiters of R-ARCH-034 and the FlightLock, with the local forms as the fallback; the signing of answer records |
| [guard/guard.go](../../internal/bastion/guard/guard.go) | `Guard`, `Verdict`, `AddressKey` |
| [guard/instance.go](../../internal/bastion/guard/instance.go) | `New`, `Options`, `Instance`: the limits, the miss limits, the timeout, the blocking |
| [server/shared.go](../../internal/bastion/server/shared.go) | `openShared`: builds the state and the guard from the configuration and closes them |
| [downloads/downloads.go](../../internal/bastion/downloads/downloads.go) | `Buffer`, `Stub` |
| [engine/engine.go](../../internal/bastion/engine/engine.go) | `Deps`, `Engine`, `New` (placeholder) |
| [server/server.go](../../internal/bastion/server/server.go) | `Run`, `New`, `Start`, `Shutdown` |
| [bastionmain/recognition.go](../../internal/bastionmain/recognition.go) | `Recognition`, `WithRecognition`, `RecognitionFrom` |
| [bastionmain/auth.go](../../internal/bastionmain/auth.go) | `TokenAuthenticator`, `BastionToken`, `ErrTokenInvalid` |
| [bastionmain/shape.go](../../internal/bastionmain/shape.go) | `ShapeResolver`, `Shape` |
| [bastionmain/route.go](../../internal/bastionmain/route.go) | `RouteObserver`, `RouteMatch`, `RouteDecision`, `WithObserver`, `ObserverFrom`, the kind names |
| [bastionmain/stages.go](../../internal/bastionmain/stages.go) | `Stages` (Recognize, Mark, Chain), `Stub`, `ErrNotImplemented` |
| [bastionmain/metrics.go](../../internal/bastionmain/metrics.go) | `trove_bastion_interface_requests_total`, `trove_bastion_forwarded_requests_total` |

</details>

## Seams

| Seam | Declared in | Implemented by | Used by |
|---|---|---|---|
| Recognition result in the request context | [bastionmain/recognition.go](../../internal/bastionmain/recognition.go) | package 2 (the Recognize stage) | package 3, the shared authorization step ([authorize.go](../../internal/api/v1/authorize.go), built), the interface handlers (package 2) |
| `TokenAuthenticator` (raw token → enabled registration, token ID, expiry) | [bastionmain/auth.go](../../internal/bastionmain/auth.go) | package 1 (query `BastionTokenBySecret`, the token cache and revocation bound of R-AUTH-023) | package 2 |
| `ShapeResolver` (subject × repositories → full, partial, none, status) | [bastionmain/shape.go](../../internal/bastionmain/shape.go) | **package 3** (Q-BAS-51) | package 3 (marking), package 2 (check) |
| Route kind and coarse action to a middleware | `RouteObserver` in [bastionmain/route.go](../../internal/bastionmain/route.go); called by [dispatcher.go](../../internal/api/dispatch/dispatcher.go) after the match and after the decision (built) and, to be added, by `internal/api/rawfiles` | package 3 | package 3 |
| The chain stage | `Chain.Bastion` in [chain.go](../../internal/httpserver/chain.go) (built); `Stages` | package 2 (Recognize), package 3 (Mark), composed in [wire_bastion.go](../../internal/app/wire_bastion.go) | the default endpoint's chain only |
| The permission form `{bastion: true}` | [authorize.go](../../internal/api/v1/authorize.go) (built) | contract work | the four interface operations |
| `bst` is unknown to every other route | [token.Service.Check](../../internal/auth/token/validate.go) (built) | contract work | all |
| The token API does not see a `bst` token | `IdentityListTokens` and `IdentityTokenRow` in [identity.sql](../../internal/meta/postgres/queries/identity.sql) (built) | contract work | package 1 manages the tokens through `/bastions/{name}/tokens` and `bastion_registration.sql` |
| Last contact of a replica | [bastion_contact.sql](../../internal/meta/postgres/queries/bastion_contact.sql) | package 2 writes (`BastionContactUpsert`, `BastionContactPrune`) | package 1 reads (`BastionContactList`, `BastionContactListAll`), package 8 (`BastionLastBaseURL`) |
| Feed inputs | [bastion_feed.sql](../../internal/meta/postgres/queries/bastion_feed.sql) | contract work | package 2 |
| `mainclient.Client`, `Budget`, `TokenSource` | [mainclient.go](../../internal/bastion/mainclient/mainclient.go) | package 4 | package 4, 7 (sends batches) |
| `cache.Backend` | [cache.go](../../internal/bastion/cache/cache.go) | package 5 | packages 4, 7 |
| `shared.State`, `guard.Guard` | [shared.go](../../internal/bastion/shared/shared.go), [guard.go](../../internal/bastion/guard/guard.go) | package 6 (built: [Shared state and the guard](#shared-state-and-the-guard)) | package 4, package 5 (`Sweep`) |
| `downloads.Buffer` | [downloads.go](../../internal/bastion/downloads/downloads.go) | package 7 | package 4 |
| Metrics | [metrics.go](../../internal/bastion/metrics/metrics.go) | contract work | everyone; add no metric elsewhere |
| Problem codes | [iface/problems.go](../../internal/bastion/iface/problems.go), [errs/codes.go](../../internal/errs/codes.go) | contract work | everyone |

### The interface guard

[surface_test.go](../../internal/bastion/iface/surface_test.go) lists every exported declaration of `iface` (constants with values, types
with fields and JSON tags, functions) into [testdata/interface.txt](../../internal/bastion/iface/testdata/interface.txt), the version on the
first line. `TestR_BAS_009_TheInterfaceChangesOnlyWithItsVersion` fails when the code and the file differ. To change the interface:
raise `InterfaceVersion`, then `go test ./internal/bastion/iface -run TestR_BAS_009 -update-interface`; the regeneration refuses to write
a changed surface under an unchanged version. The OpenAPI document must name the new version in `x-trove-bastion-interface`
(`LoadDocument` checks it), and the release notes announce it.

## Shared state and the guard

Package 6 built both. The server makes them in [server/shared.go](../../internal/bastion/server/shared.go) from the configuration and
hands `engine.Deps.Shared` and `engine.Deps.Guard` over; it closes them (the Valkey client, the guard's gauge loop) at the end of the drain.

**What changed in the committed interfaces (a contract change, said here on purpose).** Package 4 reads this before it calls the guard.

| Where | Change | Why |
|---|---|---|
| `guard.Stub`, `guard.IsStub` | removed; `engine.Deps.Stubbed` no longer names a guard | the stub is replaced (Q-BAS-94) |
| `guard.Verdict` | new field `Quiet`: the request is not to be logged one by one | a blocked address answers many requests, the guard logs one line per block (R-BAS-037); the engine keeps such a request out of the request log |
| `shared.State` | new field `Timeouts` (a `BlockList` for the failed-authentication timeouts) | R-BAS-036 needs a timeout that outlives the counting window |
| `shared.Limiter` | `Add` and `Count` return the count **in the units of the configured value**: a local form multiplies its count by `bastion.replicas`; `Replicas()` stays but a caller need not use it | one comparison for every form (Q-BAS-92) |
| `shared.FlightLock` | on an error `held` is **true**: the replica fetches itself | the lock is an optimization (R-BAS-034) |
| `shared.SweepLease` | with Valkey unavailable the lease is held and `err` is nil | a sweep only deletes (R-BAS-031) |
| `shared.AnswerStore` | the Valkey form signs on `Put` and verifies on `Get`; the engine hands over the record and **does not sign** | Q-BAS-93; a record above 1 KiB (signature included) is refused with an error and kept in memory by the engine only |
| `cluster.WithoutKeyspace`, `cluster.BastionStore`, `cluster.FlightKeyFromHash`, `cluster.Limiter.Estimate` | new in `internal/cluster` | the typed mechanisms behind the Valkey form ([valkey.md](valkey.md#the-bastions-keys)) |

**Where the engine calls the guard** (R-BAS-018). Every method takes the address as `guard.AddressKey` returns it.

| Step | Call | A refusal |
|---|---|---|
| 2, block list | `Blocked(addr)` | `429 address_blocked`, `Quiet`, `Retry-After` |
| 4, limits | `Request(addr, credentialed)` | `429 rate_limited` |
| a credential the Bastion refuses itself (R-BAS-019) | `FailedAuth(addr)` | none, the answer is the `401` |
| before a check or a forward for a credential with no usable answer | `AuthTimeout(addr)` | `429 auth_timeout`; nothing is sent to the main installation |
| before every request to the main installation (forward, check, conditional, probe) | `Miss(addr, credentialed)` | `429 rate_limited` while hits keep being served |
| the main installation called the credential invalid | `FailedAuth(addr)` | none |

The guard decides and returns a `Verdict` (`Status`, `Code`, `RetryAfter`, `Reason`, `Quiet`); the engine renders the problem details of R-BAS-029,
sets `Retry-After` and the `RateLimit` fields, and counts `trove_bastion_refused_total{reason=Verdict.Reason}`. The guard counts the blocks it sets
(`trove_bastion_blocks_total`, `trove_bastion_blocked_addresses`) and logs `bastion address blocked` once per block. An error of the shared state
admits the request (Q-BAS-94). `exempt_cidrs` are parsed at `guard.New`; a bad entry stops the start.

**The rules in one place.**

- Request limits: a token bucket without Valkey, the sliding window of R-ARCH-034 with it (limiters `bastion.requests_authenticated`,
  `bastion.requests_anonymous`); the local rate and burst are divided by `bastion.replicas`.
- Miss limits: the count of the sliding minute (limiters `bastion.misses_authenticated`, `bastion.misses_anonymous`), refused misses included; the
  retry delay is 15 s.
- Failed authentication (`bastion.failed_auth`): the failure that brings the count of the window to `max_failures` puts the address into the timeout
  (`Timeouts`, `failed_auth.timeout`); the next credential without a usable answer is refused. Anonymous requests and credentials with a usable answer
  are not the guard's to refuse.
- Blocking (`bastion.refusals`): every refusal of a request limit, a miss limit or the timeout counts; more than `threshold` within `window` blocks the
  address for `duration`, doubled for each earlier block within 24 h, up to `max_duration`. The decision is serialized in the process; across replicas
  two blocks can race, and the later one raises the level.
- IPv6 counts as its /64, an IPv4-mapped address as the IPv4 address (`AddressKey`).

**Valkey and its absence.** `shared.Open(ctx, shared.Options{...})` returns the local state for `valkey.mode: none` and the Valkey state otherwise,
with a closer. The Valkey form runs on `cluster.NewValkey(..., cluster.WithoutKeyspace())` and keeps a memory next to every mechanism: the block list
writes to both, reads memory first (a blocked address costs no round trip) and then Valkey; the limiters are the cluster limiters whose fallback is
the replica's share; the answers are Valkey only (the engine has its own memory). A Valkey that is down, refuses the credential or is slow makes
the state run from memory and never the replica unready; `trove_valkey_available` and `trove_ratelimit_fallback{limiter}` show it. Losing Valkey
costs the sharing, not correctness. The keys are in [valkey.md](valkey.md#the-bastions-keys).

**Add a limit.** A new limiter is a name `bastion.<name>` in [guard/instance.go](../../internal/bastion/guard/instance.go), a call to
`State.Limiter.Allow` (bucket) or `Add` (count), and a row in R-BAS-035 to R-BAS-037; the name is a metric label, so it carries no address. A new
shared mechanism is a typed method in `internal/cluster/valkey_bastion.go` first, then the interface in `shared.go`, its local form in `local.go` and its
Valkey form in `valkey.go`.

## Request flows

**Hit** (R-BAS-022): the engine classifies the request (host, block list, path, limits, credential), finds the cache entry for the
cache key, finds a usable `full` read-shape answer for the caller (or a usable URL answer) and the entry fresh, and serves the bytes
from the cache backend through the verifying reader. It adds a `DownloadEvent` to `downloads.Buffer`. No request to the main
installation.

**Check** (R-BAS-011, R-BAS-022): the entry exists and the caller has no usable answer. The engine takes a budget slot and sends one
`Check` for the credential and the repository (collapsed per credential and repository); on `full` it serves as a hit. For a `partial`
caller it sends a conditional `Do` with the caller's credential and `If-None-Match: "<sha256>"` through the real route: `304` serves the
cached bytes. The main installation's `check` handler validates the credential (throttle, `token.rejected`) and asks the
`ShapeResolver`.

**Miss** (R-BAS-021): the engine takes a budget slot, forwards the request with the caller's `Authorization`, the Bastion token in
`Trove-Bastion-Authorization` and the forwarded-for and host headers. On the main installation: the Recognize stage validates the token
and stores a `Recognition`; the Mark stage installs the observer; authentication and the dispatcher decide as for the caller's own
request; the marking wrapper adds `Trove-Bastion-Class`, `-Repository`, `-Read`, `-Subject` and `-Interface` at the status line. The
engine keeps what the class allows, streams the rest.

**Write probe** (R-BAS-014): a method other than `GET` and `HEAD` becomes a bodiless `Do` with `Probe` set, no credential and
`Trove-Bastion-Probe: 1`. The dispatcher calls `RouteObserver.Matched` after the match: for an `Upload`, `Delete`, `Admin` route or a
coarse action other than `read` it returns the `bastion_read_only` error, which the format renders in its own shape; for a read route
that takes a body it answers `204` with `Trove-Bastion-Route: read-with-body` and reports `handled`. The Bastion replays a `403` or
`404` and, after a `204`, forwards the real request.

## The cache backends (package 5)

Specification: R-BAS-030 to R-BAS-032, decisions Q-BAS-87 to Q-BAS-90, gaps H-451 to H-453. The consumers (engine, download buffer) hold
a `*cache.Cache` from `cache.New(ctx, cfg.Cache, signer, cache.Options{…})`; the audit spool and the raw operations are on
`Cache.Backend()`.

**One implementation, two stores.** `backend` (core.go) implements `cache.Backend` over a `store` of seven primitives: put a file, put
bytes, get a range, stat, list, remove, touch. `volumeStore` keeps the layout in `bastion.cache.volume.dir` and spools to `<dir>/tmp`
on the same filesystem, so a fill is committed by a `rename`; stale spool files (older than an hour) are removed at start.
`bucketStore` keeps it below `bastion.cache.bucket.prefix` through `internal/blob/s3` (the cache's only import beyond the platform
list), spools to `<tmp>/trove-bastion-cache` in the pod, and uses nothing but PutObject, GetObject, HeadObject, ListObjectsV2 and
DeleteObject, with no versioning and no conditional write. The S3 client is the main installation's, reached through the plain-typed
methods of [plain.go](../../internal/blob/s3/plain.go) ([Q-BAS-89](../spec/bastion.md#q-bas-89--the-bucket-backend-reaches-s3-through-a-plain-adapter-and-fails-fast));
`config.BastionCacheBucket.StorageS3()` maps the Bastion's keys onto it: `https` only, the trust bundle `ca_file`, credentials
`static` (the files of `secrets.cacheBucket`, re-read when they change), `webIdentity` or `default`, three attempts and 2 s of backoff.
Only the `static` source is exercised by the tests; the other two are the client's own, covered by its tests.

**What was added to the contract.** `cache.Backend` has the committed methods and one convention: `cache.WithArea(ctx, cache.EntryDocument)`
selects the area of the bytes (`sha256/` for artifacts, `documents/` for shared documents) on `PutObject`, `GetObject`, `HeadObject`
and `DeleteObject`; the default is the artifact area, and the wrong area is a miss ([Q-BAS-87](../spec/bastion.md#q-bas-87--the-bytes-of-documents-have-a-tree-of-their-own)).
`ObjectInfo.LastAccess` is the object's own time (a head does not read the marker). New errors: `ErrMismatch`, `ErrCorrupt`,
`ErrTooLarge`, `ErrInvalid`. Beyond the interface, `cache.Cache` ([Q-BAS-90](../spec/bastion.md#q-bas-90--the-cache-package-offers-a-fill-a-verified-read-and-the-first-readiness-gate-beyond-backend)):

| Call | What it does |
|---|---|
| `Ready(ctx)` | checks the backend (write, read, delete a probe) until one check succeeds, then answers nil without a request: the gate of the first readiness only |
| `NewFill(kind)` → `Fill` | spools a response while it is streamed (`Write`, running `SHA256`, `Size`); `Commit(ctx, sha)` verifies, commits under the address and removes the spool, `Abort()` discards; refuses bytes past `max_object_bytes` (artifact) or `documents_max_bytes` (document) with `ErrTooLarge` |
| `StoreEntry(ctx, cacheKey, entry)` | signs (HMAC over every field and the cache key) and stores; write the entry after `Commit` |
| `LookupEntry(ctx, cacheKey)` | `ErrNotFound` is a miss: no entry, a failed signature (entry deleted, `evictions{stale}`), bytes gone (entry deleted, `evictions{missing}`), a size that differs from the entry's (counted corrupt, object deleted); any other error is the backend's: serve a miss |
| `Open(ctx, cacheKey, entry)` | full read through the verifying reader; the last 64 KiB are released only after the SHA-256 and size matched; a mismatch returns `ErrCorrupt` from `Read`, deletes the object and the entry of the key, counts `trove_bastion_cache_corrupt_total`; a body longer than the entry is refused at once |
| `OpenRange` | a range, not verified (H-451) |
| `Touch(ctx, entry)` | the access marker, at most once per `access_resolution` per replica and content address; best effort |
| `SweepOnce(ctx, src)`, `RunSweeper(ctx, src)` | the sweep of R-BAS-031 and its loop; publish `cache_bytes`, count `evictions{size}`; `src` is a `LeaseSource` (`shared.State` is one), which offers the sweep lease only while the replicas share state, so the cache does not import `shared` |

**The sweep.** The limits are `max_bytes − documents_max_bytes` for artifacts and `documents_max_bytes` for documents
([Q-BAS-88](../spec/bastion.md#q-bas-88--documents-have-a-reserved-share-and-entries-are-removed-when-read)); `trove_bastion_cache_capacity_bytes{area}`
exports them. A cycle measures both areas (one listing each); an area above 95 % of its limit is swept: it lists its bytes and `access/`
(two listings), orders by the later of an object's marker and its own time (a refill counts as an access), and deletes oldest first until
85 % remain, taking the markers along. The sweep does not read entries (H-452). Who runs it: the volume, every replica its own; the
bucket with Valkey (`st.Shared()`), the replica that wins `shared.SweepLease` for `2 × sweep_interval`; the bucket without Valkey, every
replica at `sweep_interval ± 20 %`. The package 6 forms plug in through `shared.State`; until they land, `shared.NewLocal` is what the
tests use.

**Errors.** Every backend error except a missing key, an invalid name and a cancelled context is counted in
`trove_bastion_cache_errors_total{op}` (`get`, `put`, `delete`, `list`, `spool`) where it happens, so a caller only has to turn an error
into a miss. **Audit spool** (package 7): `SpoolWrite(ctx, replica, jsonl)` names the file `audit-spool/<replica>/<unix ms>.jsonl`
(raised by a millisecond on a clash in the process); `SpoolList` is oldest first; names that do not match the form are refused or
skipped, so no input becomes a path.

**Run it.** `go test -race ./internal/bastion/cache`; the bucket tests need the stack (`make integration-up`) and run in
`make test-integration` or alone with `go test -tags integration ./test/integration/bastioncache` after sourcing
`.dev/integration/env`. They write below `it-bastioncache/<run>/` of the `trove-backups` bucket and remove it.

## Schema and queries

Migrations ([database.md](database.md#migrations-of-the-bastion-q-bas-48)): `00032_bastion` (expand: `bastion`, `bastion_contact`,
the name rule, the token guard), `00033_bastion_kinds` (contract, `requires-app >= 0.1.0`: the checks of `principal.kind` and
`api_token.type`), `00034_bastion_validate` (expand). The query files are owned by the package that writes them and read by others
as the table in [Seams](#seams) says; nobody edits another package's file: add a file of your own.

## Configuration

The main installation's keys ([bastions.go](../../internal/config/bastions.go), `auth.tokens.bst.*`,
`auth.tokens.max_active_per_principal.bst`) are in [configuration.md](configuration.md#bastion-keys). The role's tree is
[bastion_role.go](../../internal/config/bastion_role.go) (`BastionConfig`, `LoadBastion`); its JSON Schema is the committed, generated
file [internal/config/bastion.schema.json](../../internal/config/bastion.schema.json), which the chart `trove-bastion` includes as the
schema of its `config` value. Keys the chart renders carry `x-trove-chart-managed: true`; the chart refuses a value for them.
`go generate ./internal/config` writes both schema files and the generated files of the chart (`values.schema.json` `config`
property, `chart-managed-keys.yaml`; [helm-chart.md](helm-chart.md#the-bastion-chart)).

## Run the role locally

```shell
mkdir -p .out/bastion
go run ./internal/tools/bsttoken > .out/bastion/token
cat > .out/bastion/trove.yaml <<'EOF'
server:
  listen: 127.0.0.1:18443
  ops_listen: 127.0.0.1:18081
  base_url: https://packages.example.com      # example
bastion:
  main:
    url: https://trove.example.com            # example; nothing is contacted yet
    token_file: .out/bastion/token
EOF
go run ./cmd/trove serve --role bastion --config .out/bastion/trove.yaml
```

In a second shell: `curl -s 127.0.0.1:18081/readyz`, `curl -s 127.0.0.1:18081/metrics | grep trove_bastion_`, and
`curl -si 127.0.0.1:18443/api/npm/npm-all/lodash`, which answers `404` with the code `bastion_not_served` until the engine is built.
No TLS is configured here; with `server.tls.certificates` both listeners serve it and reload it
([tls.md](tls.md)). `ss -ltn` shows `:8443`-style and `:8081`-style listeners only. A key of another section stops the role:
add `database: {host: x}` to the file and the start names `database.host` and the role.

## Tests

| Where | What |
|---|---|
| `go test ./internal/bastion/...` | the guard of the interface, the validity rules, the metrics table against the specification, the signer, the token syntax against `internal/auth/token`, the local shared state, the guard with an injected clock (limits, misses, timeout, blocking, exempt networks, IPv6), the server skeleton (listeners, readiness, TLS reload, token and key refusals) |
| `go test ./internal/config` | the role's defaults, the refusal of another section's keys, the ranges, the chart-managed marking, the committed schema |
| `go test ./internal/api/v1` | the document's interface operations, the `{bastion: true}` form, the stubs |
| `go test ./internal/archtest/...` | R-BAS-001's import rule and the layers of the Bastion's packages |
| `go test ./cmd/trove` | the role's start: another section's key, an http main URL, a missing token file |
| `go test -race ./internal/bastion/cache` | the volume backend on a temporary directory, the fill, the sweep and its order, the audit spool, the verifying reader, the signed entries, the first-readiness gate and counted errors |
| `make test-integration` (`test/integration/bastioncache`) | the bucket backend on the stack's SeaweedFS (S3, TLS), under a prefix of its own in `trove-backups`: two replicas on one bucket, the five operations asserted through a counting transport, eviction of 300 MiB to a 100 MiB limit, two sweepers, a replaced object, a rewritten entry |
| `make test-integration` (`test/integration/postgres`) | `TestBastionMigration`: the three migrations, the constraints, the guards and every query of the Bastion files |
| `make test-integration` ([test/integration/bastionshared](../../test/integration/bastionshared)) | two replicas against the stack's Valkey with Sentinel and TLS, each test on a key prefix of its own and an injected clock: an answer stored by replica A is found by B and a forged or altered record is ignored (`TestR_BAS_034_…`); the anonymous limit in total over two replicas without Valkey, 1,000 requests in 10 s admitted at most 140 times, 100 misses a minute at most 30 (`TestR_BAS_035_…`); 21 tokens (`TestR_BAS_036_…`); a flood blocked on both replicas for 15 and then 30 minutes (`TestR_BAS_037_…`); the fetch lock and the sweep lease. Valkey unavailable is an address nothing listens on: the tests never stop Valkey or trigger a failover |
| `go test ./internal/cluster` | `valkey_bastion_test.go`: the Bastion's keys against the in-memory server, the address mapping, `Limiter.Estimate` |

## Extension checklist

1. A new field in a body of the interface: edit `iface`, raise `InterfaceVersion`, regenerate the surface file, update the OpenAPI
   schema and its `x-trove-bastion-interface`, run `make generate` and `npm run generate:api` in `web/` (the UI types), and name the change in the release notes.
2. A new Bastion setting: add the key to `BastionConfig` with tags and a validation, run `go generate ./internal/config`, add it to
   R-BAS-046's block in the spec and to the chart's values; if the chart renders it, tag it `chart:"managed"`.
3. A new metric: a line in `internal/bastion/metrics` and a row in R-BAS-040; the test compares the two.
4. A new import in `internal/bastion/**`: it must be in `bastionAllowed()` with the requirement that justifies it, or the rule fails.
5. Replacing a stub: delete the stub's type or the `bastionStub` call in the same change, and remove the placeholder from
   `engine.Deps.Stubbed` if it was one.
