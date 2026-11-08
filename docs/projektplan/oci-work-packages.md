# T-16 · Docker registry (OCI distribution) — the work-package cut

The plan of task T-16 (docs/projektplan/promt.md section 10), written by the planning agent on 2026-11-07 and kept on the branch `t16-oci`
with WP0. WP0 is built (ADR-161; its review added Q-OCI-24 to Q-OCI-34, which supersede what this plan says on the same points:
the removal cascade, remote media types and aliases, upload-session references, Link by source name, platform on the reference
edge, withheld manifests, the package parameter of a route). Spec: `docs/spec/formats/oci.md`, requirements **R-OCI-001 to
R-OCI-035**, decisions **Q-OCI-1 to Q-OCI-34**, open gaps **H-579, H-580**. The spec is written first; every other page changes in the work package that builds its mechanism, in the
same change as the code (the table "Changes to other pages" of oci.md), because the contract blocks of R-ARCH-008/009 are held
equal to the code by `TestR_ARCH_008_TheWrittenContractEqualsTheCode`.

Working mode (promt.md section 10): plan, build, check by separate agents; no builder checks its own package. Builders do not
commit; the development agent integrates and commits per package (conventional commits naming R-OCI-… IDs, no attribution).

## 1 · Reading the plan against the catalog

Full table with one row per entry: oci.md, section "Catalog coverage". Grouped here:

- **Built in T-16:** PKG-DOCKER-001, 002 (grammar tightened, Q-OCI-3), 003 (strict), 005/006 (path method), 008 (as an endpoint
  with a bound host, R-OCI-029), 011, 012 (token service, Q-OCI-5), 013 (as setup snippets without a credential), 015, 016, 017,
  018, 019, 020 (labels not), 021 (fixed refusal, Q-OCI-8), 022, 023, 025, 026, 027, 031, 033, 034, 035, 038 (plain remote with a
  namespace prefix), 039, 041 (in part), 043, 044 (by nature), 045 (Docker CLI with Buildx, Podman, Kubernetes), 046, 052;
  PKG-OCI-001, 003 (`ns` ignored), 004, 005, 006 (all four categories; `?tag=` not), 007, 008 (registry side), 009, 010, 011 (not
  needed), 013, 014 (multi-platform push), 019 (Docker, Podman).
- **Stays in F4** (Q-OCI-2): PKG-DOCKER-014, 028, 029, 030, 032, 036, 040, 041 (layer history), 042, 045 (Kaniko, OpenShift);
  PKG-OCI-002, 006 (`?tag=`), 012, 015, 016, 017, 018 (F5), 019 (other clients), 020; Cosign beyond referrers; OCI on endpoints
  with exposure rules and through the Bastion (Q-OCI-23).
