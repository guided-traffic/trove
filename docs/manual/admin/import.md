# Importing from another registry

How to copy the packages of an existing npm registry, NuGet feed or Ansible Galaxy into a trove local repository, so that a team can move to trove without publishing every version again by hand: what the command needs, how to run it, what is copied and what is not, how it reports, and how to resume a run that was cut short.

The command is `trove admin import`. It is a client of both registries: it reads the old registry the way a package manager does and writes the trove repository through its normal publish protocol with a trove token. It needs no database and no access to trove's storage, and it can run on a workstation, in any trove pod, or as a Kubernetes Job. Because every version goes in through the publish protocol, everything a publish checks is checked for an imported version too: the permissions of the token on the repository and on each package name, the digest of the bytes, the rule that a stored version never changes, the audit event, the vulnerability evaluation and the storage quota.

The formats are **npm**, **NuGet** and **Ansible Galaxy** (the collections; see [Ansible Galaxy](#ansible-galaxy)).

## Before you start

1. **A local repository of the format** in trove, created the usual way ([Managing repositories](repositories.md)). Give it the settings you want imported packages to have; the import does not change them.
2. **A trove token** that may publish to that repository: the permissions `read` and `deploy` on it, and on the package names you import if the grant has path patterns. A token with only `read` can be used for a dry run and for nothing else. Create it with the usual tools ([Managing tokens](tokens.md)); a service account is the right owner for a migration.
3. **Access to the old registry**, and a credential if it needs one.
4. **A route from where you run the command** to the old registry and to trove's client address. The addresses are checked by the same egress address policy as the upstream of a remote repository (`proxy.outbound.address_policy`): a registry on a private network is reached only if `allow_cidrs` admits its address, and the policy refuses loopback and link-local addresses by default. The command reads the configuration file for this and the trust store (`proxy.outbound.tls`); a registry with a private CA needs that CA in `ca_files`.

## Run it

Put the tokens in files that only you can read. They are never taken as arguments, so they do not show in the process list or the shell history:

```shell
umask 077
printf '%s' "$TROVE_TOKEN" > trove-token            # example
printf '%s' "$OLD_REGISTRY_TOKEN" > source-token     # example
```

Name the packages in a file, one per line (empty lines and lines starting with `#` are ignored):

```text
# the packages of the web team   (example)
@acme/ui
@acme/forms
left-pad
```

Look first without writing anything:

```shell
trove admin import npm \
  --from https://registry.old.example.com/ \
  --to https://trove.example.com/api/npm/web/npm-local/ \
  --packages-file packages.txt \
  --source-auth bearer --source-password-file source-token \
  --token-file trove-token \
  --dry-run                                           # example
```

The `--to` address is the repository's address as `npm config set registry` takes it. For NuGet, `--from` and `--to` are the service indexes, `.../v3/index.json`. For Ansible Galaxy, `--from` is the API root of the old Galaxy and `--to` the address of the repository, [see below](#ansible-galaxy).

Then run it, with a state file and a report:

```shell
trove admin import npm ... --state import.state --report import-report.json
```

| Flag | What it does | Default |
|---|---|---|
| `--package <name>` | A package to copy; repeat it for several | none |
| `--packages-file <file>` | Names, one per line | none |
| `--search <text>` | Also copy what the old registry's search finds for the text. npm: `GET -/v1/search`; NuGet: the search resource; Ansible Galaxy: the collection index, filtered on `<namespace>.<name>` (`--search ''` copies every collection). A registry without the endpoint finds nothing, and only the named packages are copied. A NuGet package whose versions are all unlisted is not found by a search; name it | not given |
| `--source-auth none\|basic\|bearer\|token` | How the old registry is asked; `token` is the API token of a Galaxy server (galaxy_ng), sent as `Authorization: Token` | `none` |
| `--source-username <name>` | The account for `basic` | none |
| `--source-password-file <file>` | The password (`basic`) or token (`bearer`, `token`) of the old registry; the environment variable `TROVE_IMPORT_SOURCE_PASSWORD` works too | none |
| `--source-extra-host <pattern>` | A host, besides the old registry's own, that serves its files (a CDN, an object store); a host name or a pattern such as `*.cdn.example.com`. Repeat it. The file requests to such a host carry no credential | none |
| `--token-file <file>` | The trove token; the environment variable `TROVE_IMPORT_TOKEN` works too | none |
| `--state <file>` | Remember the packages that are complete (see [Resuming](#resuming-a-cut-run)) | none |
| `--report <file>` | Write the report as JSON (mode 0600) | none |
| `--concurrency <n>` | Packages in flight | `2` |
| `--rate <n>` | Requests per second to the old registry; `0` for no limit | `5` |
| `--max-package-bytes <n>` | The largest file taken from the old registry; larger files fail with `too_large` | `536870912` |
| `--spool-dir <dir>` | Where a file waits between its download and its publish | the temporary directory |
| `--dry-run` | Read both sides, report, download and write nothing | off |

In a Kubernetes Job, use the image of trove and the same configuration as the other pods (`--config`), mount the two tokens from Secrets as files, and keep the state file on a volume that survives a restart of the Job.

## What is copied, and what is not

| From the old registry | Copied? |
|---|---|
| Every version of the named packages, as the files the registry serves | Yes |
| npm: the manifest (`package.json`) of each version | Yes |
| npm: the `readme` of the version | Yes, where the registry's version document has one |
| npm: dist-tags (`latest`, `next`, ...) | Yes, set after the versions are in, `latest` last. A tag that points to a version that was not copied is reported and skipped |
| NuGet: the metadata in the `.nuspec` | Yes, trove reads it from the package |
| NuGet: unlisted versions | The versions are copied. The unlisted state is set again only if the trove repository's delete mode is `unlist` **and** your token may read the repository's configuration (an Owner of the project, or `repositories:read-config`). In a repository with delete mode `delete` the version stays listed, because the request that unlists would remove it for good. The report tells you which case you are in |
| The time a version was published | **No.** trove sets the time of each publish itself. The versions are published in the order of the old registry's publication times, so their order among themselves stays; their dates are the date of the import. The time is not copied on purpose: it is evidence, and no client may set it |
| npm: deprecation messages | **No.** trove cannot set a deprecation yet. The report lists the deprecated versions under "not kept" |
| NuGet: deprecation | **No.** trove stores none for a local package |
| npm: provenance attestations | **No**, listed in the report |
| Ansible Galaxy: the collection archives, with the manifest and the README they hold | Yes |
| Ansible Galaxy: roles | **No.** A trove local repository has no role API. The report names the number of roles the old Galaxy counts, once, with the first collection. Publish a role you need as a collection (`roles/` directory of a collection) |
| Ansible Galaxy: the deprecation flag, signatures, namespace metadata | **No** (the deprecation flag is reported) |
| Owners, maintainers, download counts, the old registry's users and tokens | **No.** The user of the token owns the names it publishes, by the rules of the repository ([Permissions](permissions.md)) |

## How a version is checked

For each version the command takes the strongest digest the old registry announces (npm: the strongest entry of `dist.integrity`, else `dist.shasum`; NuGet: the `packageHash` of the registration; Ansible Galaxy: the `artifact.sha256` of the version document) and compares it with the digest of the bytes it downloaded.

- **The digest does not match:** the version fails with `digest_mismatch`, the file is thrown away and nothing is sent to trove.
- **The registry announces no digest:** the version fails with `no_digest`. There is no switch to import it anyway. Publish that version by hand if you need it.
- **The package says it is something else** than the registry listed (an npm tarball whose `package.json` has another name or version): `identity_mismatch`, not sent.

Then trove checks it again as it does for any publish. The command never overwrites: if trove already holds the version with other content, the version is a **conflict** and nothing is written.

## The report

While it runs, the command prints one line per package to the error output. At the end it prints, to the standard output, one line for every conflict and failure, the states that were not kept, and the totals:

```text
conflict @acme/ui@1.0.0: version_exists the target holds the version with sha512:09f42ffd7d1156f8, the source with sha512:51ffab7e2337e4a4
failed left-pad: source_error the source holds no version of the package
not kept @acme/forms: deprecation of 0.9.0 (trove has no way to set a deprecation yet)
3 packages, 41 imported, 2 skipped, 1 conflict; 2 packages need a look
```

| Outcome | Meaning |
|---|---|
| `imported` | trove did not hold the version; it was published |
| `skipped` | trove holds the version with the identical content |
| `conflict` | trove holds the version with other content. Decide by hand which is right; the import never decides |
| `failed` | not copied. The code says why |
| `would_import` | a dry run only |

| Code | Meaning and what to do |
|---|---|
| `digest_mismatch` | The file is not the one the registry announced. Check the registry and the network; try again |
| `no_digest` | The registry announced no digest |
| `foreign_host` | The file is on a host that is neither the registry nor a `--source-extra-host`. Add the host if you trust it |
| `identity_mismatch` | The package is not the one the registry listed |
| `too_large` | The file is over `--max-package-bytes` |
| `source_error` | The old registry did not answer as a registry does, or does not hold the package |
| `forbidden` | trove refused the publish with 403: the token may not publish this package name or path |
| `target_refused` | trove refused in another way; the detail has the status and trove's code (`413`, `400 npm_invalid_name`, ...) |
| `invalid_name` | The name is not a package name |
| `state_not_set` | The versions are in, but a dist-tag or the listing could not be set |

The exit status is `0` when every package is complete, `1` when something needs a look or the run was stopped, and `2` for a wrong command line. A failure of one package does not stop the others. Two answers stop the whole run, because no other package can get around them: trove does not accept the token (401), and trove is full (507, the storage quota or capacity). The report says which.

Every imported version is an ordinary publish in the audit log: the event `npm.publish` (npm) or `package.published` (NuGet and Ansible Galaxy), with the user or service account of the token as the actor.

## Ansible Galaxy

The command copies the **collections** of an existing Galaxy (the public one, `galaxy_ng`, Pulp-based or Automation Hub) into a local repository of the format Ansible Galaxy. It speaks the Galaxy API v3 that `ansible-galaxy` speaks.

```shell
trove admin import ansible \
  --from https://galaxy.old.example.com/api/ \
  --to https://trove.example.com/api/ansible/web/ansible-local/ \
  --package acme.tools --package acme.base \
  --source-auth token --source-password-file source-token \
  --token-file trove-token --dry-run                    # example
```

- **`--from` is the API root** of the old Galaxy, the address `ansible-galaxy` has as `url=` in its `ansible.cfg`, with a trailing slash. The command asks it for `v3/collections/…`. A Galaxy that serves its collections below a prefix (`content/<repository>/`, as some Automation Hubs do) is given with the prefix: `--from https://hub.example.com/api/galaxy/content/published/`. The command does not guess. **Run `--dry-run` first**: it reads the version lists and documents of the old Galaxy and the repository of trove and tells you what it would copy, so a Galaxy that answers differently shows up before anything is written.
- **`--to` is the repository's URL** as `ansible-galaxy` takes it. The command reads the discovery document there first: it must be a trove repository (its description starts with `trove`) that lists `v3`.
- **Names** are `<namespace>.<name>`, folded to lower case. A name the target does not accept (trove's grammar is `[a-z0-9_]`, 1 to 64 characters, not starting with `_`, with a letter) is `invalid_name`. `--search <text>` lists the index of the old Galaxy and keeps the names that contain the text; without `--package`, `--packages-file` and `--search` the command is refused, and `--search ''` copies everything.
- **The road** is the one of `ansible-galaxy collection publish`: a `POST` of the archive and its SHA-256 to `v3/artifacts/collections/` with the token as `Authorization: Bearer`, then the import task is read until it is finished. A task that failed is `target_refused` with the code of the task (`invalid_archive`, `version_exists`, …).
- **Digests.** The old Galaxy's `artifact.sha256` is the digest the file is compared with before a byte is sent. A version document without it is `no_digest`.
- **Versions** are copied in the order the old Galaxy published them. Their dates are the date of the import.
- **What the report names under "not kept":** the number of roles of the old Galaxy (its `v1` API, asked once, when it answers), and a collection the old Galaxy marks as deprecated.
- **The first real source.** The command was written against the documents of the public Galaxy and the counting stub of the tests, not against your Galaxy. If a dry run shows something odd (versions missing, `source_error`), send the output of `--dry-run --report` and the API root; do not work around it by hand before that.

## Running it again

Running the same command again is safe. A version that is stored with the identical content is `skipped`, and nothing is written for it: no row and no audit event. A second run is the way to finish a package after you fixed the cause of a failure.

## Resuming a cut run

An import of tens of thousands of versions survives a stop, a restart of the pod it ran in and a drain of the node. Run the same command again. trove is the record of what is done: a version that was published is found and skipped, a version that was cut in transit was never stored and is published now. Nothing is published twice and nothing is lost.

`--state <file>` only makes this cheaper. After a package is complete, the command appends it to the file and flushes it. A run started again with the file does not read those packages again, and its report marks them `resumed`. The file holds names, versions and outcomes, and no credential. It belongs to one import: a file written for another source or another target is refused. A package that gets new versions in the old registry after it was recorded is not read again with that file; use a new file for a new import.

`SIGINT` and `SIGTERM` stop the run: the request in flight is cancelled, the packages in progress are reported as stopped, and the command exits with status `1` and the report of what was done.

## Being polite to the old registry

The command sends at most `--rate` requests per second to the old registry (five by default), over all packages together, and works on `--concurrency` packages at a time (two by default). When the registry answers `429` or `503`, the command waits as long as the answer's `Retry-After` says, but never more than two minutes, tries each request at most five times, and **halves its rate** for the rest of the run. A run against a registry that is small or shared should use a lower `--rate`.

## Security notes

- The token and the old registry's password are read from files or the environment, never from arguments, and are written nowhere. The old registry's credential goes to its own address only, not to a redirect target and not to an extra host.
- The token should be able to do what the import needs and no more: a service account with `deploy` on the target repository. The command can publish every name the token may publish.
- The addresses of both registries are checked like the upstream of a remote repository, and the egress address policy applies. Do not add an `allow_cidrs` entry for more than the old registry needs.
- The state file and the report are written with mode 0600; they hold package names, which can be sensitive.
