# Developer documentation

Status: phases 1 to 6 are built: the foundation (M0), the access core (M1), the npm registry (M2), the NuGet registry (M3), projects, the GitLab CI OIDC federation,
the trove Bastion and the vulnerability scanning (M4b). The repository holds the Go binary,
its configuration, the database and cache layers, the S3 blob layer, the system and management APIs, the ops
endpoints, the OIDC login and the break-glass account, the tokens, the permission policies and the grant index,
the endpoints and exposure rules, the generic repository format with raw transfer under `/files/`, the npm and NuGet formats with their local, remote (pull-through proxy) and virtual repositories, the audit
trail, the Angular admin UI with the package screens, client setup and the in-app manual, the Helm chart, the local development profile on kind, the integration, end-to-end
and chart tests, and the CI workflows. The vulnerability scanning (the advisory mirror, the findings and their triage, the download block, the client audits, the report) is integrated
and its flow is tested end to end at the integration tier ([vulnerabilities.md](vulnerabilities.md), [test/integration/vulnflow](../../test/integration/vulnflow)); its browser journeys and client runs are the
end-to-end wave's. The contracts of the backup lane exist (phase 7, [backup.md](backup.md)); the operations behind them are not built yet. Everything marked *planned* below is the target design in
[docs/spec/](../spec/README.md).

## What to have in your head first

- **Four parts, one direction.** The [feature catalog](../features/README.md) says *what* a full-featured
  repository manager does. The [specification](../spec/README.md) says *how* trove does it, one
  requirement at a time (`R-<AREA>-nnn`). [Coverage](../coverage/README.md) measures the gap, and
  [decisions.md](../decisions.md) records *why*.
