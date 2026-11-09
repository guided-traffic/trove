# T-17 · APT registry (Debian and Ubuntu) — the work-package cut

The plan of task T-17 (docs/projektplan/promt.md section 10), written by the planning agent on 2026-11-08 and revised the same day
after a separate architecture and security review that tested the plan's APT and OpenPGP assumptions against the real clients,
and again after its re-check (section 8 lists every finding and where it went). Spec: [docs/spec/formats/debian.md](../spec/formats/debian.md), requirements
**R-DEB-001 to R-DEB-035**, decisions **Q-DEB-1 to Q-DEB-43**, open gaps **H-620 to H-630**. Reserved for the edits of other
pages, recorded there by the package that applies them: **Q-ARCH-380 to Q-ARCH-385**, **Q-REPO-270 to Q-REPO-274**,
**Q-STOR-180 to Q-STOR-182**, **ADR-170 to ADR-172**; Q-AUTH-220 to Q-AUTH-229 are not used (no identity change). Migrations:
**00070 to 00072**, the range the run assigned to this plan; the development agent renumbers them at build time if main has moved
past it.

The spec is written first; every other page changes in the work package that builds its mechanism, in the same change as the code
(the table "Changes to other pages" of debian.md), because the contract blocks of R-ARCH-008 and R-ARCH-009 are held equal to the
code by `TestR_ARCH_008_TheWrittenContractEqualsTheCode`.

Working mode (promt.md section 10, rule 10): plan, build and check by separate agents; no builder checks its own package; an agent
and a branch (worktree) per package, started as soon as its dependencies are on the integration branch. Builders do not commit;
the development agent integrates and commits per package (conventional commits naming R-DEB-… IDs, no attribution). **No two
packages that run at the same time own the same file**; WP0 creates every shared skeleton file and names the one package that
owns it afterwards.

## 0 · What T-17 takes from T-16

- **T-16's WP0 is on main** (6abafcd1: ADR-161, migrations 00054 to 00056, the key grammar, `Prepared.Aliases`, the upload-session
  port, `discover.Upstream.Artifact` as a declared method that answers `not_implemented`). APT's WP0 builds on it.
- **`discover.Upstream.Artifact` is still a stub** (`internal/formathost/mirror_host.go`). APT needs it in three places: the mirror
  (WP7), the virtual build (WP8) and the parse job's full-index fetch (WP6). It belongs to OCI WP10 as well. **WP13** builds it with
  the text oci.md gives it if OCI WP10 has not landed it when wave 1 starts, together with the background `Upstream` outside a pass
  that WP6 and WP8 need (Q-REPO-272); otherwise WP13 builds only the latter. The same holds for `SelectItems` counting versions and
  the rule that `<format>.mirror` is kept from an Owner like the patterns (Q-OCI-22): whichever of OCI WP10 and APT WP7 lands first.
- Nothing else of T-16 is needed: no registry root, no registry tokens, no upload sessions.

## 1 · Reading the plan against the catalog

Full table with one row per entry: debian.md, section "Catalog coverage". Grouped here:

- **Built in T-17:** PKG-DEB-001, 002 (coordinates as rows, derived pool path), 004 (`PUT upload/<file>`), 005, 006, 007 (through
  remotes), 008, 009 (stricter: refused, not stored), 010 (in part: read-only properties, screens), 011 (gzip and xz), 013, 014,
  015, 016 (as R-REPO-016), 017, 018 (per-repository keys, co-signing rotation), 020, 021, 022 (automatic), 023, 026, 027.
- **Stays in FULL.md:** PKG-DEB-004 (the deploy dialog's option), 007 (local source uploads and `Sources`), 010 (search on control
  fields), 012, 024, 025; `.udeb` and installer indexes, `dput`, translations and `Contents` for local and virtual repositories,
  mirrors of flat archives, `Valid-Until` on local releases, keys in a key service, Debian vulnerability matching, copy and move
  (with R-STOR-019), exposure rules, the Bastion, nested virtual repositories.
- **Not offered:** PKG-DEB-003 for local and virtual repositories (flat upstreams through remotes), PKG-DEB-019, PKG-DEB-028,
  bzip2 and LZMA indexes, browsing an upstream before anything is cached.

**FULL.md block D status** (written by the closing change, not now; the phase sets the row to 🟡 when building starts): the row
"Debian / APT repositories (Debian and Ubuntu; apt, apt-get)" at 🟢 in all four columns, and below the table:

```markdown
- Debian / APT: built — local, remote (`pullThrough` and `mirror`) and virtual repositories; uploads with suites and components;
  indexes built by a job as signed snapshots served by hash; a signing key per repository with co-signing rotation; remotes that
  pass upstream releases through and verify them; the mirror of named suites, components and architectures; virtual repositories
  that merge verified member releases and re-sign; clients Debian 13 and 12, Ubuntu 24.04 and 22.04. Stays: local source packages,
  snapshots, debug-symbol and installer packages, `dput`, translations for local and virtual repositories, mirrors of flat
  archives, `Valid-Until` on local releases, keys in a key service, Debian vulnerability matching (debian.md, Deferred).
```

## 2 · Contracts first: WP0

One package, one builder (`voltagent-lang:golang-pro`, with `voltagent-qa-sec:architect-reviewer` checking), before every other
package. It writes the contract additions, the ports, the schema, the configuration keys, the API document and the skeletons, with
no behaviour behind them beyond compile-time stubs (methods answer `errs.Unsupported` or `not_implemented`, routes 501
`deb_not_implemented`), so that the packages build against fixed types in parallel. Three ADRs, written by WP0 into decisions.md
with the table rows of Q-DEB-1 to Q-DEB-43:

- **ADR-170 · APT indexes are built by a job and published through the item store's publication port as generated items, also in
  virtual repositories** (R-ARCH-012 item 2; Q-DEB-6, Q-DEB-33, Q-ARCH-380, Q-STOR-180, Q-STOR-182).
- **ADR-171 · Every Debian local and virtual repository has its own EdDSALegacy signing key, generated by trove and sealed with the
  stored-secret key ring; rotation by a co-signing next key; the journal withholds keys and never erases them** (Q-DEB-9 to
  Q-DEB-11, Q-DEB-30, Q-DEB-31).
- **ADR-172 · The format contract gains the remote name index and the serve-facts kinds `deb_local` and `deb_remote`** (additions
  keep `ContractVersion` (3 since ADR-178), R-ARCH-025; the surface file changes; Q-ARCH-381).

### 2.1 Go contract (`internal/formats`, `internal/servefacts`)

```go
// NameServer is the optional interface of a RemoteAdapter whose upstream has no document per package: the shadowing check of
// repositories-and-proxy.md R-REPO-048 asks it instead of probing a package document (ADR-172, Q-REPO-270, Q-DEB-37). It reads
// the rows of the indexes the remote repository holds through h, in the scope of the virtual repository whose exposure the
// check decides. known is false when an index of that scope is not parsed yet; the format then enqueues its parse.
type NameServer interface {
	ServesName(ctx context.Context, h Host, q NameQuery) (served, known bool, err error)
}

// NameQuery names the remote member, the virtual repository through which it would serve the name (its reference and its format
// settings, from which the format reads the suites, components and architectures it serves) and the normalized name.
type NameQuery struct {
	Remote      RepoRef
	Via         RepoRef
	ViaSettings json.RawMessage
	Name        string
}

// RemoteCaps gains:  Names NameServer

// internal/servefacts gains two kinds; the item lookup of a download reads their rows in its own statement (R-ARCH-027,
// Q-ARCH-291):
//   DebLocal  (formats.ServeDebLocal):  whether deb_tombstones marks the (repository, path) of a local package file (R-DEB-032);
//   DebRemote (formats.ServeDebRemote): the SHA-256 and size the newest parsed index lists for a remote path (R-DEB-022), with
//             a facts type of its own (ServeDebRemoteFacts), which the remote adapter's FactsGate decides on.
```

The engine side of `NameServer`: the shadowing check (where R-REPO-048 is implemented) takes `RemoteCapabilities(id).Names` when
the format's `PackageDocumentPath` reports `ok` false, once per (virtual repository, remote member) pair of the exposure set. WP0
wires the call with the stub; WP6 implements it.

### 2.2 Ports below the contract (declared by WP0, each implemented by one package)

