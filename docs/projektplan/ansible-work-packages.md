# F0 · Ansible Galaxy registry — the work-package cut

The plan of unit F0 of block B ([FULL.md](FULL.md), the checklist under "F0 · Ansible Galaxy"), written by the planning agent on 2026-11-10.
Spec: [docs/spec/formats/ansible.md](../spec/formats/ansible.md), requirements **R-ANS-001 to R-ANS-032**, decisions **Q-ANS-1 to
Q-ANS-28**, open gaps **H-839 to H-848** (H-850 was taken by the remote class; H-849 was taken and is closed), **ADR-191**. Migrations are numbered at build time by the development agent
from the next free number on `main`; this plan names them by purpose. Reserved for the edits of other pages, recorded there by the
package that applies them: the "Changes to other pages" table of ansible.md.

The plan is cut for **at most four builders, each owning one whole feature end to end** (code, migration, OpenAPI, tests, the docs of
that feature), not a list of small packages that share files. What a builder owns is named by directory; no two builders write the same
file, and the shared skeleton is written once by **WP0** (part of the first builder's work, landed first) and frozen.

Two facts about the base:

- The importer (`internal/importer`, `docs/spec/import.md`, `trove admin import`) is on `main` (commit 9c00846b) and **not** in the commit
  this plan was written on (b0b4538d). The builders start from the `main` of the day they start; the spec page names the importer in plain
  text for that reason and gets its links when it is merged.
- Nothing of the Galaxy format exists in the tree. The existing formats that serve as pattern are named per package below; a contract in
  this plan that the format kit already gives is **not** repeated (Descriptor, Routes/Match/Classify, Inspect/OnCommit, the remote
  adapter and its optional interfaces, the virtual adapter, the view adapter, setup templates and guide, the conformance kit).

## 0 · Reading the plan against the catalog

Full table: ansible.md, section "Catalog coverage". Summary:

- **Built:** PKG-ANSIBLE-001 to 009, 011, 012, 013; PKG-ANSIBLE-010 (the hub) is specified and built **last**, by the remote builder, against a
  stub realm only (H-842).
- **Stays (FULL.md, "Deferred" of ansible.md):** deletion/namespace/search routes of the Galaxy API, signing and signature verification,
  vulnerability evaluation, exposure rules and the Bastion, Git and URL roles, role publish, the federated form.
- **Beyond the catalog, owner-required:** the import of the existing Galaxy's content (FULL.md line 53; R-ANS-027) and a mirror option
  (R-ANS-021).

**FULL.md block B status text** (written by the closing change, not now; the development agent sets F0 to 🟡 when building starts):

```markdown
### F0 · Ansible Galaxy (replaces the existing Galaxy)
- 🟢 Spec page `formats/ansible.md` (one format `ansible`; collections stored, roles by translation, Q-ANS-1 and Q-ANS-6)
- 🟢 Local: publish (multipart, import tasks), install, immutability and tombstones, reindex
- 🟢 Remote: the public Galaxy (`pullThrough` and `mirror`), pages, digests, roles from GitHub archives; 🟡 the Automation Hub's token exchange (stub realm only)
- 🟢 Virtual: merge, first match, publish through the deploy target
- 🟢 Authentication with trove tokens (`Authorization: Token`, Basic, netrc for roles)
- 🟢 Import of the existing Galaxy's collections (`trove admin import ansible`)
- 🟢 Integration tests (stub, kit) and E2E tests (set `ansible`: ansible-core 2.17.14 and 2.21.6 through local, remote, mirror and virtual repositories on three replicas)
- 🟢 Client setup instructions in the UI
- ⚪ Stays: signing, vulnerability evaluation, exposure rules and the Bastion, Git and URL roles through a proxy, the Galaxy API's delete and namespace routes
```

## 1 · The cut: four builders

| Builder | Feature end to end | Requirements | Owns (directories and files) | Needs the stack | Needs kind |
|---|---|---|---|---|---|
| **B1 · Core and local class** (does WP0 first) | The format skeleton, the shared leaf packages, the `Token` scheme of the stage, the local class, the format's data port and tables, the reindex, the view | R-ANS-001 to R-ANS-013, R-ANS-024 (the target side), R-ANS-026 (API half), R-ANS-028 to R-ANS-031 | `internal/formats/ansible/` except the files B2, B3 and B4 own below; `internal/formats/ansible/{galaxydoc,collection,version}/` (leaves); `internal/formatdata/ansible/` except `remote_*`, `virtual_*`; `internal/auth/` (the scheme: `stage.go`, `auth.go`, tests); `internal/config/ansible.go`; the migrations; `queries/ansible_local.sql`; `internal/api/v1/ansible_*.go`; `api/openapi.yaml` (all Ansible operations and schemas); `test/integration/ansiblelocal/`; `docs/developer/ansible.md`; `docs/security/ansible.md` | yes | no |
| **B2 · Remote class, mirror, roles, the hub** | Discovery, paging, rewriting, digests, the archive fill, roles by translation, the mirror lister, the hub exchange, **the counting stub** | R-ANS-016 to R-ANS-021, R-ANS-032 (stub, remote integration) | `internal/formats/ansible/{class_remote,handlers_remote,remote,remote_gate,remote_fill,remote_roles,mirror,hub}*.go`; `internal/formatdata/ansible/remote_*.go`, `mirror_store.go`; `queries/ansible_remote.sql`; `internal/repository/validate_ansible.go`; `test/ansiblestub/` (the stub, its `cmd`, `Containerfile`, the corpus generator `ansiblegen/`); `test/integration/ansibleremote/`; the mirror rows of `test/integration/mirror/ansible_test.go`; Makefile region `ansible-stub-image`; `deploy/dev/ansiblestub.yaml`; `docs/developer/ansible-remote.md`; `docs/security/ansible-remote.md` | yes | no |
| **B3 · Virtual class and import** | The merge, first match, role ids by member, publish through the deploy target, the importer `Format` for Galaxy | R-ANS-022 to R-ANS-024, R-ANS-027 | `internal/formats/ansible/{class_virtual,handlers_virtual,virtual,virtual_gate,virtual_roles}*.go`; `internal/formats/ansible/merge/` (leaf: pure merge of version lists); `internal/importer/ansible.go` and its test; `test/integration/ansiblevirtual/`; `test/integration/importer/ansible_test.go`; `docs/developer/ansible-virtual.md`; `docs/manual/admin/import.md` (the Galaxy section); `docs/spec/import.md` Scope and format table | yes | no |
| **B4 · Client surface: setup, guide, UI, e2e** | The setup templates and guide, the UI entries and screens, the client images, the e2e set `ansible` and its CI leg, the user documentation | R-ANS-025, R-ANS-026 (UI half), R-ANS-028 and R-ANS-032 (e2e) | `internal/formats/ansible/{setup,guide}*.go`, `internal/formats/ansible/guide/*.md`; `web/src/app/features/setup/clients.ts`, the repository form's `ansible` settings and tabs (import tasks, tombstones), the package detail, `web/src/app/shared/format-icon.ts`, i18n units, specs; `test/e2e/ansible_*_test.go`; `test/e2e/ansibleclients/` (the two client images and their pin check); `deploy/dev/e2e.sh` (a region `>>> F0 ansible` and the set `ansible`), `deploy/dev/ansible-clients.yaml`; Makefile region `ansible-clients-image`; `.github/workflows/release.yml` (job `e2e-ansible`, name `e2e (ansible)`); `docs/manual/` user pages; `README.md` (naming tables, TL;DR row, documentation table); `docs/developer/{README,formats,e2e,build-and-test}.md` rows | no (journey only) | **yes** (the only kind user) |

Shared files with a rule instead of an owner: `docs/projektplan/FULL.md`, `BETA.md`, `promt.md` (the development agent), `docs/coverage/*`
(regenerated by `make coverage` in each builder's last commit), `docs/decisions.md` (each builder appends its rows; the rows of Q-ANS
exist already and only the ADR text of B2's hub interface is added by B2), `api/openapi.yaml` (B1 writes every Ansible operation in WP0 so
that no one else touches it).

## 2 · Contracts first: WP0 (B1, landed before B2 to B4 build on it)

WP0 is small: types, ports, schema, registrations, routes with a 501 behind them, the auth flag. It is committed and on the integration
branch before B2 to B4 start code that touches the format (their stub, their images and their importer unit work do not need it and
start at once; section 4). It writes **one ADR**, ADR-191, as decisions.md already holds it, and no more.

### 2.1 Additions to the contract (the only ones; all additive, `ContractVersion` stays 3, R-ARCH-025)

```go
// internal/formats/route.go
type Route struct {
	// ...existing fields...
	// TokenScheme marks a route that accepts "Authorization: Token <trove token>" (the Galaxy clients' scheme), examined by the
	// authentication stage only there, from the dispatcher's own match, like APIKey (Q-ANS-4, R-ANS-006).
	TokenScheme bool
}

// internal/auth/auth.go
type EndpointRules struct {
	// ...existing fields...
	// TokenSchemeRoute reports whether the request matches a route that declares TokenScheme; nil: none does.
	TokenSchemeRoute func(*http.Request) bool
}
const MethodGalaxyToken Method = "galaxy_token" // the label of the request log and of trove_auth_attempts_total (metricMethod)
```

`credentialHeader` (`internal/auth/stage.go`) returns the secret of a `Token` header with `MethodGalaxyToken` only when
`TokenSchemeRoute` says so; everywhere else the header stays "not a trove token". The composition root hands the dispatcher's match,
exactly as it does for `APIKeyRoute`. **Tests** (B1): the stage with and without the declaration, the session cookie ignored on such a
route when a header is present, the metric label, a token of the wrong type, a Basic user name ignored.

The **hub exchange** (R-ANS-019) is B2's own optional interface of the remote adapter (its shape is B2's decision; the existing
`Authenticator` hands the adapter the challenge and not the credential, so it cannot carry the offline token into a form body). It is
the last package of B2 and is specified in `ansible-remote.md`; until it lands the settings `ansible.authUrl` and `ansible.clientId` are
accepted by the schema and have no effect (the remote's status says so).

Nothing else is added: `Discoverer` (`api-root.json`), `FillInspector` (archive identity), `FactsGate` (the announced digest),
`MirrorLister`, `VersionPather`, `ViewAdapter`, `ServeFactsKeyer` and the `NameServer`-less shadowing probe (`PackageDocumentPath`) exist.

### 2.2 The format's packages and their ports (B1 writes the interfaces; each implementer is named)

```go
// internal/formats/ansible/galaxydoc (leaf; imports the standard library only; B1 writes it complete in WP0 because B2, B3 and B4 read it)
type VersionEntry struct{ Version, RequiresAnsible string; CreatedAt, UpdatedAt time.Time }
type Version struct {                      // the neutral form of one version document, from a row or from an upstream document
	Namespace, Name, Version, RequiresAnsible string
	SHA256 [32]byte; Size int64; Filename string
	Dependencies map[string]string; Info json.RawMessage // collection_info without the plugin listing
	Signatures []json.RawMessage           // opaque, passed through
	CreatedAt, UpdatedAt time.Time; CollectionID string
}
func ParseUpstreamVersion(doc []byte) (Version, upstreamURL string, err error)   // bounded; strips metadata.contents/docs_blob/manifest/files
func RenderVersion(v Version, a Addr) []byte                                       // download_url, hrefs on a.Repo
func RenderVersionList(page []VersionEntry, count, limit, offset int, a Addr) []byte
func RenderCollection(c CollectionSummary, a Addr) []byte
func ParseUpstreamList(doc []byte) (entries []VersionEntry, count int, next string, err error)
func RenderTask(t Task) []byte
type Addr struct{ Repo string } // "<base>/api/ansible/<project>/<repo>/", from formats.LinkBuilder

// internal/formats/ansible/version (leaf): Parse(string) (Semver, error); Compare(a, b Semver) int; SortKey(Semver) []byte
// internal/formats/ansible/collection (leaf): ReadArchive(r io.ReaderAt, size int64, lim untrusted.Limits) (*Archive, error)
//   Archive{ Namespace, Name, Version string; Info json.RawMessage; Dependencies map[string]string; RequiresAnsible string; Files int; ReadmeName string }
//   enforces R-ANS-010 and R-ANS-031; fuzz targets; the crafted corpus of the kit.
// internal/formats/ansible/merge (leaf, B3): MergeLists(parts []Part) []galaxydoc.VersionEntry with Conflicts []Conflict; MergeCollection(...)
```

```go
// internal/formats/ansible/store.go — the data port (formats.Descriptor.DataPort = reflect.TypeFor[Store](), Q-ARCH-193); frozen by WP0
type Store interface {
	LocalStore
	RemoteStore
}
type LocalStore interface {                 // implemented by internal/formatdata/ansible (B1)
	PutVersion(ctx context.Context, tx formats.Tx, v VersionRow) error                    // in OnCommit's transaction
	RemoveVersion(ctx context.Context, tx formats.Tx, repo formats.RepoRef, name, version string, tombstone bool, by string) error
	Version(ctx context.Context, repo formats.RepoRef, name, version string) (VersionRow, bool, error)
	Versions(ctx context.Context, repo formats.RepoRef, name string, limit, offset int) (rows []VersionRow, total int, err error) // semver descending
	Collection(ctx context.Context, repo formats.RepoRef, name string) (CollectionSummary, bool, error)
	Tombstoned(ctx context.Context, repo formats.RepoRef, name, version string) (sha256 []byte, ok bool, err error)
	ClearTombstone(ctx context.Context, repo formats.RepoRef, name, version, by string) error
	Tombstones(ctx context.Context, repo formats.RepoRef, after string, limit int) ([]Tombstone, error)
	CreateTask(ctx context.Context, tx formats.Tx, t Task) error                          // in the publish transaction (or alone for a failed task)
	Task(ctx context.Context, repo formats.RepoRef, id string) (Task, bool, error)
	Tasks(ctx context.Context, repo formats.RepoRef, state string, after string, limit int) ([]Task, error)
	Reindex(ctx context.Context, repo formats.RepoRef, reader ArchiveReader, checkpoint string) (next string, done bool, err error)
}
type RemoteStore interface {                // implemented by internal/formatdata/ansible (B2, files remote_*.go)
	RecordAnnounced(ctx context.Context, tx formats.Tx, repo formats.RepoRef, rows []Announced) error   // first digest wins, reports a changed one
	Announced(ctx context.Context, repo formats.RepoRef, name, version string) (Announced, bool, error)
	AnnouncedNames(ctx context.Context, repo formats.RepoRef, name string) ([]Announced, error)        // the virtual repository's cached-version set
}
```

`VersionRow`, `Task`, `Announced`, `Tombstone`, `CollectionSummary` are plain structs whose fields are the columns of R-ANS-011.

### 2.3 Schema (`internal/meta/postgres/migrations`; numbered at build time; names by purpose)

| Migration (by purpose) | Tables | Export class and registrations |
|---|---|---|
| the Galaxy collection versions | `ansible_collection_versions`, `ansible_tombstones` (R-ANS-011) | export class `data`; the revision column registered with `meta.RegisterRevisionColumn` if B1's reindex needs it (decided by B1); both in the restore check of R-BAK-022 |
| the Galaxy import tasks | `ansible_import_tasks` (R-ANS-011) | export class `data` (kept for the retention window; a restore drops nothing) |
| the digests a remote announces | `ansible_upstream_versions` (R-ANS-011, R-ANS-018) | export class `data`; **in the list a restore without remote caches empties** (R-BAK-008); B2 writes the migration file, B1's WP0 only reserves the registration in `internal/formatdata/ansible/tables.go` |

Indexes beyond the primary keys: `ansible_collection_versions (repo_id, name, sort_key DESC)` for the version list; `ansible_import_tasks
(repo_id, created_at DESC)` and `(created_at)` for the sweep. **Queries: one file per builder** (sqlc writes one output file per file;
`models.go` changes only with the migrations): `queries/ansible_local.sql` (B1), `ansible_remote.sql` (B2). B3 reads only through
the ports and has no query file.

### 2.4 Configuration, API and registrations

- Configuration (R-ARCH-020, `internal/config/ansible.go`, `ansible:` section; the chart passes `config:` through, so no new value and no
  new route, as for Debian): `max_archive_bytes` (1 GiB, 1 MiB to 16 GiB), `role_archive_max_bytes` (256 MiB, 1 MiB to 4 GiB),
  `import_task_keep` (720h, 24h to 8760h). `make generate` writes the schema and `docs/developer/configuration.md` is updated by B1.
- **Format settings schema** `internal/formats/ansible/settings.schema.json`: the member `ansible` of the repository document with
  `authUrl` (string, `https` URL, `x-trove-classes: ["remote"]`) and `clientId` (string, default `cloud-services`, remote); no setting for
  local and virtual. Unknown fields are refused (`additionalProperties: false`).
- **OpenAPI** (`api/openapi.yaml`, all written by B1 in WP0; the per-class enums follow the registry, `TestR_ARCH_010_PerClassFormatEnums`;
  `make generate` for the Go and TypeScript clients):

  | operationId | Method and path | Permission | Notes |
  |---|---|---|---|
  | `listAnsibleImportTasks` | `GET /api/v1/projects/{projectKey}/repositories/{repoKey}/ansible/imports` (`state`, `limit`, `cursor`) | `read` on the repository | 405 `not_applicable` for a remote; for a virtual repository the deploy target's tasks |
  | `listAnsibleTombstones` | `GET …/ansible/tombstones` (`limit`, `cursor`) | `manage` | local only |
  | `clearAnsibleTombstone` | `DELETE …/ansible/tombstones/{collection}/{version}` | `manage` | audit event `ansible.tombstone.cleared`, journaled |
  | (existing) `POST …/reindex` | R-REPO-016 | `manage` | the format's job `ansible-reindex` (local); virtual drops its cache; remote 405 |

  The Galaxy protocol routes are **not** OpenAPI operations (they are the format's route table, as for every format).
- **Routes** (frozen in `routes.go`, one handler per path shape that decides the class itself, as Q-DEB-45; an unregistered class handler
  answers 501 `ansible_not_implemented`). Names, templates relative to the repository URL, kind, action, classes, `TokenScheme`:

  | Route name | Method and template | Kind | Action | Classes |
  |---|---|---|---|---|
  | `ansible.root.get` | `GET ` (empty) | Metadata | read | all |
  | `ansible.collection.get` | `GET v3/collections/{ns}/{name}` | Metadata | read | all |
  | `ansible.versions.list` | `GET v3/collections/{ns}/{name}/versions` | Metadata | read | all |
  | `ansible.version.get` | `GET v3/collections/{ns}/{name}/versions/{version}` | Metadata | read | all |
  | `ansible.artifact.get` | `GET v3/artifacts/collections/{file}` | Download | read | all |
  | `ansible.collection.publish` | `POST v3/artifacts/collections` | Upload | deploy | local, virtual (remote: 405 in the handler) |
  | `ansible.import.get` | `GET v3/imports/collections/{id}` | Metadata | read | local, virtual |
  | `ansible.roles.find` | `GET v1/roles` | Volatile | read | remote, virtual |
  | `ansible.role.versions` | `GET v1/roles/{id}/versions` | Metadata | read | remote, virtual |
  | `ansible.role.archive` | `GET v1/roles/archives/{id}/{file}` | Download | read | remote, virtual |

  All ten set `TokenScheme: true`. The trailing slash is optional in the matcher (`Route.Match` for the three-segment shapes). The
  ambiguity check (kit check 1) covers `v3/collections/{ns}/{name}/versions` against `.../versions/{version}`.
- **Job kinds and tasks** (registered in `internal/formatdata/ansible/jobs.go` by WP0 with handlers owned by their packages):
  `ansible-reindex` (queue `index`, `coalesce`, key `index:<repository id>`; B1), `ansible-import-task-sweep` (periodic, queue
  `maintenance`, daily, `exclusive`; B1). The mirror's passes use the engine's job kind; B2 adds none.
- **Journal events** registered (journal flag, handlers in B1): `ansible.version.deleted`, `ansible.tombstone.cleared`; the other events and
  metrics of R-ANS-030 in the audit and metrics registries (B1 registers the shared ones; B2 and B3 their own series in their files).
- `go.mod`: **no new module** is planned (tar and gzip from the standard library; YAML for `meta/runtime.yml` is read with the YAML module
  already in `go.mod`, restricted to a scalar lookup; if it is not there, B1 decides and records it).

### 2.5 Skeleton of the format (files and their owners afterwards)

| File | Content in WP0 | Owner afterwards |
|---|---|---|
| `internal/formats/ansible/format.go` | the Descriptor of R-ANS-001 (`ChecksumAlwaysVerified`, `VerifyAnnouncedDigests`, `DefaultExtraHosts`, `MaxDocumentBytes`, `DataPort`, `NotifyAreas`), `Classes` from the registered class files | B1 (frozen) |
| `internal/formats/ansible/routes.go`, `handlers.go` | the ten routes above; `handlers.go` looks up `classHandlers[class][routeName]` filled by the `class_*.go` files | B1 (frozen) |
| `internal/formats/ansible/paths.go` | `Classify`, the canonical paths, `PackageDocumentPath`, `VersionPath`, `Names` with complete unit tests | B1 (frozen) |
| `internal/formats/ansible/errors.go` | `WriteError` for both shapes (`errors[]`, `default`) with every code of R-ANS-007 | B1 (frozen) |
| `internal/formats/ansible/store.go` | the ports of 2.2 | B1 (frozen) |
| `internal/formats/ansible/{galaxydoc,version,collection}/` | complete leaf packages with tests | B1 |
| `internal/formats/ansible/class_local.go`, `handlers_local.go`, `hooks.go`, `inspect.go`, `view.go`, `servecheck.go` | stubs | B1 |
| `internal/formats/ansible/class_remote.go`, `handlers_remote.go`, `remote*.go`, `mirror.go`, `hub.go` | not created by WP0 | B2 creates them |
| `internal/formats/ansible/class_virtual.go`, `handlers_virtual.go`, `virtual*.go`, `merge/` | not created by WP0 | B3 creates them |
| `internal/formats/ansible/{setup,guide}*.go`, `guide/` | `SetupTemplates()` and `Guide()` returning the minimal set the kit accepts | B4 |
| `internal/formatdata/ansible/{port.go,tables.go,jobs.go}` | the composed port, the table registrations, the job kinds | B1 (frozen) |
| `internal/formatdata/ansible/{local_store,view_store,journal}.go` | stubs | B1 |
| `internal/formatdata/ansible/{remote_store,mirror_store}.go` | stubs | B2 |
| `internal/importer/ansible.go` | not created by WP0 | B3 |
| `internal/auth/{stage.go,auth.go}` | the scheme (2.1) with its tests | B1 |

Registered in `internal/formats/all/all.go`; `internal/app/wire_formats.go` binds the data port (B1); the archtest layer table gains the
leaves `internal/formats/ansible/{galaxydoc,version,collection,merge}`; the conformance kit is green with the local class before the
other classes exist.

### 2.6 Spec edits of WP0 (same change)

The "Changes to other pages" rows for architecture.md (R-ARCH-008 Go block: `Route.TokenScheme`; `testdata/contract.txt`; the layer table),
identity-and-authentication.md (R-AUTH-001, R-AUTH-033), authorization.md (R-AUTHZ-007, the Ansible table), repositories-and-proxy.md
(R-REPO-003, R-REPO-016), api-and-ui.md (the three operations), endpoints.md (R-EP-018) and operations-and-observability.md (events,
metrics, job kinds). ansible.md itself is already written; WP0 changes it only where the build finds a claim wrong, in the same change.

**Tests of WP0:** the contract spec test, the surface test, `TestR_ARCH_010_*` (kit with the local class, enums), `Classify`'s table, the
migration test (export classes, the revision columns), the stage tests of 2.1, `make generate-check`, the lint.

## 3 · What each builder builds (order inside the feature)

Every item names the requirement; tests are named `TestR_ANS_nnn_…` and prove only that criterion.

### B1 · Core and local class

1. **WP0** (2.1 to 2.6), then the leaf packages: `version` (semver 2.0.0 precedence and the sort key, a table of pairs), `collection`
   (R-ANS-010, R-ANS-031: crafted archives, fuzz target), `galaxydoc` (R-ANS-004: rendering against golden documents that are
   byte-checked against the public Galaxy's shapes recorded in the stub corpus).
2. **Local class**: the publish handler (R-ANS-008: the multipart reader in both part orders on the pattern of `nuget/handlers_publish.go`,
   `Spool` without `Expect`, the SHA-256 comparison, `Inspect`, `Authorize`/`CheckOwnership`/shadowing check, `Commit`), the import task
   (R-ANS-009) in the commit transaction, the `/files/` upload with the path/identity check, `OnCommit` and `OnItemsRemoved`
   (R-ANS-011, R-ANS-012 with the journal), the three read operations and the download for the local class, `WriteError`, the view
   adapter (`ViewAdapter`, README by ranged reads), `VersionPather`.
3. **The scheme** in the stage (2.1) and `TestR_AUTHZ_007_AnsibleActionMap`.
4. **Reindex** (`ansible-reindex`, R-ANS-012), the task sweep, the management operations of 2.4, the config section, the metrics/events.
5. **Integration `ansiblelocal`**: publish over HTTP with the counting of rows, both part orders, task polling, idempotent retry,
   `version_exists`, tombstones and their journaled replay (R-ANS-029), `Token`/Basic/Bearer/anonymous, permissions, ownership and the
   shadowing refusal (without remote members: the claim path), `/files/` upload, error shapes, ranges, Valkey stopped (R-ANS-028),
   reindex after corrupting a row, the kit's conformance run for the local class.
6. Docs: `docs/developer/ansible.md` (layout tree, packages and files, the route table, the schema, how to add a Galaxy field),
   `docs/security/ansible.md` (the credentials, the archive reader, immutability; its `What this does not cover` links H-841, H-843,
   H-845), `docs/developer/configuration.md`.

### B2 · Remote class, mirror, roles, the hub, the stub

1. **The stub first** (it unblocks B3 and B4): `test/ansiblestub` in the style of `test/debstub` (counts, control API, modes, the
   corpus generator with a tar writer, the GitHub stand-in on a second host name, the realm for the hub), its image and manifest. Unit tests
   of the stub itself; a short `docs/developer/ansible-remote.md` section on how to drive it.
2. **Remote class**: `Discoverer` (`api-root.json`), `UpstreamRequest`, `AcceptResponse`, `PrepareFill` (identity checks, bounds, the URL map
   row of the archive), `OnFill` (`RecordAnnounced`, first digest wins), `Rewrite` (R-ANS-017), the page assembly for the version list
   (Q-ANS-12), `BeforeArtifact`/`FactsGate` (R-ANS-018), `FillInspector`, the shadowing probe, `validate_ansible.go` (the policies `ignore`
   and `pass-through` refused, `authUrl` rules), negative entries, the upstream test of an API root.
3. **Roles** (R-ANS-020): the lookup as a query, the versions documents, the archive pull through the URL map, the dropped versions and the
   metric; the open point on the package name `role/<id>` is settled here first with a unit test of the core's name machinery.
4. **Mirror** (R-ANS-021): `MirrorLister` over the index, the plan of a name, the rows in the gate; `test/integration/mirror/ansible_test.go`.
5. **The hub exchange** (R-ANS-019), last: the optional interface, the replica cache of the access token, the refusal reasons, the tests
   against the stub's realm; records the interface in architecture.md R-ARCH-008 and in ADR-191's consequences.
6. **Integration `ansibleremote`**: one request per document however many clients (three replicas, concurrent), paging with 250 versions,
   the changed and the wrong digest, the identity mismatch of a fetched archive, the role flow with netrc-less and netrc requests,
   the dropped version, offline, a mirror pass with a rate limit, the hub exchange, the kit's remote fixture.
7. Docs: `docs/developer/ansible-remote.md`, `docs/security/ansible-remote.md` (the host rule, the digest rule, roles from GitHub, the hub
   token; links H-839, H-840, H-842, H-844, H-846, H-848).

### B3 · Virtual class and import

1. **Merge leaf and virtual adapter** (R-ANS-022, R-ANS-023): `merge/` pure on fixtures first; `VirtualAdapter` (`Strategy`, `Merge`,
   `LocalDocument`, `ArtifactPrefix` = `collections/<ns>/<name>/`), the remote member's pages read through the member (the NuGet pattern,
   Q-NUGET-82), the conflict counter, role ids by member (Q-ANS-19), the discovery document's `v1` rule, the read gate.
2. **Publish through the deploy target** (R-ANS-024): `DeployTarget`, the task lookup in the target, the shadowing check with remote
   members; the `/files/` upload needs no code.
3. **Importer** (R-ANS-027): `internal/importer/ansible.go` implementing `Format` for Galaxy (collections only), registered in the
   importer's format table; unit tests over the stub (plan, held, publish, the failed task, `digest_mismatch`, `no_digest`); the first
   `--dry-run` against the owner's real source is a manual step for the owner (the plan cannot do it) whose result is recorded in
   H-847; `docs/spec/import.md` and `docs/manual/admin/import.md` gain the Galaxy rows.
4. **Integration `ansiblevirtual`** (on the stack after B1 and B2 are merged; against fixtures before): the merge order, the conflict
   counter, claims and pins, the shadowing check, publish through the target and polling through the virtual address, role ids by member,
   a member the caller may not read, the partial merge with the stub down, the kit's merge fixture; `importer/ansible_test.go`.
5. Docs: `docs/developer/ansible-virtual.md`.

### B4 · Client surface and e2e

1. **Setup and guide** (R-ANS-025): the six templates of the spec per class, the guide sections and topics (kit check 8 green), the
   `Descriptor.Clients` entry; the netrc and environment-variable forms for PowerShell and cmd.
2. **UI** (R-ANS-026): `FORMAT_CLIENTS.ansible = [{ id: 'ansible-galaxy', label: 'Ansible Galaxy (ansible-galaxy)' }]`, the snippet
   explanations, the repository form's tabs for import tasks and tombstones (lazy, local and virtual; remote has neither), the package
   detail's dependency and `requires_ansible` block, i18n, Vitest specs, the web lint; one browser journey step.
3. **Client images** `test/e2e/ansibleclients`: two images pinning ansible-core **2.17.14** and **2.21.6** (their Python versions as each
   requires), a pin check like `aptclients/check-pins.mjs`, an entrypoint that runs a command in a clean home with the given `ansible.cfg`
   and environment; client pods in a namespace `trove-e2e-ansible`; Makefile `ansible-clients-image`.
4. **The e2e set `ansible`** (`deploy/dev/e2e.sh` set `ansible` with `TROVE_E2E_ANSIBLESTUB=1`; CI job `e2e-ansible` with the display name
   `e2e (ansible)` on the large class): `TestAnsibleLocal` (build a collection in the pod with `ansible-galaxy collection init`/`build`,
   publish on replica 1, install through replicas 2 and 3, both client images), `TestAnsibleRemote` (install and a `requirements.yml`
   with a collection and a role; the stub's counts), `TestAnsibleMirror` (a pass, then installs with the stub's counts unchanged),
   `TestAnsibleVirtual` (one list of local and remote versions, publish through the target, `server_list` of two servers),
   `TestAnsibleAuth` (token in `ansible.cfg`, token from the environment, netrc for a private role, a wrong token, no token),
   `TestAnsibleDigest` (the stub changes a digest; the client fails), `TestAnsibleSnippets` (the rendered snippets are executed in the
   pod), and `TestAnsibleClosing` (Valkey stopped; dispatch input `closing`). The set runs in the `scatter` profile.
5. **User documentation**: `docs/manual/` pages for the repository classes, the client setup and the import; README naming tables
   (routes, settings, properties, config keys, job kinds, events, metrics, the `ANSIBLE_GALAXY_SERVER_TROVE_TOKEN` and `~/.netrc` lines),
   TL;DR row; developer index rows.

## 4 · Order and waves

| Wave | Starts when | B1 | B2 | B3 | B4 |
|---|---|---|---|---|---|
| 0 | at once | **WP0** and the leaf packages | the stub (needs no format code) | merge leaf on fixtures; importer unit work against the stub when it lands | client images, pin check, e2e harness skeleton |
| 1 | WP0 on the integration branch | local class, scheme, integration `ansiblelocal` | remote class, roles, integration `ansibleremote` | virtual adapter on fixtures; publish through the target as B1 lands | setup, guide, UI; e2e `Local` and `Auth` when B1 is on the branch |
| 2 | B1 and B2 on the integration branch | reindex, sweep, docs | mirror, then the hub exchange | integration `ansiblevirtual`, the importer integration, docs | e2e `Remote`, `Mirror`, `Virtual`, `Digest`, `Snippets`, the CI job, user docs |
| 3 | all merged | — | — | — | the closing run (`closing`), the coverage record, FULL.md text (written by the development agent) |

The kind cluster is used by B4 only, one run at a time; the integration stack by B1 to B3 through the slot wrapper, one run per touched
area. **Merge order:** WP0, then B1, B2, B3 and B4 as they finish (disjoint files), the development agent resolving `docs/decisions.md`,
`api/openapi.yaml` (B1 only) and the coverage files.

## 5 · Checks per builder (before each commit)

`go test` of every touched package (Makefile flags), `go test ./internal/archtest/... ./internal/tools/coverage/`, the integration
areas the builder touched, the lint (`/Users/hfi/repos/trove/.bin/golangci-lint-v2.15.0-go1.27.3 run --allow-parallel-runners ./...`),
`make cyclo`, `make generate-check`, `go run ./internal/tools/coverage -links`, the white-label scan of the common brief, and for B4 the
web unit tests and the web lint. A requirement ID appears in each test name that proves a criterion, in the doc comment of the code, and in
the commit message's last line. No builder checks another's work (rule 10 of the run: no review agents in ALPHA); a finding that is not the
builder's own goes into its report as one line.

## 6 · Exit criteria and their evidence

| Criterion (FULL.md F0) | Evidence |
|---|---|
| Spec page | ansible.md reviewed at the walkthrough; `make coverage` records PKG-ANSIBLE-* as listed |
| Local: publish and install | `TestR_ANS_008_*`, `TestR_ANS_009_*` in `ansiblelocal`; `TestAnsibleLocal` on both client images |
| Remote: proxy of the public Galaxy | `ansibleremote` counts; `TestAnsibleRemote`, `TestAnsibleMirror`; a read-only check against `https://galaxy.ansible.com/api/` in the closing run (install of one small collection and one role, as the Debian closing run does for its archive) |
| Virtual: one endpoint | `ansiblevirtual`; `TestAnsibleVirtual` |
| Authentication with trove tokens | `TestR_ANS_006_*`; `TestAnsibleAuth` (`ansible.cfg`, `--token`, environment, netrc) |
| Migration of the existing Galaxy's content | `importer/ansible_test.go`; the owner's `--dry-run` result recorded under H-847 (not provable by the plan) |
| Integration tests | the three areas green on the stack; the kit green for all three classes |
| E2E tests | the CI leg `e2e (ansible)` green on a push; red legs are findings, not release blockers (CLAUDE.md) |
| Client setup instructions in the UI | `TestAnsibleSnippets`, the Vitest specs, the journey step |

## 7 · Open points for the development agent

Each is a thing the plan could not settle from the code or the clients; the builder named decides and records it as a decision block on
ansible.md with the next free Q-ANS number after 28 (the development agent hands out the range at the start).

1. **How the core selects a format's reindex job** (B1): deb enqueues a `coalesce` job of its own kind; whether the generic endpoint needs a
   hook (`Reindexer`) for a format whose index is rows, not generated items, is not visible from the contract. If a hook is needed it is an
   additive optional interface (ADR-191 consequences).
2. **Whether the core's name machinery takes the role package name `role/<id>`** or a classification without a package (B2, R-ANS-020); the
   unit test decides; the spec states both outcomes.
3. **The shape of the hub exchange interface** (B2) and whether the engine's host rule for credentials already sends the offline token to
   the realm only.
4. **The YAML reader for `meta/runtime.yml`** (B1): whether the module in `go.mod` can be restricted to a scalar lookup with a node limit;
   otherwise the field is dropped and R-ANS-010's `requires_ansible` becomes "from the manifest only" (the client reads the field from the
   version list and document; an empty value is accepted by the client, which ignores it).
5. **Page size of the public Galaxy's version list** (B2): only the collection index's cap of 100 was probed; if the version list caps lower,
   the stored page unit of Q-ANS-12 follows the cap.
6. **The first real source of the import** (B3, owner): which software the existing Galaxy is, its API root and whether its collection
   routes sit below a prefix. The planning agent has not seen it; H-847 is the record.
7. **ansible-core versions at build**: 2.17.14 and 2.21.6 are the newest of their series on 2026-11-10; the images pin what PyPI offers
   then, and the Python each needs is checked at the build of the image (B4).
8. **Import task retention versus the client's patience**: 30 days is a guess far above any client's wait; no evidence needed.