- **Owner constraints are fixed.** Go 1.27.3, Angular 22, PostgreSQL, S3-compatible storage, Helm, OIDC
  login, own tokens, detailed permissions, GitLab CI OIDC, pull-through proxying, npm and NuGet first, and
  the high-availability and scale constraints of 2026-10-28 ([spec/README.md](../spec/README.md#owner-constraints)).
- **Invariants `INV-1` to `INV-20` bind every spec page.** A page may refine an invariant but must not
  contradict it ([spec/README.md](../spec/README.md#design-invariants)).
- **Roles, one binary** ([R-ARCH-001](../spec/architecture.md)). `web` answers HTTP and never claims a job or
  holds a lease. `worker` claims jobs, holds leases and serves only the ops listener. `all` combines both
  for a local run outside the chart. `bastion` is the read-only cache of phase 5b with a configuration tree of its own and
  no PostgreSQL ([bastion.md](bastion.md)). `backup` is a command of the binary, not a serving role ([backup.md](backup.md)).
- **PostgreSQL decides, Valkey accelerates.** State whose loss would corrupt data lives in PostgreSQL,
  including every mutual exclusion whose failure would corrupt data. Valkey holds shared ephemeral state and
  never the only copy of anything. Only `internal/cluster` talks to Valkey, and every key expires.
- **No state between requests** ([R-ARCH-032](../spec/architecture.md)). Consecutive requests of one client
  may land on different replicas.
- **Every repository is in one project** ([projects.md](projects.md)). Its name is the pair `<projectKey>/<repoKey>`
  (`qname.Repo`), unique as a pair; every address names both (`/api/<format>/<projectKey>/<repoKey>/…`,
  `/files/<projectKey>/<repoKey>/…`, `/api/v1/projects/<projectKey>/repositories/<repoKey>`), and a name that is stored or
  labelled (a job's target, the `repo` label of a metric) is `Repo.String()`. Users and groups reach a project's repositories
  through their role there.
- **Tools read the headings.** `### <ID> · <Title>` followed directly by a `Priority:` line is how the coverage
  generator finds catalog entries and requirements. Keep both on one line each.
- **One version per tool.** Pins live in [tools/versions.env](../../tools/versions.env) (tools and images),
  `go.mod` (Go, sqlc, oapi-codegen), `web/package-lock.json` (Angular) and `web/.nvmrc` (Node.js). `make
  versions-check` compares the installed tools with the pins.

## Pages

| Page | Content |
|---|---|
| [architecture.md](architecture.md) | Components, layers, request path, startup and shutdown, coordination, configuration and secrets |
| [build-and-test.md](build-and-test.md) | Build, generate, lint and test targets, the test tiers and their build tags, and the in-process harness (`testenv`: a stack, replicas through `app.New`, seeded principals and real tokens) |
| [releasing.md](releasing.md) | The CI jobs and their make targets, semantic-release and the release pipeline (Docker Hub and the Helm chart repository), the Renovate policy, the secrets and settings the pipeline needs |
| [configuration.md](configuration.md) | How `trove.yaml`, environment variables and flags combine, and how a key is added |
| [coverage.md](coverage.md) | The requirement coverage generator, its rules and its evidence record |
| [database.md](database.md) | PostgreSQL connections, pools, migrations, sqlc and the job and change tables |
| [blob-store.md](blob-store.md) | The S3 blob layer: keys, spool, commit, download, garbage collection |
| [backup.md](backup.md) | The backup lane: the on-target format, the run record and its lease, the ports, the restore fix-ups, the revocation journal, the command tree and how to add to them |
| [coordination.md](coordination.md) | Change log, signals, leases, the job queue and the replica registry |
| [valkey.md](valkey.md) | The Valkey client, Sentinel discovery, key families, degraded mode, the flight lock and the negative store |
| [observability.md](observability.md) | Logs, metrics, traces, health and the ops listener |
| [secrets.md](secrets.md) | The stored-secret key ring and the envelope of sealed columns |
| [tls.md](tls.md) | TLS material on every listener and client: loading, reloading, rotating, testing |
| [endpoints.md](endpoints.md) | Endpoints: listeners, identity, hosts, surfaces, TLS per endpoint, the chart rendering, accepted credentials, exposure rules and their ceiling, package formats on an endpoint (the exposure view, filtered documents, member names, the cache-fill policy), the endpoint rate-limit class, the endpoint API |
| [http-api.md](http-api.md) | Listeners, the middleware chain, the system API and the problem format |
| [helm-chart.md](helm-chart.md) | The chart: values, rendered objects, validation and the chart tests |
| [local-profile.md](local-profile.md) | The HA-shaped dev profile on kind, its scripts and its pins |
| [e2e.md](e2e.md) | The end-to-end suite on the dev cluster: harness, driver, k6 load, tests and open findings; the npm client matrix and its tests (E2.1 to E2.12), the counting npm test upstream and its corpus, the OIDC issuer double of the CI tests, the closing run |
| [audit.md](audit.md) | Audit trail: registry, recorder, journal row, query, partitions, how to add an event type |
| [repositories.md](repositories.md) | The repository model: service, snapshot and unknown-key lookup, validation, patterns, deletion job, the format registry and the generic format |
| [proxy.md](proxy.md) | The pull-through proxy: the upstream clients of remote repositories (settings applied to the outbound client, credentials, secret references, the client cache, the foreign-host check, the upstream test); the engine adds its sections |
| [virtual.md](virtual.md) | Virtual repositories: members, deploy target and pin rules as rows, namespace claims and their API, the routing (`Route`, `AutoClaim`, `ExposureSet`), the detach of a deleted member; the virtual engine (`Plan`, tiers, the merged-metadata cache, the shadowing check, the explanation API) and the seam to bind the proxy engine to |
| [packages.md](packages.md) | The package screens' API: the read model across repositories (`internal/packages/view`), the visibility filter (`authz.ReadScoper`) and the statements that count with it, the format's `ViewAdapter`, the README renderer (`internal/packages/readme`), the client setup snippets and the tests that pin them (R-API-029, R-API-030, R-UI-008, Q-API-164 to Q-API-168) |
| [npm.md](npm.md) | The npm format: the route table, classification, error shape, publish, the data port and the leaf libraries `name`, `semver`, `manifest`, `tarball`, `publish` (R-NPM-001 to R-NPM-008, R-NPM-041, R-NPM-043, R-NPM-044, R-ARCH-028); the package documents with their validators and rendition cache, tarballs and the serve-time check, dist-tags, unpublish and the removal hooks, whoami and ping (R-NPM-009 to R-NPM-017, R-NPM-019, R-NPM-047); the remote and virtual classes are in its sections; deprecate (P1) is not built |
| [oci.md](oci.md) | The OCI and Docker registry format as far as it is built: the registry root and its address, the grammars, the storage layout, the error shape, the settings, the data port and its tables, the upload-session tables, the key grammar of every format, the configuration keys, and what each work package fills in (R-OCI-001, R-OCI-002, R-OCI-007, R-OCI-008, R-OCI-026, Q-OCI-3, Q-OCI-4) |
| [deb.md](deb.md) | The Debian (APT) format as far as it is built: the OpenPGP leaf package (keys, import rules, signing, verification, bounds) and the counting stub archive with its generator of installable `.deb` files, keys, switches and control API (R-DEB-015, R-DEB-016, R-DEB-020, R-DEB-034, R-DEB-035) |
| [nuget.md](nuget.md) | The foundation of the NuGet format before the format exists: the rule libraries (`id`, `version`, `nupkg`, `nuspec`), the golden vectors and their regeneration, fuzzing, the counting test upstream and the contract additions the format uses |
| [proxy.md](proxy.md) | The pull-through proxy: the cache invalidation of remote repositories (rows, the per-replica matcher the engine asks, the invalidate and entry-list operations, the fold task); the engine sections are added as it is built |
| [login.md](login.md) | The OIDC login, the server-side session, CSRF, logout and the break-glass account: files, flows, seams to the identity, api and audit areas, configuration, tests |
| [identity.md](identity.md) | Principals, groups, service accounts, tokens, the authentication stage, caches and revocation propagation, the failed-credential throttle |
| [authorization.md](authorization.md) | Permission policies, roles, the evaluation, the compiled grant index and its invalidation, epochs and `Trove-Authz-Min-Epoch`, how an operation declares its permission, the populate check and `MemberUsable`, package ownership |
| [projects.md](projects.md) | Projects, members and roles: the role in the decision path, the two selector contexts, the stored names of a project's policies, the project service (members, the last Owner, the move of a repository), error codes, audit events, the schema and the tests |
| [bastion.md](bastion.md) | The trove Bastion (phase 5b): the contracts of the eight work packages: package tree, the seams and who implements each, the interface guard, the request flows (hit, check, miss, write probe), the schema and configuration it added, how to run the role locally; status: contracts laid, work packages pending |
| [ci-oidc.md](ci-oidc.md) | GitLab CI OIDC federation: the contracts laid for beta phase 5 (schema, the `internal/ci` packages and interfaces, the token and evaluation seam, the gate, API, configuration, events, tasks), the project tier, the work packages W1 to W7 and their files; status: contracts laid, work packages pending |
| [download-statements.md](download-statements.md) | The statements of a download: the budget of R-ARCH-027 per route, measured; `GetLiveItem` reading the rows of the serve check and the cache entry; how a format adds its rows |
| [vulnerabilities.md](vulnerabilities.md) | Vulnerability scanning (phase 6, M4b): the contracts of the work packages and a section per package A to G (the packages and layers, the ports `States`, `Evaluator` and `Advisories`, the download policy and verdict, the format contract's optional vulnerability adapter, the schema, the API operations, configuration, coordination, audit, metrics and errors), the work packages S and A to H with the files each owns, the contract files; status: built and integrated |
| [mirror.md](mirror.md) | The mirror mode of remote repositories (phase 6b): the contract wave, the four work packages A to D with the directories each owns, the engine (work package C2: its files, how a pass runs and ends, what the status reads, the seams of its tests), the tests each package writes, the rules every package keeps; status: the engine is built, the host of the remote engine and the formats' discovery are their own work packages |
| [files.md](files.md) | Raw transfer under `/files/`: the generic layer over the item store, deploy, download with Range, folders, the items API, deletes, activity and download statistics |
| [items.md](items.md) | The item store: the one commit and its order, the storage hooks, spool, read, listing rows, subtree delete and the `items-delete` job |
| [formats.md](formats.md) | The format contract: `formats.Format`, the registry and its start-up checks, the `Host`, the storage hooks, the dispatcher for `/api/<format>/<repoKey>/`, content headers and error shapes, the wiring, how to add a format |
| [m1-contracts.md](m1-contracts.md) | The M1 package map, shared Go contracts, marked sections in shared files, error codes, Valkey and audit names, API path prefixes per work package |
| [web-ui.md](web-ui.md) | The Angular workspace, the local UI development profile with hot reload, the embedded build, the budget and the UI tests |
| [design-language.md](design-language.md) | The look of the UI: the measured colours of both schemes, what was adjusted for WCAG AA, where the values live and how to change one |
| [manual.md](manual.md) | The user and administrator manual: embedding, rendering and link checks, the manual operations, the help area of the UI, how to add a page |

## Core flows

One fact per line; each links to the page that explains it.

- **Start-up:** `trove serve` loads the configuration, builds the components of its role and waits for the
  PostgreSQL schema check, then becomes ready ([architecture.md](architecture.md)).
- **Configuration:** defaults, then the file, then the environment, then flags; strict validation; the
  effective view is printed by `trove admin config validate` ([configuration.md](configuration.md)).
- **Requests:** the middleware chain assigns `X-Request-Id`, applies the client address and the body limits,
  then the handler; every error is written as `application/problem+json` ([http-api.md](http-api.md)).
- **Migrations:** `trove migrate up` holds the migration advisory lock and applies each step in its own
  transaction; a replica waits, not ready, until the schema is current ([database.md](database.md)).
- **Cross-replica change:** every replica polls the change log; a Valkey message only evicts early
  ([coordination.md](coordination.md)).
- **Singleton work:** a worker takes a session-level advisory lease on its lease pool and fences its writes
  to that session ([coordination.md](coordination.md)).
- **Jobs:** a worker claims rows with `FOR UPDATE SKIP LOCKED`, extends its lease with a heartbeat, and settles
  the attempt with the worker and attempt as owner ([coordination.md](coordination.md)).
- **Valkey:** keys come from registered families with a mandatory expiry; the client follows the primary that
  Sentinel reports ([valkey.md](valkey.md)).
- **Blobs:** content-addressed keys under `<prefix>/sha256/…`; the S3 client sits behind an outbound factory
  ([blob-store.md](blob-store.md)).
- **Health:** readiness checks, watched loops and the liveness watchdog ([observability.md](observability.md)).
- **Login:** `GET /auth/oidc/{providerId}/start` writes a flow row and sets `__Host-trv_oidc_binding`; the callback
  checks the binding, exchanges the code with PKCE, verifies the ID token, provisions the principal and creates the
  session in a second transaction ([login.md](login.md), Q-AUTH-32).
- **Break-glass login:** `POST /auth/local/login` reserves an attempt before it verifies the password, and refuses while
  the lockout or the rotation hold applies ([login.md](login.md), Q-AUTH-171 and Q-AUTH-14).
- **Token request:** a `Bearer` value `trv_pat_…` or `trv_sat_…` is hashed and looked up; the auth caches answer
  while the change log is fresh; the scope is intersected with the subject's permissions ([identity.md](identity.md)).
- **Decision:** the subject (who, and the endpoint the listener accepted the request on, which fixes the exposure ceiling)
  and the action go to `authz`, which denies by default; the request takes the grant index once and asks every question
  of it; the index is per replica and a request may name the epoch it must see with `Trove-Authz-Min-Epoch`
  ([authorization.md](authorization.md)).
- **Format request:** `/api/<format>/<projectKey>/<repoKey>/…` reaches the dispatcher, which splits the path with `qname.ParseAddress`
  and cleans it, asks the format's `Match`, takes the request's index, runs the coarse check, resolves the repository by its project and
  key and calls the route through `formats.ServeRoute`; the handler works through `Host`; every error, the chain's refusals included, is rendered by the format ([formats.md](formats.md)).
- **Raw upload:** `PUT /files/{projectKey}/{repoKey}/{path}` is decided before the body is read; the body is spooled and digested, the write
  is decided again on a new index, then `items.Store.Commit` writes the item, the committer's rows, the format's hook and the
  audit row in one transaction ([files.md](files.md), [items.md](items.md)).

## Repository layout

```text
trove/
├── Makefile                  # the task runner: build, generate, lint, scans, test, web, chart, image, dev profile, integration, MinIO
├── go.mod                    # go 1.27.3; sqlc and oapi-codegen as tool directives
├── tools/versions.env        # pins of every tool and image that is not a Go module or an npm package
├── tools/github-download.sh  # downloads a GitHub release asset, with the job's token when there is one (Q-ARCH-356)
├── tools/sbom-generator/     # the BuildKit SBOM generator that runs the pinned syft (registry.sh serves it during the release build), and check-pins.sh for make versions-check
├── Containerfile             # the release image: web build, cross-compiled Go build, distroless final stage
├── package.json              # release tooling only (semantic-release); its lockfile is package-lock.json, web/ has its own
├── .releaserc.json           # semantic-release: conventional commits, releases from main
├── renovate.json             # Renovate: the Go, npm, GitHub Actions, Docker and tools/versions.env managers
├── cmd/trove/                # main.go (dispatch), serve.go, migrate.go, admin.go (dispatch), admin_login.go and admin_identity.go
│                             # (the recovery and bootstrap commands of R-AUTH-015)
├── api/openapi.yaml          # OpenAPI 3.1 source of truth; go generate copies it into internal/api/v1
├── internal/
│   ├── app/                  # composition root: components per role, start and stop order
│   ├── api/edge/             # request-to-domain conversions every entry point shares: SubjectFrom (with the endpoint), Access (the request's index)
│   ├── api/dispatch/         # the format dispatcher: /api/<format>/<projectKey>/<repoKey>/…, the error shape of a format prefix
│   ├── api/login/            # the browser login routes under /auth/ (edge of internal/auth/oidc): cookies, redirects, the error page
│   ├── api/rawfiles/         # the /files/ handler (edge of internal/files)
│   ├── api/v1/               # management API handlers and the conventions (paging, ETag, merge patch, idempotency, CORS,
│   │                         # rate limits); gen/ is generated (oapi-codegen); apitest/ validates responses against the document
│   ├── archtest/             # layer, import, state and source rules, tested on fixtures in testdata/
│   ├── audit/                # event registry (journal flag), recorder (transactional and async), store and query
│   ├── auth/                 # the authentication stage and the failed-credential throttle; principal/ (users, groups,
│   │                         # service accounts, provisioner), token/ (format, store, lifetime, revocation), scope/ (the scope value type, a leaf),
│   │                         # session/ (UI sessions, CSRF, the session cache), oidc/ (providers, login flow, callback, break-glass),
│   │                         # jws/ (compact JWS and JWK sets, a leaf the login and the CI validation share)
│   ├── authz/                # decision point: engine, compiled grant index, epochs, policy and role service, decision audit
│   ├── bastion/              # the trove Bastion's own code (R-BAS-001, bastion.md): iface (the wire contract), answers, metrics, mainclient, cache, shared, guard, downloads, engine, server; its own packages import no subsystem of the main installation and the role enables none (archtest, transitive, with the listed exceptions of its dependency closure)
│   ├── bastionmain/          # the main installation's side of the Bastion: the seams of the work packages (recognition, token authenticator, read-shape resolver, route observer, chain stages)
│   ├── backup/               # trove backup: contracts, run record, lease; format/ target/ fixup/ tables/ (backup.md)
│   ├── journal/              # the revocation journal: object, ports, replay handlers (backup.md)
│   ├── blob/                 # store, spool, digests, commit, download, GC; s3/ is the S3 client; blobtest/ fixtures
│   ├── buildinfo/            # version, commit and build date from -ldflags
│   ├── cache/                # bounded in-process caches with change-area invalidation
│   ├── ci/                   # GitLab CI OIDC federation (contracts laid, ci-oidc.md): types, service interfaces, stubs, metrics; reason/, claims/ (claim engine, built) and subject/ (subject-token validation, built) are pure leaves
│   ├── cluster/              # the only Valkey client and key registry (valkey_keys.go), change log, signals,
│   │                         # FlightLock, leases, lock categories, replica registry, watchdog loops, the cluster limiter
│   ├── config/               # struct tree with defaults, loader, validation, schema.json (generated)
│   ├── errs/                 # error kinds, the code registry (codes.go), problem rendering
│   ├── httpserver/           # listeners, routes, middleware chain, client address, ops endpoints; content/ the content-header wrapper
│   ├── formathost/           # the Host of the format contract, the hook and seam adapters, the data-port bindings
│   ├── packages/             # the generic package rows: store and writer (rev_seq raised once per package per transaction)
│   ├── jobs/                 # job kinds, tasks, the queue, workers, scheduler, reaper
│   ├── meta/                 # transactions and the revision-column registry
│   ├── meta/postgres/        # pools, connect, retry, migrations/ (embedded), queries/ (sqlc input), q/ (sqlc output),
│   │                         # listq/ (dynamic queries with bound values)
│   ├── observability/        # logs, metrics, tracing and OTLP export, health checks
│   ├── outbound/             # the one HTTP client factory and the egress address policy
│   ├── project/              # projects: create with the first Owners, the members and the last-Owner rule, the move of a repository
│   ├── qname/                # the name of a repository (project and key) and of a project's objects, the one parser of the protocol and
│   │                         # raw-file paths (ParseAddress), the selector grammar (ParseSelector); standard library only, a leaf
│   ├── repository/           # repository model: service, snapshot reader, validation, patterns, deletion job
│   ├── files/                # the generic layer: decides on the request's index, resolves, admits; over items/
│   ├── items/                # the item store: spool, the one commit, stat, open, listing, subtree delete, statistics; no authz
│   ├── formats/              # format contract and registry; catalog/ (the facts of every format, a leaf), untrusted/ (limits of
│   │                         # untrusted content), generic/ (raw files), all/ registers every format
│   ├── secrets/              # key ring and sealed-column registry
│   ├── vuln/                 # vulnerability scanning (contracts laid, vulnerabilities.md): the domain vocabulary and ports; advisory/, match/, osv/, metrics/ are leaves; store/, sync/, evaluate/, triage/, report/ are the work packages'
│   ├── mirror/               # the mirror mode of remote repositories (mirror.md): the engine (dispatch, pass, fence, window, bounds, store, metrics) beside the names of its job kinds and queue; discover/ is the leaf contract a format's remote adapter implements
│   ├── tools/coverage/       # the requirement coverage generator (docs/coverage)
│   └── webui/                # go:embed of the Angular build; dist/index.html is the committed placeholder
├── web/                      # Angular workspace: angular.json, src/, scripts/ (build-embed, check-budget), .nvmrc
├── deploy/
│   ├── helm/trove/           # the chart: templates/, values.yaml, values.schema.json, ci/ value sets
│   └── dev/                  # the local profile: kind, HAProxy Ingress, cert-manager, CloudNativePG, valkey-operator, internal-secrets-operator, SeaweedFS, scripts
├── test/
│   ├── integration/          # build tag `integration`: area folders with an env_test.go: api/, apikey/, audit/, authz/, endpoints/, files/,
│   │                         # identity/, login/, repository/, tls/, coordination/, postgres/, server/ (each creates its own database)
│   ├── chart/                # build tag `chart`: render assertions and the lint script
│   ├── npmstub/              # the counting npm test upstream (TLS only): stub, corpus/ (700 synthetic packages), recorded set, cmd/npmstub, Containerfile
│   ├── nugetstub/            # the counting NuGet test upstream (TLS only): corpus, two hosts, catalog, throttling modes, control API, cmd/nugetstub, Containerfile
│   ├── ocistub/              # the counting OCI test upstream (TLS only): registry, token realm and blob CDN on three hosts, generated image set, switches, counters, control API, cmd/ocistub, Containerfile
│   ├── osvstub/              # the test advisory source of the vulnerability tests (TLS only): OSV records, cmd/osvstub, Containerfile
│   ├── oidcstub/             # the GitLab-shaped OIDC issuer double (TLS only): rotating keys, minted ID tokens, faults, counters, control API, cmd/oidcstub, Containerfile
│   └── e2e/                  # build tag `e2e`: M0 scenarios (e03 to e16, e99), the M1 access-core scenarios (e1_01 login,
│                             # e1_08 rate limits, e1_11 scatter, e1_13 transfer, e1_14 exposure), m1_env_test.go; k6 load script
│                             # clients/: the npm client matrix image (R-NPM-045), npm_clients_test.go
│                             # nugetclients/: the NuGet clients image (.NET SDK, nuget.exe on Mono; R-NUGET-027), nuget_clients_test.go
├── perf/                     # data of the performance tests: npm/reference (package.json, package-lock.json), see perf/README.md
├── .github/
│   ├── workflows/              # release.yml (Test and Release), build.yml (Release Docker & Helm), renovate.yml
│   ├── actionlint.yaml         # actionlint's configuration (the App token inputs of the pinned version)
│   ├── scripts/                # s3-conformance.sh, run by make s3-conformance
│   ├── actions/load-pins/      # loads tools/versions.env into the job
│   ├── actions/install-buildx/ # the pinned Docker Buildx, verified and cached under .bin (Q-ARCH-355)
│   └── actions/select-leg/     # decides whether a leg of the nightly matrix runs in this dispatch
├── hack/                     # verify-release-tooling.mjs: the release notes of .releaserc.json rendered
├── test/coverage/            # report.sh: merges the unit and integration profiles into the badge
└── docs/                     # README.md, decisions.md, developer/, spec/, security/, coverage/, features/
```

M1 ([m1-contracts.md](m1-contracts.md)): the shared types and interfaces of `internal/audit/` (event registry, recorder),
`internal/auth/` with `principal/`, `token/`, `session/` and `oidc/`, `internal/authz/` and `internal/repository/` are
implemented; `internal/api/rawfiles/` and `internal/files/` carry the raw transfer. The `Principal` the
chain stores is in [internal/httpserver/principal.go](../../internal/httpserver/principal.go).

M2 starts with its schema: the tables of the remote engine, the virtual engine, claims, ownership and npm exist
([database.md](database.md#m2-tables)), with their configuration keys ([configuration.md](configuration.md#m2-keys)), change
areas and layer rows in `internal/archtest`. Built, from [R-ARCH-003](../spec/architecture.md): the npm format (`internal/formats/npm`), its store
(`internal/formatdata/npm`), the remote fetch engine (`internal/proxy/`) and the virtual engine (`internal/virtual/`).
The NuGet format (`internal/formats/nuget`) followed in phase 4 ([nuget.md](nuget.md)). The OCI format (`internal/formats/oci`, data port `internal/formatdata/oci`) is registered as a skeleton ([oci.md](oci.md)).

## Test tiers

| Tier | How it runs | Where |
|---|---|---|
| Unit | `make test` (`go test -race`, JSON to `.out/test-unit.json`) | next to the code; `_test.go` |
| Architecture | part of unit: `go test ./internal/archtest` | `internal/archtest/` |
| Integration | `make integration-up`, then `make test-integration`; build tag `integration`; `-p 1`; S3 conformance against MinIO: `make minio-up`, `make s3-conformance` | `test/integration/<area>/` |
| Chart | `make chart-lint`, `make chart-test`; build tag `chart` | `test/chart/` |
| End-to-end | `make e2e` against the dev cluster; build tag `e2e` | `test/e2e/` |
| Web unit | Angular's `unit-test` builder with Vitest and jsdom | `web/` |

The coverage evidence is written by CI from the unit, chart and integration reports and committed with each release; the
rules are in [coverage.md](coverage.md).

## ID schemes

| ID | Where it lives | Rule |
|---|---|---|
| `<PREFIX>-nnn` | [docs/features/](../features/README.md), one prefix per page (page table there) | Three digits; `### <ID> · <Title>`, then `Priority:` |
| `FMT-nnn` | [features/formats/README.md](../features/formats/README.md) | Cross-format behavior |
| `PKG-<FORMAT>-nnn` | [docs/features/formats/](../features/formats/README.md), one prefix per format page | The prefix is a short form shown by the page's first entry, e.g. `alpine.md` uses `PKG-APK`, `rubygems.md` uses `PKG-GEMS` |
| Priority | Every catalog entry and requirement | `MVP`, `P1`, `P2`, `Later`, `Out of scope`; rubric in [features/README.md](../features/README.md#priority-rubric) |
| `INV-n` | [spec/README.md](../spec/README.md#design-invariants) | Design invariants |
| `R-<AREA>-nnn` | [docs/spec/](../spec/README.md), one prefix per page (page table there) | `### R-<AREA>-nnn · <Title>`, then `Priority: **<P>** · Covers: <catalog IDs>` or `Covers: trove-only` |
| `Q-<AREA>-n` | `## Decisions` of each spec page | Status line, options table, the development agent's choice, rationale with a revisit condition, and what the choice changed; format in [spec/README.md](../spec/README.md#requirement-format) |
| `H-n` | `## What this does not cover` of the one page that records the gap: the spec page for a gap the specification owns, the security page of its mechanism for a gap docs/security owns; the security page of the mechanism links a spec-owned gap under its number | Unique across all spec and security pages; see [security/README.md](../security/README.md#form) |

Next free number for a prefix (examples for `RMT`, `R-REPO` and `H`):

```shell
grep -rhoE '^### RMT-[0-9]{3} · ' docs/features | sort | tail -n 1
grep -rhoE '^### R-REPO-[0-9]{3} · ' docs/spec | sort | tail -n 1
grep -rhoE '^### H-[0-9]+ · ' docs/spec docs/security | sed -E 's/^### H-([0-9]+).*/\1/' | sort -n | tail -n 1
```

## How to add something

The ordered steps for each kind of addition live on the topic pages, next to the code they describe:

- a configuration key: [configuration.md](configuration.md);
- a system API operation or an error code: [http-api.md](http-api.md) (the code registry is `internal/errs/codes.go`);
- a Valkey key family: [valkey.md](valkey.md);
- a job kind or a periodic task: [coordination.md](coordination.md);
- a metric family: [observability.md](observability.md);
- a Helm value or a rendered object: [helm-chart.md](helm-chart.md);
- a spec requirement or a catalog entry: the steps below.

For a spec requirement: take the next free number in the page's prefix, write the heading and the
`Priority:` line, state the normative text with MUST, SHOULD and MAY, and put any open question under
`## Decisions` in the block format of [spec/README.md](../spec/README.md#requirement-format). A security gap goes
under `## What this does not cover` as `H-<n>`, numbered across all spec and security pages. Then regenerate the
coverage checklist with `make coverage` and check the links with `go run ./internal/tools/coverage -links`.

## Conventions

- English only, in code, comments, commits and documentation.
- Documentation of a behaviour changes in the same change as the behaviour.
- Generated files are committed and carry the `DO NOT EDIT` header; `make generate-check` fails on a diff.
- No secret in a repository file, a ConfigMap or a log line; secrets are referenced as files.