```go
// internal/items — the publication port (Q-STOR-182, ADR-170). Implemented by WP4 for both classes; WP8 adds only the rule that
// a virtual repository accepts generated items from this port and from nothing else.
type GeneratedFile struct {
	Path       repository.Cleaned
	Body       io.Reader // read once; the port spools and hashes it
	Size       int64
	MediaType  string
	ModifiedAt time.Time // the Release Date for every file of a generation (Last-Modified, R-DEB-010)
}
type Publisher interface {
	// Publish stores every file's blob first, outside any transaction (R-STOR-005), then runs one transaction that upserts a
	// package-less generated item per file (an identical blob changes nothing but modified_at), deletes the generated items at
	// remove, and runs commit with the transaction for the caller's own rows. commit returning ErrSuperseded rolls everything
	// back and Publish returns it; the stored blobs are left to garbage collection. Quota admission does not apply (Q-STOR-181).
	Publish(ctx context.Context, repo RepoRef, files []GeneratedFile, remove []repository.Cleaned,
		commit func(ctx context.Context, tx meta.Tx) error) error
}
var ErrSuperseded = errors.New("items: superseded")

// internal/formathost — the background Upstream outside a mirror pass (Q-REPO-272). Implemented by WP13.
// BackgroundUpstream returns a discover.Upstream for one job run of one remote repository: the egress, credential, background
// share (R-REPO-055) and throttling (R-REPO-054) rules of a pass, without a pass, its fence or its window. Document and Artifact
// behave as for a pass.
func (h *Host) BackgroundUpstream(ctx context.Context, repo RepoRef) (discover.Upstream, error)

// internal/formats/untrusted (Q-ARCH-382) — bounded readers, implemented by WP1.
func NewAr(r io.ReaderAt, size int64, lim Limits) (*ArReader, error) // members in order, names, sizes; Next/Open per member
func NewXZReader(r io.Reader, lim Limits) (io.Reader, error)         // decompressed-byte cap, fails instead of cutting
func NewZstdReader(r io.Reader, lim Limits) (io.Reader, error)

// internal/openpgp (leaf, Q-ARCH-383) — the only importer of the OpenPGP module, implemented by WP2.
type Key struct{ /* an opened secret key */ }
type Certificate struct{ Fingerprint string; Algorithm uint8; Created, BoundSince time.Time /* … */ }
// BoundSince is the creation time of the earliest valid binding self-signature (Q-DEB-40); Key carries the same two times.
// Generate makes an EdDSALegacy (algorithm 22) key over Ed25519, version 4, created at created (the caller passes the
// PostgreSQL time minus one hour, Q-DEB-30), with its binding self-signature made at the same time (Config.Time), so BoundSince
// equals Created; it never makes algorithm 27.
func Generate(uid string, created time.Time) (secretArmored, publicArmored []byte, fingerprint string, err error)
func ParseSecret(armored, passphrase []byte, now time.Time, lim Limits) (*Key, error)  // import rules of R-DEB-015
func ParseCertificates(armored []byte, now time.Time, lim Limits) ([]Certificate, error) // deb.upstreamKeys (R-DEB-020)
func ClearSign(text []byte, keys []*Key, at time.Time) ([]byte, error)                 // InRelease; at ≥ max(Created, BoundSince) + 1 s of each key
func DetachSign(data []byte, keys []*Key, at time.Time) ([]byte, error)                 // Release.gpg, armored
func VerifyClear(msg []byte, certs []Certificate, now time.Time, lim Limits) (text []byte, signer string, err error)
func VerifyDetached(data, sig []byte, certs []Certificate, now time.Time, lim Limits) (signer string, err error)
func ArmorCertificates(certs ...Certificate) []byte                                     // signing-key.asc

// internal/repository — the creation hook seam (Q-REPO-273). The repository service runs the hooks registered for a format
// inside the transaction that creates a repository of that format; the composition root binds the Debian one (WP2).
func RegisterCreateHook(format string, fn func(ctx context.Context, tx meta.Tx, repo RepoRef, class string) error)
```

The data port (`internal/formats/deb/store.go`) is one interface that embeds a group interface per package, each in a file of its
own: `store_local.go`, `store_keys.go`, `store_build.go`, `store_remote.go`, `store_mirror.go`, `store_virtual.go`, `store_view.go`,
`store_journal.go`. WP0 writes all of them and freezes them; a change after WP0 goes through the architecture reviewer. Value types
live in the leaf `internal/formats/deb/rows`. The implementation in `internal/formatdata/deb` composes one stub per group, each in
the file of the package that replaces it:

| Group | Methods (names indicative) | Interface file (WP0) | Implementation file | Owner |
|---|---|---|---|---|
| Local writes and reads | `RecordUpload`, `SetCoordinates` (returns removed coordinates for the journal), `Removed`, `Restored`, `Binary` | `store_local.go` | `local_store.go` | WP3 |
| Keys | `CreateFirstKey` (creation hook), `PublicCertificates`, `Keys`, `CreateNext`, `Promote`, `RetireNext`, `ConfirmWithheld`, `Reinstate` | `store_keys.go` | `keys_store.go` | WP2 |
| Builds and state | `Suites`, `WaitPublished`, `GenerationInputs`, `RecordGeneration` | `store_build.go` | `build_store.go` | WP4 |
| Remote | `RecordRelease` (anti-rollback decision in `OnFill`), `Release`, `EntryDigest`, `ServesName`, `EnqueueParse`, `EnsureParsed`, `ClearVerifiedBy` | `store_remote.go` | `remote_store.go` | WP6 |
| Mirror | `MirrorNames`, `MirrorEntries` | `store_mirror.go` | `mirror_store.go` | WP7 |
| Virtual | `VirtualInputs`, `RecordCheck` | `store_virtual.go` | `virtual_store.go` | WP8 |
| View | `Describe`, `Stored`, `Find`, `Withheld` | `store_view.go` | `view_store.go` | WP3 |
| Journal | `Withhold`, `Reinstate`, `RemoveCoordinatesByJournal` (keys: `WithholdKey`, `PromoteByJournal` in the keys group) | `store_journal.go` | `journal_store.go` | WP3 |

### 2.3 Schema (`internal/meta/postgres/migrations`, new and therefore editable until merged)

- `00070_deb.sql`: `deb_suites`, `deb_binaries`, `deb_coordinates`, `deb_generations`, `deb_tombstones` exactly as R-DEB-008;
  `deb_suites.change_seq` and `built_seq` registered with `meta.RegisterRevisionColumn`; export class `data`.
- `00071_deb_signing_keys.sql`: `deb_signing_keys` as R-DEB-015 (states `primary`, `next`, `withheld`, `retired`; the fingerprint
  unique in every state; `key_created`, `bound_since`); `private_key` registered in the sealed-column registry of `internal/secrets` (row key
  `id`); export class `data`.
- `00072_deb_remote.sql`: `deb_remote_releases` (with `document = 'absent'` and `floor_date`), `deb_remote_indexes`,
  `deb_remote_entries` as R-DEB-023; export class `data`, and
  the three tables in the list a restore without remote caches empties (R-BAK-008).
- The registrations live in `internal/formatdata/deb/tables_deb.go` (WP0).
- **Queries: one file per package**, so that sqlc writes one output file per package and parallel packages never regenerate the
  same file (`models.go` changes only with these migrations, which WP0 owns): `queries/deb_keys.sql` (WP2), `deb_local.sql` (WP3),
  `deb_build.sql` (WP4), `deb_remote.sql` (WP6), `deb_mirror.sql` (WP7), `deb_virtual.sql` (WP8). Each package creates its own
  file. WP0 alone adds the `deb_local` and `deb_remote` facts to the item lookup statement (R-ARCH-027).

### 2.4 Configuration, API and registrations

- Configuration keys (R-ARCH-020, `internal/config`): `deb.index_delay` (2s, **1s**–60s), `deb.virtual_index_interval` (60s,
  10s–1h), `deb.by_hash_keep` (1h, 10m–7d), `deb.index_wait_max` (30s, 1s–120s).
- OpenAPI (`api/openapi.yaml`): `DebSettings` (members of R-DEB-008 with their classes), `DebMirrorSettings` (R-DEB-024), the `deb`
  member of the repository document with the read-only `upstreamKeyFingerprints`; the operations `GET`/`POST …/deb/signing-keys`,
  `POST …/deb/signing-keys/promote`, `DELETE …/deb/signing-keys/{fingerprint}`, `POST …/deb/signing-keys/{fingerprint}/reinstate`,
  `PUT …/deb/coordinates`, `GET …/deb/index` (`waitFor`), and `POST …/reindex` (R-REPO-016); the per-class enums follow the registry
  (`TestR_ARCH_010_PerClassFormatEnums`); `make generate` for the Go and TypeScript clients.
- The format's settings schema `internal/formats/deb/settings.schema.json` (defaults of R-DEB-008 and R-DEB-024; `upstreamKeys` at
  most 256 KiB).
- Job kinds registered in `internal/formatdata/deb/jobs.go` (WP0) with handlers that call functions of their owners' files:
  `deb-index` (queue `index`, `coalesce`, key `index:<repository id>`; `RunIndexJob` in `build.go`, WP4), `deb-remote-parse` (queue
  `index`, `coalesce` per repository, directory and index path, parsing the hash the stored release names when it runs; in
  `remote_parse.go`, WP6), the task `deb-index-refresh` (queue `maintenance`, every minute, `exclusive`;
  `refresh.go`, WP4, which only enqueues and calls the hooks that `refresh_remote.go` (WP6) and `refresh_virtual.go` (WP8)
  register).
- Journal events registered (journal flag, handlers later): `deb.package.deleted`, `deb.coordinates.removed`,
  `deb.signing_key.promoted`, `deb.signing_key.retired`; the part `debUpstreamKeysRemoved` of `repository.narrowed` (handler WP6);
  the other events of R-DEB-033 in the audit registry.
- `go.mod`: `github.com/ProtonMail/go-crypto` (v1.5.2 or newer) with the module it requires, `github.com/cloudflare/circl` (v1.6.3
  or newer), and `github.com/ulikunitz/xz` (v0.5.17 or newer) — **three new modules** (Q-DEB-24), each pinned once, imported by the
  skeletons of `internal/openpgp` and `untrusted` so that `go mod tidy` keeps them. Licences: the OpenPGP module and `xz` BSD,
  `circl` v1.6.3 BSD-3-Clause (verified by the review).
- Chart: no new route (`/api` already goes to `packages`) and no new value.

### 2.5 Skeleton of the format (files and their owners afterwards)

