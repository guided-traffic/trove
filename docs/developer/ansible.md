# Ansible Galaxy repositories

The Ansible Galaxy format (`internal/formats/ansible`) serves collections to `ansible-galaxy` from local, remote and virtual repositories,
and roles by translation from a remote. This page is for a contributor to the format: the address and the routes, the files, the leaf
packages, the schema, the scheme `Token` of the authentication stage and the data port. The specification is
[formats/ansible.md](../spec/formats/ansible.md); the plan and its cut into work packages are
[ansible-work-packages.md](../projektplan/ansible-work-packages.md); the decision for the contract is
[ADR-191](../decisions.md#adr-191--the-format-contract-gains-the-route-flag-for-the-galaxy-token-scheme-and-the-hub-token-exchange-is-an-optional-interface-of-the-remote-adapter).

What is built today is the **skeleton and the leaves** (work package WP0): the format registers with the local class, its route table, the
grammars, the classification of paths, the Galaxy error shape, the settings schema, the data port with its tables, the management
operations of the OpenAPI document, the scheme `Token`, and the leaf packages with their tests. Every Galaxy route and every management
operation answers 501 `ansible_not_implemented` until the class that serves it is built. [What is built and what is not](#what-is-built-and-what-is-not)
says which.

## What has to be in your head first

- **One format, `ansible`**, at `/api/ansible/<projectKey>/<repoKey>/` (Q-ANS-1). The repository URL itself is what a client is configured
  with: it answers the discovery document, and every other route is below it (`v3/…`, `v1/…`, Q-ANS-3).
- **Collections are the stored artifact; roles exist by translation** on remote and virtual repositories only (Q-ANS-6). A local repository
  lists `v3` alone in its discovery document and has no `v1` API; a request below `v1/` answers 404 in the `v1` shape with the sentence that
  says why ([errors.go](../../internal/formats/ansible/errors.go)).
- **The route table is complete and frozen** ([routes.go](../../internal/formats/ansible/routes.go)): ten routes, each with the classes that
  serve it. The table grows with the classes registered by `class_*.go` files (a route none of whose classes is registered is left out), and
  a route that several classes serve is one route whose handler decides by class: `Match` does not know the class (Q-DEB-45). A handler is
  registered by class and route name ([handlers.go](../../internal/formats/ansible/handlers.go)); one that no file registered answers 501.
- **Every route declares `TokenScheme`.** `ansible-galaxy` sends a configured token as `Authorization: Token <token>`; the stage reads that
  scheme only on a route that declares it, from the dispatcher's own match, and nowhere else (ADR-191, Q-ANS-4).
- **A version is stored and served as written; it is compared by semantic-version precedence with build metadata ignored.** The data port
  orders a list by `sort_key`, a byte string whose memcmp order is the precedence ([version](../../internal/formats/ansible/version/version.go)).
- **The documents are rendered from one neutral form** ([galaxydoc](../../internal/formats/ansible/galaxydoc/galaxydoc.go)): a database row, a
  document an upstream sent and a merged member document all become a `Version` and are rendered with the repository's own addresses, so
  the three classes serve the same bytes for the same version, and nothing of the upstream's addresses reaches a client.
- **An archive is read once, as a stream** ([collection](../../internal/formats/ansible/collection/archive.go)): every member goes through a
  digest, `MANIFEST.json`, `FILES.json` and `meta/runtime.yml` are kept, and the archive is accepted only when it is consistent. It is never
  extracted. The bounds are the core's (Q-ANS-30).

## The files

```
internal/formats/ansible/
├── format.go             # Descriptor, class registry (registerClass), New
├── routes.go, match.go   # the route table and the matchers of the ten protocol shapes
├── handlers.go           # registerHandler(class, route, build); the 501 of a route without one
├── paths.go              # Classify, canonical paths, PackageDocumentPath, VersionPath, CanonicalPath
├── names.go              # the package-name rule (Normalize)
├── errors.go             # WriteError: errors[] below v3/ and at the root, {"default"} below v1/
├── inspect.go            # Inspect: the archive reader and the path/identity check
├── store.go              # the data port: Store = LocalStore + RemoteStore
├── settings.go, settings.schema.json  # the ansible member of the repository document (authUrl, clientId)
├── class_local.go        # the local class registers itself
├── rows/                 # leaf: the values that cross the data port, the CollectionUUID
├── version/              # leaf: semver 2.0.0, Compare, SortKey
├── collection/           # leaf: the archive reader (ReadArchive), the name rule, runtime.yml; collectiontest builds archives for tests
└── galaxydoc/            # leaf: Render*/ParseUpstream* of the Galaxy documents; testdata/ holds the golden documents
internal/formatdata/ansible/   # the data port: port.go, tables.go, jobs.go, local_store.go, remote_store.go
internal/auth/                 # stage.go, auth.go: the scheme Token (galaxy_token_test.go)
internal/api/dispatch/         # TokenSchemeRoute beside APIKeyRoute
internal/api/v1/ansible_*.go   # the management operations (501 until the local class)
internal/config/ansible.go     # the ansible: section
```

| File | Responsibility |
|---|---|
| `class_remote.go`, `handlers_remote.go`, `remote*.go`, `mirror.go`, `hub.go` | Not created yet: the remote class (work package B2) |
| `class_virtual.go`, `handlers_virtual.go`, `virtual*.go`, `merge/` | Not created yet: the virtual class (B3) |
| `setup*.go`, `guide*.go`, `guide/` | Not created yet: the setup templates and the guide (B4); the conformance fixture skips check 8 until then |

## The routes

| Route | Method and path (below the repository URL) | Kind | Action | Classes |
|---|---|---|---|---|
| `ansible.root.get` | `GET ` (empty) | Computed | read | all |
| `ansible.collection.get` | `GET v3/collections/{ns}/{name}/` | Metadata | read | all |
| `ansible.versions.list` | `GET v3/collections/{ns}/{name}/versions/` | Metadata | read | all |
| `ansible.version.get` | `GET v3/collections/{ns}/{name}/versions/{version}/` | Metadata | read | all |
| `ansible.artifact.get` | `GET v3/artifacts/collections/{file}` | Download | read | all |
| `ansible.collection.publish` | `POST v3/artifacts/collections/` | Upload | deploy | local, virtual |
| `ansible.import.get` | `GET v3/imports/collections/{id}/` | Metadata | read | local, virtual |
| `ansible.roles.find` | `GET v1/roles/` | Volatile | read | remote, virtual |
| `ansible.role.versions` | `GET v1/roles/{id}/versions/` | Metadata | read | remote, virtual |
| `ansible.role.archive` | `GET v1/roles/archives/{id}/{file}` | Download | read | remote, virtual |

The trailing slash is optional on every path and no route redirects: a redirect would drop the `Authorization` header of some clients.

## Paths and names

A protocol path is what a client requests; a canonical path is where the item lives (`collections/<ns>/<name>/<ns>-<name>-<version>.tar.gz`,
and for a remote the stored documents beside it: `collection.json`, `versions.json`, `<version>.json`, `api-root.json`, `roles/…`).
`Classify` maps a name or a version that does not parse to metadata of no version, so it costs a refetch and never freezes a list. The
package name is `<ns>.<name>`, each part `[a-z0-9_]{1,64}`, not starting with `_`, with a letter. `Names.Normalize` folds upper case (a remote may
serve what the client's grammar `[a-zA-Z0-9_]` accepts) and a publish refuses it (`invalid_name`). `classifyRole` in [paths.go](../../internal/formats/ansible/paths.go)
is the one place the remote class's work package may refine when it settles whether the name machinery takes `role/<id>` (R-ANS-020).

## Errors

`WriteError` renders `{"errors":[{"status","code","title","detail"}]}` below `v3/` and at the root and `{"default": …}` below `v1/`. The code is the
registry code without the prefix: `ansible_version_exists` is `version_exists`; `unauthenticated` is `not_authenticated`, `forbidden` is
`permission_denied`, `malformed_request` is `invalid_request`, and a 502 or 503 of an upstream is `upstream_unavailable` (Q-ANS-29). The codes
are in [codes.go](../../internal/errs/codes.go) (`CodeAnsible…`), the table that holds their kinds is the registry there.

## The scheme Token

`formats.Route.TokenScheme` marks a route that takes `Authorization: Token <trove token>`; `auth.EndpointRules.TokenSchemeRoute` reports
whether the request reaches one, answered by `Dispatcher.TokenSchemeRoute` (the same match as `APIKeyRoute`, [dispatcher.go](../../internal/api/dispatch/dispatcher.go));
`credentialHeader` in [stage.go](../../internal/auth/stage.go) returns the secret with `MethodGalaxyToken` (`galaxy_token`, the label of
the request log, of `trove_auth_attempts_total` and of the audit event) only then. The stage asks the route at most once and only for a
header with that scheme. Everywhere else the header is "not a trove token" and is rejected without a lookup. The tests are in
[galaxy_token_test.go](../../internal/auth/galaxy_token_test.go).

## The schema and the data port

[00068_ansible_collections](../../internal/meta/postgres/migrations/00068_ansible_collections.sql) and
[00069_ansible_import_tasks](../../internal/meta/postgres/migrations/00069_ansible_import_tasks.sql) create the tables of R-ANS-011; the
table of the announced digests of a remote comes with the remote class. The port is `Store` in [store.go](../../internal/formats/ansible/store.go),
implemented by [internal/formatdata/ansible](../../internal/formatdata/ansible/port.go). It speaks repository ids and the values of the `rows` leaf
(Q-ANS-33), carries no revision column (Q-ANS-31), and its reindex step is `RebuildRows`, the enqueue being the `Reindexer` the endpoint
already finds (Q-ANS-32). The job kinds `ansible-reindex` and `ansible-import-task-sweep` are registered in [jobs.go](../../internal/formatdata/ansible/jobs.go).

## Tests

| Tier | Where | What |
|---|---|---|
| Unit | the leaf packages | semver precedence against a reference and fuzz; the archive reader against crafted archives and fuzz (`collectiontest` builds them); the documents against golden files (`go test ./internal/formats/ansible/galaxydoc -update` rewrites them) |
| Format | `internal/formats/ansible` | the descriptor, the route table, the matchers, `Classify`, the error shapes, the settings, the conformance kit over the skeleton |
| Stage | `internal/auth`, `internal/api/dispatch` | the scheme with and without the declaration, the metric label, the cookie; the dispatcher's answer to the stage ([tokenscheme_test.go](../../internal/api/dispatch/tokenscheme_test.go)) |

## What is built and what is not

Built: the skeleton, the leaves, the scheme, the tables and the data port as stubs. Not built yet: the local class (publish, import tasks,
the three read operations, tombstones, the reindex, the screens' adapter), the remote class, the virtual class, the setup and the guide, the
importer. The management operations and every Galaxy route answer 501 until their class lands.
