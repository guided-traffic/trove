# UI asset store

Status: implemented on 2026-11-06 (R-HA-020, [Q-HA-120](../spec/ha-and-helm.md#q-ha-120--how-a-replica-serves-a-hashed-ui-file-of-the-other-release)
to [Q-HA-125](../spec/ha-and-helm.md#q-ha-125--the-rolling-upgrade-from-a-release-without-the-store-stays-strict)). Every replica
that serves the UI (`api`, `web` in the bridge, `all`) writes the hashed files of its embedded build to the blob store and names them
in the table `ui_assets`; a replica asked for a hashed file its own build lacks reads it from there and serves it to the browser
under `/ui/<locale>/`, to anyone, as it serves its own build.

This page says what goes into the store, who can write it, what a request can read from it and what it costs, and how long a file
stays. It describes [internal/webui/shared.go](../../internal/webui/shared.go) and [hashed.go](../../internal/webui/hashed.go),
[internal/webui/shared](../../internal/webui/shared/) and [internal/app/wire_ui_assets.go](../../internal/app/wire_ui_assets.go).

## What goes in, and who writes it

- **Only the replica's own build.** The writer is the replica's start-up step `ui-assets` and its keeper's hourly renewal
  (`Keeper` in [keeper.go](../../internal/webui/shared/keeper.go)). They read the build embedded in the binary
  (`webui.HashedFiles` over the embedded tree), nothing else. No route, no operation of the management API and no job writes the
  table; a request can only read it.
- **Only hashed names.** A file is published only when its path below the locale matches the hashed pattern of R-UI-001
  (`webui.IsHashed`, [Q-UI-140](../spec/api-and-ui.md#q-ui-140--which-file-names-of-the-ui-build-are-hashed)): at most two plain
  directories, then `<name>-<8 characters>.<extension>`. `index.html`, the copies of `web/public` (the favicon, the logo files, the format logos of [Q-UI-226](../spec/api-and-ui.md#q-ui-226--a-logo-is-an-image-of-the-owners-file-and-the-dark-scheme-shows-the-owners-light-variant-or-a-plate)) and every other
  path are never published, and `build:embed` fails a build in which a copy of `web/public` would match the pattern. The table's
  CHECK refuses a path with a dot segment or a character outside `[A-Za-z0-9_./-]`.
- **One row per name, renewed by content.** A replica renews only the rows that hold its own bytes. A row with other bytes stays as
  long as a build with those bytes renews it; a replica replaces it with its own bytes only when the row's blob is not live and `ok`
  or no replica renewed it for 2 hours ([Q-UI-142](../spec/api-and-ui.md#q-ui-142--which-bytes-the-shared-store-keeps-for-a-name-that-two-builds-fill-differently)).
  Either way the new bytes are those of a build embedded in a replica. A replica serves its own file before the store's in every
  case.
- **The content is a blob, always verified.** It is spooled, hashed and committed by the protocol of R-STOR-005, deduplicated by
  SHA-256 with the rest of the blob store, and verified against its digest whenever it is read, also with
  `storage.download.verify: false` ([Q-UI-143](../spec/api-and-ui.md#q-ui-143--a-file-of-the-shared-store-is-verified-whatever-storagedownloadverify-says)):
  the blob store verifies the read (`OpenOptions.Verify`) and the store checks the length and the SHA-256 of the buffered bytes once
  more. An object that was changed in the bucket is not served: the read fails, the request answers `503`, the blob is marked
  corrupt.

## What a request can read

- A `GET` or `HEAD` of `/ui/<locale>/<path>` whose path matches the hashed pattern and is missing from the replica's own build
  asks the store for `(locale, path)`; nothing else under `/ui/` does, `index.html` and the client-side routes included.
- The answer has the headers of the replica's own files: the content type by extension, `X-Content-Type-Options: nosniff` and
  `Cache-Control: public, max-age=31536000, immutable`. The page that loads the file still has the CSP of the `index.html` it came
  from (R-UI-001): `script-src 'self'` admits the file because it comes from trove's origin.
- A name no release published is `404`; a store that cannot be asked (PostgreSQL or the object store away, 10 s at most) is `503`
  with `Retry-After: 1` and `Cache-Control: no-store`, and the replica logs it
  ([Q-UI-141](../spec/api-and-ui.md#q-ui-141--what-a-replica-answers-for-a-hashed-file-of-another-release)).
- The files are the UI's own code, the same for every user; they hold no data of the installation. Serving them without
  authentication is what R-UI-001 does for the replica's own build.

## What it costs a replica

- A found file is kept in the replica's memory, 32 MiB at most, the oldest out first, and read again after an hour; a name the
  store did not know is remembered for 10 s, 4,096 names at most; concurrent requests for one file read the store once. A file larger than 8 MiB is never
  published, so a read holds at most that much.
- The publish at start-up waits for the store for at most a minute before the replica turns ready
  ([Q-HA-123](../spec/ha-and-helm.md#q-ha-123--when-a-replica-publishes-its-build)); a store that refuses writes delays the
  readiness by that minute and does not keep the replica from serving.

## How long a file stays

Every replica renews the rows of its build at start-up and every hour; every replica deletes the rows that no replica renewed for
7 days, and the delete trigger queues their blobs for the collector (R-STOR-024,
[Q-HA-122](../spec/ha-and-helm.md#q-ha-122--how-long-the-files-of-a-release-stay-in-the-store)). The table's export class is
transient: a backup leaves it out and a restore leaves it empty; the replicas publish again.

## What this does not cover

### H-543 · A writer of the database can make every UI replica serve a script of its choosing

Stage: the lookup of a hashed file in `ui_assets` and the blob it names ([shared.go](../../internal/webui/shared/shared.go),
`Store.Asset`).

Dormant: live only for a principal who can write trove's PostgreSQL schema. Such a writer can insert a row
`(locale, path, blob)` for a hashed name no build has and point it at any live blob, for instance the content of a package file an
uploader stored; every replica that serves the UI then serves that blob as a script of trove's origin to any browser that asks
for the name. Subject: the browsers of trove's users, whose sessions run on that origin. A page loads such a script only if
something names it: an `index.html` of trove never does, so the writer still needs a way to make a browser load it.

What makes it real: write access to the database, which already decides every role binding and token of the installation
([roles-and-trust-boundaries.md](roles-and-trust-boundaries.md)): the store adds a path from that access into the browser. The
digest check of every read, which holds whatever `storage.download.verify` says (Q-UI-143), protects against a writer of the bucket
alone, not against one of the database, which names the digest. A signature of the
build's files that a replica checks before it serves them is not built.

### H-544 · A request for an unknown hashed name costs a database read, and `/ui/` has no request limit

Stage: the shared lookup of [shared.go](../../internal/webui/shared.go) on every replica that serves the UI.

Live: the general rate limits of [api-edge.md](api-edge.md) cover `/api/` and `/files/`, not `/ui/`. Every request for a hashed
name that the replica's build lacks and that it has not looked up in the last 10 s is one primary-key read of `ui_assets`
(`GetUIAsset`); a name that exists is one object read the first time a replica is asked for it. Subject: the request pool of
PostgreSQL and the object store, reached by an unauthenticated client that sends many distinct names. The remembered misses bound
the cost of one repeated name, not of distinct ones.

What makes it real: a client that sends many distinct names. The read is a lookup by primary key; nothing in trove limits the
rate of requests under `/ui/`, so what bounds it is the edge in front of trove, which the operator configures.

### H-545 · The files of a release stay servable for a week after its last replica stopped

Stage: the retention of [keeper.go](../../internal/webui/shared/keeper.go) (`Retention`, 7 days).

Live, by design: after an upgrade, the hashed files of the previous release are served by the replicas of the new one for 7 days,
so that an open tab keeps working. A release whose UI code had a defect that a later release fixed keeps that code reachable under
its hashed names for that week. Subject: the users of a browser that loads such a file, which needs a page that names it: the
`index.html` of the release that has it, which no replica serves any more, or a tab that was open before the upgrade. There is no
operation that removes the files of one release before the retention ends; deleting its rows from `ui_assets` by hand does
(the replicas that still keep a file in memory serve it until they restart).

What makes it real: a defect in the UI code of a release that is only exploitable while its files load, together with a tab of that
release open across the upgrade.