| File | Content in WP0 | Owner afterwards |
|---|---|---|
| `internal/formats/deb/format.go` | the Descriptor of R-DEB-001, `Classes` assembled from the classes registered by `class_*.go` files | WP0 (frozen) |
| `internal/formats/deb/routes.go` | every route of R-DEB-003; a handler is looked up by route name in a registry the `handlers_*.go` files fill; an unregistered route answers 501 `deb_not_implemented` | WP0 (frozen) |
| `internal/formats/deb/paths.go` | the layout, `Classify` complete with its unit tests (flat directories by index file name, the flat root), the canonical path | WP0 (frozen) |
| `internal/formats/deb/errors.go` | `WriteError` per R-DEB-005 with every code | WP0 (frozen) |
| `internal/formats/deb/store*.go` | the port interfaces of 2.2 | WP0 (frozen) |
| `internal/formats/deb/class_local.go` | registers the local class | WP3 |
| `internal/formats/deb/class_remote.go`, `class_virtual.go` | not created by WP0 | WP6, WP8 create them |
| `internal/formatdata/deb/{port.go,tables_deb.go,jobs.go}` | the composed port, the table registrations, the job kinds | WP0 (frozen) |
| `internal/formatdata/deb/{local_store,view_store,journal_store}.go` | stubs | WP3 |
| `internal/formatdata/deb/keys_store.go` | stub | WP2 |
| `internal/formatdata/deb/{build_store,build,refresh}.go` | stubs | WP4 |
| `internal/formatdata/deb/{remote_store,remote_parse}.go` | stubs | WP6 |
| `internal/formatdata/deb/mirror_store.go`, `virtual_store.go` | stubs | WP7, WP8 |
| `internal/items/publish.go` | the `Publisher` port, a stub | WP4 |
| `internal/formathost/background_upstream.go` | the constructor, a stub | WP13 |
| `internal/formats/untrusted/{ar,xz,zstd}.go` | the readers' signatures, stubs | WP1 |
| `internal/openpgp/openpgp.go` | the API above, stubs | WP2 |
| `internal/repository/create_hooks.go` | the seam and its call in the creation transaction | WP0 (frozen); WP2 binds the Debian hook in the composition root |

Registered in `internal/formats/all/all.go`; the archtest layer table gains the leaves `internal/formats/deb/{control,debfile,
version,index,rows}` and `internal/openpgp`; the conformance kit is green.

### 2.6 Spec edits of WP0 (same change)

R-ARCH-008 Go block (`NameServer`, `NameQuery`, `RemoteCaps.Names`, `ServeDebLocal`, `ServeDebRemote`, `ServeDebRemoteFacts`) and
its stale pointer for setup templates (R-API-030, not R-API-033), `internal/formats/testdata/contract.txt`, the layer table,
Q-ARCH-381 and Q-ARCH-383; R-REPO-003 and R-REPO-009 with Q-REPO-273; R-STOR-003 with Q-STOR-180 (a virtual repository of a format
with a job-built index holds its generated items) and the publication port with Q-STOR-182; decisions.md ADR-170 to ADR-172 and
the rows of Q-DEB-1 to Q-DEB-43.

**Tests of WP0:** the contract spec test, the surface test, `TestR_ARCH_010_*` (kit with the local class, enums), `Classify`'s
table, the migration test (export classes, the sealed column, the revision columns, no blob reference without its triggers),
`make generate-check`, the lint.

## 3 · Work packages after WP0

Every package lists: owner subagent type, requirement IDs, owned files (disjoint across packages; shared skeleton files as in
2.5), what it depends on, and whether it needs the integration stack (shared `trove-it-*`, never `integration-down/up`) or the kind
cluster (one agent at a time).

| WP | Subject | Agent | Requirements | Owned files | Depends on | Stack | kind |
|---|---|---|---|---|---|---|---|
| WP1 | Package-file and deb822 libraries | `voltagent-lang:golang-pro` | R-DEB-002, R-DEB-007 (parsing), R-DEB-034 (bounds) | `internal/formats/untrusted/{ar,xz,zstd}.go` and tests, `internal/formats/deb/control/` (deb822 parser and writer, fuzz targets), `internal/formats/deb/debfile/` (archive to normalized paragraph), `internal/formats/deb/version/` (grammars, dpkg order) | WP0 | no | no |
| WP2 | OpenPGP, signing keys, rotation, key journal | `voltagent-infra:security-engineer` + `voltagent-lang:golang-pro` | R-DEB-015, R-DEB-016 (signer), R-DEB-017, R-DEB-018 (local, virtual), R-DEB-020 (verifier library), R-DEB-032 (key replay) | `internal/openpgp/`, `internal/formatdata/deb/{keys_store.go,keys.go,keys_journal.go}`, `internal/api/v1/deb_keys.go`, `queries/deb_keys.sql`, the Debian creation hook's binding in the composition root | WP0 | yes | no |
| WP3 | Local class: upload, rows, coordinates (with the journal of removals), deletion, serve check, view, setup and guide (local) | `voltagent-lang:golang-pro` + `voltagent-core-dev:backend-developer` | R-DEB-001, R-DEB-004, R-DEB-005, R-DEB-006 to R-DEB-009, R-DEB-013, R-DEB-029 (local), R-DEB-030, R-DEB-032 (deletion and coordinate replay, reinstate) | `internal/formats/deb/{class_local,handlers_local,handlers_upload,hooks,inspect,serve,view,setup_local,guide_local}.go`, `internal/formatdata/deb/{local_store,view_store,journal_store,journal_replay,reinstate}.go`, `internal/api/v1/deb_coordinates.go`, `trove admin deb reinstate`, `queries/deb_local.sql` | WP0, WP1 | yes | no |
| WP4 | Index builder and publication port (both classes), retention, index state, reindex, the coalesce change | `voltagent-lang:golang-pro` + `voltagent-data-ai:postgres-pro` | R-DEB-010, R-DEB-011, R-DEB-012, R-DEB-014, R-DEB-032 (restore fix-up), R-REPO-016 (endpoint), Q-ARCH-384, Q-STOR-182 | `internal/items/publish.go` (port, both classes, quota exemption), `internal/formats/deb/index/{packages,release}.go` (renderers, pure), `internal/formatdata/deb/{build_store,build,generations,retention,state,refresh}.go`, `internal/api/v1/deb_index.go`, `internal/api/v1/reindex.go`, `internal/jobs/queue.go` and `queries/jobs.sql` (an earlier run time moves a waiting job; a reindex upgrades a waiting automatic job), `queries/deb_build.sql`, the restore fix-up registration | WP0, WP1 (writer), WP2 (signer); its renderers start on fixture rows in wave 1 | yes | no |
| WP13 | Background fetches | `voltagent-lang:golang-pro` | Q-REPO-272; `Artifact` per oci.md if OCI WP10 has not landed it | `internal/formathost/background_upstream.go`; `internal/formathost/mirror_host.go` (`Artifact`) only when OCI WP10 is not building it | WP0 | yes | no |
| WP5 | Counting stub archive | `voltagent-qa-sec:test-automator` | R-DEB-035 (stub) | `test/debstub/` (new, with `debgen/`, the generator of installable `.deb` files that integration and e2e uploads use too), Makefile `deb-stub-image` and `DEB_STUB_IMAGE`, `deploy/dev/debstub.yaml` | WP0 (names only) | no | no |
| WP6 | Remote class | `voltagent-lang:golang-pro` | R-DEB-018 (remote), R-DEB-019 to R-DEB-023, R-DEB-029 (remote), R-DEB-032 (`debUpstreamKeysRemoved` replay), R-REPO-048 through `NameServer`, Q-ARCH-385, Q-REPO-274 | `internal/formats/deb/{class_remote,handlers_remote,remote,remote_gate,remote_names,setup_remote,guide_remote}.go`, `internal/formatdata/deb/{remote_store,remote_parse,housekeeping,refresh_remote}.go`, the shadowing check's use of `NameServer`, `internal/repository/validate_deb.go` (keys, member and mirror refusals, clearing `verified_by`), the `debUpstreamKeysRemoved` part in `internal/repository/narrowed.go` and its replay, the dispatcher's uncleaned flat root (`internal/api/dispatch/`), `queries/deb_remote.sql` | WP0, WP1, WP2 (verifier), WP5, WP13 | yes | no |
| WP7 | Mirror | `voltagent-lang:golang-pro` | R-DEB-024, Mirror discovery; R-REPO-050 to R-REPO-052 edits | `internal/formats/deb/mirror*.go`, `internal/formatdata/deb/mirror_store.go`, `internal/repository/validate_deb_mirror.go`, `queries/deb_mirror.sql`, `test/integration/mirror/deb_test.go`; shared with T-16 WP10 if not yet on main: `internal/mirror/policy.go` (`SelectItems`), the generic `<format>.mirror` rule | WP5, WP6, WP13 | yes | no |
| WP8 | Virtual class | `voltagent-lang:golang-pro` | R-DEB-018 (virtual), R-DEB-025 to R-DEB-028, R-DEB-029 (virtual); R-STOR-018/028 edits | `internal/formats/deb/{class_virtual,handlers_virtual,virtual_gate,setup_virtual,guide_virtual}.go`, `internal/formats/deb/index/merge.go` (streaming merge), `internal/formatdata/deb/{virtual_store,virtual_build,refresh_virtual}.go`, `internal/items/publish_virtual.go` (the virtual repository's acceptance of generated items, the summary), `queries/deb_virtual.sql` | WP3, WP4, WP6, WP13 | yes | no |
| WP9 | Integration tests, conformance, read budget | `voltagent-qa-sec:test-automator` | R-DEB-035 (integration), R-ARCH-027 rows, `TestR_AUTHZ_007_DebActionMap`, crafted inputs | `test/integration/deb/` (new), `test/integration/querybudget/` Debian rows, the kit's crafted inputs for the format, the Makefile integration area `deb`, the CI integration leg | WP3, WP4 (local rows), WP6 (remote rows), WP8 (virtual rows) | yes | no |
| WP10 | e2e harness and CI | `voltagent-qa-sec:test-automator` + `voltagent-infra:kubernetes-specialist` + `voltagent-infra:devops-engineer` | R-DEB-035 (e2e), R-DEB-031, R-DEB-017 (rotation row on three images), R-REPO-058/059 for Debian | `test/e2e/deb_*_test.go`, `test/e2e/aptclients/` (pins and their check), Makefile pins `E2E_APTCLIENTS_*` (digests of `debian:13`, `debian:12`, `ubuntu:24.04`, `ubuntu:22.04`), `deploy/dev/apt-clients.yaml`, `deploy/dev/values-*.yaml` additions, `test/e2e/mirror_*` additions, `.github/workflows/release.yml` (set `deb`, closing-run legs), `.github/workflows/performance.yml` (`deb-long`) | WP3 to WP8, WP5 | — | **yes** |
| WP11 | UI | `voltagent-lang:angular-architect` | R-DEB-029 (snippets), R-DEB-030, R-DEB-014 (state), R-DEB-015, R-DEB-017, R-DEB-032 (key panel with withheld keys) | `web/src/app/features/setup/clients.ts` (`deb`: `apt`), `web/src/app/shared/format-icon.ts`, the repository settings' `deb` section, the signing key panel (confirm, reinstate), the index state panel (member failure reasons), the package detail (architectures, coordinates), i18n units, specs, a browser journey step | WP0 (OpenAPI), WP2 (key API), WP3 (view) | no | journey only (WP10's turn) |
| WP12 | Documentation | `voltagent-dev-exp:documentation-engineer` | all (docs) | README (naming tables: `/api/deb/<p>/<k>/` routes, `deb.*` settings and properties, `deb.*` configuration keys, job kinds, events, metrics, the `/etc/apt` file names of the snippets; TL;DR row for APT), `docs/developer/deb.md` (new), `docs/developer/{README,formats,e2e,build-and-test}.md` rows, `docs/security/deb-signing.md` (key custody, rotation, the withheld state; links H-620, H-621, H-622, H-627, H-629) and `docs/security/deb-remote.md` (verification, re-signing; links H-623, H-625, H-626, H-628, H-630), `docs/manual` user and admin pages for APT, FULL.md row (closing), coverage regenerated | WP3 to WP10 as they land | no | no |

