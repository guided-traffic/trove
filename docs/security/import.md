# Import tooling

Status: built ([internal/importer](../../internal/importer/), [cmd/trove/admin_import.go](../../cmd/trove/admin_import.go)). This page says
what the command `trove admin import` is trusted with, what a source registry can make it do, what it can do to a trove repository,
and where its credentials live. The specification is [import.md](../spec/import.md) (R-IMP-001 to R-IMP-009), the operator's page is
[the manual](../manual/admin/import.md), and the build is described in [developer/import.md](../developer/import.md).

## Roles and trust boundaries

| Party | Trusted with | Not trusted with |
|---|---|---|
| The operator who runs the command | Choosing the source, the target and the packages; holding both credentials for the length of the process | Nothing beyond what the target token allows: the command is no administrator |
| The target token | The permissions of its principal: `read` and `deploy` on the repository, on the names and paths the grants name | It is not widened by the command; every write is decided by the target's decision point as for any publisher |
| The source registry | Naming packages, versions, digests and file addresses | Anything it says is checked: the names, the host of every file, the size, the digest of the bytes |
| The target (trove) | Deciding every write | |

```
      operator                      the import process                          trove (target)
   ┌────────────┐   tokens     ┌─────────────────────────┐   publish protocol  ┌──────────────────┐
   │ files/env  │────────────▶ │ reads documents, files  │────────────────────▶│ decision point,  │
   └────────────┘              │ verifies the digest,    │   Bearer <token>    │ digests, audit,  │
                               │ spools to a temp file   │                     │ vulnerabilities, │
          source registry      │ publishes one version   │                     │ quota            │
   ┌────────────┐  GET         │ after the other         │                     └──────────────────┘
   │ untrusted  │◀──────────── │                         │
   └────────────┘ credential   └─────────────────────────┘
        │          to its origin only
        └──── files: its origin, or an extra host, asked without the credential
```

## What the importer holds, and where

| Secret | Where it comes from | Where it goes | Where it is not |
|---|---|---|---|
| The trove token | `--token-file`, or `TROVE_IMPORT_TOKEN` | the `Authorization` header (and `X-NuGet-ApiKey` for NuGet) of requests to the target's origin | not an argument, not the state file, not the report, not a log line, not an error message |
| The source's password or token | `--source-password-file`, or `TROVE_IMPORT_SOURCE_PASSWORD` | the `Authorization` header of requests to the source's origin | as above; not to an extra host, not to a redirect target |

The command has no flag that takes a secret, so neither shows in `ps` or in the shell history. A file's trailing line break is
removed and a file over 8 KiB is refused, as the stored-secret files of a remote repository ([R-REPO-033](../spec/repositories-and-proxy.md)).
Both credentials are attached by the hop policy of a remote repository (`outbound.CredentialPolicy`,
[hop.go](../../internal/outbound/hop.go)): the credential goes to the origin on every hop that reaches the origin, to no other host, and
over `https` only unless the origin itself is `http`. The importer does not read trove's stored secrets, its key ring or its database.

## What a source can make the importer do

The source's documents are untrusted input. The importer bounds and checks them:

- **Addresses.** The source's own address and the target's are checked by the upstream URL rule ([`repository.NormalizeUpstreamURL`](../../internal/repository/validate_remote.go)):
  no user information, no fragment, `http` or `https` only, no query. Every request is made by a client of
  [internal/outbound](../../internal/outbound) and meets the egress address policy ([egress.md](egress.md)) in the dialer, after
  DNS resolution; a denied address is refused and not retried. A source cannot make the importer reach `127.0.0.1` or a metadata
  address unless the operator admitted it in `allow_cidrs`.
- **File addresses.** A package document names the address of each file. The importer fetches it only if it is on the source's origin
  (scheme, host, port) or on a host the operator named with `--source-extra-host`; any other address fails that version with
  `foreign_host` before a request is made. The extra hosts are asked without the credential, so a document cannot route the source's
  credential to a host of its choosing, whatever it names.
- **Names.** A package name from a document or a search is checked against the name grammar of the format before it is put into an
  address. A name that carries `..`, a second slash, a query or a space is `invalid_name`; scoped npm names are written with the slash
  as `%2f` in the one path segment npm uses.
- **Sizes.** A package document is read up to 256 MiB, a search page up to 32 MiB, a registration document up to 64 MiB, and a file
  up to `--max-package-bytes` (512 MiB by default); the declared length is checked before the body is read and the stream is cut at
  the limit. A redirect chain is at most five hops and never from `https` to `http`.
- **The number of requests.** One document per package and one request per file. A source with a search endpoint can list at most
  400 pages (npm) or 1,000 pages (NuGet) of results.
