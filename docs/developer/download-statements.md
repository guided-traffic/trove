# The statements of a download

A download of a stored file costs one PostgreSQL statement per route ([R-ARCH-027](../spec/architecture.md#r-arch-027--performance-targets)), counted on the
database's log, not assumed. This page is how the one statement is built, what reads from it, and how to add a format's rows to it.

## Measured

`TestR_ARCH_027_ADownloadCostsOneStatement` ([test/integration/querybudget](../../test/integration/querybudget/download_test.go)) turns `log_statement` on for the
database of its own stack, warms a path, sends 30 downloads to one replica and counts the statements by their sqlc name. Statements per download:

| Route | Before | After | Budget |
|---|---|---|---|
| npm tarball, local repository | 2 (`GetLiveItem`, `NPMTombstoned`) | 1 (`GetLiveItem`) | 1 |
| npm tarball, `/files/` raw view | 2 (`GetLiveItem`, `NPMTombstoned`) | 1 | 1 |
| npm tarball, remote repository, cache hit | 4 (`ProxyEntryGet`, `GetLiveItem`, `NPMRemoteTarballFacts` x2) | 1 | 1 |
| npm tarball, virtual repository, file in the local member | 3 (`GetLiveItem`, `NPMTombstoned` x2) | 1 | 1 |
| npm tarball, virtual repository, file in the remote member (local member first) | 6 (`GetLiveItem` x2, `ProxyEntryGet`, `NPMRemoteTarballFacts` x3) | 2 | 2 ([Q-ARCH-290](../spec/architecture.md#q-arch-290--what-does-a-download-of-a-file-of-a-virtual-repository-cost-in-statements)) |
| `.nupkg`, local repository | 2 (`GetLiveItem`, `NuGetServeState`) | 1 | 1 |
| `.nupkg`, remote repository, cache hit | 2 (`ProxyEntryGet`, `GetLiveItem`) | 1 | 1 |
| `.nupkg`, virtual repository, file in the local member | 3 (`GetLiveItem`, `NuGetServeState` x2) | 1 | 1 |
| `.nupkg`, virtual repository, file in the remote member (local member first) | 3 (`GetLiveItem` x2, `ProxyEntryGet`) | 2 | 2 |

The vulnerability scanning adds none: its block is a lateral join of the same statement ([vulnerabilities.md](vulnerabilities.md#the-download-policy)).
At the scale of the performance run of R-VULN-028 (100,000 stored versions, 10,000 findings), V-5b counts the same in the replicas by request ID
(`testenv.RequestTransactions`, [Q-VULN-98](../spec/vulnerability-scanning.md#q-vuln-98--v-5b-is-counted-in-the-replicas-by-request-not-from-the-databases-commit-counter)):
2,000 downloads of an npm tarball of a local repository from a replica with the scanning on and 2,000 from one with it off, one statement
(`GetLiveItem`) and one transaction each, except the one or two downloads per replica and about 15 s that refill the authentication caches.
A remote hit whose package document is expired, or was validated before a restore, revalidates the document first (one conditional request in one flight,
[Q-NPM-13](../spec/formats/npm.md#q-npm-13--removal-marks-after-a-restore-and-for-cached-tarballs)) and then reads the facts again; that request is outside the budget and costs the statements of the revalidation.

The first-request measurement of `TestR_ARCH_027_ADocumentOfACallerWithPathPatternsCostsOneStatement` (a document after a write) does not use the log:
it starts the replica with `testenv.RequestTransactions` and reads the statements of the one request by its `X-Request-Id`, so a statement of the replica's
own loops (the readiness check reads `system_state` twice) never falls into the count. The log window stays where a test counts many identical requests
and tolerates a lost line (`perRequestAs`).

## How the one statement is built

`GetLiveItem` ([files.sql](../../internal/meta/postgres/queries/files.sql)) is the item lookup of every download route. Besides the item, the blob digests and the blocking
finding it reads, by parameter, what the route needs and a separate statement used to read:

| Parameter of `GetLiveItem` | Set from | Reads | Replaces |
|---|---|---|---|
| `facts_kind`, `facts_package`, `facts_version` | `items.RepoRef.Facts` (a `servefacts.Key`) | `npm_local`: the tombstone; `nuget_local`: the version row (stored, listed) and the tombstone; `npm_remote`: the facts of the remote version, the state of the package document's cache entry and `serveRemovedVersions` | `NPMTombstoned`, `NuGetServeState`, `NPMRemoteTarballFacts` |
| `entry_probe`, `entry_variant` | `items.RepoRef.Entry` (an `items.EntryProbe`) | whether the artifact's cache entry exists at the item's path and is not marked gone (`Item.EntryStored`) | `ProxyEntryGet` of a hit |

A part of the statement whose kind is not asked reads nothing (`CASE`, or a lateral join with a parameter condition). The result is `items.Item.Facts` (`servefacts.Facts`, which carries the key
it was read for) and `Item.EntryStored`.

```
 route                         key (who names it)                         ServeCheck decides on
 ----------------------------  -----------------------------------------  --------------------------------
 format route (Items().Stat)   formathost/ingest.go: serveKeyOf            ItemRef.Facts of the Stat handle
 /files/ (files.Service.Stat)  files/download.go via files.Formats          items.Item.Facts
 remote hit, cache view        formathost/remote.go request -> proxy.Request.Facts -> proxy lookupHit
 virtual tier, local member    Items().Stat of the member (as the route)
 virtual tier, remote member   Remote().Cached (as the remote hit), then GateWith
```

* The format names the rows: `formats.ServeFactsKeyer.ServeFactsKey(repo, path)` (an optional interface, found at `Register` as `Capabilities.ServeFacts`), implemented by
  [npm](../../internal/formats/npm/servecheck.go) and [NuGet](../../internal/formats/nuget/servecheck.go). The zero key reads nothing.
* `ServeCheck` computes the same key and uses `ItemRef.Facts` only when `Facts.For(key)` holds. A caller that did not pass the key (a fake, a route not listed above) makes the check
  ask the data port as before, so a route cannot serve on facts read for another version.
* The facts live as long as the item of one request. Nothing is kept between requests ([R-ARCH-032](../spec/architecture.md#r-arch-032--no-state-between-requests-on-one-replica-inv-18)): a tombstone or a removal mark
  written on any replica applies to the next lookup of every other, and a loss of Valkey's data changes nothing.

### The remote hit

`proxy.Engine.lookupHit` ([artifact.go](../../internal/proxy/artifact.go)) is the hit: `GetLiveItem` with the entry probe and the key. An item whose entry exists is a hit; an item without an entry is a
miss; with no item the engine asks `ProxyEntryGet` once, to tell a miss from an entry whose item is gone (404, as before), and when it finds the entry it reads the item once more: a fill on another replica can have committed between the two statements, and the 404 of an entry without its item is for an item that is still missing after the entry was seen (`TestHitRace_ALookupBeforeACommitAndAnEntryAfterItIsServed`; the first version answered 404 to the clients that raced a fill, which failed the collapse legs of the end-to-end suites). The artifact entries are never marked gone (`remote_cache_entries`
has the check `kind = 'metadata' OR upstream_gone_at IS NULL`), so `entry_stored` also refuses a gone entry only as a guard.

The format's artifact gate (`BeforeArtifact`) used to run before the engine and read the facts itself. `formathost.remote.Artifact` now calls `Engine.Cached` first, hands the facts of that read to
the gate (`RemoteCaps.Gate`, `formats.FactsGate.BeforeArtifactWith`; `RemoteFetcher.GateWith` for a remote member of a virtual repository), and passes the item to the engine as `proxy.Request.Hit`, so the
engine reads neither the entry nor the item again. The gate returns the facts the serve check is to use: those it read again after a revalidation, so a withdrawal the revalidation brought is never
served from facts read before it. If the gate asked the upstream after the lookup (the revalidation sets `Facts.Refreshed`), the item, its entry and the scanning's verdict are older than the answer: the hit is dropped and read again
(`formathost.remote.Artifact` clears `Request.Hit`; the virtual tier calls `Cached` again), so a version that became blocked, or an item removed, while the conditional request ran is refused as before
(`TestR_VULN_015_AVersionBlockedDuringARevalidationIsRefused`). Facts are stamped with their key only when `GetLiveItem` reports the kind it evaluated (`fact_kind`); a kind without a branch in the statement is read by the check itself. A miss takes the old road: the gate reads the facts itself, the engine fetches, and `serveStored` reads the item again with the key.

## Adding a format's rows to the lookup

1. Add the kind to [internal/servefacts](../../internal/servefacts/servefacts.go) (`Kind`, the fields of `Facts`), and its alias to the contract ([hooks.go](../../internal/formats/hooks.go)).
2. Add the part of the statement to `GetLiveItem` in `files.sql` (a `CASE` branch or a lateral join with `facts_kind = '<kind>'` in its condition), run `make generate`, and map the columns in `itemFrom` ([item.go](../../internal/items/item.go)).
3. Implement `ServeFactsKey` on the format, use `it.Facts.For(key)` in `ServeCheck` and read the data port when it does not hold. Keep the data-port statement: it is the fallback and the reader of every route that passes no key.
4. The contract is frozen ([Q-ARCH-280](../spec/architecture.md)): regenerate the surface file with `go test ./internal/formats -run TestR_ARCH_025 -update-contract` and name the new exported names in the contract blocks of R-ARCH-008 and in an ADR/Q block.
5. Add the route to `TestR_ARCH_027_ADownloadCostsOneStatement`.