Notes per package:

- **WP1** is pure and leaf: no I/O besides the readers it is given. Fuzz targets for the deb822 parser and the `ar` reader (kit
  `fuzz.go`); crafted inputs of R-DEB-034 as unit tests; the dpkg order against a table of pairs written in the test. `ulikunitz/xz`
  is only ever read through the bounded reader (CVE-2021-29482).
- **WP2** spec edits: architecture R-ARCH-021 (the sealed column of keys in its examples), R-REPO-009 (creation hook). Tests: the
  generated key's algorithm octet 22, version 4, no expiry, creation one hour back, a binding self-signature whose time equals
  the key's creation (`bound_since` = `key_created`, Q-DEB-40); `BoundSince` of an imported key whose only self-signature is
  fresh, and the build's wait until a signature can follow it; import rules (2048-bit RSA, algorithm 27, an
  expiry, a future creation, a fingerprint known in any state); sealing (`v1:` prefix); no private key in an answer or a log line;
  the key operations with and without `manage`; promotion and next-key retirement (private key erased, journal entries
  `deb.signing_key.promoted` and `deb.signing_key.retired`); the replay: promoted with the new key present (new primary, old
  withheld, sealed key kept) and absent (old withheld, no primary), retired (withheld), confirm and reinstate; signatures verified by
  the module, and in WP10 by `gpgv` and `sqv`.
- **WP3** spec edits: authorization R-AUTHZ-007 (the per-format notes name the Debian table), storage R-STOR-018 (409 for a
  generated item). Its control normalization is reviewed by the security auditor and attacked by the penetration tester (index
  injection). The local guide and setup templates are its; WP6 and WP8 add their classes' sections in their own files.
- **WP4** builds the publication port and the snapshot publication on it: blobs first, then one transaction that upserts every
  by-hash item of the generation, removes what retention gives up, and holds the monotonic condition; `modified_at` = `Date`; the
  `Date` and signature-time rules of Q-DEB-30 with the wait instead of running ahead; the fault-injection test between blob writes
  and commit; the integration asserts of R-DEB-010 and R-DEB-016 on times; retention only inside the job; the `waitFor` read; the
  R-REPO-016 endpoint (405 for formats without a job-built index; a reindex upgrades a waiting automatic job); the coalesce change
  of the job queue (Q-ARCH-384) with tests of its own. Spec edits: architecture R-ARCH-012 (the format as the first job-built index,
  its delay bound), R-ARCH-016 (Q-ARCH-384), backup R-BAK-022 (the fix-up), storage (Q-STOR-182).
- **WP13** builds the `Upstream` that a job run of one remote repository gets outside a pass (egress, credentials, background share,
  throttling, no fence, no window), and `Artifact` when OCI WP10 has not; spec edit R-REPO-055 (the background share also covers
  these runs, Q-REPO-272). It is in wave 1 because WP6, WP7 and WP8 depend on it.
- **WP5** generates everything in code: `.deb` files that `dpkg` installs (an `ar` archive with `control.tar.gz` and `data.tar.gz`
  holding one script), indexes, releases signed with EdDSALegacy keys from fixed seeds whose creation lies an hour back, the by-hash
  tree, pdiffs, the flat directory and the flat root, the large index; the switches of R-DEB-035; a control API like
  `test/nugetstub`'s. The stub signs with the OpenPGP module directly, not through `internal/openpgp`, so trove's verifier is tested
  against an independent signer.
