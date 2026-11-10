# Import tooling

Status: built (2026-11-10) for npm and NuGet; the tool as built is described on [developer/import.md](../developer/import.md), the
operator's page is [manual/admin/import.md](../manual/admin/import.md), and the security perspective is
[../security/import.md](../security/import.md). The decisions are Q-IMP-1 to Q-IMP-9, decided by the development agent for the
owner's walkthrough.

This page specifies how an operator copies the packages of an existing registry into a trove local repository, so that a team can
move to trove without publishing every version again by hand. It covers:

- who runs the import and where its state lives (R-IMP-001);
- how the source is addressed, authenticated and bounded (R-IMP-002) and how its packages are named (R-IMP-003);
- the road a package takes into trove, and what that road guarantees (R-IMP-004);
- what is kept from the source and what is not (R-IMP-005);
- the outcome of a version, idempotence and conflicts (R-IMP-006), and the resume of a cut run (R-IMP-007);
- the politeness towards the source (R-IMP-008) and the seam for another format (R-IMP-009).

| Topic | Owner |
|---|---|
| The publish of an npm version, its digest rule, the immutability rule, dist-tags | [formats/npm.md](formats/npm.md) R-NPM-007, R-NPM-016 |
| The push of a NuGet package, unlist and relist, the registration | [formats/nuget.md](formats/nuget.md) R-NUGET-006, R-NUGET-007, R-NUGET-009 |
| The upstream URL rule, the stored credential rule, the egress address rules | [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-024, R-REPO-033, R-REPO-038 |
| Download responses with `ETag` and `Repr-Digest`, the quota | [storage.md](storage.md) R-STOR-012, R-STOR-029 |
| Permissions on a repository and a path, the audit events | [authorization.md](authorization.md) R-AUTHZ-005, R-AUTHZ-007; [operations-and-observability.md](operations-and-observability.md) |
| The vulnerability evaluation of a stored package | [vulnerability-scanning.md](vulnerability-scanning.md) |

Conventions: `https://trove.example.com` is the main endpoint *(example)*; `web/npm-local` and `web/nuget-local` are local
repositories of project `web` *(examples)*; `https://registry.example.com/` is the source registry *(example)*.

## Scope

The formats are **npm** and **NuGet**. Ansible Galaxy is left out: trove has no Galaxy format, and a package that no repository can
hold cannot be imported. The importer is built so that a format is one implementation of one interface (R-IMP-009); when a Galaxy
format exists, its import is that implementation and its documentation, and nothing in the runner, the report or the state file
changes.

## Requirements

### R-IMP-001 · The command, and what it is
Priority: **P1** · Covers: trove-only

The import is the subcommand `trove admin import <format>`. It is a **client** of the source registry and of the target
repository: it holds no database connection, reads no storage and writes through no other door than the target's package protocol
([Q-IMP-1](#q-imp-1--the-import-is-an-admin-subcommand-that-is-a-client-of-both-registries),
[Q-IMP-2](#q-imp-2--an-imported-version-takes-the-road-of-a-publish-with-the-token-of-the-operator)). It loads the configuration
file for `proxy.outbound` (the trust store and the egress address policy) and nothing else of it.

```
trove admin import npm   --from https://registry.example.com/ \
                         --to   https://trove.example.com/api/npm/web/npm-local/ [flags]
trove admin import nuget --from https://nuget.example.com/v3/index.json \
                         --to   https://trove.example.com/api/nuget/web/nuget-local/v3/index.json [flags]
```

| Flag | Meaning | Default |
|---|---|---|
| `--from`, `--to` | the source and the target, each as a package manager is given its registry: the registry address for npm, the service index for NuGet | required |
| `--package <name>` (repeatable), `--packages-file <file>` | the packages to copy (R-IMP-003) | none |
| `--search <text>` | also copy what the source's search finds (R-IMP-003) | not given |
| `--source-auth none\|basic\|bearer`, `--source-username`, `--source-password-file`, `--source-extra-host <pattern>` (repeatable) | how the source is asked (R-IMP-002) | `none` |
| `--token-file` | the trove token of the target (R-IMP-002) | `TROVE_IMPORT_TOKEN` |
| `--state <file>` | the state file (R-IMP-007) | none |
| `--report <file>` | the report as JSON (R-IMP-006) | none |
| `--concurrency <n>`, `--rate <per second>` | packages in flight, requests per second to the source (R-IMP-008) | `2`, `5` |
| `--max-package-bytes <n>` | the largest file taken from the source | `536870912` |
| `--spool-dir <dir>` | where a file waits between its download and its publish | the temporary directory |
| `--dry-run` | read both sides, report what would be copied, download and write nothing | off |
| `--config <file>`, `--log-level <level>` | as every admin command | |

- The command runs wherever the operator runs it: on a workstation, in the pod of any role (`kubectl exec`), or as a Kubernetes Job
  of the image. It needs a route to the source and to the target's client address, nothing else.
- It reads each package of the source and the target, copies what the target does not hold, and sets the state a publish cannot
  carry (R-IMP-005). It prints one line per package to stderr while it runs and the summary of R-IMP-006 to stdout at the end.
- The exit status is 0 when every package is complete (every version imported or already identical), 1 when a package needs a
  look (a conflict, a failure) or the run was stopped, and 2 for a usage error.
- `--dry-run` reads the source and the target as a real run does and compares what both announce; it does not download a file, so
  it cannot find a digest mismatch (R-IMP-004), and it says so in its report by marking the versions it would copy `would_import`.

Acceptance criteria:

- `trove admin import npm` without `--from` and `--to` exits 2 and prints the usage.
- A dry run against a source with three versions and a target holding one reports one `skipped` and two `would_import` and sends no
  `PUT` and no file request.

### R-IMP-002 · The source and the credentials
Priority: **P1** · Covers: trove-only

- **Address rule.** `--from` and `--to` obey the upstream URL rule of a remote repository (R-REPO-024): an absolute `http` or `https`
  URL with a host, without user information, without a fragment (`repository.NormalizeUpstreamURL`), and in addition without a
  query. A refused address stops the command before any request.
- **One client factory.** Every request of the importer, to the source and to the target, is made by a client of `internal/outbound`
  built from `proxy.outbound`: the trust store (system roots and `ca_files`, TLS 1.2 minimum, no switch to turn verification off),
  the egress address policy of R-REPO-038 in the dialer (an address the policy denies is not connected to and not retried), the
  timeouts, the redirect policy (at most five hops, never from `https` to `http`) and the `User-Agent`. A source on a private
  address needs an `allow_cidrs` entry as a remote repository's upstream does.
- **Origin rule.** The source's documents name the addresses of the files. A file is fetched only from the source's origin (scheme,
  host and port of `--from`) or from a host that matches a `--source-extra-host` pattern (`path.Match` on the lower-case host name).
  Any other address makes that version fail with `foreign_host` and nothing is requested. The extra hosts are asked **without** the
  credential.
- **Source credential.** `--source-auth basic` sends `Authorization: Basic` with `--source-username` and the password;
  `bearer` sends `Authorization: Bearer <password>`, as `authMode` of a remote repository does (R-REPO-033). The password is read from
  `--source-password-file` (the trailing line break removed, at most 8 KiB) or from `TROVE_IMPORT_SOURCE_PASSWORD`. It is **never** a
  command-line argument. The credential is attached by the hop policy of a remote repository (`outbound.CredentialPolicy`): it goes
  to the source's origin on every hop to that origin, to no other host and to no redirect target, and over `https` only, unless the
  origin itself is `http`.
- **Target credential.** The trove token of the target is read from `--token-file` or `TROVE_IMPORT_TOKEN`, never from an argument,
  and goes to the target's origin only, as `Authorization: Bearer` (for NuGet also as `X-NuGet-ApiKey`, as `dotnet nuget push` sends
  it).
- Neither credential is written to the state file, the report, a log line or an error message; addresses in messages and in the
  state file are shown without query and user information.

Acceptance criteria:

- `--from ftp://x`, `--from https://user:pw@x/` and `--from https://x/#f` are refused with the reason of the upstream URL rule.
- With the default egress policy, a source on `127.0.0.1` makes the first request fail with an egress denial after one attempt.
- A file on an extra host is requested without `Authorization`; a file on an unlisted host is not requested.

### R-IMP-003 · Which packages
Priority: **P1** · Covers: trove-only

A registry has no protocol that lists every package, so the operator says which ([Q-IMP-3](#q-imp-3--the-packages-are-named-by-the-operator-or-found-by-the-search-of-the-source)):

- `--package <name>` and `--packages-file <file>` (one name per line, empty lines and lines that start with `#` ignored) name packages.
- `--search <text>` adds the names the source's search endpoint finds for the text: npm's `GET -/v1/search?text=&size=250&from=`
  (Verdaccio, GitLab and Nexus implement it) and NuGet's `SearchQueryService` (`take=100`, `prerelease=true`). A source without the
  endpoint finds nothing and the command says so by importing only the named packages. A NuGet package whose every version is
  unlisted is not found by a search; name it.
- The names are deduplicated and sorted; the order of the run is the sorted order.
- A name is checked before it is put into an address: npm names by the grammar of an npm name (`@scope/name`, at most 214
  characters; the target applies R-NPM-002 to a new name), NuGet IDs by `[A-Za-z0-9][A-Za-z0-9_.-]{0,99}`. A name that fails is
  reported `invalid_name` and nothing is requested for it.
- A command with no name and no search is refused.

Acceptance criteria:

- A names file with a comment, an empty line and `@acme/ui` yields exactly that one package.
- `--package a --package a` copies `a` once.

### R-IMP-004 · The road into trove, and what it guarantees
Priority: **P1** · Covers: trove-only

An imported version is written by **the publish request of the format's own protocol** to the target repository, with the operator's
token: `PUT {name}` with the `.tgz` attachment as `npm publish` sends it (R-NPM-007), `PUT <feed>/package` with a multipart body
whose first part is the `.nupkg` as `dotnet nuget push` sends it (R-NUGET-006)
([Q-IMP-2](#q-imp-2--an-imported-version-takes-the-road-of-a-publish-with-the-token-of-the-operator)). The importer has no other way
in, so every rule of a publish applies to an imported version, because the same code decides it:

| Rule | Where it is decided for an imported version |
|---|---|
| Authentication and the permission `deploy` on the repository, the package name and the path of the file; path patterns of a grant; ownership of the name | the target's decision point (R-AUTHZ-005, R-AUTHZ-007, R-AUTHZ-018); a refusal is `403`, reported `forbidden` |
| The digest of the bytes against the digest the request announces | the target (R-NPM-007 step 3, R-STOR-004), after the importer's own check below |
| Immutability of a version, tombstones, the identical retry | the target (R-NPM-007, R-NUGET-006) |
| The audit event of the publish, with the token's principal as the actor | the target (`npm.publish`, `package.published`, `artifact.deployed`) |
| The vulnerability evaluation and a block | the target's publish hooks (R-VULN-001 and following); a block refuses the download and never the publish |
| The quota and the capacity of the storage | the target (R-STOR-029): `507 quota_exceeded` stops the run (R-IMP-006) |
| The shadowing check of a first publish of a name (R-REPO-048), the dependency-confusion rules (R-REPO-023) | the target |

**The importer verifies the digest itself, before it sends a byte.** For each version it takes the **strongest digest the source
announces** (npm: the strongest of `dist.integrity`, in the order `sha512`, `sha384`, `sha256`, `sha1`, else `dist.shasum`; NuGet:
the `packageHash` of the registration with its `packageHashAlgorithm`) and compares it with that digest of the bytes it downloaded
([Q-IMP-4](#q-imp-4--the-importer-verifies-the-strongest-digest-the-source-announces-and-refuses-a-version-without-one)).

- A mismatch fails the version with `digest_mismatch`; the file is deleted and **nothing is sent to the target**.
- A version for which the source announces no digest fails with `no_digest`; there is no switch to import it unverified.
- The request to the target announces the digests of the verified bytes (npm `dist.integrity` as `sha512` and `dist.shasum`), so the
  target checks them again against what it receives.
- The npm manifest sent is the `package.json` of the tarball, as `npm publish` builds it, plus `readme`, `readmeFilename` and
  `gitHead` of the source's version document where the tarball's manifest lacks them; a tarball whose `package.json` names another
  package or version than the source listed fails with `identity_mismatch` and is not sent.

Acceptance criteria:

- Imported versions are stored with the source's digest, one `npm.publish` or `package.published` event per version, whose actor is
  the user of the token.
- A source that announces a digest its file does not have: the version is `failed digest_mismatch`, no row and no event are written
  in the target.
- A token that holds only `read`: every version of every package is `failed forbidden`, nothing is stored, and the run goes on to
  the next package.

### R-IMP-005 · What is kept, and what is not
Priority: **P1** · Covers: trove-only

| From the source | Kept? | How, or why not |
|---|---|---|
| The package files | yes | the verified bytes (R-IMP-004) |
| npm: the manifest of each version | yes | the `package.json` of the tarball and the source's `readme`, `readmeFilename`, `gitHead` |
| NuGet: the `.nuspec`, the metadata | yes | trove reads them from the package, as for any push (R-NUGET-005) |
| Version order | yes, as an order of publication | versions are published one after the other in the order of the **source's publication times**, by version number where times are equal or unknown; the order trove serves (by version) is its own rule |
| npm: dist-tags | yes | each tag of the source is set with `PUT -/package/{name}/dist-tags/{tag}` after the versions are in, `latest` last; a tag whose version the target does not hold is reported and skipped |
| NuGet: unlisted | only where safe | after the versions are in, a version the source lists as unlisted is unlisted with the protocol `DELETE` (R-NUGET-007), but **only** when the importer has read the repository's configuration with the token and `nuget.deleteMode` is `unlist` (or unset); with `deleteMode: delete` that request would remove the version and write its tombstone, so the version stays listed and the report says why. A token that may not read the configuration (the response is the consumer view, `ETag` ending in `-consumer`) cannot know, and the state is not set |
| **Publication timestamps** | **no** | trove sets the time of a publish itself, on the database clock. No client can give a version an earlier time: a field that did would let any publisher forge the history that cooldown rules, audits and incident analysis read ([Q-IMP-5](#q-imp-5--publication-times-are-not-copied)). The source's time is used only for the order above |
| npm: deprecation messages | **no, reported** | trove has no way to set a deprecation yet (R-NPM-018 answers `404`); the report names the deprecated versions under "not kept". When R-NPM-018 is built, the importer sets it after the versions, as it sets tags |
| NuGet: deprecation, vulnerabilities | no | trove stores neither for a local package (R-NUGET-009); trove evaluates the vulnerabilities of what it stores itself |
| npm: provenance attestations, signatures | no, reported | the importer sends no `.sigstore` attachment; the source's bundle is for the source's tarball URL and publisher |
| Owners, maintainers, download counts, the source's users and tokens | no | trove's ownership and permissions are its own; the importing token's principal owns a name by the rules of the repository (`local.autoClaim`, R-REPO-023) |
| npm: unpublished versions, the source's tombstones | no | only versions the source lists are copied |

The report lists, per package, the states that are not kept (`notKept`), so an operator sees them before the source is switched off.

### R-IMP-006 · Outcomes, idempotence and conflicts
Priority: **P1** · Covers: trove-only

For each version of each package the run reports exactly one outcome:

| Outcome | Meaning |
|---|---|
| `imported` | the target did not hold the version; the publish was accepted |
| `skipped` | the target holds the version with the identical content |
| `conflict` | the target holds the version with **other** content, or refuses a publish of the version with `409` (`npm`: a version that exists, `nuget_version_exists`, `nuget_version_deleted`); **nothing is written**, the stored version is never changed, there is no overwrite switch |
| `failed` | the version was not copied; the code says why: `digest_mismatch`, `no_digest`, `foreign_host`, `identity_mismatch`, `too_large`, `source_error`, `forbidden` (the target's `403`), `target_refused` (any other refusal, with the status and the target's code) |
| `would_import` | a dry run only |

- **Comparison.** Before a version is downloaded, the importer compares the digest the source announces with the digest the target
  holds of that file in the same algorithm: npm's `dist.integrity` and `dist.shasum` of the target's document, NuGet's `Repr-Digest`
  (`sha-512`, `sha-256`) of a `HEAD` of the target's package content, which the target answers from its database (R-STOR-012). Equal
  is `skipped` without a download; different is `conflict` without a download. When the target holds no digest in the source's
  algorithm, the file is downloaded and verified (R-IMP-004), and compared in the strongest algorithm both have.
- **A package is complete** when every version is `imported` or `skipped`. A re-run of a complete import imports nothing and writes
  no event.
- **A failure does not stop the rest.** A `403`, a `409`, a failed download or a digest mismatch ends that version; the package's
  other versions and the other packages go on. Two answers stop the whole run, because no other package can get around them:
  `401` (the token is not accepted) and `507` (the target is full). The report then says why the run stopped; the packages not read
  are listed as `not read`.
- **Retries.** A transport failure, `429`, `502`, `503` and `504` are retried (R-IMP-008); a publish is safe to retry because the
  target answers the identical bytes of a stored version without a change (R-NPM-007, R-NUGET-006).
- **The summary** (stdout) prints one line for every conflict and failure, the states not kept, and the totals per outcome; the
  report (`--report`, JSON, mode 0600) holds every package with every version.
- **Preflight.** Before any package is read the importer asks the target whether the repository is of the format and the token is
  accepted: npm `GET -/whoami`, NuGet the service index with the publish resource. A `401` stops the run at once.

Acceptance criteria:

- Importing `imp-a` twice: the second run reports all versions `skipped` and the target has the same number of `npm.publish` events.
- The target holds `x@1.0.0` published from another registry; importing `x` from a source with other bytes for `1.0.0` and a new
  `1.1.0`: `1.0.0` is `conflict`, `1.1.0` is `imported`, the stored digest of `1.0.0` is unchanged, the run exits 1.

### R-IMP-007 · Resume after an interruption
Priority: **P1** · Covers: trove-only

An import of tens of thousands of versions survives a kill, a pod drain and a restart, and goes on without publishing a version twice
and without leaving a gap ([Q-IMP-8](#q-imp-8--a-run-resumes-from-the-targets-idempotence-and-an-optional-state-file)):

- **The target is the record.** Every step is idempotent against the target (R-IMP-006): a run that is started again reads each
  package, finds what the target holds, and copies the rest. A version that was published but not yet reported is `skipped` the next
  time; a version that was cut in transit was never stored (a publish is one transaction, R-ARCH-019) and is `imported` the next time.
  This holds with no state file at all.
- **The state file** (`--state <file>`) makes the resume cheap: after a package is complete (R-IMP-006) its result is appended to the
  file as one JSON line and flushed to disk. A run started again with the same file does not read those packages again; the report
  marks them `resumed`. The first line names the import (format, source, target); a file of another import is refused. A line cut by
  a kill is ignored. The file holds names, versions and outcomes, no credential, and has mode 0600.
- **One file is one import.** A package that gained versions at the source after it was recorded as complete is not read again with
  that file; a new import uses a new file. A dry run neither reads nor writes the state file.
- **Signals.** `SIGINT` and `SIGTERM` cancel the run: the request in flight is cancelled, the packages in progress are reported as
  stopped, and the process exits with status 1 and the report of what was done. A publish that was cancelled either committed or did
  not, because the target stores a version in one transaction (R-ARCH-019); the next run finds out which.
- A pod drain of the pod that runs the command needs nothing from trove: the Job is started again and resumes. For a Job, the state
  file belongs on a volume that survives the restart; without one the resume costs a re-read of the packages (two requests per
  package, plus one `HEAD` per version of NuGet) and nothing else.

Acceptance criteria:

- A run cut after two publishes (the context cancelled between two requests) leaves exactly two versions in the target; the same
  command with the same state file ends with all versions stored, every version published once in the audit log, and the packages
  complete before the cut marked `resumed`.
- A state file that was written by a different `--to` is refused with "belongs to another import".

### R-IMP-008 · Politeness towards the source
Priority: **P1** · Covers: trove-only

- Requests to the source are spaced by a limiter: `--rate` per second (default 5), counted over all packages of the run; `0` turns the
  spacing off. A source's `429`, and a `503`, **halve** the rate for the rest of the run (down to one request in ten seconds), as a
  mirror does (R-REPO-054).
- At most `--concurrency` packages (default 2) are in flight; the versions of one package are copied one after the other.
- A request is tried at most five times: a transport failure, `429`, `502`, `503` and `504` wait `500 ms` doubled per attempt, or
  the source's `Retry-After` (delta seconds or an HTTP date), but never longer than two minutes. Any other status is the answer.
- The importer asks for a package document once per run and reads each file once; it sends no request to the source for a version the
  target holds with the same digest.
- Bodies are bounded: a package document up to 256 MiB, a search page up to 32 MiB, a file up to `--max-package-bytes`.

Acceptance criteria:

- A source that answers `429` with `Retry-After: 3600` makes the importer wait two minutes (the cap) and halve its rate; a source that
  answers `404` is not retried.

### R-IMP-009 · The seam for a format
Priority: **P1** · Covers: trove-only

A format of the importer is one implementation of `importer.Format` and one line in `importer.FormatByID`
([Q-IMP-9](#q-imp-9--a-format-is-one-implementation-of-one-interface)): `ValidName`, `Preflight` (the target accepts the token and has
the format), `Search`, `Plan` (what the source holds of a package, in the order to import, with the strongest digest of each file and
the states not kept), `Held` (what the target holds, with its digests), `Publish` (one verified file through the target's publish
protocol) and `Finish` (the state a publish cannot carry). The runner, the limiter, the retry, the state file, the report and the
digest rule are shared and know no format.

The formats of the importer are the formats of the target repositories: a format the importer gains needs the format in trove first.
Ansible Galaxy is not a trove format, so it is not an importer format ([Scope](#scope)). Adding it later means the Galaxy format of
trove, then its `importer.Format`, its `FormatByID` line, a section on the pages of the admin manual and of the developer
documentation, and tests against a stub of the Galaxy API.

## Security notes

The trust boundary and the data flow are on [../security/import.md](../security/import.md). In short: the importer holds two
credentials for the length of one process, never writes them anywhere and sends each to its own origin only; the source is untrusted
input, bounded and verified by digest before anything is sent; and the importer has no authority of its own, because the target's
decision point decides every write with the token's permissions.

## Tests

Unit tests are in [internal/importer](../../internal/importer/importer_test.go) and [cmd/trove](../../cmd/trove/admin_import_test.go):
the limiter, the retry, the address rules, the egress denial, the credential scope, the state file, the runner's outcomes with a fake
format, the npm plan and publish body. The integration suite [test/integration/importer](../../test/integration/importer) runs the
importer against the counting stubs [test/npmstub](../../test/npmstub) and [test/nugetstub](../../test/nugetstub) and a replica on
the shared stack: a full import of each format, the rerun, a run cut short and resumed, a conflicting version, a digest the file does
not have, a token without the publish right, and the unlisted state with a repository whose delete removes the version.
No end-to-end test with the real clients is written: it needs the kind cluster, which the tooling run did not use; the e2e wave
adds `npm install` and `dotnet restore` of imported packages to the sets of the two formats.

## Decisions

Each question below was decided by the development agent from the listed options; the owner reviews the choices at the walkthrough.

### Q-IMP-1 · The import is an admin subcommand that is a client of both registries

Status: **Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.**

| Option | What it means | For | Against |
|---|---|---|---|
| (a) An admin subcommand that is a client (chosen) | `trove admin import` runs where the operator runs it and talks to the two registries over HTTP | No new server surface, no new table, no stored source secret; the road is the real publish (Q-IMP-2); a cut run is stopped by the operator's tools and resumes by idempotence (Q-IMP-8) | The operator keeps a process running, or a Job |
| (b) A job on the worker role | an enqueued job copies the packages inside trove, resumable by the job queue | Survives a drain by the framework; visible in the UI | The job needs the source's credential stored server-side (a new secret and a new egress path per run), needs an internal publish path that is not the protocol (a second road to keep as strict as the first), and needs a table for its pending set and an API to start it. All for a tool that runs once per team |
| (c) The CLI starts a job on the server | (b) with a command to enqueue | The operator's process ends early | everything of (b) |

**Choice:** (a).

**Rationale:** the import is a one-off migration, not a standing feature, and every property the task needs follows from the target's
idempotence (R-IMP-006): a drained pod or a killed process resumes by running the command again. (b) and (c) would add a second way
into the storage that must be kept as strict as the publish, and a stored source credential that a restore, a backup and an export
then have to treat. Revisit when imports become routine (a mirror-like standing sync of a foreign registry into a local repository,
which is the existing mirror mode of a remote repository): then the engine of R-REPO-053 is the place, not a new job.

**Applied in the spec:** R-IMP-001, R-IMP-007.

### Q-IMP-2 · An imported version takes the road of a publish, with the token of the operator

Status: **Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.**

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The protocol publish with a token (chosen) | `PUT` as `npm publish` and `dotnet nuget push` send it | Every rule is decided by the code that decides a publish: permissions and path scopes, digests, immutability, audit, vulnerability hooks, quota, ownership, shadowing. Nothing to keep in step | The importer is limited to what the protocol carries (no timestamps, no deprecation) |
| (b) An import service that calls the format's commit directly with a system principal | in-process, can set more fields | Could keep timestamps | A system principal bypasses the operator's permissions; each skipped step is a hole to find; a new write path to test and secure |
| (c) An import endpoint on the API | a privileged `POST` that takes a package and its source metadata | One call per version | A privileged way to write history; needs its own authorization model |

**Choice:** (a).

**Rationale:** the brief of this feature is that an import which skips a rule is a hole. (a) cannot skip one, because it does not
reimplement any. What the protocol cannot carry is documented (R-IMP-005). Revisit only if the owner wants publication times kept: that
is a decision about trusting a client-supplied time (Q-IMP-5), not about the road.

**Applied in the spec:** R-IMP-004, R-IMP-005.

### Q-IMP-3 · The packages are named by the operator or found by the search of the source

Status: **Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.**

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Names and the search endpoint (chosen) | names from flags and a file; optionally the names a search finds | Works on any registry; the operator controls the scope | A source with no search endpoint needs a list |
| (b) A full listing of the source | `-/all` (npm), the catalog (NuGet) | No list needed | `-/all` is gone from most registries; the NuGet catalog is a feed of changes of the public index, not every server has it |
| (c) Read the source's database or storage | | Complete | A different tool for every registry product |

**Choice:** (a).

**Rationale:** a team that moves knows its packages, and most can list them from their registry's own tools. The search covers the
common case without a promise no registry keeps. Revisit when a source's listing endpoint is common to the registries teams move from.

**Applied in the spec:** R-IMP-003.

### Q-IMP-4 · The importer verifies the strongest digest the source announces and refuses a version without one

Status: **Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.**

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Verify the strongest announced digest; refuse a version with none (chosen) | `sha512` over `sha384`, `sha256`, `sha1`; NuGet's package hash | The bytes are the ones the source vouched for before a byte is sent; a swapped file in transit or a corrupt source is caught | An old npm version with no digest cannot be imported |
| (b) Let the target verify only | the publish request carries the digests of the downloaded bytes | Simplest | The request would then announce the digest of whatever arrived: the check would compare the bytes with themselves |
| (c) A flag to import an unverified version | | Complete migration | A switch that turns off the one check that makes the import trustworthy |

**Choice:** (a).

**Rationale:** the target's digest check compares a request with its own attachment, so it protects the transfer to trove and not the
transfer from the source; the check against the source's announcement is the importer's. A version with no digest is rare (npm has
had `dist.shasum` since the beginning) and can be republished by hand. Revisit only with the owner's decision to accept unverified
versions.

**Applied in the spec:** R-IMP-004.

### Q-IMP-5 · Publication times are not copied

Status: **Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.**

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Not copied; the source's time orders the publishes (chosen) | trove's time is the time of the import | No client can forge history; no change to the publish protocol | The imported versions show the import date |
| (b) A header or field of the publish that sets the time | with a permission | History is kept | A time any holder of the permission can set; every consumer of the time (age rules, audit, incident analysis) must then ask whether it is real |
| (c) An admin-only import endpoint that sets it | | History is kept | A privileged write path (Q-IMP-2 (c)) |

**Choice:** (a).

**Rationale:** the publication time is evidence. Making it writable for the sake of a migration weakens it for ever; the order of the
publishes keeps the relative order, which is what version selection and the audit need. Revisit if the owner wants history kept and
accepts an admin-signed time.

**Applied in the spec:** R-IMP-005.

### Q-IMP-6 · An unlisted state is replayed only where a delete only unlists

Status: **Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.**

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Replay only when `deleteMode` is read as `unlist` (chosen) | read the repository document, check the mode | Never deletes a version by mistake | Needs a token that may read the configuration |
| (b) Always send the `DELETE` | | Always sets the state | In `deleteMode: delete` it removes the version for good and tombstones its number: the import would destroy what it just copied |
| (c) Never replay | | Safest | An unlisted version becomes listed |

**Choice:** (a); where the state cannot be set the report says so, and the operator unlists by hand.

**Rationale:** (b) can destroy data; (c) loses a state that is safe to keep when the mode allows it.

**Applied in the spec:** R-IMP-005.

### Q-IMP-7 · Credentials come from files or the environment and go to their origin only

Status: **Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.**

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A file or an environment variable, the hop policy of a remote repository (chosen) | `--token-file`, `--source-password-file`, `TROVE_IMPORT_TOKEN`, `TROVE_IMPORT_SOURCE_PASSWORD`; `outbound.CredentialPolicy` | Not in `ps` or the shell history; the same scope rule as an upstream credential | The operator prepares a file |
| (b) Flags that take the secret | | Convenient | Visible to every user of the host, and in history |
| (c) Use the stored credential of a remote repository by name | | One place for source secrets | The importer would need the database and the decryption key: a new, strong capability for a client tool |

**Choice:** (a).

**Rationale:** the existing handling of an upstream credential that a client can reach without trove's keys is the hop policy and the
file/environment form of `secretRef`; the importer uses both. Revisit when the import becomes a server job (Q-IMP-1).

**Applied in the spec:** R-IMP-002.

### Q-IMP-8 · A run resumes from the target's idempotence and an optional state file

Status: **Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.**

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Idempotence of the target, plus a state file of complete packages (chosen) | a rerun reads each package and copies the rest; the file skips complete packages | Correct with no file; cheap with one; nothing in trove's database | A rerun without the file re-reads every package |
| (b) A table of pending versions in trove | the mirror's design | A server job can resume | See Q-IMP-1 |
| (c) A state file as the only record | | Cheapest | A lost or stale file would hide a gap or re-publish |

**Choice:** (a).

**Rationale:** the file is an optimization, never the truth: the target decides what exists. A package is recorded only when it is
complete, so a cut package is read again.

**Applied in the spec:** R-IMP-007.

### Q-IMP-9 · A format is one implementation of one interface

Status: **Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.**

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One `Format` interface and a registry line (chosen) | the runner knows no format | A format is a file and a test; Ansible Galaxy later costs only its adapter | The interface fits registries that have a version list per package |
| (b) A per-format command with its own loop | | Each format as simple as it needs | The rate limit, the report and the state file duplicated and drifting |

**Choice:** (a).

**Rationale:** the shared parts are exactly the ones whose differences would be a hole.

**Applied in the spec:** R-IMP-009.

## What this does not cover

The open gaps of the mechanism are on [../security/import.md](../security/import.md#what-this-does-not-cover) (H-831 to H-834).

- Timestamps, deprecations and attestations of the source are not copied (R-IMP-005); the importer reports them.
- An import is a copy at a moment; changes at the source afterwards are copied by a later run (with a new state file). A standing
  synchronization of a foreign registry is the mirror mode of a remote repository ([repositories-and-proxy.md](repositories-and-proxy.md)).
- Ansible Galaxy, and every format trove does not have.
