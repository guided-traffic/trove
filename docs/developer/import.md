# Import tooling

Status: built (2026-11-10) for npm, NuGet and Ansible Galaxy collections. Specified on [import.md](../spec/import.md) (R-IMP-001 to R-IMP-009, Q-IMP-1 to Q-IMP-9);
the operator's page is [manual/admin/import.md](../manual/admin/import.md), the security perspective is
[security/import.md](../security/import.md).

`trove admin import <format>` copies the packages of an existing registry into a trove local repository. It is a **client**: it
reads the source over HTTP, writes the target through the format's own publish protocol with a trove token, and has no database, no
storage and no server-side component. That is the central fact of the design ([Q-IMP-1](../spec/import.md#q-imp-1--the-import-is-an-admin-subcommand-that-is-a-client-of-both-registries),
[Q-IMP-2](../spec/import.md#q-imp-2--an-imported-version-takes-the-road-of-a-publish-with-the-token-of-the-operator)): every rule of a
publish (permissions, digests, immutability, audit, vulnerability evaluation, quota) is decided by the code that decides a publish,
and the importer reimplements none of them.

## What has to be in your head

- The runner knows no format. A format is an implementation of `importer.Format` ([types.go](../../internal/importer/types.go)); the
  runner, the limiter, the retry, the state file and the report are shared.
- The target is the record of what exists. A re-run reads each package, compares by digest and copies the rest. The state file only
  skips packages that are complete.
- The importer verifies the strongest digest the source announces **before** it sends a byte; the target's check compares a request
  with its own attachment and protects the other leg.
- Every HTTP client comes from `internal/outbound` (R-ARCH-002): `importer.Build` makes two clients from one `outbound.Factory`, each
  with an `outbound.CredentialPolicy`, so the credential of the source goes to the source's origin and the token to the target's.
- The importer sets nothing a publish cannot carry. Publication times and npm deprecations are not copied (R-IMP-005); the report says so.

## Layout

```
internal/importer/        # the library (a domain package, archtest layer "domain")
cmd/trove/admin_import.go # the command: flags, secrets from files, the config for proxy.outbound, the summary
test/integration/importer # the importer against the counting stubs and a replica on the shared stack
```

| File | Responsibility |
|---|---|
| [doc.go](../../internal/importer/doc.go) | the package comment |
| [types.go](../../internal/importer/types.go) | the outcome and failure codes, `Digest`, `Version`, `Plan`, `Held`, `File`, the `Format` interface, `Refusal` |
| [runner.go](../../internal/importer/runner.go) | `Options`, `Run` (preflight, discovery, the worker pool), `pkg` (plan, held, versions, finish, journal), `version` (the decision per version: compare, download, verify, publish, classify), `Report` and its totals, the two run-stopping refusals (`fatalOf`) |
| [client.go](../../internal/importer/client.go) | `SourceClient` (origin rule, bounded `GetStatus`/`GetJSON`, `Download` into a spool file with four digests), `TargetClient.Do`, `sendWithRetry`, `Retry`, `Limiter`, `Refusal` from an answer |
| [assemble.go](../../internal/importer/assemble.go) | `Params`, `Build` (address rule through `repository.NormalizeUpstreamURL`, the two clients, the hop policies), `FormatByID` |
| [npm.go](../../internal/importer/npm.go) | `NPM`: name grammar, search, `Plan` from the full document, `Held` from the target's document, `Publish` (the `package.json` of the tarball, the streamed base64 body), `Finish` (dist-tags) |
| [nuget.go](../../internal/importer/nuget.go) | `NuGet`: service index of both sides, search, `Plan` from the registration (paged or inline, gzip tolerated), `Held` (registration plus a `HEAD` for `Repr-Digest`), `Publish` (multipart), `Finish` (unlist only where `deleteMode` is read as `unlist`) |
| [ansible.go](../../internal/importer/ansible.go) | `Ansible`: name grammar (R-ANS-002, folded to lower case), `Preflight` (the discovery document is a trove one with `v3`), `Search` (the collection index, `links.next` followed), `Plan` (version list and version documents, `artifact.sha256` as the digest, the roles of the source and the deprecation flag as `NotKept`), `Held` (the target's version documents), `Publish` (multipart `sha256` and `file`, then the import task is polled), `Finish` (nothing) |
| [journal.go](../../internal/importer/journal.go) | the state file: header, one flushed line per complete package, a cut last line ignored |
| [order.go](../../internal/importer/order.go) | the import order (source time, then version number), `compareVersions`, `maybeGunzip` |
| [cmd/trove/admin_import.go](../../cmd/trove/admin_import.go) | `importCmd`, `secret` (file or environment, never an argument), `readNames`, `writeReport` |

## How a run goes

1. `importCmd` parses the flags, reads the two secrets, loads the configuration for `proxy.outbound`, and builds the factory
   (`factory.Err()` fails the command if the trust store or the policy is broken).
2. `importer.Build` checks both addresses with the rule of a remote repository's upstream URL and builds the clients.
3. `Run`: `Format.Preflight` (npm `GET -/whoami`; NuGet the service index with the publish resource) — a `401` stops here. Then the
   names (flags, file, `Format.Search`), sorted, and a pool of `--concurrency` workers, each running `run.pkg`.
4. `run.pkg(name)`: the journal's complete result if there is one (`Resumed`); else `Plan` (the source), `Held` (the target), then
   each version in the plan's order through `run.version`; then `Finish` (npm dist-tags; NuGet unlisting) when at least one version is
   in the target; then the journal record if the package is complete.
5. `run.version`: no digest → `no_digest`; file host not allowed → `foreign_host`; the target holds the version and the digest in the
   source's algorithm → equal is `skipped`, different is `conflict`, no download; else download into the spool (bounded), verify →
   `digest_mismatch` and nothing sent; compare with the target's digest when it holds the version without a comparable one; else
   `Format.Publish`. A `409` is a `conflict`, `403` is `forbidden`, anything else `target_refused`. `401` and `507` stop the run.
6. `writeReport` prints the problems and the totals and writes the JSON report.

## Add a format ("how to add an X")

The format has to exist in trove first: the importer writes a trove repository through that format's protocol. The Galaxy
collections are the example of a publish that answers with a task: `Ansible.Publish` returns when the task is finished, and a task
that failed becomes a `*Refusal` with the task's code.

1. Implement `importer.Format` in a new file `internal/importer/<format>.go`: `ValidName` (a conservative grammar; the name goes into
   an address), `Preflight`, `Search` (return nil, nil when the source has no search), `Plan` (versions in `sortVersions` order, the
   strongest digest of each file in `Version.Want`, `Plan.NotKept` for the states the target cannot hold), `Held` (digests of the
   stored files, from the target's headers or documents; a non-nil `Digests` map for every version that exists), `Publish` (the
   format's own publish request, built from the verified `File`; return `*ItemError` for a failure you can name, `*Refusal` from
   `refusal(resp)` for a target refusal), `Finish`.
2. Add the format to `FormatByID` in [assemble.go](../../internal/importer/assemble.go); if its target needs headers besides
   `Authorization`, add them to `dstHeaders` there.
3. Test the format against the stub of the source (as [test/npmstub](../../test/npmstub) and [test/nugetstub](../../test/nugetstub)):
   unit tests in `internal/importer`, and an integration test in [test/integration/importer](../../test/integration/importer) for the
   full import, the rerun, the cut run, a conflict, a digest the file does not have and a token without the publish right.
4. Document it: the format's section in [manual/admin/import.md](../manual/admin/import.md) (what is kept), the spec page, and this
   page's table.

## Tests

| Tier | Where | What |
|---|---|---|
| Unit | [internal/importer](../../internal/importer/importer_test.go), [cmd/trove/admin_import_test.go](../../cmd/trove/admin_import_test.go) | the limiter and the retry (a fake sleep), the address rule, the egress denial, the credential scope with real servers, the state file, the runner's outcomes over a fake `Format`, the npm plan and the publish body, `Repr-Digest` |
| Integration | [test/integration/importer](../../test/integration/importer) (`go test -tags integration`, through the slot wrapper) | the importer over TLS to the counting stubs and into a replica started in the test: full imports of both formats, rerun, a run cut after two publishes and resumed, a conflicting version, a bad digest, a reader token, the unlisted state with `deleteMode` `unlist` and `delete`. The audit log and the stored versions are read from PostgreSQL |
| e2e | none yet | needs the kind cluster; the sets of the two formats gain `npm install` and `dotnet restore` of imported packages |

The integration environment reaches the replica by the name its base URL carries (`trove.test`), using the factory's
`WithDialOverride` test seam, and admits `127.0.0.1` in the address policy as an operator would. A run is cut with a
`RoundTripper` around the target client that cancels the context after the n-th `201`.

## Conventions

- The report words (`imported`, `skipped`, `conflict`, `failed`, the codes) are stable; the manual page lists them.
- No log line, error or file contains a credential; addresses in messages go through `redact` (query and user information removed).
- A retry never repeats a request that is not safe to repeat: a publish is, because the target answers the identical bytes of a stored
  version without a change.
- Files that wait for their publish are deleted on every path (`defer f.Remove()`); a killed process leaves them in `--spool-dir`.