- **WP6** spec edits: R-REPO-003, R-REPO-048 (Q-REPO-270, the virtual repository's scope), R-REPO-061 and Q-REPO-246
  (`debUpstreamKeysRemoved`, Q-REPO-274), architecture R-ARCH-005 and Q-ARCH-42 (the flat root, Q-ARCH-385), R-ARCH-027 (a plain
  index path of a remote costs two statements), R-ARCH-032 (the parsed release map cache), backup R-BAK-008 (the remote rows). Its
  parse job (`coalesce`, reading the hash of the stored release when it runs) re-parses on every new release through WP13's
  `Upstream`; the `absent` record of a suite the upstream lacks; the shadowing check settled only on stored facts (Q-DEB-41); the
  re-verification of releases from their bytes after a key change, revalidation without validators, and `floor_date`
  (Q-DEB-42). Tests against the stub only.
- **WP7** spec edits: R-REPO-050 (Debian can mirror; `deb.mirror` kept like the patterns), R-REPO-051 (versions counted per version,
  if T-16 WP10 has not written it), R-REPO-052 (the Debian upstream). The lister runs the parse of its indexes in the pass through the
  data port instead of waiting for the job, and uses only verified, unexpired releases.
- **WP8** spec edits: R-REPO-019, R-REPO-020 and R-ARCH-015 (Q-REPO-271: one stored merged index per suite, no `rev_seq` check, the
  gate, no partial merge published), R-AUTHZ-009 (the gate for the format), storage R-STOR-028. The member-release rules of
  R-DEB-025 (verified by a current key, unexpired, not rolled back), the recorded member `Date` dropped on an upstream identity
  change and when the member leaves (Q-DEB-43), the earliest `Valid-Until` carried over, members without a suite contributing
  nothing; the merge streams in member order with a hash set; the bound of R-DEB-026 is tested with
  `compression: ["xz"]` (the xz writer's speed is not measured yet).
- **WP9** holds every integration criterion of debian.md at its tier, named `TestR_DEB_0NN_…`; the action map one test per row; the
  read budget rows; Valkey stopped; three replicas pinned per request where a criterion says so; the time asserts of Q-DEB-30 (one
  cluster shares one clock, so e2e cannot show those failures).
- **WP10** sets: `deb` (per change, `scatter` profile, three `packages` replicas, two workers): the four images through `deb-local`,
  `debian` (`pullThrough`), `debian-mirror` (after a pass, the stub stopped) and `apt`; anonymous read; a wrong keyring that must fail;
  the rotation table on Debian 13, Ubuntu 24.04 and Ubuntu 22.04; the flat root on Ubuntu 24.04. `deb-long` (nightly and dispatch):
  the delay bounds under load (local, and virtual with xz), the drain of the worker running a virtual build. `mirror` gains a Debian
  pass on two workers while clients install; `mirror-long` the drain during a Debian pass. Closing run: `deb` with Valkey stopped;
  the read-only check against `https://deb.debian.org/debian/`. APT runs with `--error-on=any`, CA trust through
  `Acquire::https::CAInfo`, credentials in `auth.conf.d`, never `trusted=yes`.

## 4 · Order and parallel waves

```text
WP0 ──┬─ WP1 ──┬────────── WP3 ──┬──────────────┐
      ├─ WP2 ──┼─ WP4 ───────────┤              │
      ├─ WP13 ─┤                 ├─ WP8 ────────┼─ WP9 ── WP10 (kind) ── WP12 (closing docs)
      └─ WP5 ──┴─ WP6 ──┬────────┘              │
                        └─ WP7 ─────────────────┘
                 WP11 after WP2 and WP3 (parallel to WP6–WP8)
```

- Wave 1 (after WP0): WP1, WP2, WP5 and WP13 in parallel (disjoint files); WP4's pure renderers (`index/packages.go`,
  `index/release.go`) may start on fixture rows.
- Wave 2: WP3 (after WP1), WP4 (after WP1 and WP2), WP6 (after WP1, WP2, WP5 and WP13).
- Wave 3: WP7 (after WP6 and WP13), WP8 (after WP3, WP4, WP6 and WP13), WP11 (after WP2 and WP3), WP9's local rows.
- Wave 4: WP9's remote and virtual rows, WP10 on kind (the only agent using it), the verifier on kind; WP12 closes.
- Every package writes the docs of its mechanism in its own change (rule: documentation in the same change); WP12's own pass covers
  README, the manual, the security pages and FULL.md.
- Findings of section 8 of promt.md run beside these waves with an agent each (rule 10); nothing here waits for them.

## 5 · Checks (separate agents, promt.md section 10 step 3)

- `voltagent-qa-sec:architect-reviewer` on WP0 (contract, the ports, ADR-170 to ADR-172, schema, generated items in virtual
  repositories, the creation hook, the file ownership of 2.5) before wave 1 starts; again on the seams of WP4 (publication port,
  coalesce change), WP6 (`NameServer` in the virtual scope, the release pair, the flat root) and WP8 (merge, member-release rules).
- `voltagent-qa-sec:code-reviewer` on every package's diff.
- `voltagent-qa-sec:security-auditor` on WP2 (key custody, sealing, import, rotation, the replay that withholds), WP3 (control
  normalization, index injection, immutability, the coordinate journal), WP4 (times, retention), WP6 (verification, anti-rollback,
  key validity, upstream paths, unverified fills, the key-removal journal) and WP8 (re-signing only verified, fresh, not rolled-back
  member releases, the gate); `voltagent-qa-sec:penetration-tester` on WP1 (the `ar` reader and the deb822 parser), WP3 (crafted
  `.deb` uploads) and WP6 (crafted releases, signatures, far-future dates and `Packages` from the stub).
- A verifier that runs the real clients against the chart on kind (WP10's sets) with APT's verification on, and reads every failure
  back to a builder.

## 6 · Exit criteria and their evidence

| Criterion | Evidence |
|---|---|
| Every R-DEB acceptance criterion at its tier | test names `TestR_DEB_0NN_…`, coverage regenerated |
| `apt-get update` and `apt-get install` in Debian 13, Debian 12, Ubuntu 24.04 and Ubuntu 22.04 through local, remote (both modes) and virtual repositories, signatures verified on the client, a wrong keyring refused | e2e set `deb` in Test and Release of the closing commit |
| The rotation table of R-DEB-017 on `sqv` and `gpgv` 2.2 | e2e rotation row on Debian 13, Ubuntu 24.04 and Ubuntu 22.04 |
| `sqv` reads the module's EdDSALegacy signatures (verified by the review's re-check; kept proven per change) | the Debian 13 rows of the set `deb` |
| A "new key only" client on `sqv` verifies an imported key whose only self-signature is fresh | the extra Debian 13 rotation run (R-DEB-017, Q-DEB-40) |
| Release dates and signature times trail the database clock; keys are older than their signatures | the integration asserts of R-DEB-010 and R-DEB-016 |
| The delay bounds of R-DEB-010 and R-DEB-026 (with xz) under load; a drain during a virtual build | `deb-long` in HA and Performance (nightly), dispatched on the closing commit |
| A mirror pass on n replicas; a drain during a pass | e2e `mirror` (Debian rows) per change; `mirror-long` nightly |
| Valkey stopped; the read-only check against the Debian archive | closing run legs |
| The read budget rows | `TestR_ARCH_027_ADownloadCostsOneStatement` Debian rows |
| After a restore: a deleted package withheld, a removed coordinate and a removed upstream key removed again, a promoted key primary with the old one withheld and its sealed key kept | the restore drill (R-BAK-029) and WP2's and WP3's replay tests |
| White-label scan empty, no attribution | scan output, `git log` check |

## 7 · Open points for the development agent

- **Order with T-16.** T-16's WP0 is on main; `Artifact`, `SelectItems` and the `<format>.mirror` rule go with whichever of OCI
  WP10 and APT WP13/WP7 lands first (section 0).
- **Three new Go modules** (Q-DEB-24): the OpenPGP module, `cloudflare/circl` which it requires, and `ulikunitz/xz`; pinned once in
  `go.mod`, picked up by Renovate; no tool pin changes; all BSD-licensed (verified).
- **The creation hook** (Q-REPO-273) is a new seam of the repository service, not of the format contract; WP0 declares it, WP2 binds
  it. If the architecture review prefers a lazy first key (the first build creates it), the deviation is recorded on debian.md and
  `signing-key.asc` answers 404 until the first build.
- **The coalesce change** (Q-ARCH-384) touches the shared job queue (`internal/jobs/queue.go`, `queries/jobs.sql`); WP4 owns it in
  this run, and any other package that needs the queue changed in parallel coordinates through the development agent.
- **The closing run reaches `deb.debian.org`** and fetches Debian's archive key from `ftp-master.debian.org` (checked against a
  fingerprint pinned in the test), anonymously, at most 30 upstream requests.
- **Architectures of the kind node.** The owner's local cluster runs `arm64`, CI `amd64`; the stub serves both and the client images
  are pinned by the digest of their multi-architecture index.
- **The owner's installation** needs nothing beyond the release: no chart value, no Secret (keys are generated in the database).
- **Unverified client behaviour** that a package proves (debian.md, "Client and library behaviour"): pdiff publication on the
  Debian archive (WP6), the xz writer's speed (WP8), the exact clock thresholds of R-DEB-010 (derived, not measured).

## 8 · The review of 2026-11-08 and where each finding went

| # | Finding | Change |
|---|---|---|
| 1 | Signature times break Debian 13 clients | Q-DEB-30: `Date` and signature time = PostgreSQL time − 60 s, at least previous + 1 s, the build waits instead of running ahead; key creation = PostgreSQL time − 1 h; imports created in the future refused; `deb.index_delay` ≥ 1 s; integration asserts (R-DEB-010, R-DEB-015, R-DEB-016) |
| 2 | Ed25519 key type ambiguous; algorithm 27 unreadable by GnuPG | Q-DEB-10 revised: EdDSALegacy (22) over Ed25519, v4; 27 and v6 refused; unit test on the algorithm; "deterministic" and the subpacket layout dropped (R-DEB-015, R-DEB-016) |
| 3 | The replay erases private keys | Q-DEB-31, Q-DEB-22 revised: state `withheld`, sealed key kept, confirm or reinstate; `deb.signing_key.promoted` journaled so the replay promotes a restored next key; CHECK adjusted (R-DEB-015, R-DEB-032, H-629) |
| 4 | A virtual repository re-signs unverified, withdrawn, expired or rolled-back upstream content | Q-DEB-32: member releases only when verified by a current key, unexpired and not older than the last merged; key removals clear `verified_by`; earliest `Valid-Until` carried over; the mirror pass the same (R-DEB-020, R-DEB-024, R-DEB-025, R-DEB-026, H-624, H-630) |
| 5 | No publication port | WP0 declares `items.Publisher`; WP4 owns it for both classes, WP8 adds the virtual rule (2.2, Q-STOR-182, ADR-170) |
| 6 | By-hash removal races builds | Q-DEB-33: removals only inside the `deb-index` job; the commit upserts every by-hash item of its generation (R-DEB-010, R-DEB-012, R-DEB-021) |
| 7 | Coalesce semantics differ from the spec | Q-ARCH-384: an earlier run time moves the waiting job; a reindex upgrades a waiting automatic build; WP4 changes the job queue (R-DEB-010, R-DEB-014) |
| 8 | R-REPO-020 / R-ARCH-015 MUSTs broken silently | Q-REPO-271 records the exemption for a job-built merged index (Changes to other pages; WP8) |
| 9 | pdiffs keep parsed rows stale | Q-DEB-36: a new release re-parses the changed indexes through the background path; the stub serves pdiffs; H-625 corrected (R-DEB-023) |
| 10 | `NameServer` answers from whatever is parsed | Q-DEB-37: `NameQuery` with the virtual repository's scope; unparsed scope undetermined with the parse enqueued; virtual builds parse what they merge; the `contrib` test (R-DEB-023, R-DEB-026) |
| 11 | Trust narrowings not journaled | Q-DEB-34: `debUpstreamKeysRemoved` in `repository.narrowed` (Q-REPO-274) and `deb.coordinates.removed`, replayed fail-closed (R-DEB-009, R-DEB-020, R-DEB-032) |
| 12 | A retired key can come back through import | Fingerprint unique in every state; keys with an expiry or a future creation refused (R-DEB-015) |
| 13 | Upstream keys checked only at signature time | Q-DEB-35: valid now and at signing, created before the signature, signature time ≤ now + 10 min (R-DEB-020) |
| 14 | Anti-rollback can freeze a remote | Q-DEB-35: `Date` more than 10 min ahead refused; anti-rollback only between verified releases (R-DEB-019) |
| 15 | Owned files overlap | Section 2.5: explicit file lists, per-class registration files, per-group port and stub files, one query file per package |
| 16 | WP8 depends on `Artifact` | WP13 (background fetches, wave 1) with the `BackgroundUpstream` contract declared by WP0; WP6, WP7, WP8 depend on it |
| 17 | 64 KiB / 16 certificates refuse Debian's keyring | 256 KiB and 32 certificates; test with Debian 13's keyring (R-DEB-008, R-DEB-020, R-DEB-034) |
| 18 | Flat remotes misclassified; `/./` redirected | Classify flat index files by name; Q-DEB-39 and Q-ARCH-385: the flat root reaches the format uncleaned; e2e on Ubuntu 24.04 (R-DEB-003, R-DEB-021) |
| 19 | `Hash` armor header rule too strict | Checked only when present (R-DEB-019, R-DEB-034, Q-DEB-35) |
| 20 | Merge order conflicts with streaming | Q-DEB-38: member order with a bounded hash set (R-DEB-011, R-DEB-025) |
| 21 | Rotation not tested on gpgv 2.2 | Rotation row on Ubuntu 22.04 too (R-DEB-017, R-DEB-035, Q-DEB-27) |
| 22 | Error table incomplete | R-DEB-005 lists every code |
| 23 | Stale 304 within one second | `modified_at` of every generated item = the `Release` `Date` (R-DEB-010, Q-STOR-182) |
| 24 | Module accounting | Three modules with versions and licences; the xz advisory and the bounded reader; WP8's bound test with xz (Q-DEB-24) |
| 25 | Migration numbers | 00070 to 00072 (2.3) |
| 26 | Offline mirror misses `dep11` and `cnf` | H-628 (Mirror discovery) |
| 27 | A member without a suite fails the build | Contributes nothing; no failure (R-DEB-025, R-DEB-026) |

