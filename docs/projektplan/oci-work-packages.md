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
revalidation, plan artifacts and upload sessions** (additions keep `ContractVersion` 2, R-ARCH-025; the surface file changes).

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
	TokenRequest(ctx context.Context, rc RemoteContext, p Path, status int, challenge http.Header) (req *http.Request, key string, ok bool, err error)
	// ReadToken reads the realm's answer (the engine bounds it at 64 KiB): the Authorization value to retry with and its
	// lifetime (0: 60 s).
	ReadToken(ctx context.Context, rc RemoteContext, status int, body []byte) (authorization string, ttl time.Duration, err error)
}
type Revalidator interface {
	// RevalidationRequest returns the request (OCI: a HEAD) for an expired stored document; ok false: the engine's conditional GET.
	RevalidationRequest(ctx context.Context, rc RemoteContext, p Path, variant string, stored RemoteDocument) (req *http.Request, ok bool, err error)
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
| WP8 | OCI remote adapter | `voltagent-infra:docker-expert` | R-OCI-021 to R-OCI-025 | `internal/formats/oci/remote*.go` (`UpstreamRequest` with the namespace prefix and `library/`, `Accept`, `AcceptResponse`, `PrepareFill` digest checks, `FillInspector`/`OnFill` rows, `BeforeArtifact`, `Authenticator`, `Revalidator`, tags list document, referrers pass-through), registers class `remote` | WP4, WP6, WP7 | yes | no |
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