- **Not offered:** PKG-DOCKER-004, 007 (Q-EP-76), 009, 010, 024, 037 (the URL's namespace prefix covers it), 047, 048, 049,
  050, 051.

**FULL.md F4 status text** (written by the closing change, not now; the phase sets F4 to 🟡 when building starts):

```markdown
### F4 · OCI and Docker (container images)
- 🟢 Spec page `formats/oci.md` (one format `oci` for Docker and OCI content, Q-OCI-1)
- 🟢 Registry API under `/v2/` (all four categories of the distribution specification, conformance suite v1.1.1), token service, chunked uploads across replicas
- 🟢 Local, remote (Docker Hub and other registries; `pullThrough` and `mirror`), virtual; multi-architecture images; referrers; deletion and the sweep of unreferenced blobs; the Docker Engine mirror of a bound host
- 🟡 Clients: Docker CLI with Buildx multi-platform push, Podman and Kubernetes pulls built; BuildKit registry caches, ORAS and Cosign beyond referrers stay
- ⚪ WebAssembly modules as OCI artifacts (`wasm-to-oci`)
- ⚪ Image vulnerability scanning (the analyser is decided on oci.md when this item starts)
- ⚪ Tag caps and kept tag revisions (with the repository caps), promotion, foreign layers, tag resolution by push time, labels and layer history, exposure rules and the Bastion for the format
- 🟢 Integration tests (protocol over HTTP, the counting stub registry, the conformance suite)
- 🟢 E2E tests: push and pull with Docker and Podman through local, remote (both modes) and virtual repositories on several replicas, an upload interrupted by a drain (nightly set `oci-long`), a mirror pass on n replicas
```

## 2 · Contracts first: WP0

One package, one builder (`voltagent-lang:golang-pro` with `voltagent-qa-sec:architect-reviewer` checking), before every other
package. It writes the contract additions, the schema and the configuration keys, with no behaviour behind them beyond
compile-time stubs (methods answer `errs.Unsupported`/501, as the NuGet skeleton did, Q-NUGET-32), so that WP1 to WP11 build
against fixed types in parallel. One ADR: **ADR-161 · The format contract gains the registry root, the upstream handshake, HEAD
revalidation, plan artifacts and upload sessions** (additions keep `ContractVersion`, R-ARCH-025; the surface file changes; the contract is at 3 since the upload sessions' Finalize took an offset, ADR-178).

### 2.1 Go contract (`internal/formats`, `internal/formats/catalog`, `internal/mirror/discover`)

```go
// catalog.Facts — additive facts
RegistryRoot bool             // ProtocolBasePath is "/v2/{project}/{key}/" and "/v2/" is the bound-host prefix; at most one format (R-OCI-001, Q-OCI-4)
DefaultCredentialHosts []string // DefaultExtraHosts entries proposed with sendCredentials: true (R-OCI-021); each must also be in DefaultExtraHosts

// formats.Validate: a format with routes has ProtocolBasePath "/api/<id>/{project}/{key}/", or "/v2/{project}/{key}/" when RegistryRoot.
// formats.Register: a second RegistryRoot format panics.

// Refusal gains the base template so Registry() renders BaseURL + qname.ProtocolBase(Base, Name) (same output for /api formats).
type Refusal struct { Format ID; Name qname.Repo; CacheView bool; BaseURL string; Base string }

// SetupData gains ImagePrefix: "<host>/<projectKey>/<repoKey>" for the RegistryRoot format, "" otherwise (R-OCI-030).

// Optional interfaces of a RemoteAdapter (found by CapabilitiesOf; RemoteCaps gains Auth and Revalidate)
type Authenticator interface {
	// TokenRequest reads an upstream 401 for path p. ok false: no handshake, the 401 stands. key names the token in the
	// replica's cache (with the repository id and its config version). The engine adds the credential by the host rule of
	// R-REPO-033, sends req through the repository's upstream client, and retries the original request once.
	TokenRequest(ctx context.Context, rc RemoteContext, orig *http.Request, p Path, status int, challenge http.Header) (req *http.Request, key string, ok bool, err error)
	// ReadToken reads the realm's answer (the engine bounds it at 64 KiB): the Authorization value to retry with and its
	// lifetime (0: 60 s).
	ReadToken(ctx context.Context, rc RemoteContext, status int, body []byte) (authorization string, ttl time.Duration, err error)
}
type Revalidator interface {
	// RevalidationRequest returns the request (OCI: a HEAD) for an expired stored document; ok false: the engine's conditional GET.
	RevalidationRequest(ctx context.Context, rc RemoteContext, p Path, variant string, stored RemoteDocument, get *http.Request) (req *http.Request, ok bool, err error)
	// Unchanged reads its answer: true renews the stored document's freshness without a body; false makes the engine fetch it.
	Unchanged(p Path, stored RemoteDocument, resp *http.Response) bool
}
// RemoteDocument must expose the stored document's SHA-256 and media type (add the fields if absent).

// discover.Upstream — additive method
// Artifact fills an artifact of the repository through the format's gate and the remote engine as Host.Fill does for a
// plan item (a background request of the pass, counted as an item it stored), and returns its stored bytes when they are at
// most max. found is false when the upstream does not have it.
Artifact(ctx context.Context, path string, max int64) (body []byte, found bool, err error)

// Host additions (R-ARCH-009)
// Ingestor.Link commits an item for a blob that is already stored (R-STOR-008 deploy by checksum): mount, the link step of a
// virtual push, the tag item of a push by tag whose manifest exists. Refused when the blob is not live.
Link(ctx context.Context, rq *Request, sha256 [32]byte, size int64, c Commit) (Committed, error)
// Uploads is the multi-request upload session port of R-STOR-011.
Uploads() UploadSessions

type UploadSessions interface {
	Start(ctx context.Context, rq *Request, target RepoRef, binding string) (UploadSession, error) // binding: the OCI image name
	Get(ctx context.Context, rq *Request, id uuid.UUID) (UploadSession, error)                     // principal, addressed repo, target, binding checked → errs.NotFound
	Append(ctx context.Context, rq *Request, id uuid.UUID, offset int64, body io.Reader) (received int64, err error) // offset ≠ received → *errs.Error 416 with Range
	Finalize(ctx context.Context, rq *Request, id uuid.UUID, sha256 [32]byte, last io.Reader, c Commit) (Committed, error)
	Abort(ctx context.Context, rq *Request, id uuid.UUID) error
}
type UploadSession struct { ID uuid.UUID; Target RepoRef; Binding string; Received int64; PartSize int64 }
```

Commit rule to respect (existing contract): "a companion goes with a lead that changed". A push by tag therefore commits the
**tag item as lead** and the manifest item as companion, or links the tag with `Ingestor.Link` when the manifest is stored;
a push by digest has the manifest as lead. WP5 owns the choice; WP0 documents it in R-ARCH-009.

### 2.2 Schema (`internal/meta/postgres/migrations`, new and therefore editable until merged)

- `00054_upload_sessions.sql`: the DDL of R-STOR-011, plus `addressed_repo_id bigint NOT NULL REFERENCES repositories (id) ON
  DELETE CASCADE` and `binding text CHECK (length(binding) <= 512)`; export class `transient` (tables_uploads.go).
- `00055_oci.sql`: `oci_manifests`, `oci_references` exactly as R-OCI-007; export class `data` (tables_oci.go); sqlc queries in
  `internal/meta/postgres/queries/oci.sql` (WP5 fills them; WP0 creates the file with the reads the data port declares).
- `00056_key_grammar.sql`: the `CHECK` of `repositories.key` and of the project key to the grammars of Q-OCI-3.

### 2.3 Configuration and API

- Configuration keys (R-ARCH-020, `internal/config`): `oci.token_lifetime` (300s, 60s–3600s), `oci.unreferenced_blob_grace`
  (24h, > `storage.uploads.session_max_age`), `storage.uploads.{part_size,writer_lease,session_ttl,session_max_age}` (R-STOR-011
  defaults; startup refuses `session_max_age` ≥ `storage.orphans.multipart_max_age`).
- OpenAPI (`api/openapi.yaml`): `OciSettings` (`mirror.tags`, `mirror.excludeTags`, `mirror.platforms`, local/virtual empty),
  the `oci` member of the repository document, `defaultCredentialHosts` in the format entry; the generated enums follow the
  registry (`TestR_ARCH_010_PerClassFormatEnums`); `make generate` for the Go and TypeScript clients.
- The format's JSON settings schema `internal/formats/oci/settings.schema.json` (defaults of R-OCI-026).
- Chart: no new edge route (`/v2` already goes to `packages`, R-HA-037); no new values. Dev only: see WP11.

### 2.4 Skeleton of the format

`internal/formats/oci/format.go` (Descriptor with `Classes: local` only until WP7/WP8 register remote and virtual, as
Q-NUGET-28 did), `routes.go` with every route of R-OCI-009 to R-OCI-018 answering 501 `UNSUPPORTED`, `names.go` (the grammars of
R-OCI-002, unit-tested), `paths.go` (layout and `Classify`), `errors.go` (`WriteError` per R-OCI-008), the data port interface
`store.go` (`Store`: manifest rows, references, referrers page, tag list page, catalog page, sweep candidates) and its empty
implementation `internal/formatdata/oci/store.go`; registered in `internal/formats/all/all.go`; conformance kit green.

### 2.5 Spec edits of WP0 (same change)

R-ARCH-005 (`/v2/` row, route-table rule), R-ARCH-008 and R-ARCH-009 Go blocks, `internal/formats/testdata/contract.txt`,
README INV-5/INV-6, R-REPO-002, R-AUTHZ-026 (key grammar), storage.md R-STOR-011 (columns), decisions.md ADR-161 and the table
rows of Q-OCI-1 to Q-OCI-23.

**Tests of WP0:** contract spec test, surface test, `TestR_ARCH_010_*` (kit, enums), key grammar unit tests against the
distribution specification's regular expression (R-OCI-002), migration test (guard/release triggers not needed: no blob
column), `make generate-check`, `make lint`.

## 3 · Work packages after WP0

Every package lists: owner subagent type, requirement IDs, owned files (disjoint unless noted), contracts it consumes, tests, and
whether it needs the integration stack (shared `trove-it-*`, never `integration-down/up`) or the kind cluster (one agent at a time).

| WP | Subject | Agent | Requirements | Owned files | Depends on | Stack | kind |
|---|---|---|---|---|---|---|---|
| WP1 | Upload sessions | `voltagent-lang:golang-pro` + `voltagent-data-ai:postgres-pro` | R-STOR-011, R-OCI-011 (storage side), R-OCI-032 | `internal/uploads/` (new), `internal/formathost/uploads.go`, `internal/blob/gc*.go` (session expiry in the collection run), `internal/blob/` orphan sweep for `_uploads/`, `internal/meta/postgres/queries/uploads.sql` | WP0 | yes | no |
| WP2 | Registry tokens and the token service | `voltagent-infra:security-engineer` + `voltagent-lang:golang-pro` | R-OCI-004, R-OCI-005, R-OCI-006 (grant), R-OCI-034 (token metrics/event) | `internal/auth/registrytoken/` (new: form, HKDF from the key ring, scope grammar, grant via the decision point's coarse check), `internal/auth/stage.go` (accept `trv_rgt_` on `/v2/`, no cookie there), `internal/api/registry/token.go` | WP0 | yes | no |
| WP3 | Registry root and dispatch | `voltagent-core-dev:backend-developer` | R-OCI-001 (mount), R-OCI-003, R-OCI-017, R-OCI-029 | `internal/api/dispatch/` (mount by `ProtocolBasePath`, bound-host dispatch with the repository fixed), `internal/api/registry/{register,ping,catalog,challenge}.go`, `internal/httpserver/routetable*.go`, `internal/app/wire_api.go` (registration) | WP0; WP2 for the challenge text (shared constant in `registrytoken`) | yes | no |
| WP4 | OCI format, local class: manifests, pulls, tags, referrers, deletion, sweep, view, setup | `voltagent-infra:docker-expert` + `voltagent-lang:golang-pro` | R-OCI-006 to R-OCI-010, R-OCI-013 to R-OCI-020, R-OCI-030, R-OCI-031, R-OCI-033 | `internal/formats/oci/` except `handlers_upload.go`, `remote*.go`, `virtual*.go`, `mirror*.go`; `internal/formats/oci/manifest/` (parser), `internal/formats/oci/tagorder/` (Q-OCI-17), `internal/formatdata/oci/` (the store, the job `oci-sweep` in `sweep.go` using the item store and the job queue directly as NuGet's symbol-index job does, and `journal_replay.go`: the replay handler of `oci.manifest.deleted` over `oci_tombstones`, R-OCI-033, Q-OCI-30; WP0 registered the event and built the table), `internal/meta/postgres/queries/oci.sql` | WP0 | yes | no |
| WP5 | OCI uploads and mount | `voltagent-infra:docker-expert` | R-OCI-011, R-OCI-012 | `internal/formats/oci/handlers_upload.go`, `handlers_mount.go` | WP0, WP1 (port), WP4 (routes file is WP4's: WP5 adds its handlers through a registration function WP0 declares) | yes | no |
| WP6 | Remote engine: handshake, HEAD revalidation, credential hosts, quota header (the media-type column, `RemoteDocument.MediaType` and `Prepared.Aliases` were built by WP0) | `voltagent-lang:golang-pro` | R-OCI-021 (engine side), R-OCI-022 (engine side), R-OCI-025 (parser), R-REPO-033/054 edits | `internal/proxy/` (`authenticator.go`, `revalidate.go`, the token cache per replica, `upstream/` rate-limit parse of `0;w=`), `internal/formathost/remote.go` (wire `RemoteCaps.Auth/Revalidate`), `internal/repository/validate_remote.go` (`DefaultCredentialHosts`) | WP0 | yes | no |
| WP7 | Counting stub registry | `voltagent-qa-sec:test-automator` | R-OCI-035 (stub) | `test/ocistub/` (new), Makefile pin `OCI_STUB_IMAGE`, `deploy/dev/ocistub.yaml` | WP0 (names only) | no | no |
| WP8 | OCI remote adapter | `voltagent-infra:docker-expert` | R-OCI-021 to R-OCI-025 | `internal/formats/oci/remote*.go` (`UpstreamRequest` with the namespace prefix and `library/`, `Accept`, `AcceptResponse`, `PrepareFill` digest checks, `FillInspector`/`OnFill` rows, `BeforeArtifact`, `Authenticator`, `Revalidator`, tags list document, referrers pass-through), registers class `remote`; its `TokenRequest` takes the scope from `orig` and `p` and only the realm and the service from the challenge (Q-OCI-82); the upstream test of an OCI remote (R-REPO-024) runs no handshake: WP8 sets the probe path (`v2/`) and decides what it reports, recommended: a `401` with a Bearer challenge is "reachable, token handshake required" and does not read as `authenticated: false` | WP4, WP6, WP7 | yes | no |
| WP9 | OCI virtual adapter | `voltagent-infra:docker-expert` | R-OCI-027, R-OCI-028 | `internal/formats/oci/virtual*.go` (tag order by member, digest tiers, union lists, the link step via `Ingestor.Link`, writes to the deploy target), registers class `virtual` | WP4, WP5, WP8 | yes | no |
| WP10 | Mirror | `voltagent-lang:golang-pro` + `voltagent-infra:docker-expert` | R-OCI-026, Mirror discovery, R-REPO-050/051 edits | `internal/formats/oci/mirror*.go` (lister: `listing`, `names`, plan with tags/platforms/attestations), `internal/mirror/policy.go` (`SelectItems` counts versions), `internal/formathost/mirror_host.go` (`Artifact`), `internal/repository/validate_mirror.go` (`<format>.mirror` restricted), `test/integration/mirror/oci_test.go` | WP6, WP7, WP8 | yes | no |
| WP11 | e2e harness and CI | `voltagent-qa-sec:test-automator` + `voltagent-infra:kubernetes-specialist` + `voltagent-infra:devops-engineer` | R-OCI-035 (e2e), R-OCI-032 (scatter, Valkey stopped), R-OCI-011 drain, R-REPO-058/059 for OCI | `test/e2e/oci_*_test.go`, `test/e2e/ociclients/` (Dockerfile or pinned images: `docker:<v>-dind` with Buildx, `quay.io/podman/stable:<v>`; Makefile pins and `check-pins`), `deploy/dev/oci-clients.yaml` (namespace `trove-e2e-clients` with Pod Security `privileged`, dev CA in `certs.d`), `deploy/dev/values-*.yaml` additions (a bound host for the Docker Engine mirror), `deploy/dev/kind.yaml` only if the node's containerd needs `config_path` for the Kubernetes pull row, `test/e2e/mirror_*` additions, `.github/workflows/release.yml` (set `oci` per change; closing-run legs), `.github/workflows/performance.yml` (`oci-long` nightly) | WP3, WP4, WP5, WP7, WP8, WP9, WP10 (rows join as their packages land) | — | **yes** |
| WP12 | UI | `voltagent-lang:angular-architect` | R-OCI-030 (snippets UI), R-OCI-031 | `web/src/app/features/setup/clients.ts` (`oci`: docker, podman; snippet explanations), `web/src/app/shared/format-icon.ts`, the package detail's platform table, i18n units, specs; a browser journey step for the package screen | WP4 (view API), WP0 (OpenAPI) | no | for the journey only (WP11's turn) |
| WP13 | Conformance suite and integration tests | `voltagent-qa-sec:test-automator` | R-OCI-035 (integration, conformance) | `Makefile` (`oci-conformance`, pin `OCI_CONFORMANCE_TAG := v1.1.1`), `test/integration/oci/` (new: protocol criteria across replicas, `TestR_AUTHZ_007_OCIActionMap`, crafted inputs, Valkey stopped, conformance runner through local and virtual addresses), CI integration leg | WP2, WP3, WP4, WP5 (local), WP8, WP9 (remote/virtual rows) | yes | no |
| WP14 | Documentation | `voltagent-dev-exp:documentation-engineer` | all (docs) | README (naming tables: image reference, `/v2/` routes, `trv_rgt_`, `oci.*` keys; TL;DR row for Docker), `docs/developer/oci.md` (new subsystem page), `docs/developer/{README,formats,e2e,build-and-test}.md` rows, `docs/security/oci-registry.md` (token service, scopes, key ring, H-579) and `docs/security/oci-remote.md` (handshake hosts, credential hosts), `docs/manual/user|admin` pages for Docker/Podman and Docker Hub remotes and mirrors, FULL.md F4 status (closing change), coverage regenerated | WP4 to WP11 as they land | no | no |

Notes per package:

- **WP1** builds R-STOR-011 completely (it was P1 and unbuilt): claims, parts, tails, finalize states, expiry in the collection
  run, orphan sweep, `OCI-Chunk-Min-Length`. Its integration tests are R-STOR-011's acceptance criteria (three replicas pinned per
  request, the fault-injection build, concurrent `PATCH`, the cut finalize, expiry, another principal → 404). The drain criterion
  with a 2 GiB `PATCH` runs in WP11's `oci-long`.
- **WP2** spec edits: identity R-AUTH-001, R-AUTH-002, R-AUTH-022, R-AUTH-041; authorization R-AUTHZ-007 (OCI table). Tests:
  form and MAC unit tests, the query-counter test (no DB query for a forged token), revocation within the R-AUTH-023 bound on two
  replicas, key-ring rotation, `POST` form, anonymous token on an endpoint with and without `auth.anonymous`.
- **WP3** spec edits: architecture R-ARCH-005 rows; endpoints R-EP-007 pointer. Tests: route table, 404 `NAME_UNKNOWN` shapes,
  `Docker-Distribution-API-Version`, bound host on a test endpoint, catalog filtering with a package-scoped token.
- **WP4** is the largest; split by files if two builders run it (`handlers_manifest.go`+`manifest/` vs
  `handlers_tags.go`/`handlers_referrers.go`/`handlers_delete.go`/`sweep.go`/`view.go`/`setup.go`/`guide/`), with
  `isolation: "worktree"` and a merge. It owns `routes.go`. The manifest parser gets fuzz targets (kit `fuzz.go`).
- **WP6** spec edits: R-REPO-033 (realm credential rule), R-REPO-054 (quota header parse), architecture R-ARCH-032 row (upstream
  tokens per replica). Tests against a generic test upstream in `internal/proxy/proxytest` (no OCI knowledge needed).
- **WP7** stub: deterministic set generated in code (no recorded Docker Hub responses); hosts: registry, realm, blob CDN; switches
  as R-OCI-035 lists; a control API like `test/nugetstub`'s.
  Built (see `docs/developer/oci.md`, "The counting stub registry"). Hand-offs: `deploy/dev/e2e.sh` wiring (marker, `TROVE_E2E_OCISTUB`,
  loading the image, exporting the three host names) is WP11's; the in-process hosts are `registry.localhost`, `auth.localhost` and
  `cdn.localhost` (valid in `extraHosts`, whose grammar cannot name an IPv6 literal). They are not resolvable: WP8's tests feed
`run.DialOverride()` into `app.Options.DialOverride` (and allow the listener's loopback address in the egress policy) and list
`auth.localhost` and `cdn.localhost` in `extraHosts`; `run.DialContext` serves a Go client; the mirror criterion `linux/arm64` is `linux/arm64/v8` in the stub's `acme/b` (WP10 decides whether the platform
  filter matches variants); the fixtures `app:1`, `library/app:1` and `acme/tool:1` are in the stub (WP9 for R-OCI-027).
- **WP8** confirms Docker Hub's current blob redirect hosts with one anonymous request against Docker Hub (curl, no trove) and
  writes them into R-OCI-021 and the Descriptor; records the check in the commit message.
- **WP10** spec edits: R-REPO-050 (OCI can mirror; `<format>.mirror` restricted), R-REPO-051 (versions counted per version). The
  `SelectItems` change keeps every npm/NuGet mirror test green unchanged (one item per version there).
- **WP11** e2e sets: `oci` (per change, scatter profile, three `packages` replicas), `oci-long` (nightly + dispatch: drain during a
  push; R-HA-052 style), `mirror` += OCI pass on the two-worker pool with clients pulling, `mirror-long` += drain during an OCI
  pass; closing run: `oci` with Valkey stopped, Docker Hub read-only (≤ 6 manifest GETs). Rows: login (both clients), push/pull
  local, pull through remote `pullThrough` (stub), pull through remote `mirror` after a pass with the stub stopped, pull and push
  through the virtual repository, multi-platform with Buildx, `docker manifest`, Podman manifests, anonymous pull, Kubernetes pod
  with an image pull secret from the virtual repository, Docker Engine `registry-mirrors` on a bound host, rendered setup
  snippets.
- **WP13** conformance: `go mod download github.com/opencontainers/distribution-spec/conformance@v1.1.1` into `.out/`, `go test -c`
  there with the repository's Go (`GOTOOLCHAIN=local`), run with the variables of R-OCI-035; JUnit to `.out/`; the CI leg uploads
  it. No skip beyond the documented switches (hard rule 6).

## 4 · Order and parallel waves

```text
WP0 ──┬─ WP1 ─────────────┐
      ├─ WP2 ── WP3 ───────┤
      ├─ WP4 ──┬─ WP5 ─────┼─ WP9 ──┐
      ├─ WP6 ──┤           │        ├─ WP13 ── WP11 (kind) ── WP14 (closing docs)
      └─ WP7 ──┴─ WP8 ─────┴─ WP10 ─┘
                 WP12 after WP4 (parallel to WP8–WP10)
```

- Wave 1 (after WP0): WP1, WP2, WP4, WP6, WP7 in parallel (disjoint directories; WP4 is the only writer in `internal/formats/oci/`
  besides later WP5/WP8/WP9/WP10 files).
- Wave 2: WP3 (after WP2), WP5 (after WP1+WP4), WP8 (after WP4+WP6+WP7), WP12.
- Wave 3: WP9 (after WP5+WP8), WP10 (after WP8), WP13 (local rows after wave 2, remote/virtual rows after WP9).
- Wave 4: WP11 on kind (only agent using it), then the verifier on kind; WP14 closes.
- WP14 writes docs incrementally per package where the package's builder did not (rule: docs in the same change); its own
  pass at the end covers README, manual and FULL.md.

## 5 · Checks (separate agents, promt.md section 10 step 3)

- `voltagent-qa-sec:architect-reviewer` on WP0 (contract, ADR-161, schema) before wave 1 starts; again on WP3/WP6 seams.
- `voltagent-qa-sec:code-reviewer` on every package's diff.
- `voltagent-qa-sec:security-auditor` on WP2 (tokens, key derivation, stage), WP3 (catalog filtering, bound host), WP6/WP8
  (credential hosts, handshake), WP5 (mount disclosure); `voltagent-qa-sec:penetration-tester` on WP4's parser and WP8's
  fill path (crafted manifests, wrong digests, oversized token answers, realm on a foreign host).
- A verifier that runs the real clients against the chart on kind (WP11's sets) and reads every failure back to a builder.

## 6 · Exit criteria and their evidence

| Criterion | Evidence |
|---|---|
| Conformance suite v1.1.1, all four categories, local and virtual address | CI integration leg, JUnit artifact, `make oci-conformance` output |
| Every R-OCI acceptance criterion at its tier | test names `TestR_OCI_0NN_…`, coverage regenerated |
| e2e `oci` green per change, scatter profile | Test and Release run of the closing commit |
| Upload interrupted by a drain | `oci-long` in `HA and Performance (nightly)`, dispatched on the closing commit |
| Mirror pass on n replicas | e2e `mirror` (OCI rows) per change; `mirror-long` (drain) nightly |
| Valkey stopped, Docker Hub read-only check | closing run legs |
| White-label scan empty, no attribution | scan output, `git log` check |

## 7 · Open points for the development agent

- **ADR-161** is written by WP0 (decisions.md is not touched by this planning change). The decision rows of Q-OCI-1 to
  Q-OCI-23 go into decisions.md's table in the same change.
- **Global key grammar (Q-OCI-3)** changes keys that are valid today; the owner's installation on `wds18-k8s-main` must not hold a
  key like `npm-` before the migration runs (pre-alpha rule: break freely, but name it to the owner in the report).
- **Privileged client pod** (Q-OCI-21): if the dev cluster's admission refuses a namespace with Pod Security `privileged`, WP11
  falls back to option (b) of Q-OCI-21 and records the deviation on oci.md.
- **Docker Hub figures** are not written anywhere (they change); the closing run's Docker Hub check stays anonymous and below six
  manifest `GET` requests.
- The catalog entries kept in F4 are recorded on oci.md (Deferred) and in FULL.md F4's status text above.


## WP0 as built (additions after the review)

- Built in WP0 beyond the first cut: `ItemsEvent.Cascade` with the system actor `removal`; `Ingestor.Link(from ItemRef)` with the host
  side (`items.Store.CommitLinked`, `blob.Store.LinkExisting`); `SessionRef` on every `UploadSessions` call; `Route.PackageParam`,
  `EndpointFacts.Bound`, `LinkBuilder.ClientName`; `remote_cache_entries.media_type` and `Prepared.Aliases` in the engine's fill
  transaction (`items.Store.LinkIn`); `oci_tombstones`, the event `oci.manifest.deleted` (journal flag, no handler yet);
  `upstream_rate_limited`; key grammar folded into the composite-name checks.
- Left to the packages: WP3 sets `EndpointFacts.Bound` and mounts `/v2/` (R-ARCH-005, INV-6 edits); WP1 builds `UploadSessions`
  (it decides principal, binding and `deploy` on the target on every call); WP4 builds the replay handler, the sweep and the hook that
  calls `Cascade`, `PutTombstone` and records the event; WP6/WP8 call the engine's handshake with `TokenRequest(orig, p, ...)`.

- Second review round: `Ingestor.Link(from SourceRef)` resolves the source by repository name (WP5 uses it for the mount, WP9 for the link
  step); the serve-facts kind `oci_local` and `Format.ServeCheck` are built (WP4 fills the store); WP4 builds the replay handler (it
  WITHHOLDS: writes the mark, deletes nothing), `trove admin oci reinstate <p>/<r> <name>@<digest>` (event `oci.reinstate`, not journaled)
  and the 409 `DENIED` of a push of a withheld digest; a remote's manifest rows are removed by the hook with the alias item
  `<name>/_manifests/<digest>` (WP8); a hook's cascade follows the event's reason once the trash can exists.

## WP2 as built

- Registry tokens (`internal/auth/registrytoken`): the form with the key ID before the claims (Q-OCI-54), the binding to the trove token
  by ID, type and the SHA-256 of its secret, verified through the token cache (Q-OCI-50, ADR-163), HKDF keys from the key ring
  (`secrets.KeyRing.Derive`), the configured lifetime and the issue time checked at verification, the scope grammar (a cache view under
  its base repository), the challenge text, the metric. The stage accepts `Bearer trv_rgt_` below `/v2/` only (escaped and unescaped
  path) and never the session cookie there; both token paths share `Stage.accept`; `/v2/_token` is not authenticated by the chain, the
  token service presents its credential through `Stage.AuthenticateTokenRequest` (Q-OCI-51); the form follows the Basic switch
  (Q-OCI-57). `internal/api/registry`: the token service (400 in both shapes, Q-OCI-52; `auth.registry_token.denied` deduplicated per
  address, endpoint, reason and credential, Q-OCI-53), the grant decision (`Grantor`, `ActionOf`, Q-OCI-55) and the enforcement point
  `Enforcer` - `Mount` the only way below `/v2/`, the route table admitting only its patterns, the dispatcher's own match
  (`Dispatcher.RegistryTarget`), the code `oci_insufficient_scope` - with `registrytest.Walk` through the whole handler (Q-OCI-56). Gap H-595 (bearer for the lifetime); H-579 narrowed (the forger needs a live
  token's hash, and a minted token verifies for at most the configured lifetime).
- Left to WP3 (done, see "WP3 as built"): the enforcer per endpoint in `rt.registry`, every `/v2/` handler through `Mount`, the
  dispatcher's `/v2/` handler routing by the enforcement point's match, the walk through the app's handler, the token service's route
  and wiring, `Bound` restricted to repositories of the format, the stage's `RegistryScope`, the version check's own challenge. Left to
  WP1/WP3/WP5: the upload-session contract in the developer page (every session call checks repository, image name and principal).
  The mount's source is checked by the enforcer since WP3 (Q-OCI-102); what is left is the replacement of the copy `ociMountSource` by
  `ociformat.MountSource` and the integration mount test, by whichever of WP3/WP5 merges second. Left to WP13: `TestR_AUTHZ_007_OCIActionMap`.
  The tests of the token service name their R-OCI requirement in a comment.
- Touched outside the owned files: `internal/auth/auth.go` (`Credential.Registry`), `internal/auth/token/validate.go`
  (`CheckSecretHash`), `internal/secrets/keyring.go` (`Derive`), `internal/httpserver/principal.go` (`RegistryGrant`),
  `internal/app/{m1_convert,wire_identity,areas,wire_endpoints,app}.go`, `internal/api/dispatch/registry_target.go` (new),
  `internal/httpserver/routetable.go` (`PatternPath` exported; `CheckRouteTable` takes the registry's and the host-root patterns as two
  lists), `internal/archtest/{imports,layers}.go` (the dispatcher's transitive ban, the leaf `internal/api/registry/route`),
  `internal/errs/codes.go` (`oci_insufficient_scope`), `internal/audit/{events_registry_token,dedup}.go`,
  `internal/config/{oci,auth_identity}.go` and `schema.json` (descriptions), `test/integration/identity/env_test.go`
  (`replicaOpts.dbOpts`).

## WP4a as built (manifests, pulls and the manifest push path)

- Built: `internal/formats/oci/manifest/` (the strict parser, [Q-OCI-60](../spec/formats/oci.md#q-oci-60--a-manifest-is-read-by-a-strict-reader-that-refuses-what-two-readers-could-interpret-differently)),
  `internal/formats/oci/tagorder/` (the tag order of Q-OCI-17, which WP4b and WP10 reuse), the handlers `handlers_manifest.go` and `handlers_blob.go`, the
  hook `hooks.go` (`OnCommit`; `OnItemsRemoved` is the deletion's, in its own file), `view.go`, the stub file `handlers_wp5_stub.go` (`handlers_wp4b_stub.go` went with WP4b)
  (a package deletes its file), the data-port methods of `internal/formatdata/oci/store_manifests.go` and `queries/oci_manifests.sql`.
- The data port `Store` gained `ExistingItems`, `WithheldAmong`, `TagViews`, `StoredTags`, `WithheldVersions`; `PutManifest`, `Manifest`, `References` and
  `Tombstone` are implemented. The error mapping maps the kind `conflict` to `DENIED` (409).
- Helpers other files may reuse (all in `handlers_manifest.go`): `storeOf`, `portOf`, `errDigestInvalid`, `errTagInvalid`, `errUnsupported`, `ioError`,
  `serveStored`, `checkReferences`.
- The integration test of the routes mounts the format at `/api/oci/` in `test/integration/files/oci_host_test.go` until WP3 mounts `/v2/`; WP13 moves the
  protocol criteria to `test/integration/oci` over `/v2/` (done, see "WP13 as built").

## WP4b as built (tags, referrers, deletion, sweep, replay, reinstate, setup)

- Built: the tags list, the referrers API, `DELETE` of tags, manifests and blobs, the hook `OnItemsRemoved` with the cascade, the refusals and
  `oci.manifest.deleted` (journal entry `{repository, name, digest, actorId}`), the job `oci-sweep` (hourly, `maintenance.tasks`,
  `trove_oci_sweep_blobs_removed_total`), the replay handler (withholds), `trove admin oci reinstate` with `oci.reinstate`, the clients
  `docker` and `podman` with their setup snippets and the guide, `Store.CatalogNames` (the page of one repository
  for the catalog of the registry root, WP3). ADR-168, Q-OCI-70, Q-OCI-71, Q-OCI-73 to Q-OCI-75, H-606, H-607, H-643, H-644 (`docs/security/oci-content.md`).
- Aligned with WP4a (merged): the tag order is WP4a's `tagorder` (Q-OCI-64, the package view); the tags list is in byte order and does not use it.
  `store_removal.go` holds `RemoveManifest` and `ReferencedBy`; `registryError` maps a conflict to `409 DENIED`; the conformance fixture and the
  twin carry the list and delete side (`store_twin_test.go` adds methods to WP4a's `twin`).
## WP3 as built (the registry root and its dispatch)

- Everything below `/v2/` is two patterns on one handler, `/v2` and `/v2/{rest...}`, mounted by `registry.Register` through the enforcement point
  (`internal/api/registry/register.go`, `wire_registry.go`). The dispatcher's `RegistryHandler` matches with `registryMatch` - the
  function `RegistryTarget` is a reduction of, so the enforcement point and the handler cannot read a path differently - and serves the
  route the match names: the version check, the catalog and the token service through their handlers, every repository route through
  `serveRoute`, the tail of the `/api/<format>/` path, shared (Q-OCI-100, ADR-173). A bound host is read by `BoundRepository`, only for
  a repository of the format, by the match, the token service and the catalog (Q-EP-80).
- The route table (`httpserver.CheckRouter`, Q-ARCH-390) probes every pattern in a scratch mux, refuses a `HandleUncleaned` prefix at or
  below `/v2` (`HandleRegistryUncleaned` is the enforcement point's), the prefixes that enclose `/v2/` and a fallback handler.
- The version check (200 `{}`, its own 401 challenge for an anonymous caller without a registry token), the catalog (readable names
  in byte order over repositories, `n`, `last`, `Link`, bound host, package-scoped grants; the page is the data port's `CatalogNames`),
  the 404 `NAME_UNKNOWN` shapes, `Docker-Distribution-API-Version` on every answer of the dispatcher's handler and of the format's error
  shape for the chain's refusals below `/v2/`.
- `registrytest.Walk` runs against the real app handler of the default endpoint and of a bound-host endpoint
  (`test/integration/registry`), with registry tokens from the token service, its own expectation (the format's `Match`) and the served
  route read from the replica's `trove_http_requests_total{handler}`.
- Gaps H-648 (the catalog reads names it does not list), H-649 (the catalog grant is not narrowed by the other entries), H-650 (token
  requests count in the bucket of the client address). Round 2 (after WP1 and WP4): the catalog is bound to the data port
  (`var _ registry.CatalogStore = (*ocidata.Store)(nil)`), a request is matched once (a memo in the request's context), `Mount` takes
  several patterns (`/v2` and `/v2/{rest...}`, so a bare `/v2` is the version check and H-651 is gone), literal pattern segments are
  unescaped before the below-`/v2` test, the router refuses a duplicate uncleaned prefix and an archtest rule limits
  `HandleRegistryUncleaned` to `internal/api/registry`, the challenge names a scope only for a valid image name, a failed binding lookup
  is a 503 for a registry token too; the integration tests push (blobs seeded, the upload routes are WP5's), pull, list and log in
  through `/v2/`. The registry root's integration tests keep an `R-OCI-…` ID in the name only where they prove its acceptance criteria through `/v2/` (R-OCI-003, 004, 005, 017); R-OCI-001 and R-OCI-029 are in comments. Round 3: `EndpointFacts.Bound` is set from the match, the enforcer's strip drops the parsed form and logs, uncleaned prefixes are unescaped, the WP1/WP4 test renames of round 2 are reverted.
- Touched outside the owned files: `internal/api/registry/{token,enforce,registrytest/walk}.go` (WP2's: `Bound` returns an error and is
  mandatory, `Mount` registers the uncleaned prefix, `Target` replaced by `Scope`, the walk's fields), their
  tests (the `Bound` signature, the walk's fields; the names are unchanged), `internal/app/{app,wire_identity,wire_endpoints}.go` (`registryMetrics`, `RegistryScope`, `EndpointHandler`,
  `EndpointRouter`, `RegistryEnforcer`), `internal/httpserver/{router,routetable}.go`, `internal/api/registry/route/route.go` (the
  version header), `docs/decisions.md`, `docs/spec/{README,architecture,endpoints}.md`.
- `registry.CatalogStore` is the interface of `Store.CatalogNames`; `*ocidata.Store` satisfies it (compile-time assertion in `wire_registry.go`).


## WP5 as built (blob uploads and the mount)

- Built: `handlers_upload.go` (start, monolithic `POST`, `PATCH`, `PUT`, status, abort over the host's session port, the image name of the path as the binding on every call),
  `handlers_mount.go` (`handleMount`, the exported `MountSource`, the mount through `Ingestor.Link` by source name), the commit-hook check that a blob item holds the content of its digest,
  `routes.go` without `stub` (every operation has a handler). ADR-175, Q-OCI-110 to Q-OCI-116, H-660 to H-663, ADR-178 (contract version 3) ([oci-uploads.md](../security/oci-uploads.md)).
- WP3 (landed first) strips `mount` and `from` of a request whose registry token lacks `pull` on the source; WP5 closed H-660 with `EndpointFacts.BoundRepo` and `ociformat.MountSource` in the composition root. The `Location` of a session is built by the link builder of the request. A write to a virtual
  repository reaches the handler as a request for the deployment target, and the session and the `Location` name the target (H-663): WP9, which builds the OCI virtual class, makes the dispatcher keep the addressed
  repository on a write redirect, `uploadTarget` accept an addressed virtual, and the `Location` come from `Links(rq).Registry(addressed)`; it is the only place the path can be tested.
- Touched outside the owned files: `internal/formats/oci/hooks.go` (the blob branch of `OnCommit`), `routes.go` (`stub` removed), `format_test.go` and `conformance_test.go` (the routes
  are built), `docs/security/README.md`, `docs/decisions.md`.

## WP6 as built (the remote engine's handshake and revalidation)

- The engine calls `Authenticator` and `Revalidator` (ADR-165): `proxy.Authenticator`/`proxy.Revalidator` are the engine's views,
  `formathost` adapts the format's; `proxy.QueryRequest.Auth` and `proxy.Get.Auth` carry the handshake of a pass-through query and a
  mirror's discovery request (`passUpstream.Get` passes the format's, WP10 needs no more). `Exchange` runs no handshake. `Revalidator.RevalidationRequest` receives the engine's GET (`get`) and derives the request from it.
- The engine remembers the last challenge per repository and configuration version and asks the format before a request (Q-OCI-82, superseding Q-OCI-80): only the first request of a replica is challenged. A token goes to the
  upstream origin only (`outbound.WithAuthorization`, Q-REPO-255). `authMode: bearer` runs no handshake.
- WP8 implements `TokenRequest(ctx, rc, orig, p, status, challenge)` (the realm and the service from the challenge, the scope from `orig` and `p`) and
  `ReadToken` (the token and `expires_in`); it names `auth.docker.io` and the confirmed blob hosts in `DefaultExtraHosts` and
  `DefaultCredentialHosts` (the repository validation proposes the credential hosts with `sendCredentials: true`).
- The generic test adapter of `internal/proxy/proxytest` (`Auth`, `HeadRevalidation`, `Tokens`, `TokenService`, `Challenge`, `As`) is what WP8's
  adapter tests may copy; the stub registry of WP7 is the OCI-shaped counterpart.
- Security: [remote-repositories.md](../security/remote-repositories.md) (the handshake section, H-610 to H-612).


## WP8 as built (the OCI remote adapter)

- Built: `internal/formats/oci/remote.go` (upstream request with the namespace prefix, `Accept` of the four manifest types, `UpstreamQuery`, `AcceptResponse`,
  `PrepareFill` with the alias under the digest, `OnFill` and `InspectFill`/`OnCommit` rows, `BeforeArtifact`, `Rewrite`), `remote_auth.go` (the challenge
  parser, the scope from the request, the token document), `remote_revalidate.go` (the `HEAD` derived from the engine's `GET`, `Unchanged` by digest),
  `remote_handlers.go` (manifests, blobs, the tags list window with the pass-through, referrers, the `library/` mapping); the class `remote` in the
  descriptor, `ProbePath: "v2/"` and `catalog.Facts.ProbeChallengeReachable` (a `401` with a Bearer challenge is reachable, `authenticated: null`). ADR-176,
  Q-OCI-120 to Q-OCI-124, Q-REPO-280, H-665 to H-668 ([oci-remote.md](../security/oci-remote.md)).
- Docker Hub's blob host, checked on 2026-11-08 with an anonymous request: a blob `GET` was answered `307` to `production.cloudfront.docker.com`; the default
  extra hosts are `auth.docker.io` (credentials) only: `extraHosts` do not gate redirects, so the CDN host is not listed (Q-OCI-125). The check cost a token request, two manifest `GET` requests (the index and the amd64 manifest of
  `library/hello-world`) and one blob `GET`.
- Touched outside the owned files: the four read handlers of WP4 (`handlers_manifest.go`, `handlers_blob.go`, `handlers_tags.go`, `handlers_referrers.go`: the remote
  class is handed to the remote handlers), `hooks.go` (`OnCommit` writes the rows of a remote manifest from its inspection), `catalog.Facts` and
  `internal/formats/testdata/contract.txt` (one fact), `internal/proxy/upstream/probe.go`, `internal/repository/upstream.go` (a comment), `api/openapi.yaml`
  and its generated copies (a description) (round 2b: `SourceSpeller`, `RemoteCaps.Stood`, the engine's stood-401 hook; `app.Options.DispatchFormats` of round 1 is gone).
- Left to others: the virtual
  class (WP9) calls the same handlers for a remote member through `Request.Member`; the mirror (WP10) uses `UpstreamQuery`'s path shapes and the
  `Authenticator`; the e2e rows and the cross-replica flight criterion (six concurrent pulls on three replicas) are WP11's.

### WP8 round 2 (hand-offs and what changed)

- Reviewed and audited as `726d7ab1`; round 2a is `wip: T-16 WP8 round 2a` (ADR-176 revised, Q-OCI-125 to Q-OCI-129, Q-REPO-281, H-669; H-665 closed by the
  integrity event).
- Not proven by the `/v2/` harness, with the reason, and handed to the packages named: the criteria that run `docker pull`, `crane` or six concurrent pulls on
  three replicas (R-OCI-022 to R-OCI-024) are WP11's e2e rows; the harness drives the same requests over HTTP but is no client.
- Handed to WP10: the remote half of R-OCI-025's second criterion (`quota_header` in `trove_mirror_rate_limited_total`) and the mirror's use of `UpstreamQuery`'s path shapes.
- Handed to WP4/WP12: the remote half of R-OCI-031 (the package screens of a remote's cache: image names and manifests through the format's tables; a tag of a
  remote has no version row, Q-OCI-121, so the tag list of the screen is empty).
- Phase B (round 2b, on the `/v2/` mount): the harness goes through `/v2/` with registry tokens; `DispatchFormats` and the wrapper are gone; the remote gates follow the
  package document path (`<name>/_taglist`, the subject's manifest path); the grant sees the name the client typed (Q-OCI-129).

## WP10 as built (the mirror)

- Built: `internal/formats/oci/mirror.go` (methods, exact names), `mirror_listing.go` (the catalog), `mirror_tags.go` (the tags list in pages, the tag choice and order),
  `mirror_plan.go` (the plan: tags, child manifests by platform, attestations, configs and layers); `internal/mirror/policy.go` (`SelectItems` counts versions),
  `fill.go` (a path is filled once per attempt; the tally of an artifact a plan stored), `host.go` (`ReportStored`); `internal/formathost/mirror_host.go` (`Artifact`
  built on the same `fillPath` as `Fill`, `VersionBounds`); `internal/mirror/discover` (the optional `Bounded`, `BoundsOf`); `internal/repository/validate_mirror.go`
  (`checkDelegatedFormatMirror`: `<format>.mirror` kept from an Owner, 422 `mirror_restricted` at `/<format>/mirror`). The settings schema and the OpenAPI member of
  `oci.mirror` were there already (`make generate` changes nothing). ADR-182, Q-OCI-180 to Q-OCI-184, Q-REPO-295, Q-ARCH-415, H-710 to H-714
  ([oci-remote.md](../security/oci-remote.md#the-mirror)).
- Decided (the stub's `acme/b` has `linux/arm64/v8`): the platform filter `linux/arm64` matches every variant, `linux/arm64/v8` that variant, an entry without a variant is the
  architecture's default (Q-OCI-181). The first criterion of R-OCI-026 (defaults read back) is now "the format reads the defaults" (Q-OCI-184).
- The mirror builds its requests with the shared `registryURL` of `remote.go` (the namespace prefix, the name), not with `UpstreamQuery`, whose shapes are the client's pass-through
  queries; the token handshake is the engine's (`passUpstream.Get` hands the format's `Authenticator`), the scope of a catalog or tags list request is taken from the request (`scopeOf`).
- The remote half of R-OCI-025's second criterion is proven: `TestR_OCI_025_ADockerHubQuotaAnswerIsAQuotaHeaderRateLimitOfThePass` (the stub under Docker Hub's names answers the first
  manifest `GET` with `429-hub`; `trove_mirror_rate_limited_total{reason="quota_header"}` counts it and the pass completes).
- Tests: [mirror_test.go](../../internal/formats/oci/mirror_test.go), [versions_test.go](../../internal/mirror/versions_test.go), [mirror_format_test.go](../../internal/repository/mirror_format_test.go),
  `test/integration/mirror/oci_test.go` (two worker processes claiming the queue `mirror`, a client-serving process, the counting stub: catalog pass with the tag bound and a pull with the stub stopped,
  exact names, platforms, a second pass and a moved tag, the byte bound, a Docker Hub mirror with the quota answer).
- Touched outside the owned files: `internal/formats/testdata/contract.txt` (the optional `discover.Bounded`), `docs/spec/architecture.md` (the contract comment, Q-ARCH-415),
  `docs/spec/repositories-and-proxy.md` (R-REPO-050, R-REPO-051, Q-REPO-295), `docs/decisions.md`, `docs/developer/mirror.md`, `README.md`, `internal/mirror/rig_test.go` (a hook of the fake lister).
- Left to others: the e2e rows (a pass while real clients pull, `mirror-long` drain) are WP11's. Not done: referrers (stay pull-through), nested indexes (H-712), a per-name status of skipped tags (H-713).

## WP9 as built (the OCI virtual class)

- Built: `internal/formats/oci/virtual.go` (the adapter: the tags list is the one merged document), `virtual_read.go` (the plan, the `walk` over the members, a manifest by tag, a manifest and a
  blob by digest in the three tiers, the tags list and the referrers as unions), `virtual_write.go` (`writeRequest`, the link step of a manifest push, the refusal of a delete); the class
  `virtual` in the descriptor. ADR-180, Q-OCI-160 to Q-OCI-162, Q-OCI-164 to Q-OCI-166, H-700 to H-704 ([oci-virtual.md](../security/oci-virtual.md)).
- The H-663 hand-off of WP5 is closed without a change of the dispatcher: the write routes serve the class virtual (ADR-180), so the request reaches the handler with the virtual repository as the
  addressed one, `uploadTarget` already accepted it, and the session and every `Location` name it. The delete routes serve the class as well and answer 405 from the handler (before, the
  dispatcher would have carried a delete to the target).
- Deviation recorded: a write to a virtual repository without a deployment target is refused by the decision point (403 `DENIED`, a registry token without `push`), before the handler's 405
  (Q-OCI-166); R-OCI-028's criterion now says so.
- Tests: [virtual_test.go](../../internal/formats/oci/virtual_test.go) and the merge cases in the conformance fixture; `test/integration/ocivirtual` (new, shard b): tag order across members, a
  remote member's tag asked before a later member's stored copy, the digest tiers, a claim, the tags list paged, the referrers union, a chunked push to the target with the session and `Location`
  naming the virtual repository, the link of a cached layer, a mount from the virtual repository, the shadowing decision, the refusals of a delete and of a write without a target.
- Touched outside the owned files: `handlers_manifest.go`, `handlers_blob.go`, `handlers_tags.go`, `handlers_referrers.go` (the class branch), `handlers_upload.go`, `handlers_mount.go`,
  `handlers_delete.go`, `routes.go`, `remote_handlers.go` (`remoteReferrerList` extracted), `format.go`; `api/openapi.yaml` and its generated copies (`VirtualFormatName` lists `oci`); the guide texts
  of the virtual class; `docs/security/{README,oci-uploads,oci-content}.md`.
- Left to others: the `docker pull` and `docker push` rows through a virtual address (R-OCI-027, R-OCI-028 acceptance criteria that run a client) are WP11's; the conformance suite through
  a virtual address is WP13's. The package screens need no class switch (the OCI screens read the rows of the repositories that hold the images).

## WP13 as built (the conformance suite, the protocol criteria and F-88)

- Built: `make oci-conformance` (pin `OCI_CONFORMANCE_TAG := v1.1.1`, `oci-conformance-build` resolves the tag to its commit, downloads the module into `.out/oci-conformance/mod` and compiles it with the
  repository's Go, `GOTOOLCHAIN=local`), `TestOCIConformance` (build tag `ociconformance`, added to the lint build tags), the CI step and the artifact `oci-conformance-junit` in shard b of the
  `integration` job, `TestProtocol` (20 subtests, three replicas, every request pinned), `TestR_AUTHZ_007_OCIActionMap` (a subtest per row of the OCI table), `TestRegistryWorksWithValkeyStopped`. ADR-189,
  Q-OCI-210 to Q-OCI-214, H-755, H-756 ([developer/oci.md](../developer/oci.md#the-protocol-tests-and-the-conformance-suite)).
- The spec said `go mod download …/conformance@v1.1.1`; the module has no tags of its own, so the tag of the repository is resolved to its commit (Q-OCI-210). R-OCI-035 says so now.
- **The suite passes in all four categories through the local address** (80 specifications, none failed; four skipped: the two that read their data from the environment, the cross-mount spec for automatic
  discovery enabled, and the cross-mount spec of a nonexistent blob, which runs only after a 202 - the mount answers 201 for a blob the source holds), **and pull, push and content discovery through the
  virtual address** (80 specifications, none failed, ten more skipped: the content-management category, Q-OCI-211: a delete through a virtual repository is 405 by R-OCI-028).
- **Product defect found by the suite and fixed first** (own commit): the challenge of a mount named the target's `push` only, so a client that takes its scope from the challenge never held `pull` on the
  source and the mount fell back to a session (202); the three specs after a 201 were skipped. `Enforcer.mountScope` adds the source's pull (Q-OCI-212, `TestTheChallengeOfAMountNamesTheSourcesPullToo`).
- **Area:** the protocol criteria are in `test/integration/oci` (the area the spec names, shard b), not `ocilocal` (state seeded by the item store) and not a new area (Q-OCI-214); no shard list changed.
  `test/integration/files/oci_host_test.go` held the 11 protocol tests of the `/api/oci/` mount: their criteria run over `/v2/` now (pushes, the references and platforms of an index, the moved tag,
  concurrent pushes, a withheld manifest, crafted manifests, the path-scoped policies and the lists decided on the package document, tag against digest pushes) and the fixture stays as
  `oci_fixture_test.go` for the other seven files of the area, which need a lock, a guard or a standing session that a replica cannot stage.
- **"Valkey stopped"** is the replicas' configuration (`StackOptions.Valkey` false: their Sentinel answers nothing), not a stop of the stack's container, so no failover lock was taken.
- **F-88** (Q-OCI-213): 171 test names lost an R-OCI ID in `internal/formats/oci`, `internal/api/registry`, `internal/auth/registrytoken`, `test/integration/oci*` and `test/integration/registry`; each carries
  "Mechanism of R-OCI-0nn, not a proof of its criteria: <what the criteria are>" in its comment. `TestWriteErrorMapsEveryRowOfTheTable` gained R-OCI-008 (its criterion), one test moved from R-OCI-020 to
  R-OCI-022 (the mismatched digest). `docs/coverage/implemented.txt` and the checklist were regenerated from the names.
- **Criteria no test of the integration tier proves, and who does:** R-OCI-002 (the repository keys: the repository service's tests); R-OCI-003 (the fixture plug-in of the route table);
  R-OCI-004 (`docker login`, `podman login` and the anonymous `docker pull`: e2e `oci`); R-OCI-005 (the query counter and the key-ring rotation: `test/integration/identity`); R-OCI-007 (the object
  store holds one object: the test counts the `blobs` rows, not an S3 listing); R-OCI-011 (the drain during a 300 MiB push: `oci-long`); R-OCI-014 and R-OCI-030 (real clients: WP11); R-OCI-021 to
  R-OCI-025 (the six concurrent pulls on three replicas and `crane`: WP11; the `429` of a pass: `test/integration/mirror`); R-OCI-026 (defaults, 422 and the restriction: the repository service's tests; the
  pass: `test/integration/mirror/oci_test.go`); R-OCI-032 (Docker and Podman with Valkey stopped: WP11's closing run); R-OCI-033 (the restore drill, R-BAK-029); R-OCI-034 (the metric after the e2e matrix: WP11).
- Not verified: the suite against more than one replica (it runs against one, and the protocol tests cover three); the conformance run in CI (the step is written and its pieces run here, the job itself has not run).
- Touched outside the owned files: `internal/api/registry/{enforce,mount_test}.go` (Q-OCI-212), `.golangci.yml` (the build tag), `Makefile` (block `T16 oci-conformance`), `.github/workflows/release.yml` (two
  steps in the `integration` job), `docs/decisions.md`, `docs/spec/formats/oci.md`, `docs/security/{oci-virtual,oci-content}.md`, `docs/developer/{oci,build-and-test,formats}.md`, `docs/projektplan/promt.md` (F-88 removed),
  `docs/coverage/`, and the test names of the packages F-88 names.


## WP11 as built (the e2e sets on kind)

- Built: `test/e2e/oci_*_test.go` (the harness, the rows, the closing and long tests), `test/e2e/ociclients/` (the two client images: the pinned dind image with the forwarder and the pinned Podman image;
  the pins `E2E_OCICLIENTS_DOCKER_IMAGE` and `E2E_OCICLIENTS_PODMAN_IMAGE` in `tools/versions.env`, `check-pins.mjs` in `make versions-check`, `make oci-clients-image`), `deploy/dev/oci-clients.yaml` (the
  namespace `trove-e2e-clients` with Pod Security `privileged`, the pod), the wiring in `deploy/dev/e2e.sh` (markers `T16 oci-stub`, `T16 oci-clients`: the counting stub of WP7, the client pods, the node setup of the
  Kubernetes pull row; the sets `oci`, `oci-closing`, `oci-long`, `oci-clients`, and the OCI additions to `mirror` and `mirror-long`), `deploy/dev/values-dev.yaml` and `pki.yaml` (the endpoint `ocimirror`, the bound host of the Docker Engine
  mirror), `deploy/dev/kind.yaml` (`config_path` of the nodes' containerd), `test/e2e/mirror_oci_test.go` and `mirror_long_oci_test.go`, the jobs `e2e-oci` of `release.yml` (leg `oci` per change that touches the registry,
  `oci-closing` on a closing dispatch; `runs-on: large`) and `performance.yml` (leg `oci-long`, nightly), and the OCI additions of the scope of `e2e-mirror`. ADR-188, Q-OCI-200 to Q-OCI-203, H-750.
- Q-OCI-21 held: the cluster admitted the namespace with Pod Security `privileged`; option (b) was not needed.
- Found while building, none a defect of the product: Docker treats `localhost` as a registry that may be plain HTTP and reports the last failure (an `EOF`) of a refused login; BuildKit inside dockerd reads the system trust
  and not `certs.d` (the dev CA is added to both); the kind node image's containerd has no `config_path`; `docker manifest push` writes the kind of list its members call for; a client that holds a layer does not pull it again, so the
  tests that count what the upstream delivered empty the clients' stores first (`pruneClients`); the remote answers 404 `MANIFEST_UNKNOWN` for a name its upstream does not know (Q-OCI-202).
- Not done: the six manifest `GET` requests at Docker Hub of the closing run are not counted (Q-OCI-203); the browser step of the package screen is the UI package's; no client of another version than the two pins.
- The criteria of WP7 to WP10 that needed a client are the rows of `docs/developer/e2e.md#the-oci-tests`.