The re-check of the revision (1cbf0b22) found all 27 resolved and six further points:

| # | Finding | Change |
|---|---|---|
| N1 | `sqv` refuses a data signature older than every binding self-signature, even when the key is older (verified) | Q-DEB-40: the generated key's self-signature carries its creation time; `bound_since` recorded per key; signatures wait for max(creation, `bound_since`) + 1 s; WP2 unit test on the self-signature time; the Debian 13 rotation run with an imported key whose only self-signature is fresh (R-DEB-015 to R-DEB-017) |
| N2 | "Not served" vacuously true for a suite the remote never fetched | Q-DEB-41: every suite of V's scope must be settled (stored release with parsed indexes, or a PostgreSQL record `absent`); otherwise undetermined with fetch and parse enqueued; acceptance case (R-DEB-019, R-DEB-023) |
| N3 | `deb-remote-parse` as `exclusive` loses a trigger during a run | `coalesce` per index path, parsing the hash the stored release names when it runs (R-DEB-023, Q-DEB-36) |
| N4 | Clearing `verified_by`: a 304 and an older release signed by a remaining key | Q-DEB-42: re-verification from stored bytes by the remote's job, revalidation without validators, a 304 never verifies; `floor_date` as the rollback floor for any release verified once; H-630 states the window (R-DEB-019, R-DEB-020) |
| N5 | The recorded member `Date` has no reset | Q-DEB-43: dropped on the member's upstream identity change and when it leaves; a reindex keeps it; remove and re-add to accept an older release (R-DEB-025) |
| N6 | Troubleshooting for trailing client clocks | R-DEB-010 and the guide (R-DEB-029): `sqv` clients from about 60 s ("created after the --not-after date … is not signed"), `gpgv` clients from about 70 s ("not valid yet"), derived from the verified limits |

Also from the re-check, recorded as verified on debian.md: `sqv` and `gpgv` read the module's algorithm-22 signatures (single,
co-signed, detached) on Debian 13, Debian 12, Ubuntu 24.04 and Ubuntu 22.04; `circl` v1.6.3 is BSD-3-Clause.

## 9 · WP0 as built (2026-11-08)

WP0 is built on the base 081bd9ae by the builder of the run of 2026-11-08 (evening). What it holds, what differs from section 2 and what the
next packages must know:

**Built.** The contract additions (`NameServer`, `NameQuery`, `RemoteCaps.Names`, the serve-facts kinds `deb_local` and `deb_remote` with
`ServeDebRemoteFacts`; `internal/formats/testdata/contract.txt`, the contract blocks of R-ARCH-008); `items.Publisher`, `GeneratedFile`,
`ErrSuperseded` with a `Store.Publish` stub; `Host.BackgroundUpstream` as a stub; `repository.RegisterCreateHook` and its call in the creation
transaction (`create_hooks.go`; nothing is bound, WP2 binds the Debian hook); the virtual engine's `NameServers` (`virtual.Options.Names`,
`formathost.NewNameServers`, undetermined until WP6) and the shadowing check's call for a format without a package document; the data port of
`internal/formats/deb` (eight group files, `rows`) and its composition in `internal/formatdata/deb` (one stub file per group, `tables_deb.go`,
`jobs.go`, `build.go`, `remote_parse.go`, `refresh.go` with `RegisterRefreshHook`); the migrations; the `deb.*` configuration keys; the OpenAPI
operations, schemas and the generated Go and TypeScript clients (the handlers answer 501, or 503 on a replica without a database:
`api/v1/deb_keys.go` (WP2), `deb_coordinates.go` (WP3), `deb_index.go` and `reindex.go` (WP4), `deb_stub.go` goes with the last of them); the audit
events (four journaled, listed in `cmd/trove/journal_handlers_test.go` as pending); `untrusted.{ar,xz,zstd}`; the format with its local class, route
table, grammars, `Classify`, errors, settings schema, serve check and skeleton `Inspect`; the layer entries of `formats/deb/{control,debfile,version,index}`;
ADR-170 to ADR-172, Q-ARCH-381 to Q-ARCH-383, Q-REPO-270, Q-REPO-272, Q-REPO-273, Q-STOR-180, Q-STOR-182, Q-DEB-44 to Q-DEB-46, H-720.

**Deviations from section 2, decided by the development agent for this run.**

- No `internal/openpgp` and no OpenPGP module or `circl` in `go.mod`: the APT remote builder builds the library in parallel against the API of 2.2.
  The data port and `rows` carry keys as plain data. Only `github.com/ulikunitz/xz` v0.5.17 is added, imported by `untrusted/xz.go`. The layer
  entry of `internal/openpgp` is that builder's.
