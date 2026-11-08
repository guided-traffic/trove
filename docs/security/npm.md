# npm

Status: built (M2): the publish, the unpublish and the dist-tags of a local npm repository with the immutability of a version number and
its tombstones, the remote class with its document checks, takedown marks and tarball digests, the merge of a virtual repository, the
audit forward of R-NPM-038, and the journal entry a restore replays. The local audit (mode `local`, M4b) is on
[vulnerability-scanning.md](vulnerability-scanning.md#client-audits), the mirror passes of a remote (phase 6b) on [mirror.md](mirror.md).
Not built: the trusted-publishing exchange of R-NPM-022, deprecation (R-NPM-018) and the signing keys and attestations of an npm
registry (below).

This page describes what a caller can do to an npm repository through the protocol, what an upstream registry can make trove store and
serve, and what stops either from doing more. The authorization it relies on is on [permissions.md](permissions.md), the credentials on
[tokens-and-credentials.md](tokens-and-credentials.md), the raw `/files/` roads on [file-transfer.md](file-transfer.md), the generic
remote engine (freshness, negative cache, collapsing, circuit, offline modes) on [remote-repositories.md](remote-repositories.md), the
outbound rules on [egress.md](egress.md), the names the audit forward sends and withholds on
[data-and-secret-flow.md](data-and-secret-flow.md#the-audit-forward), the CI identity mappings on [ci-federation.md](ci-federation.md),
the exposure of documents on endpoints with rules on [endpoints-and-exposure.md](endpoints-and-exposure.md#what-a-package-document-reveals),
and the restore on [backup-and-restore.md](backup-and-restore.md). The requirements are in
[spec/formats/npm.md](../spec/formats/npm.md); the code is [internal/formats/npm](../../internal/formats/npm) and its data port
[internal/formatdata/npm](../../internal/formatdata/npm).

## The order of the checks of a publish

`PUT {name}` with a `.tgz` attachment publishes one version ([R-NPM-007](../spec/formats/npm.md#r-npm-007--publish),
[publish.go](../../internal/formats/npm/publish.go) `handlePublish`). A refusal stops the request at its step and nothing after it runs.

| Step | What is decided | Where | Answer |
|---|---|---|---|
| 1 | The route is a write of the class `local`: a remote repository refuses it, a virtual repository hands it to its deployment target; the caller holds `deploy` on the repository at all | the dispatcher's coarse check and `redirectClass` ([dispatcher.go](../../internal/api/dispatch/dispatcher.go)), [routes.go](../../internal/formats/npm/routes.go) | 401, 403, 405 `npm_class_not_writable`, 405 `no_deploy_target` |
| 2 | `deploy` on the name of the path, and the read-only form of the ownership rule (R-AUTHZ-018), before a byte of the body is read; a `Content-Length` above the cap | `Host.Authorize`, `precheckOwnership` | 403, 413 `npm_too_large` |
| 3 | The body is read once as a stream, capped at the base64 form of `npm.publish.max_tarball_size` plus 768 KiB of metadata and the base64 form of a 256 KiB provenance bundle; the tarball is decoded straight into the spool; only one `.tgz` and at most one `.sigstore` attachment | [publish/scan.go](../../internal/formats/npm/publish/scan.go) `Scan`, the host's spool | 400 `npm_bad_publish`, 413 `npm_too_large` |
| 4 | The request: `name` equals the path's name and passes the rules for new names (R-NPM-002), `_id` equals `name`, exactly one version and exactly one dist-tag pointing to it, a canonical SemVer version without build metadata, no NUL in the manifest, each attachment's length its data's | [publish/request.go](../../internal/formats/npm/publish/request.go) `Request` | 400 `npm_bad_publish`, `npm_invalid_name`, `npm_invalid_version` |
| 5 | Every `sha512` entry of `dist.integrity` and `dist.shasum` equal the digests computed while the tarball was spooled | `Request.CheckDigests` | 409 |
| 6 | The tarball in one streaming pass: one gzip member, clean entry names under one first component, exactly one regular `package.json`, no duplicate install path, links inside the package, the size, entry and ratio bounds | [tarball.go](../../internal/formats/npm/tarball/tarball.go) `Validate` | 400 `npm_bad_publish`, 413 `npm_too_large` |
| 7 | The `name` and `version` of the tarball's `package.json` are the request's; the compared fields of the request manifest and the `package.json` agree (`npm.publish.manifest_consistency`) | [manifest/compare.go](../../internal/formats/npm/manifest/compare.go) `CheckIdentity`, `Mismatches` | 400 `npm_manifest_mismatch` |
| 8 | `deploy` on the name and the version | `Host.Authorize` | 403 |
| 9 | The number: tombstoned, or live with other bytes; the acknowledged retry (below) | `Store.VersionFacts`, `versionTaken` | 409 `npm_version_unpublished`, 409 `npm_version_exists`, or 201 without a write |
| 10 | For the first version of a name: whether a remote member of a containing virtual repository serves it (R-REPO-048) | `shadowCheck` | 403 `shadows_remote_package`, 503 `shadow_check_unavailable` |
| 11 | The commit transaction: the package row is locked, the live versions are read again, the ownership rule decides and claims, the tombstone is read again, the name fold must be free, then the rows, the tags and the audit event are written | [hooks.go](../../internal/formats/npm/hooks.go) `OnCommit`, [store.go](../../internal/formatdata/npm/store.go) `Publish` | 409, 403, 400 `npm_invalid_name`, 503 `shadow_check_unavailable`, or 201 |

A `PUT {name}` whose body carries no attachment is no publish: after steps 1 to 3 it ends in `metadataUpdate`
([handlers_deprecate.go](../../internal/formats/npm/handlers_deprecate.go)) and changes nothing. A body without `versions` (the partial update
of `npm owner`) is 400 `npm_bad_publish`, because maintainer lists are not supported and ownership is managed in trove; a body with
`versions` (`npm deprecate`, R-NPM-018, P1) is 404 "Deprecating a version is not available yet", the answer R-NPM-043 prescribes for an
unimplemented `PUT`, which npm does not retry.

Steps 9 and 10 run outside any transaction and are decided again at step 11; the check inside the transaction is the one that protects
the repository. Unlike a NuGet push, an npm publish names its package in the URL, so the permission on the name and the ownership are
decided before the body costs the replica anything (step 2); only a grant that differs per version waits for the body (step 8).

What is stored is the request's manifest with every server-controlled field replaced ([manifest/stored.go](../../internal/formats/npm/manifest/stored.go)
`Stored`): every `_`-prefixed key except `_id`, `_nodeVersion` and `_npmVersion` is removed, `dist.tarball`, `dist.signatures` and
`dist.attestations` are removed, and `_npmUser` (the principal's name, no e-mail), `dist.integrity`, `dist.shasum`, `dist.fileCount`,
`dist.unpackedSize`, `_hasShrinkwrap` and `hasInstallScript` are set by trove from the validated tarball. A publishing client therefore
cannot forge trust evidence that pnpm's trust policy reads ([R-NPM-027](../spec/formats/npm.md#r-npm-027--signatures-attestations-and-publisher-fields)).

**`manifest_consistency: warn`.** In the default `reject` mode a request manifest whose dependency, binary, engine, platform or
install-script fields differ from the tarball's `package.json` is refused. With `warn` the difference is recorded in the asynchronous
event `npm.publish.manifest_mismatch` and the publish is accepted with the request's manifest, so the document a client resolves its
dependency tree from can list other dependencies than the tarball's `package.json`. `hasInstallScript` is computed from the tarball in
both modes. The `name` and `version` check of step 7 refuses in both modes (Q-NPM-6, Q-NPM-19).

A file that reaches an npm repository by any other road than a publish, a raw upload through `/files/` included, is refused by `OnCommit`
with 400 `npm_bad_publish`: no raw path can block a version's tarball path or place a tarball that no document lists.

## A version number is taken for good

A name and version once published to a local repository is never accepted again with other bytes there
([R-NPM-008](../spec/formats/npm.md#r-npm-008--version-immutability-and-tombstones)):

- **Other bytes** under a live number are 409 `npm_version_exists`, for a principal with `overwrite` as well.
- **An unpublish writes a tombstone** with the version's integrity, in the transaction that removes the version
  ([hooks_remove.go](../../internal/formats/npm/hooks_remove.go) `removeVersions`, `Store.Tombstone`). A tombstoned number refuses every
  publish, identical bytes included (409 `npm_version_unpublished`), and the serve check refuses the file of a tombstoned version on
  every route, the npm tarball route, `/files/` and a virtual repository's tiers alike ([servecheck.go](../../internal/formats/npm/servecheck.go)).
- **Nothing removes a tombstone** except the restore of the same bytes from the trash can (P1, `OnItemsRestored`, which clears the
  tombstone only when it holds the integrity of the restored version), `trove admin npm reinstate` for a version a restore's replay
  withheld ([reinstate.go](../../internal/formatdata/npm/reinstate.go), only a tombstone of source `journal` and only while the version is
  stored with the integrity the tombstone holds), and the deletion of the repository.
- **The acknowledged retry** (Q-NPM-11): a publish whose answer was lost and that the client repeats is answered 201 and changes nothing,
  when the same principal sends the same SHA-512, the same dist-tag and a provenance bundle exactly when the version has one, with the same
  SHA-256, within `npm.publish.retry_window` (default 10 minutes, `0s` turns it off) on PostgreSQL's clock (`isRetry`). It writes an
  `npm.publish` event with the reason `retry_acknowledged` and nothing else, so it can neither replace a version nor move a tag.
- **Look-alike names** (case, `-`, `_` and `.` folded) are refused by the unique name fold of `npm_packages`, enforced by the database in
  the commit transaction, so of two concurrent first publishes of `acme-ui` and `acme_ui` one answers 400 `npm_invalid_name`.
- **The provenance bundle** commits as a companion of the tarball in the same transaction (Q-NPM-58): a version is never visible without the
  bundle the client sent, and a bundle whose version is gone rolls the publish back.

## Unpublish

npm removes a version in two requests and a package in one ([R-NPM-017](../spec/formats/npm.md#r-npm-017--unpublish),
[handlers_unpublish.go](../../internal/formats/npm/handlers_unpublish.go)):

| Request | Effect | Needs |
|---|---|---|
| `PUT {name}/-rev/{rev}` with the document | every live version the body's `versions` omits is removed; a version the body names that the package does not hold is 400; nothing else of the body is applied, the tags are recomputed | `delete` on the name, the ownership, the current revision |
| `DELETE {name}/-/{file}/-rev/{rev}` | nothing: 200 for a version that is gone, 400 for one still live | `delete` on the name and the version |
| `DELETE {name}/-rev/{rev}` | every live version is removed, with all dist-tags | `delete` on the name, the ownership, the current revision |

The permission is decided before the transaction; a name without a package row is 404 before any lock is taken, so an unpublish never
creates a package. The ownership rule and the revision are decided inside the transaction under the package row lock. A stale revision
is 409 `npm_stale_rev`, except for a request that removes nothing, which succeeds whatever its revision, so a retry of a request that
already committed cannot overwrite a concurrent change. The body of the `PUT` is read as a stream within 128 MiB and only the keys of
`versions` are kept.

The route removes the version's tarball and provenance bundle through the package writer, and the hook `OnItemsRemoved` writes the
tombstone, moves the tags off the version, deletes the version rows (permanently: the trash can is P1) and raises the revision. A raw
delete of a tarball through `/files/` or the items API takes the same hook, after the generic layer asked the ownership of every package
whose files it removes under the package row lock (Q-AUTHZ-161), so no road can unpublish without the tombstone or without the
ownership check (Q-NPM-27).

## Dist-tags

`GET -/package/{name}/dist-tags` is a read; `PUT` and `DELETE -/package/{name}/dist-tags/{tag}` need `deploy` and the ownership of the
name ([R-NPM-016](../spec/formats/npm.md#r-npm-016--dist-tags-and-the-latest-rule),
[handlers_disttags.go](../../internal/formats/npm/handlers_disttags.go)). A tag that is a valid SemVer range is refused, the body of a
`PUT` is a JSON string of at most 1 KiB holding a canonical version, and the version must be live in the package (400 `npm_invalid_tag`).
`latest` cannot be removed; it can be moved to any live version, older ones included. A write that changes nothing writes no event.

Every tag change, by a publish, a tag write or a removal, runs under the package row lock and applies the rules of
[tags.go](../../internal/formats/npm/tags.go) to the tags read under that lock: a package with a live version always has `latest`, a tag
never points to a removed version, and with `npm.latestGuard` (default on) a publish under `latest` of a version lower than the current
`latest`, or of a pre-release while `latest` is a release, stores the version and leaves `latest` where it is, with an `npm-notice` in the
answer.

A virtual repository merges the members' tags ([R-NPM-031](../spec/formats/npm.md#r-npm-031--dist-tag-merge)): `latest` by
`npm.latestStrategy` (`highest-member-latest` by default, `newest-published`), the other tags by `npm.distTagStrategy` (`member-order` by
default, `highest-semver`, `newest-published`); a tag pointing to a version absent from the merge is dropped. Each version entry of the
merged document comes whole from the one member tier resolution picks for it, never combined from two
([R-NPM-030](../spec/formats/npm.md#r-npm-030--merged-package-document)), and a version two members hold with different integrity is
counted in `trove_npm_virtual_version_conflicts_total`. With automatic namespace claims (`local.autoClaim`), a name a local member holds
never mixes with upstream versions ([repository-model.md](repository-model.md#virtual-repositories-members-claims-and-pin-rules)), so a
public version of an internal name cannot become its `latest`.

## Trusted publishing

The exchange of a GitLab ID token for a package-bound publish token
([R-NPM-022](../spec/formats/npm.md#r-npm-022--trusted-publishing-through-ci-oidc-exchange), P1) is not built. What a request to its paths
does today:

- Below a registry URL (`/api/npm/<projectKey>/<repoKey>/-/npm/v1/oidc/…`) and at the host root the path belongs to the family
  `ci_exchange` ([surface.go](../../internal/httpserver/surface.go), `familyOfPath`). What answers depends on the role of the process:
  - **`web` in the bridge and `all`**, which serve `ci_exchange` and `packages`: the authentication stage does not read the
    `Authorization` header of the `POST` as a credential, so the caller is anonymous and the ID token is not evaluated
    ([stage.go](../../internal/auth/stage.go) `isExchangeRoute`); below a registry URL the request is decided as an anonymous read of the
    repository and, where that is allowed, answered 501 `npm_not_implemented` by the npm route of unimplemented endpoints (R-NPM-043).
    On an endpoint whose surface does not hold `ci_exchange` the path is no exchange route either, so the request is decided as on
    `packages` below: an ID token in `Authorization` is refused with 401, and without a credential the dispatcher answers 404
    ([dispatcher.go](../../internal/api/dispatch/dispatcher.go)).
  - **`packages`**, which does not serve `ci_exchange`: the path is no exchange route there, so an `Authorization` header is a credential
    like on any other path (Q-ARCH-302) and an ID token in it is refused with 401; without a credential the dispatcher answers 404. Both
    come in npm's error shape, not as `problem+json`
    ([H-521](../spec/architecture.md#h-521--two-path-rules-outside-the-stages-of-authentication-and-rate-limits-do-not-ask-whether-the-family-is-served)).
  - **`api`**, which serves `ci_exchange` but not `packages`: the caller is anonymous as on the bridge, but no format prefix is
    registered there, so the router's catch-all answers 404 `problem+json` like an unknown path
    ([router.go](../../internal/httpserver/router.go) `fallback`).
- No route is registered at the host root, so a `POST` there ends in the router's 404 on every role (on `packages`, an `Authorization`
  header is evaluated first, as below a registry URL). No token is issued on any path, and the
  event type `npm.trusted_publishing.exchange` is registered without a producer.

npm, pnpm and Yarn treat a failed exchange as silent and go on with the configured credential. A CI job publishes through the generic
exchange of [ci-federation.md](ci-federation.md#the-exchange-an-anonymous-endpoint) as a pipeline step instead, and what its token may
publish is decided by the identity mappings.

## Provenance and registry signatures

- **Local repositories** store the `.sigstore` attachment of a publish (at most 256 KiB) as a file of the version and do not interpret it
  (R-NPM-007 step 3). It is served as a stored file through `/files/`, with the response headers of every served file
  ([file-transfer.md](file-transfer.md#content-isolation-r-arch-007)). The stored manifest carries no `dist.signatures` and no
  `dist.attestations`, and `_npmUser` names the publishing principal (above).
- **Remote repositories** pass `dist.signatures`, `dist.attestations` with its `url`, `_npmUser` with `trustedPublisher` and `approver`,
  `deprecated` and every other member except `dist.tarball` through unchanged ([remote/rewrite.go](../../internal/formats/npm/remote/rewrite.go)
  `Entry`, [R-NPM-027](../spec/formats/npm.md#r-npm-027--signatures-attestations-and-publisher-fields)). trove does not check any of
  them cryptographically.
- trove serves no signing keys and no attestations: `GET -/npm/v1/keys` and `GET -/npm/v1/attestations/…` answer 404 (R-NPM-043), so
  `npm audit signatures` against trove verifies nothing (H-32).

## What an upstream registry can make an npm remote store and serve

The upstream is a party trove does not trust. The engine's request path, freshness, negative cache, collapsing and circuit are on
[remote-repositories.md](remote-repositories.md); what npm adds is in [remote.go](../../internal/formats/npm/remote.go) and
[internal/formats/npm/remote](../../internal/formats/npm/remote).

**Where an address comes from.** A package document is fetched from `<upstream>/<name with %2f>`, a tarball from the upstream base plus
its canonical path; every segment is escaped as one path segment, and a segment that is empty, a dot, a dot-dot or holds a NUL never
reaches the wire ([remote/upstream.go](../../internal/formats/npm/remote/upstream.go) `URL`). A tarball URL a document names that differs
from that address is kept as a URL-map row (R-REPO-043) and passes the engine's host rule when it is used: the upstream's origin or a
`remote.extraHosts` entry, and the credential only to hosts with `sendCredentials` ([egress.md](egress.md#which-hosts-receive-credentials)).

**What is read of an upstream document** ([R-NPM-024](../spec/formats/npm.md#r-npm-024--package-document-fetch-and-validation),
[remote/check.go](../../internal/formats/npm/remote/check.go) `Check`). Only the full document is fetched and stored; the abbreviated form
is derived from it. The document is bounded by the remote's `limits.maxMetadataBytes` (else `proxy.max_metadata_bytes`) while it is
received, and read once as a stream before anything is stored: one JSON object, `name` equal to the requested name, each version listed
once, each entry's `version` and `name` equal to its key and the name, a `dist` with an absolute `http` or `https` `tarball`, and a
`dist.integrity` that parses as a subresource integrity string where one is given. A document that fails is 502
`npm_invalid_upstream_document` and the previous entry stays. A `dist.shasum` that is no SHA-1 in hex is dropped, not refused. A stored
document is rendered within 128 MiB held in memory (`MaxDocumentBytes`), else 502 `upstream_item_too_large`.

**One stored document, one set of records.** The facts of every version (the upstream's integrity, shasum and publish time, the listed
mark) are written in the transaction that stores the document, under the lock of the package's record
([internal/formatdata/npm/remote.go](../../internal/formatdata/npm/remote.go) `ApplyFill`). A document whose `time.modified` is older than
the stored one is a stale copy (a lagging edge): it replaces nothing, marks no version removed, and counts in
`trove_npm_stale_upstream_documents_total`. The stored `time.modified` is capped at PostgreSQL's clock in that transaction
(`npm_remote.sql`, `LEAST(…, clock_timestamp())`), so a document dated in the future cannot block every later refresh.

**What a client is served.** Every `dist.tarball` is rewritten to the serving repository's registry URL, built from the response base URL
of the request and never from the upstream's address or a `Host` header (`TarballPrefix`); through a virtual repository it is the virtual
repository's URL. Every other member passes as the upstream wrote it.

**Which bytes a cached tarball must have.** The expected digest is the `sha512` of the document's `dist.integrity`, else its `dist.shasum`
([R-NPM-025](../spec/formats/npm.md#r-npm-025--tarball-urls-and-tarball-fetching), `BeforeArtifactWith`); the engine compares it under the
remote's `integrity.checksumPolicy` (default `verify-if-available`, 502 `upstream_integrity_mismatch`, nothing stored). npm declares no
fill inspection: a cached tarball is not opened, so the announced digest is the one tie between the document and the bytes. Under the
default policy a version whose document announces neither digest is stored as the upstream sent it; `require` refuses it before a byte is
fetched, because it asks for a SHA-256 or stronger digest ([internal/proxy/integrity.go](../../internal/proxy/integrity.go) `admitsDigests`).

**Versions removed upstream** ([R-NPM-026](../spec/formats/npm.md#r-npm-026--versions-removed-upstream)). A refreshed document that no
longer lists a version marks it unlisted and writes `npm.remote.version_removed` in the storing transaction; an upstream 404 or 410 for a
stored document is the engine's `upstream_gone_at`. With `npm.serveRemovedVersions: false` (the default) an unlisted version and every
version of a gone package answer 404, cached or not, on every route (`refuseRemoved`, called from `BeforeArtifactWith` and `ServeCheck`);
with `true` their cached tarballs stay served and they do not reappear in documents. The marks are PostgreSQL rows read in the lookup that
serves the tarball, never cached per replica or in Valkey, so a lost Valkey changes nothing. A cached tarball is served only while its
package document is current, not expired and validated after the last restore; otherwise the request first revalidates the document in
one conditional request (Q-NPM-13), so a lockfile install that never asks for documents does not bypass a takedown. When the upstream
cannot be reached the stored marks decide (H-33). The cache view never contacts the upstream.

## The audit forward

`POST -/npm/v1/security/advisories/bulk` ([R-NPM-038](../spec/formats/npm.md#r-npm-038--security-audit-endpoints),
[handlers_audit.go](../../internal/formats/npm/handlers_audit.go)) is answered by mode, `npm.audit.mode` of the repository document:

- **`local`** answers from trove's own advisory data and sends nothing out ([vulnerability-scanning.md](vulnerability-scanning.md#client-audits)).
  A document that leaves the mode out is `local` when `vulnerabilities.enabled` is on and the OSV source is not off; otherwise it is
  `forward` for a remote or virtual repository and `disabled` for a local one, and `forward` on a local repository is `disabled`
  ([handlers_audit_local.go](../../internal/formats/npm/handlers_audit_local.go) `auditModeOf`).
- **`forward`** sends the names to the advisory provider of the forwarding remote: the remote itself, or for a virtual repository its
  first enabled remote member in resolution order whose mode is `forward`, on which the caller needs `read` (Q-NPM-62). Nothing is
  forwarded on a remote's cache view, on an endpoint that does not fill caches, on an endpoint with exposure rules (the split would tell
  which names the remote has fetched), or for an offline remote: these answer 501 `npm_audit_unavailable`.
- **`disabled`** answers 501 `npm_audit_unavailable`, and so do npm 6's endpoints `-/npm/v1/security/audits[/quick]` in every mode.

The body is bounded before it is parsed (Q-NPM-64): `npm.audit.maxBodySize` as sent (default 1 MiB, 1 KiB to 10 MiB), ten times that
decompressed, gzip or no content coding, and at most 100,000 names. One statement decides which names leave and which are withheld as
local, claimed, pinned or not proxied (Q-NPM-63; the table is on [data-and-secret-flow.md](data-and-secret-flow.md#the-audit-forward)).
The provider is `npm.audit.providerURL`, `https` without user info, query or fragment, else the remote's upstream URL; the request goes
through the remote's upstream client with its egress, TLS, redirect and credential rules, within `npm.audit.timeout` (default 30 s, 1 to
300 s, shortened to the replica's shutdown bound). The engine's exchange takes no fetch slot and neither reads nor moves the remote's
circuit ([internal/proxy/exchange.go](../../internal/proxy/exchange.go)). The provider's answer is passed to the client within 128 MiB with
`Cache-Control: no-store`, `X-Content-Type-Options: nosniff`, and an `npm-notice` naming the count of withheld names.

## What the audit trail and the journal record

The state changes are written in the transaction of the change ([R-NPM-044](../spec/formats/npm.md#r-npm-044--npm-audit-events-and-metrics),
[events_npm.go](../../internal/audit/events_npm.go), [store.go](../../internal/formatdata/npm/store.go)):

| Event | Written by | Journal |
|---|---|---|
| `npm.publish` (with `tag`, `latest_guard`, the claim of a first version; the reason `retry_acknowledged` for an acknowledged retry) | the publish commit | no |
| `repository.remote_package_shadowed` | a first version published on the strength of `manage` over a name a remote member serves | no |
| `npm.publish.manifest_mismatch` (asynchronous; outcome `failure` in `reject` mode) | step 7 of a publish | no |
| `npm.unpublish` | the removal of a version by any road | yes: `{"repositoryId", "package", "version", "integrity", "actorId"}`, identifiers and a digest, no secret |
| `npm.dist_tag.set`, `npm.dist_tag.delete` (with the previous version) | a tag write | no |
| `npm.reinstate` | `trove admin npm reinstate` | no |
| `npm.remote.version_removed` | a fill that unlists a version | no |

A restore's replay of an `npm.unpublish` entry ([journal_replay.go](../../internal/formatdata/npm/journal_replay.go),
[R-NPM-047](../spec/formats/npm.md#r-npm-047--npm-data-in-backups-and-restores), Q-NPM-12) treats the entry as a claim to be checked: it
acts only on a local npm repository that stores the version with exactly the integrity the entry names, and then withholds the version
(a tombstone of source `journal`, the tag rule, the revision) and deletes nothing. A forged or mistaken entry costs availability until an
administrator reinstates the version, never data. Dist-tag moves are not journaled; what a restore reverts is H-51. The replay as a whole
is on [backup-and-restore.md](backup-and-restore.md#the-revocation-journal).

## What this does not cover

- [H-31](../spec/formats/npm.md#h-31--trusted-publishing-widens-the-set-of-credentials-that-can-publish): trusted publishing widens the set of credentials that can publish; dormant while the exchange of R-NPM-022 is not built.
- [H-32](../spec/formats/npm.md#h-32--registry-signatures-and-provenance-cannot-be-verified-through-trove): registry signatures and provenance cannot be verified through trove.
- [H-33](../spec/formats/npm.md#h-33--takedown-delay-and-earlier-downloads): a takedown takes effect after the metadata period, and earlier downloads stay.
- [H-34](../spec/formats/npm.md#h-34--audit-forwarding-discloses-the-dependency-set): audit forwarding discloses the dependency set.
- [H-51](../spec/formats/npm.md#h-51--a-restore-reverts-npm-metadata-changes-made-after-the-backups-snapshot): a restore reverts npm metadata changes made after the backup's snapshot.
- [H-300](data-and-secret-flow.md#h-300--the-advisory-providers-answer-is-trusted-as-it-is): the advisory provider's answer is trusted as it is.
- [H-301](data-and-secret-flow.md#h-301--an-audit-body-is-held-in-memory): an audit body is held in memory.
- [H-302](data-and-secret-flow.md#h-302--pnpm-and-yarn-do-not-show-that-an-audit-was-partial): pnpm and Yarn do not show that an audit was partial.