- **The bytes.** A file is written to a temporary file with mode 0600 in `--spool-dir` while its SHA-512, SHA-384, SHA-256 and SHA-1
  are computed, compared with the strongest digest the source announced, and deleted after its publish or its failure. A version with
  no announced digest is refused. The archive is read for its `package.json` only up to 2 GiB unpacked, the bound the target applies
  to a publish. Nothing from the archive is executed.
- **Identity.** An npm tarball whose `package.json` names another package or version than the source listed is not sent
  (`identity_mismatch`); the manifest sent is the tarball's own, as `npm publish` sends it.

## What the importer can do to the target

Nothing the target token cannot do with `npm publish` or `dotnet nuget push`. The importer has no database, no object-store credential
and no administrative API call except one read: for NuGet, `GET /api/v1/projects/{project}/repositories/{repository}`, to learn
whether the protocol `DELETE` only unlists (R-NUGET-007). A token that cannot read the configuration gets the consumer view and the
importer then leaves the unlisted state unset. The importer never sends `DELETE` in a repository whose `deleteMode` is `delete`, which
would remove a version and tombstone its number ([Q-IMP-6](../spec/import.md#q-imp-6--an-unlisted-state-is-replayed-only-where-a-delete-only-unlists)).

Every rule of a publish is therefore decided by the target for an imported version: the permission `deploy` on the repository and on
the path of each file, ownership of the name, the shadowing check of a first publish, immutability, the quota, the audit event with the
token's principal as the actor, and the vulnerability evaluation ([npm.md](npm.md), [nuget-publish.md](nuget-publish.md),
[vulnerability-scanning.md](vulnerability-scanning.md)). A conflict is never overwritten: a version that the target holds with other
content is reported and untouched, and the comparison is made by digest, from the target's database
([R-IMP-006](../spec/import.md)).

The publication time of an imported version is the time of the import. This is by design: no client can set the time of a publish
([Q-IMP-5](../spec/import.md#q-imp-5--publication-times-are-not-copied)).

## The state file and the report

The state file and the report hold package names, versions, outcomes and codes. They hold no credential and no address with a query or
user information (addresses are shown with the query removed). Both are written with mode 0600. The state file is an optimization of
the resume and never the record of what exists: the target is ([R-IMP-007](../spec/import.md)). A stale state file therefore costs
completeness of a later run, never a duplicate, because a skipped package is not published to.

## What this does not cover

### H-831 · A source whose digests are consistent with malicious bytes is imported

Component: the digest rule of R-IMP-004. Subject: a source registry that is compromised or hostile. Live today, for any import.

The importer proves that the bytes are the ones the source announced, not that the source is honest. A source that announces the digest
of a malicious tarball gets it published into the target under the name and version it lists, with the permissions of the token. What
stops it afterwards is only what stops any publish: the vulnerability evaluation of the stored version (a block refuses the download
and never the publish, [H-311](../spec/vulnerability-scanning.md#h-311--a-block-refuses-the-download-not-the-resolution-or-an-earlier-install))
and the owner's review. What makes it real: an import from a registry that an attacker controls or has changed since the team last
trusted it. An operator limits it by naming the packages instead of using `--search`, and by dry-running against the list first.

### H-832 · The two credentials live in the process for the length of the run, and an environment variable is readable by the same user

Component: the credential handling of R-IMP-002. Subject: the host that runs the command. Live today.

A token read from `TROVE_IMPORT_TOKEN` or `TROVE_IMPORT_SOURCE_PASSWORD` is in the environment of the process, which another process of the same user (and root) can read from `/proc/<pid>/environ` while the run lasts. A file is read once into the process's memory and not zeroed. The importer does not check the mode of the file. What makes it real: a shared host where another account runs as the same user; in a Kubernetes Job the Secret mounted as a file is the better form. The command writes neither credential to disk, so what is left after the run is the operator's file.

### H-833 · The state file is not protected against a change

Component: the state file of R-IMP-007. Subject: the file the operator keeps. Live today, for a run that uses `--state`.

The file has mode 0600 and a header that names the import, but no signature or checksum. A person who can write it can add a package
as complete, and a later run with that file then does not read that package: the import ends with a clean report and a package
missing. The run without the file finds it. What makes it real: a shared volume for the state file of a Job that others can write.
The target is not endangered, since a state file can only hide work, never cause a write.

### H-834 · The importing principal owns every name it creates, and the command does not narrow the token

Component: the road of R-IMP-004. Subject: the token of the import. Live today.

The first publish of a name by the token's principal claims it under the repository's ownership rule (`local.autoClaim`,
[R-REPO-023](../spec/repositories-and-proxy.md)), so after an import the service account of the migration owns every imported name and can publish
new versions of all of them for as long as the token lives. The command also does not ask for a narrower token than it needs, and it
publishes every name the token may publish that the operator lists. What makes it real: a long-lived token left over from a
migration. The remedy is the operator's: a service account with a short-lived token for the import, revoked afterwards
([manual](../manual/admin/import.md)), and the ownership of the names moved to their teams.