- No `test/debstub/`; the remote builder builds it.
- **Migration numbers 00060 to 00062, not 00070 to 00072.** The runner refuses numbers with a gap (`parseSteps`: "numbers must run 1..N without a
  gap"), so the migrations take the next free numbers on the base. The development agent renumbers them when another run's migrations land first
  (rename the three files, the comment in `docs/developer/database.md`, the references in `docs/developer/deb.md`).
- `DebMirrorSettings` is not a schema of its own: `deb.mirror` is described inline in `DebSettings`, because the registered-schema equality test
  compares the nested properties. The read-only `upstreamKeyFingerprints` is a `readOnly` property of `DebSettings`, which the equality test leaves out.
- The routes are one per path shape and not one per class (Q-DEB-45): `Match` is class-blind. `deb.dists.get` is `Metadata`, `deb.dists.by_hash` is
  `Download` for every class, and `deb.pool.get` serves the virtual class too.
- The 413 code is `payload_too_large` (Q-DEB-44). The sentence "a build never reads a package file" and every other rule of the spec are unchanged.
- The format declares **no client**, hence no setup templates and no guide: the configure snippet needs the project key for the key file name
  (`/etc/apt/keyrings/trove-<projectKey>-<repoKey>.asc`) and `SetupData` has none. WP3 adds `SetupData.Project` (an addition to the contract and to
  the three places that build `SetupData`: `api/v1/guide_api.go`, `packages_api.go`, the kit), the client `apt`, `setup_local.go` and `guide_local.go`.
- `secrets-reencrypt` walks `deb_signing_keys.private_key` already (`formatdata/deb/reseal.go`, `queries/deb_reseal.sql`, the handler in
  `app/wire_api.go`): registering a sealed column without a walk would have failed the key-ring rotation permanently. WP2 extends `reseal.go` if it
  changes the column; `TestSecretsReencryptTask` now expects two columns.
- The conformance kit gained `Fixture.NoDocument` (a format without a package document) and no longer requires a document path for `MayRead`.
- The fixture of the kit holds no pool path of the form `<package>_<version>_<arch>.<ext>` until WP3 gives the format a rule for the file of a version
  (Q-DEB-46, H-720); the `VersionPather` of the local class is WP3's first design decision.

**What the next packages find.** WP1 replaces `untrusted/ar.go` (`NewAr` and the methods of `ArReader` answer `not_implemented`) and creates the leaves
`control`, `debfile`, `version`, `index`; `Inspect` is `inspect.go` (WP3). WP2 binds `repository.RegisterCreateHook("deb", …)` in the composition root
with `KeyStore.CreateFirstKey`, replaces `keys_store.go` and `api/v1/deb_keys.go`. WP4 replaces `build.go`, `build_store.go`, `Store.Publish`,
`api/v1/deb_index.go`, `reindex.go` and implements `refresh.go` beyond the hooks. WP6 owns the `NameServers` bridge (`formathost/name_servers.go`),
the part `debUpstreamKeysRemoved` of `repository.narrowed` (the parts are free strings in the audit schema: add the constant and the narrowing to
`internal/repository/narrowed.go`), and the purger of the three remote tables for a restore without remote caches (`internal/backup/fixup`, R-BAK-008).
`handlers_*.go` register the handler of a route with `registerHandler(routeName, build)`; a class joins with a `class_*.go` file and `registerClass`.

## 10 · WP2 (library) and WP5 as built

Built on 2026-11-08, stage 1 of the APT remote feature, from base 081bd9ae and independent of WP0. The page
[docs/developer/deb.md](../developer/deb.md) describes both parts; the decisions are Q-DEB-70 to Q-DEB-73 in debian.md.

**WP2, the library only** (`internal/openpgp`; the key store, key API, rotation, journal replay and the creation hook's binding are
not built: they are WP2's next stage, after WP0 is on main).

- The API of section 2.2 as specified, plus `Bits` on `Key` and `Certificate`, `Key.Certificate()`, `Key.SecretArmored()` (the
  unprotected form to seal), `Key.EarliestSignature()` (the time the build waits for, Q-DEB-40), `DefaultLimits()`, the error
  classes `ErrInvalid`, `ErrUnsupportedKey`, `ErrTooEarly`, `ErrVerify`, and `Reason(err)` with the `Reason*` constants (the metric
  label of R-DEB-020). A zero `Limits` field means the bound of R-DEB-034 (Q-DEB-71).
- Tests as the WP2 note asks: algorithm octet 22, version 4, no expiry, creation as passed, the binding self-signature equal to the
  creation (so `BoundSince` = `Created`); `BoundSince` of an imported key whose only self-signature is 30 s old and signing that waits
  for it; import refusals of 2048-bit RSA, algorithm 27, version 6, an expiry, a future creation, a future binding, a signing
  subkey, a revoked key, a missing or wrong passphrase; the verification rules of R-DEB-020 and Q-DEB-35 (unknown key, co-signing,
  SHA-1 and SHA-224, an expired key with a backdated signature, a key created after the signature, a signature 9 and 11 minutes
  ahead, a changed text, the `Hash` header absent, naming another hash and naming two, text before and after the message, 32 and
  33 signatures); the bounds of R-DEB-034 (256 KiB, 32 certificates across blocks, 16 MiB, 64 KiB); Debian 13's archive keyring
  (9 certificates, 75,803 armored bytes) as a fixture, its licence (the package's copyright file: the keys "don't fall under any
  copyright"); three fuzz targets (parsers and verifiers, 15 s each run clean).
- `internal/openpgp` is a leaf in the archtest layer table. The rule "only importer of the OpenPGP module" (Q-ARCH-383) is held by
  convention and review: no archtest rule enforces it yet, and `test/debstub` imports the module on purpose.
- go.mod gains `github.com/ProtonMail/go-crypto` v1.5.2 (direct) and `github.com/cloudflare/circl` v1.6.3 (indirect), pinned once.
- Verified with the real clients, once: `apt-get update --error-on=any` against `file:` repositories signed by this library on
  `debian:13` (`sqv`), `debian:12`, `ubuntu:24.04` and `ubuntu:22.04` (`gpgv`): one key, co-signed with either key as the keyring,
  `Release.gpg` alone, an imported key with a fresh self-signature, and a foreign keyring failing with the key named.

**WP5** (`test/debstub`, `test/debstub/debgen`, Makefile `deb-stub-image` and `DEB_STUB_IMAGE`, `deploy/dev/debstub.yaml`).

- Everything of the WP5 note and of R-DEB-035's list: installable `.deb` files, the suites `stable` and `testing` (also under their
  codenames), `main` and `contrib`, `amd64`, `arm64` and `all`, a `Sources` index, the flat directory and the flat root (requested as
  `/./InRelease`, counted as written), the large index as the suite `bulk`, by-hash for four algorithms, pdiffs from the second
  generation on, validators and 304, and every switch of the list; a control API after `test/ocistub`'s.
- Differences from the note, all in debian.md: the keys have fixed creation dates instead of "an hour back" (Q-DEB-70); `InRelease`
  is framed as `gpg` writes it and there are no xz indexes (Q-DEB-73); the flat directory's `Suite` is `binary` so that APT does not
  warn.
- The stub's tests verify every document it serves with `internal/openpgp` (an independent signer against trove's verifier) and the
  digests of every listed file, by-hash path, `Filename` and source file; a generation, its pdiff (the patch is applied in the test)
  and the history by hash; each switch. Checked once outside the tests: `dpkg -i` of a generated package on `debian:13`; and the
  four client images ran `apt-get update --error-on=any` and `apt-get install` against copies of the stub's tree (suites `stable`
  and `testing` with `contrib`, the flat directory, the flat root), installing and running the packages, and failed on a keyring
  with another key.
- Not done: the manifest `deploy/dev/debstub.yaml` is applied by no e2e step (like the other stubs); the stub has not been run in
  kind; the stub does not serve `Packages.xz`; `make deb-stub-image` was built and the container answered once.

## 11 · WP2 as built (2026-11-08, stage 2)

Built on the base 82034d88 (WP0 landed). The library was stage 1 (section 10); this stage is the key store, the API, the creation hook,
the replay and `signing-key.asc` for the local and virtual classes. Page: [docs/developer/deb.md](../developer/deb.md#signing-keys),
security view [docs/security/deb-signing.md](../security/deb-signing.md), decisions Q-DEB-74 to Q-DEB-77, gap H-735.

- **Built:** `formatdata/deb/{keys_store,keys,keys_journal}.go`, `queries/deb_keys.sql`, `api/v1/deb_keys.go`, `formats/deb/handlers_keys.go`,
  `App.debKeyStore` and `bindDebCreateHook`, `debdata.Options.{Ring,KeysChanged}`, the audit schema of `deb.signing_key.promoted` (the old
  fingerprint is optional, Q-DEB-74), the replay handlers in the trove binary (`journal_handlers_test.go` no longer lists the two events as
  pending), the area `test/integration/debkeys`.
- **Differences from section 2.2:** `Options.Ring` is a new member of the data port's options (the store seals and opens); `formatPorts`
  takes the ring (nil for the restore's item store, which never creates a key). No change to the port interfaces or to `rows`. No migration
  (00061 is complete); the sealed column is unchanged, so `reseal.go` is as WP0 wrote it.
- **Found and fixed on the way:** concurrent creates of a next key answered 500 on the unique index; the repository row is now locked first
  (Q-DEB-75).
- **Not built, for the packages that follow:** the enqueue of a build on a key change (`Options.KeysChanged`, WP4); the remote class's
  `signing-key.asc` and the `debUpstreamKeysRemoved` narrowing (WP6); the UI key panel (WP11); the rotation row on the four client images
  (WP10); `trove admin` commands for keys (none are specified).
- **Not verified:** a restore drill with a real backup (R-BAK-029); the replay is run through its handlers on a live database, not through
  `trove backup restore`.

## 12 · WP13 as built (2026-11-08)

Built on the base 654f926b, after the OCI mirror landed `Artifact` and `Bounded`; WP13 therefore built only the background upstream.

**Built.** `Host.BackgroundUpstream(ctx, repo)` (`internal/formathost/background_upstream.go`) replaces the stub. It reads the repository by its id
(`Options.Repos.ByID`), takes the remote engine from `Options.Engine` (a `MirrorEngine`; otherwise `not_implemented`, like a missing reader),
builds the pass's own `passUpstream` through the shared `MirrorHost.newPassUpstream` (so the egress, credential, `Document`, `Artifact`,
`Get` and `VersionBounds` of a pass are reused, not copied) and wraps it so that every call goes through a `mirror.Window`
(`internal/mirror/window.go`: the pass's throttle without the pass, R-REPO-054) of the run, with the ceiling `proxy.BackgroundShare` (R-REPO-055).
`formathost.WithJobID(ctx, id)` names the job in the upstream log. A wait longer than `mirror.LongWait` ends the run: the call returns
`*mirror.DeferredError{Until}` (`discover.AsRateLimited` finds the answer under it), and so does every later call of that upstream.
Decisions: Q-REPO-305 (a window of its own per run), Q-ARCH-430 (the throttle exported as `Window`); the spec edits are R-REPO-055 (the bullet
"Runs outside a pass") and the applied lines of Q-REPO-272. Developer page: [mirror.md](../developer/mirror.md#background-fetches-outside-a-pass).
The integration build gained `App.FormatHost()` (`internal/app/endpoint_access_integration.go`).

**What the callers find.**

- WP6 and WP8 call `Host.BackgroundUpstream` once per job run and keep the result for the run; a second run starts a fresh window at 1.
- The job handler passes its job id with `formathost.WithJobID`; the host a worker's format is bound with is the mirror host builder's, whose
  engine is the `*proxy.Engine`, so the call works in a worker as in a client-serving process.
- A `*mirror.DeferredError` is not retried by the window: the handler returns it, the queue's retry decides when (`Until` says how long the
  upstream asked to wait). A pre-emption is repeated inside the call after 5 s, a circuit that is open after its `After`.
- `Artifact` returns a gate's refusal (the version is not listed by the document, a mark, a removal) as `*mirror.RefusedError{gate_refused}`
  and found=false only for an upstream 404; the Debian gate of R-DEB-022 should answer a path the release does not list the same way.
- The rate-limit metrics of R-REPO-057 count passes only; a run's answers show in the upstream log with the job's id. Not a requirement of any
  acceptance criterion; recorded in Q-REPO-305.

**Not done or not verified.** No Debian caller exists yet (WP6, WP8), so the contract is proved with the npm format: a pull-through remote with a bearer
credential, the counting npm stub, the format host of a `web` replica. The address policy of the egress rules is shown by the refusal of a host the
repository was not given; the refusal of a loopback address outside `allowCidrs` is the engine's and is covered by the proxy tests. The long wait is
tested with a stub answer of one hour, not with a fake clock in the integration; the unit tests of `Window` use the fake clock. `docs/developer/deb.md`
still calls the method declared; its owner updates the row with the remote class.

## 13 · WP1 and WP3 as built

Built on 2026-11-08, stage 2 of the APT local feature, from base 82034d88 (WP0 as landed). Details: [deb.md](../developer/deb.md).

**WP1.** `internal/formats/deb/version` (dpkg order), `control` (strict deb822 reader and writer, 512 fields, 1 MiB per field),
`debfile` (the `ar` container, the control archive under the bounds of R-DEB-034, the normalized paragraph) and
`internal/formats/untrusted/ar.go` (the real `NewAr`). Fuzz targets: `FuzzCompare`, `FuzzParse`, `FuzzAr`, `FuzzRead`. The crafted inputs
of R-DEB-034 (a control archive that expands to 70 MiB, 1,001 entries, ten `ar` members, 600 fields, a field of 2 MiB, a body that is no
`ar` archive) run through the kit's inspect check against the format.

**WP3.** Upload (`PUT upload/<file>`, 201 or 200 for the same bytes, coordinates added), the rows of a file and its coordinates, the
bounds, deletion with the journaled `deb.package.deleted`, the restore, the replay of both journaled events, `trove admin deb reinstate`,
`PUT …/deb/coordinates`, `GET pool/…`, the serve check, the package screens, the setup (client `apt`) and the guide, `SetupData.Project`
and `Key`, `VersionPath`.

**Differences from the plan, each decided in debian.md.**

- `VersionPath` names the architecture-independent file of the package's own source (Q-DEB-47); H-720 states what that leaves open.
- `PUT …/deb/coordinates` is implemented behind a service of the management API, bound in the composition root, because the API layer
  imports no format (Q-DEB-48). The file list names `internal/app/wire_deb_api.go` besides `internal/api/v1/deb_coordinates.go`.
- A crossed bound is `422 validation_failed` with the field error `out_of_range` (Q-DEB-49).
- `deb_binaries` is detached from `items` by migration 00063 (Q-DEB-50): the item store deletes the item of a permanent removal before
  the format's hook runs, and the hook needs the row.
- `SetupData` gains `Project` and `Key` (Q-ARCH-420).

**Not built here.** The index builder (WP4): `dists/…`, `by-hash` and `signing-key.asc` still answer 501, so no `apt-get update` against
a local repository works yet; the guide and the setup are written for the finished flow. The signing keys (WP2), the remote and virtual
classes. The browser UI. The manual pages (WP12).

**Not verified.** No real `apt-get` client was run against the upload (there is no index to read). The integration tests
(`test/integration/deblocal`) cover the upload, the coordinates with the permission per side, the bounds, the deletion, ownership, and
the store calls of the replay (`Withhold`, `RemoveCoordinatesByJournal`, `ReinstateFile`) against PostgreSQL. They do not run a real
backup and restore, and they do not run the command `trove admin deb reinstate` itself.

## 14 · WP4a (publication port, coalesce change, reindex endpoint) as built (stage 3)

Built on the base after WP13 and WP2. The half of WP4 that needs nothing of WP1; the renderers, the build job, generations, retention and `waitFor`
are WP4b (the APT local builder). Pages: [docs/developer/deb.md](../developer/deb.md#publication-builds-and-the-reindex), [items.md](../developer/items.md#the-publication-port),
[coordination.md](../developer/coordination.md); decisions Q-ARCH-384 (written now, it had been reserved), Q-STOR-210.

- **Built:** `items.Store.Publish` (both classes, `blob.CommitAll`, no event or hook, `modified_at` from the file), the 409 `generated_item` (upload over
  a generated item, delete of a subtree holding one), the coalesce change (`EnqueueSpec.MergeArgs`, `Result.Moved/Merged`, `Queue.Running`), `v1.Reindexer`,
  `ReindexRepository` (202, 409 `reindex_running`, 405 `not_applicable`), `debdata.Store.{EnqueueBuild, Reindex}` with `IndexJobArgs`, and the default
  of `Options.KeysChanged` (an immediate build). The queue is passed to the data port by `formatPorts` when it can answer `Running`.
- **Seams for WP4b:** replace `RunIndexJob` in `build.go`; read `IndexJobArgs` from the job; call `Publish`; call `EnqueueBuild` with `deb.index_delay` from the
  writers of suites. The virtual class's rule that only the port writes generated items (WP8) wraps `Publish`; the port itself writes to any repository.
- **Not built:** the reindex of npm and NuGet rows (R-REPO-016's first rule): those formats answer 405; no audit event for a reindex (the job records
  caller and target); the `trove admin` commands.
- **Verified by test:** the new areas `itempublish` and the coalesce test in `coordination`, the reindex tests in `debkeys`; the areas `files`,
  `coordination`, `ocilocal` and `uploads` once for regressions of the guard and the queue change.

## 15 · WP11 as built

Built on 2026-11-09 on the base e695dce9, in `web/`. Page: [web-ui.md](../developer/web-ui.md#the-debian-screens); decisions Q-DEB-100 to Q-DEB-104 in
debian.md and Q-UI-200 to Q-UI-202 in api-and-ui.md; gaps H-765 and H-766 in [security/deb-signing.md](../security/deb-signing.md).

**Built.**

- The client `deb` -> `apt` in `clients.ts` with the explanations of the four snippet ids and their `-scoped` forms; `snippet-ids.fixture.ts` now lists `deb:apt`
  (the format list of `TestR_API_030_RenderedSnippetIdsMatchTheUIFixture` gained `deb`: the only Go change). The format icon `unarchive`.
- The settings of the format: the settings form got a list control for arrays of strings and a textarea for a long string (Q-UI-200), which is all of
  `DebSettings` (`architectures`, `compression`, `suites`, `components`, `mirror.*`, `upstreamKeys`); the read-only `upstreamKeyFingerprints` is a block under the settings of a remote.
- The tab **Signing keys** (list, generate or import the next key, promote, remove, confirm the retirement of or reinstate a withheld key; with and without
  write rights; no private key is ever shown or kept) and the tab **Index** (suite states, reasons of a failed build in words, the full rebuild) of a local or virtual repository.
- The package detail: architectures, `suite/component` coordinates and the control fields in the Metadata tab.
- `DebApi` (HTTP and fake), 112 i18n units with the German targets, glossary rows, specs for every part, the journey `web/e2e/tests/17-deb.spec.ts`.

**Differences from the work-package row.** The index state panel shows the reason of a failed build and the member codes as the API sends them in `reason`; the API
has no per-member list, so "member failure reasons" are the sentence per code, with the member named behind the colon. No key operation lives in the dialog of the
package; no suite field in the setup dialog (Q-DEB-104).

**Not verified.**

- No panel was run against a live answer of `GET …/deb/index`: its handler was being built in parallel. The panel and its fake follow the generated type and the shape of R-DEB-014 (`reason` as `<code>[: <member>]`).
- The journey `17-deb.spec.ts` was not run (the kind cluster was in use by another builder); it type-checks and passes the lint. Its steps for the key rotation need only the built key API.
- The German texts were written without a native review.
- A real `apt-get` against a repository set up with the rendered snippets is WP10's.

## 16 · WP4b (the index builder) as built (stage 3)

Built on the base with WP4a merged. Pages: [deb.md](../developer/deb.md#the-index-builder-wp4b), [security/deb-index.md](../security/deb-index.md); decisions Q-DEB-51 to
Q-DEB-57, H-721 and H-722.

- **Built:** `internal/formats/deb/index/{packages,release,retention}.go` (pure renderers, retention and the `Date` rule, a fuzz target), the job `deb-index`
  (`Indexer.Build`: snapshot, keys, `Date` with the wait on the database clock, signing, one publication per suite, supersession, drop of an empty suite by a full build),
  `build_store.go` (suites, `WaitPublished`, the snapshot's inputs, `RecordGeneration`, `FailBuild`), `queries/deb_build.sql`, the handlers of `dists/…` and by-hash for the
  local class, `GET …/deb/index` with `waitFor`, the enqueueing from every writer of a change, the task's safety net and retention, the restore's fix-up, and the
  composition root's binding of the builder.
- **Differences from the plan (Q-DEB-56):** the Release lists the checksums of each uncompressed Packages, which APT needs and the plan did not say; each suite is published in its own transaction (Q-DEB-53); the first upload of a suite enqueues the build although it raises nothing (Q-DEB-54);
  retention keeps four generations at least (Q-DEB-51); a build without a key ends without an error (Q-DEB-52).
- **Verified:** the integration tests in `test/integration/deblocal` on the real publication port (signatures with the repository's key, `InRelease` signs `Release` byte
  for byte, `Release` lists every file with matching sizes and digests, the `Date` at least 30 s behind the database time and equal to the `Last-Modified` of `InRelease`,
  a later generation has a later `Date`, earlier generations stay by hash, a deletion and a reindex, `waitFor`, thirty parallel uploads coalesced, a build stopped before its
  commit leaves the previous generation byte for byte, co-signing while a next key exists), and `apt-get update --error-on=any` with `apt-get install` in `debian:13` and
  `ubuntu:22.04` (see the report of the change for what ran).
- **Not built or not verified:** the metric `trove_deb_index_delay_seconds`; the delay-bound test under the reference load (`TestR_DEB_010_DelayBound`, the nightly set `deb-long`);
  the twenty key changes in five seconds and the imported key with a fresh self-signature are covered by the pure test of `PickDate` and by the library's tests, not by an
  integration test; the virtual class (WP8); `Packages.xz` is rendered and tested as bytes, but no client fetched it.
