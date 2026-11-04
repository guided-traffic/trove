# trove · Project plan to the beta release

**Status:** ⚪ not started · 🟡 in progress or partly done · 🟢 done
**Last updated:** 2026-11-04

The way from zero to the beta release. The beta is trove with two package registries, **npm** and
**NuGet (.NET)**, each with local, remote (pull-through) and virtual repositories, running on the
production topology the owner requires: HA PostgreSQL, HA Valkey with Sentinel, S3, separate `web`
and `worker` roles, node drains without outage, and a backup job. Everything after the beta
(further registries such as Ansible Galaxy, PyPI, Docker, and platform breadth) is in
[FULL.md](FULL.md).

The beta release is what the [roadmap](../coverage/roadmap.md) builds in M0 to M5, M4b, the projects of M10 and the Bastion of M13 included, after
the owner walkthrough. Details, exit criteria and requirement IDs of every phase are in the roadmap
section linked under each heading.

**Releases:** Semantic Release cuts the releases from the conventional commits on `main`, on major version 0
([ADR-124](../decisions.md#adr-124--releases-stay-on-major-version-0)). This plan tracks what is built, not release
numbers (owner, 2026-11-02).

## Where we are

| Phase | Result | Status |
|---|---|---|
| [0 · Specification](#phase-0--specification) | Feature catalog, specification, decisions | 🟢 |
| [1 · Foundation](#phase-1--foundation-m0) | Skeleton, HA-shaped local stack, roles, drain-safe shutdown, TLS everywhere, endpoint foundation | 🟢 |
| [2 · Access core](#phase-2--access-core-m1) | Login, tokens, permissions, repositories, admin UI | 🟢 |
| [3 · npm registry](#phase-3--npm-registry-m2) | npm local, remote, virtual | 🟢 |
| [4 · NuGet registry](#phase-4--nuget-net-registry-m3) | NuGet v3 local, remote, virtual | 🟢 |
| [5a · Projects](#phase-5a--projects-and-delegated-administration) | Projects with project administrators who manage their repositories, policies and the technical accounts of external parties | 🟢 |
| [5 · GitLab CI OIDC](#phase-5--gitlab-ci-oidc-m4) | CI jobs get scoped tokens without stored secrets | 🟢 |
| [5b · trove Bastion](#phase-5b--trove-bastion-m13) | An exposed, read-only cache instance in front of the main installation that serves released content to external parties and takes load off it | 🟢 |
| [6 · Vulnerability scanning](#phase-6--vulnerability-scanning-m4b) | Own advisory mirror, findings and vulnerability report, malicious versions blocked, npm and NuGet audits from trove's data | 🟢 |
| [6a · Task areas](#phase-6a--task-areas) | trove cut into task areas: roles of one binary as Deployments of their own, an upstream fetch service, ingress rules between the chart's pods | 🟡 |
| [6b · Remote full mirror](#phase-6b--remote-full-mirror) | A remote repository can mirror every discoverable package of its upstream in the background, besides the default pull-through | 🟡 |
| [7 · HA, backup, Helm](#phase-7--ha-hardening-backup-and-helm-m5) | Drains and failovers without errors, backup and restore; beta candidate | ⚪ |
| [8 · Walkthrough and beta](#phase-8--walkthrough-and-beta-release) | Owner review, objections fixed; **beta release** | ⚪ |

## Testing rule for every registry

Every registry ships with both test levels before its phase counts as done:

- **Integration tests:** Go tests against real PostgreSQL, Valkey with Sentinel and an S3 store in
  containers. They drive the registry protocol over HTTP (publish, fetch, metadata, errors,
  permissions) and run the format conformance kit.
- **E2E tests:** the real package-manager clients against trove deployed with the Helm chart in a
  local Kubernetes cluster, with several `web` replicas and random request distribution, through
  local, remote and virtual repositories; plus browser tests of the registry's UI screens.

The [test matrix](#test-matrix) at the end tracks both levels per registry.

---

## Phase 0 · Specification

- 🟢 Feature catalog: 2,239 features of a full repository manager, prioritized ([docs/features](../features/README.md))
- 🟢 Specification: 553 requirements on 12 pages ([docs/spec](../spec/README.md))
- 🟢 Every open design question decided with options and rationale ([decisions](../decisions.md#decisions-by-the-development-agent))
- 🟢 Revision for Valkey with Sentinel, horizontal scale, backup job and no ReadWriteMany storage
- 🟢 Roadmap with milestones and testable exit criteria ([roadmap](../coverage/roadmap.md))
- 🟢 Coverage checklist: every MVP feature covered by the spec ([coverage](../coverage/README.md))

## Phase 1 · Foundation (M0)

Details: [roadmap M0](../coverage/roadmap.md#m0--foundation)

**Repository and toolchain**
- 🟢 Go module (Go 1.27.3) with pinned tools, task runner, architecture test
- 🟢 Angular 22 workspace; UI embedded in the Go binary
- 🟢 CI pipeline: lint, unit tests, integration tier, image build, chart lint, one version per tool
- 🟢 Coverage generator as a Go tool in the repository

**Data and storage**
- 🟢 PostgreSQL connection classes, transaction-mode pooler support, migration runner and migration Job
- 🟢 S3 blob store: spooling, hashing, content-addressed commit, multipart transfer, garbage collection
- 🟢 S3 conformance check against the local S3 store

**Coordination across replicas**
- 🟢 Job queue with leases, claimed only by `worker` replicas
- 🟢 Change log and epochs in PostgreSQL for cache invalidation
- 🟢 Valkey client with Sentinel discovery, TLS, ACL and degraded mode

**Server and deployment**
- 🟢 Server shell: roles `web`, `worker`, `all`; configuration; middleware; error model
- 🟢 Drain-safe shutdown: readiness off, endpoint delay, in-flight requests finish, jobs released
- 🟢 Helm chart skeleton: one Deployment per role, Services, PodDisruptionBudgets, topology spread
- 🟢 Local HA-shaped stack on the owner's toolstack: CloudNativePG with standby, valkey-operator with three Sentinels, SeaweedFS, HAProxy Ingress, cert-manager, internal-secrets-operator (dev-only deviation: the operator's default Valkey user, see [local-profile](../developer/local-profile.md))
- 🟢 Metrics, JSON request log, health and readiness endpoints
- 🟢 TLS on every connection, trove's listeners included; certificate rotation without restart
- 🟢 Endpoints: further client listeners with their own port, hosts (one wildcard allowed), base URL and certificates chosen by SNI; the endpoint is the listener's, a foreign host gets `421`; route families per endpoint; host-root binding reserved for later registries ([spec](../spec/endpoints.md))
- 🟢 Helm chart per endpoint: Service or own pool, Ingress with its own class, hosts and TLS Secret, NetworkPolicy per port (the Gateway route per endpoint arrives with the chart's Gateway support, R-HA-038, in phase 7)

**Exit checks**
- 🟢 PostgreSQL and Valkey failover under load without a failed request
- 🟢 Node drain under load without a failed request
- 🟢 No state between requests: test profile with random distribution across replicas
- 🟢 Listener certificate rotated under load without a failed request
- 🟢 A second endpoint with its own host and certificate answers `421` for a foreign host and serves only its route families

## Phase 2 · Access core (M1)

Details: [roadmap M1](../coverage/roadmap.md#m1--access-core)

**Identity and tokens**
- 🟢 OIDC login with a test identity provider (Keycloak in the dev profile, Q-AUTH-35), sessions, CSRF protection, logout
- 🟢 Break-glass administrator and bootstrap commands; the chart mounts its password from a Secret that internal-secrets-operator can generate and rotate (`secrets.breakGlass`)
- 🟢 Users, groups (from the login provider's claims) and service accounts
- 🟢 Personal and service-account tokens (`trv_pat_`, `trv_sat_`): scopes, lifetime, revocation
- 🟢 Brute-force protection with cluster-wide counters

**Permissions**
- 🟢 Permission policies: principals, repositories, name patterns, actions
- 🟢 Evaluation engine with caches that stay correct across replicas
- 🟢 Audit of logins, token changes, policy changes and denied requests
- 🟢 System role bindings from the Helm values (`config.authz.role_bindings`), applied by `trove migrate` and read-only in
  the API and the UI, so an IdP group becomes the administrator through GitOps; the `auditor` role reads the metadata of
  every repository and downloads no artifact (added 2026-11-02, owner:
  [Q-AUTHZ-170](../spec/authorization.md#q-authz-170--system-role-bindings-declared-in-the-configuration),
  [Q-AUTHZ-172](../spec/authorization.md#q-authz-172--the-auditor-reads-every-repositorys-metadata-and-triages-but-downloads-nothing))

**Endpoints and exposure**
- 🟢 Accepted credentials per endpoint; tokens bound to endpoints
- 🟢 Exposure rules per endpoint (repository, package and version patterns, version ranges, pre-releases, tags) as a ceiling for every caller, administrators included; hidden content answers 404
- 🟢 Management API and admin screen for endpoints and exposure rules, with an exposure check
- 🟢 Raw files on an endpoint filtered by exposure; rate limit per endpoint

**Repositories and files**
- 🟢 Repository model and management API (local, remote, virtual); the generic format implements the local class, remote and virtual repositories take shape with npm in phase 3
- 🟢 Generic local repositories and raw file transfer under `/files/`
- 🟢 API conventions: errors, cursor pagination, ETags, idempotency keys, rate limits

**Admin UI**
- 🟢 Sign-in, repository list and creation wizard, file browser
- 🟢 Users, groups, service accounts, personal tokens
- 🟢 Permission policy editor with a "why can X do Y" preview from the draft (Q-UI-21); the explain operation of R-AUTHZ-024 is P1 (Q-AUTHZ-27)
- 🟢 Endpoint screen with the exposure rule editor and its preview
- 🟢 Audit log and system status

**Tests**
- 🟢 Integration tests: login, tokens, permission evaluation, raw transfer
- 🟢 E2E tests: browser journeys across replicas with random distribution (Playwright with axe), and the exit checks E1.1, E1.8, E1.11, E1.13 and E1.14 on the kind cluster

## Phase 3 · npm registry (M2)

Details: [roadmap M2](../coverage/roadmap.md#m2--npm) · spec: [formats/npm.md](../spec/formats/npm.md)

**Engines shared by all registries**
- 🟢 Contract hardening from the M1 architecture review, before the format contract freezes ([roadmap WP2.0](../coverage/roadmap.md#m2--npm))
- 🟢 Format contract and conformance kit
- 🟢 Proxy engine: upstream settings, request collapsing through Valkey, metadata and negative caches, stale metadata while the upstream is down
- 🟢 Virtual engine: member order, merged metadata, deployment target, namespace claims, protection against dependency confusion

**npm**
- 🟢 Local: publish, package documents, dist-tags, unpublish, whoami, ping
- 🟢 Remote: proxy of the public npm registry, tarball URL rewriting, versions removed upstream
- 🟢 Virtual: merged package documents and dist-tags, writes through the deployment target
- 🟢 Clients: npm, pnpm, Yarn Classic, Yarn Berry
- 🟢 Package UI: package list and detail, README, install panel, client setup snippets
- 🟢 Repository settings form rendered from the format's settings schema (R-ARCH-004, R-UI-021, Q-ARCH-229, Q-UI-78 to Q-UI-82)
- 🟢 npm on a customer endpoint: package documents and dist-tags list only exposed versions, URLs on the endpoint's host, no upstream fetch from that endpoint by default
- 🟢 Security audit: `npm audit`, `pnpm audit` and `yarn npm audit` through remote and virtual repositories (R-NPM-038, owner 2026-10-31)
- 🟢 In-app guide for every npm variant: local, remote and virtual repositories with npm, pnpm, Yarn Berry and Yarn Classic (owner 2026-10-31)

**Tests**
- 🟢 npm integration tests: protocol over HTTP, conformance kit, nightly fuzzing of package documents (the three 30-minute fuzz legs passed in the closing run 41235811145)
- 🟢 npm e2e tests: install and publish with npm, pnpm, Yarn Classic and Yarn Berry through local, remote and virtual repositories
- 🟢 npm e2e: cold cache with three clients at once, upstream down, Valkey stopped, replica killed mid-install (the Valkey-stopped leg passed in the closing run 41235811145)
- 🟢 npm load in the drain and failover tests built (`TestNPMHA`, `TestNPMHACrash`, `make e2e SET=npm-ha`); since
  2026-11-01 they run in `HA and Performance (nightly)` and on dispatch, not per change (owner). Making them pass reliably,
  and the drain criterion of the security audit (R-NPM-038), moved to phase 7
- 🟢 npm e2e: a customer endpoint through its own Ingress lists only exposed versions; hidden versions answer 404

**Open findings of 2026-11-01, fixed in the closing run**
- 🟢 F-1 to F-16 and F-18 of [promt.md section 8](promt.md#8--open-findings): npm publish snippets, setup tokens through a
  virtual repository, the browse curl commands, the policy preview for service accounts, the `manage` help, revoke by value,
  service-account token actions, endpoint binding in the token forms, last use of tokens, the maintenance-mode row, the
  break-glass password status, the saved UI language, stored versions in the exposure check, underscore IDs in the coverage
  evidence, allocation-free pattern matching in `Decide`, the help journey with axe in both schemes, the status texts. The
  code and security reviews of the run found a readable-package leak in the exposure check and the reach of a virtual
  repository's token scope, and Test and Release found a light accent below 4.5:1; all fixed before release
- 🟢 Long tests out of the per-change pipeline (owner, 2026-11-01): `failover` and `npm-ha` run in
  `HA and Performance (nightly)`

## Phase 4 · NuGet (.NET) registry (M3)

Details: [roadmap M3](../coverage/roadmap.md#m3--nuget) · spec: [formats/nuget.md](../spec/formats/nuget.md)

**Carried over**
- 🟢 Renovate pull request #42 (TypeScript 7, a major update): it cannot merge while `@angular/compiler-cli` 22.2 and typescript-eslint accept only `typescript <6.1`; a Renovate rule holds TypeScript below 6.1 until Angular's peer range admits 7 ([ADR-140](../decisions.md#adr-140--typescript-is-held-below-61-until-angulars-peer-range-admits-7x))
- 🟢 CI checks against the latest release: `api/openapi.yaml` with a breaking-change checker (R-ARCH-025) and the embedded migration files (R-ARCH-026), job `compat` and `make compat-check`

**NuGet v3**
- 🟢 Local: service index, package content, registrations, search, push, unlist, relist, delete; search terms meet only the
  versions the caller may read and the repository's patterns apply (Q-NUGET-74, Q-NUGET-77)
- 🟢 Symbol package push (`.snupkg`): validated and stored, one indexing job per symbol package (Q-NUGET-11)
- 🟢 Remote: proxy of nuget.org with resource URL rewriting, discovery, re-paging, the identity check of a cached package
  (only content that is no package passes without it, Q-NUGET-84), pages and leaves bounded at the fill and the rendering
  (Q-NUGET-83), deprecations and vulnerabilities without the upstream's addresses (Q-NUGET-85), and the forwarded search
  with the withheld-query rule (R-NUGET-016); a claim filters the cached forwarded answers of every replica within the bound
  of R-NUGET-016, with and without Valkey (`TestNuGetSearchClaimTiming`, `TestNuGetClosing`)
- 🟢 Virtual: merged package content and metadata, a nuget.org member's registration merged whole (gzip-coded, pages by
  address, Q-NUGET-82), merged search over local and remote members decided on the addressed repository, four members at
  a time on one scan budget (Q-NUGET-75, Q-NUGET-76, Q-NUGET-81), publishing through the deployment target
- 🟢 Authentication: token as password in `nuget.config`, token as API key for push (`X-NuGet-ApiKey`, Q-AUTH-186)
- 🟢 Clients: the .NET 10 SDK on Linux and nuget.exe on Mono run against trove on every change (e2e `nuget`); setup
  instructions for Visual Studio in the guide. The Windows row and the manual Visual Studio checklist run in the owner
  walkthrough of phase 8 ([Q-NUGET-72](../spec/formats/nuget.md#q-nuget-72--the-windows-row-of-e31-and-the-visual-studio-checklist-run-in-the-owner-walkthrough-not-on-a-ci-runner))
- 🟢 Package UI and client setup snippets for .NET (`dotnet`, nuget.exe, `nuget.config`, the token in each shell's spelling)
- 🟢 In-app guide for every NuGet variant: local, remote and virtual repositories with the .NET CLI, nuget.exe and Visual Studio, and `nuget.config` (R-UI-049, owner 2026-10-31); browser journeys `10-help` and `11-nuget`
- 🟢 NuGet on a customer endpoint: version lists, registrations and search list only exposed versions, a search term meets
  only those, through a virtual repository its own rules decide, the validators follow only what the endpoint shows, and
  there is no push there (`TestNuGetEndpoint`; the search through a virtual `nuget-customers`:
  `TestR_NUGET_034_SearchThroughAVirtualRepositoryOnAnEndpointWithRules`;
  [nuget-virtual.md](../security/nuget-virtual.md), [endpoints-and-exposure.md](../security/endpoints-and-exposure.md))
- 🟢 Format contract additions found by the M2 architecture review (roadmap M3, WP3.0): a hook on a remote fill, discovery documents and the URL map for metadata, pass-through and fan-out for search, content on `Volatile` routes, a format that can name only its own repository, rendered-document mechanics in the contract, validation of remote configuration, the format's rules on the raw routes, a typed environment for a format, the dormant methods, the writer-owned eviction signal, no format knowledge in the virtual bridge, one engine value type each, links in refusals, the declared length of a body, a minimal contract, the per-format data-port leaf rule, the real populate decision in the audit event; and from the review of 2026-11-01 (WP3.0u to WP3.0w): the addressed repository on member requests through one derivation, stored versions as a method of the view adapter, and a test that the written contract equals the code
- 🟢 Format contract frozen for all later registries: `ContractVersion` 1, its surface recorded in
  `internal/formats/testdata/contract.txt`, npm and NuGet pass all eleven kit checks
  ([ADR-141](../decisions.md#adr-141--the-format-contract-is-frozen-at-version-1)); check 6 also holds a discovery parser to
  its bounds on crafted service indexes

**Tests**
- 🟢 NuGet integration tests: protocol over HTTP, conformance kit, SemVer 2 and ID normalization, golden vectors of the
  .NET version rules, the action map of R-AUTHZ-007 with and without each action (E3.4)
- 🟢 NuGet e2e tests: `dotnet restore`, `dotnet nuget push` and nuget.exe through local, remote and virtual repositories
  against the counting test upstream (set `nuget`, per change), and the run against nuget.org of E3.1 (`TestNuGetClosingPublic`,
  set `nuget-closing`), green on kind after the fix wave; its run in Test and Release on the commit that closes the phase is
  part of that closing run
- 🟢 NuGet e2e: cold cache with three clients at once (E3.2, one upstream request per `.nupkg`), a push whose answer the edge
  drops, documents and restores with Valkey stopped (set `nuget-closing`); the drain part moved to phase 7
  ([Q-NUGET-73](../spec/formats/nuget.md#q-nuget-73--the-nuget-part-of-the-drain-test-moves-to-phase-7-with-the-other-long-legs))
- 🟢 NuGet load joins the drain and failover tests: moved to phase 7 (Q-NUGET-73), where it is an item; the nightly leg `nuget-ha` runs its set
- 🟢 NuGet e2e: `dotnet restore` through a customer endpoint gets only exposed versions; a hidden version fails to resolve
  with `NU1102`, and a push answers 403 (`TestNuGetEndpoint`, Q-NUGET-71)

**Findings of 2026-11-02**
- 🟢 F-20 to F-26, F-28 and F-29 of [promt.md section 8](promt.md#8--open-findings), from the code review and the security
  audit of the phase: the forwarded search; the search decided on the addressed repository and on the versions the caller
  may read, with a bounded fan-out and the repositories' patterns; a nuget.org member's registration through a virtual
  repository; the local documents (SemVer 2 dependency ranges, validators on endpoints with rules, the lock order of a
  removal, the revision of the version list); upstream addresses in deprecations and vulnerabilities; the identity check
  and the bounds of a remote's cache fill; the package reader's central directory; the write budget of `access-long`; a kit
  check for discovery documents
- 🟢 The code review and the security audit of the fix wave: a `version:` term that carried a protected name upstream,
  upstream addresses in forwarded results, a forwarded result matched on a hidden version, an unbounded forwarded answer,
  a Unicode path ID past patterns and claims, unbounded leaves at a page fill, the search scan paging by offset, explicit
  folders left after a subtree delete, the validator of a remote document on an endpoint with rules; the remaining gaps are
  H-345, H-347, H-348 and H-351 to H-353 on the [security pages](../security/README.md)
- 🟢 R-NUGET-016's claim timing criterion, which the closing of F-20 left without a test, runs in e2e: no replica lists the
  claimed package 100 ms after the claim with Valkey and 860 ms after it with Valkey stopped (kind)
- 🟢 F-27: the run against nuget.org is green on kind, all seven cases (the forwarded search, nuget.exe, the restore and the
  registrations through the remote and the virtual repository)
- 🟢 F-30: the rows stored before the SemVer 2 classification counted dependency ranges are recomputed once per
  installation by the background job `data_migration_nuget_semver2` on a worker, after the replicas of 0.9.0 are gone
  ([Q-NUGET-93](../spec/formats/nuget.md#q-nuget-93--the-versions-stored-before-the-semver-2-classification-counted-dependency-ranges-are-recomputed-once-by-a-background-job-in-go))

## Phase 5a · Projects and delegated administration

Details: spec [authorization.md](../spec/authorization.md) R-AUTHZ-026 · decisions
[Q-AUTHZ-169](../spec/authorization.md#q-authz-169--a-technical-account-for-a-project-belongs-to-the-project) (owner)

The owner moved projects into the beta on 2026-11-02, before GitLab CI OIDC: project administrators create technical
accounts for external parties and give them fine or coarse access to chosen repositories of their project, and the
Bastion serves those parties. Phase 5 then maps GitLab pipelines into projects.

- 🟢 Spec first: R-AUTHZ-026 completed with project service accounts (Q-AUTHZ-169) and the project screens; a project
  administrator creates remote repositories too, bounded by the outbound address rules
  ([Q-AUTHZ-173](../spec/authorization.md#q-authz-173--a-project-administrator-creates-remote-repositories), owner 2026-11-02);
  R-API-047 and R-UI-041 raised to MVP; decisions Q-AUTHZ-174 to Q-AUTHZ-195, Q-AUTH-189 to Q-AUTH-191, Q-REPO-125 and
  Q-REPO-126, ADR-145 and ADR-146; contract page [projects.md](../developer/projects.md)
- 🟢 Projects: an immutable key, display name and description, the switches `managePolicies` and `manageRepositories`
  set by an administrator; a repository belongs to at most one project, repositories a project administrator creates
  carry the project key as prefix (migration `00019_projects`, `/api/v1/projects…`, `test/integration/projects`)
- 🟢 Project administrators: users and groups named on the project; they hold `manage` on the project's repositories and,
  with the switches, manage its policies and repositories; they never bind system roles and see no global policy. Built
  and tested over three replicas (`TestR_AUTHZ_026_*` in `test/integration/authz`); a switch that is off answers
  `403 project_switch_off` whatever else the caller administers (Q-AUTHZ-180 amended); `manage` follows the credential
  (Q-AUTHZ-196)
- 🟢 Project policies: fine (one repository by key) or coarse (pattern or class) and only over the project's own
  repositories; the effect is the union with global policies; their grants live where an older replica does not read
  them, so a rolling upgrade never widens them ([ADR-145](../decisions.md#adr-145--the-grants-of-a-project-policy-are-stored-where-the-previous-release-does-not-read-them))
- 🟢 Project service accounts (owner, Q-AUTHZ-169): created, disabled and deleted by project administrators, named
  `<projectKey>-…`, their tokens issued and revoked by them, granted only through the project's policies; a project is
  empty, and can be deleted, only without repositories and service accounts. Built, and the project administrator's
  journey over HTTP passes (`TestR_AUTHZ_026_ProjectAdministratorServiceAccountsAndTokens`); a delegated token is never
  self-rotatable (Q-AUTH-192), a name needs a character after `<key>-` (Q-AUTH-193)
- 🟢 Admin UI: projects for administrators; for a project administrator the project's repositories, policies, service
  accounts and their tokens; the auditor sees every project read-only. Component tests (608 web tests) and the browser
  journey `12-projects` on kind (`TestE1_11Journeys`, set `access-long`, green with 17 journeys)
- 🟢 Audit of every project change (`authz.project.*`, journal entries for the removals, repository events name the
  project, a delegate's refusals as `authz.denied`); security page
  [delegated-administration.md](../security/delegated-administration.md); the findings of the security audit and the code
  review fixed below, the open gaps are the H-items of that page
- 🟢 Integration tests: a project administrator cannot reach a repository, policy or principal outside the project, cannot
  bind a system role, cannot create a token that outlives or outranks the project's grants (`test/integration/authz`,
  `projects`, `transfer`, `identity`; the whole integration tier green, 25 packages, no project test skips any more);
  🟢 e2e: a partner's project service account installs with npm and pnpm and restores with dotnet through the main
  installation (`TestNPMProjectPartner`, `TestNuGetProjectPartner` in the per-change sets `npm` and `nuget`, green on kind;
  outside the project 404 / `E404` / `NU1301`, a revoked token 401)

**Findings of the run (2026-11-02)**
- 🟢 Listing the tokens of project service account `ext-bot` returned a personal token of user `wanda`: no leak. Without
  `tokens:read` the owner filter is the caller (R-AUTH-028, unchanged since before the phase), so wanda saw her own
  token; the test now asserts that, and the administrator's list of the account ([tokens-and-credentials.md](../security/tokens-and-credentials.md))
- 🟢 A switch that is off answered `403 forbidden` or `403 project_switch_off` for the same request depending on whether
  the caller administers another project with the switch on; the declaration step now admits every project
  administrator and the handler decides (Q-AUTHZ-180, ADR-146 amended)
- 🟢 The access bridge of the application did not forward the project questions, so every delegated operation refused
  project administrators in the real application; a probe in the transfer journey had turned that into a skip (commit
  b9a4f7f, `TestR_AUTHZ_026_TheBridgeForwardsTheProjectQuestions`)
- 🟢 A project administrator's repository delete now rechecks the project of the locked row (`Service.DeleteTxIn`)
- 🟢 A shared session lookup answered a request that came after its end, so `TestR_AUTH_023_PeekLeavesTheCacheAsItFound`
  failed in Test and Release run 41466257735 (6 of 9,000 local runs); the flight now leaves the map before it answers
  (commit fb53a61)
- 🟢 F-31 (upstream client cache) fixed; F-19 closed on the green failover leg of nightly run 41420733312
- 🟢 `TestDoubleFaultDelay` failed in Test and Release run 41531079294 ("a held answer must time the client out"): when the
  client's timeout cut the hold, the issuer double returned without writing, net/http answered an empty 200, and the Go transport
  used that response because it raced its own cancellation (one in about 450 runs under CPU load); the double now aborts the
  connection (`http.ErrAbortHandler`), and the test's 200 ms upper bound on an undelayed answer, which only measured the machine,
  is gone

**Security audit and code review of the phase (2026-11-02; fixed in the fix wave, what stays open is an H-item of its page)**
- 🟢 HIGH · A project administrator with `manageRepositories` could send installation secrets to a host it chooses: the
  unsaved upstream test resolves any `secretRef` (`env` `TROVE_CAC_…`, a file in `secrets_dir`) and sends it to the URL of
  the request, and a `PATCH` that changes the URL of an assigned remote keeps its sealed password (`{"set": true}`)
  (`upstream_api.go`, `repository/upstream.go`, `secrets_store.go`); H-281 covered `repositories:write-config` only.
  Fixed: a project administrator gives an upstream secret as a value, a stored one does not follow a new address
  (422 `secret_value_required`, Q-REPO-127), and the outbound path stays the installation's (422 `outbound_path_restricted`,
  Q-REPO-128); `TestR_AUTHZ_026_ADelegatedUpstreamSendsOnlyItsOwnSecret` received `Bearer installation-file-secret` at a
  stub before the fix; what stays reachable of an upstream an administrator assigned is H-403
- 🟢 HIGH · The `manage` grant of a project administrator worked through any credential (an `inherit` token without
  `admin: true`) and for a global service account that is a member of an administrator group, against Q-AUTHZ-183
  (`authz/index.go`; confirmed by the code review with a probe: both cases hold `manage` on `web-npm`, while `ProjectsOn`
  reports no project for the token; the tests that should catch it used subjects without groups). Fixed: `manage` on a
  project repository is decided from the administration under the same rule as `ProjectsOn`, no grant per repository
  (Q-AUTHZ-196, `TestR_AUTHZ_026_ManageOfAProjectAdministratorFollowsTheCredential`)
- 🟢 MEDIUM · Deleting a project repository with `detach=true` changed virtual repositories outside the project, and the
  409 without it named them (`repository/delete.go`, `members.go`). Fixed: a project administrator gets 409 naming only the
  project's own virtual repositories, with or without `detach` (Q-REPO-129)
- 🟢 MEDIUM · The policy list filters (`principal` with `expandGroups`, `repository`) and the policy validation told a
  project administrator about group memberships, repositories and service accounts outside its project
  (`authz_api.go`, `policy_list.go`, `validate.go`). Fixed: unknown and foreign answer alike, `expandGroups` is 403 for a
  delegate (Q-AUTHZ-197)
- 🟢 LOW · A project service account bound through `authz.role_bindings` or named in a global entry was refused only in
  code, not by the database; a race between a handler's narrowing and the policy write or the saved upstream test; global
  policy names under a project's prefix; an auto-join group in a project policy stands in for `authenticated`; a delegated
  token may be self-rotatable and so unbounded; "New policy" in a project-scoped list creates a global policy; the spec
  puts a service account's description change under two different rights; refusals of a project administrator may leave
  no audit event
  - 🟢 fixed by the fix wave: the database guard `AI005` and the build's skip of such rows (Q-AUTHZ-198, Q-AUTHZ-203; H-397
    closed), the delegate's project checked under the policy lock and the project lookups in the write's transaction
    (Q-AUTHZ-199), the project delete names the project (H-407 closed), global policy names never take a project's prefix
    and a project policy names no auto-join group (Q-AUTHZ-200), a delegate's refusals audited as `authz.denied`
    (Q-AUTHZ-201), the description row of R-AUTHZ-026 corrected; "New policy" of a project-scoped list (fix(ui) above);
    left: H-405 (a global policy that predates a project keeps its prefix), H-406 (the refusal event does not name the
    project); the saved upstream test rechecks the project (`UpstreamTestIn`), a delegated token is never
    `selfRotatable` (Q-AUTH-192), `repository.created`, `updated` and `deleted` name the project
- 🟢 MEDIUM (code review) · `GET /projects/{key}` answered `304` for a body that changed: its ETag is `version`, which an
  administrator or assignment change does not move (Q-AUTHZ-185) (`projects_api.go`); the read now ignores
  `If-None-Match` and `PUT` keeps `If-Match` on `version` (Q-AUTHZ-202)
- 🟢 MEDIUM (code review) · A service account named exactly `<key>-` (the UI prefills it) passed the code check and failed
  the trigger `AI003`, which was not mapped: `500` instead of `422 project_prefix_required` (`serviceaccounts.go`). Fixed:
  at least one character after the hyphen for service accounts, repositories and policies, `AI003` mapped (Q-AUTH-193)
- 🟢 LOW (code review) · Migration `00019_projects` built indexes and constraints on existing tables without
  `CONCURRENTLY` / `NOT VALID`, against R-HA-018's expand class (fixable in the file: not released yet); a 0.9.x rewrite of
  a project policy with no entries succeeds; the project checks of a policy write open their own transactions (500 on a
  race with a project delete, a second pool connection per lookup); the project delete removes policies by name;
  `repository.created` of a project repository carries no `project`; the project dialog's retry after a refusal reuses
  its idempotency key and treats a `404` of an undo step as failure; doc drift (Q-AUTH-189 against H-399, R-REPO-008's
  code, `internal/project` package comment, two unused queries); the delete retry branch has no test
  - 🟢 fixed by the fix wave: migration `00019` split into the expand steps `00019` to `00025` (indexes `CONCURRENTLY`,
    constraints `NOT VALID` then validated, Q-AUTHZ-203) with the database guard `AI005` against a project service account
    in a role binding, a global entry or another project's policy; the project delete removes each policy at the version
    it read and for its project (Q-AUTHZ-204, Q-AUTHZ-199; H-407 closed); the
    retry rounds are tested; the package comment and the unused queries corrected
- 🟢 LOW (UI fix wave) · The assignment API took no expected current owner (F-32): `PUT …/repositories/{repoKey}` takes
  `?from=<key>` (empty: none; absent: no expectation), compared under the project locks of the write, and answers 409
  `project_owner_changed` with `currentProject` (Q-AUTHZ-205); the project dialog sends the owner it read and asks again on
  that answer; test `TestR_AUTHZ_026_AssignNamesTheExpectedOwner` and the dialog's component test
- 🟢 (code review, predates the phase) · `repository.Service.List` ordered in SQL and compared its cursor bytewise in Go;
  under the integration database's `en_US.utf8` it lost `web-b`, `web-c` and `web.d` across pages
  (`TestR_REPO_012_ListPagesEveryKeyOnceInByteOrder`). Fixed: `ORDER BY key COLLATE "C"`; the column itself is not
  `COLLATE "C"` yet (H-404)

## Phase 5 · GitLab CI OIDC (M4)

Details: [roadmap M4](../coverage/roadmap.md#m4--gitlab-ci-oidc) · spec: [gitlab-ci-oidc.md](../spec/gitlab-ci-oidc.md)

**Open findings, worked off first (2026-11-02)**
- 🟢 F-32: a move of a repository between projects names the expected current owner (`?from=<key>`, empty for unassigned)
  and answers 409 `project_owner_changed` when it differs; the project dialog sends it
  ([Q-AUTHZ-205](../spec/authorization.md#q-authz-205--an-assignment-names-the-owner-it-expects-and-the-write-compares-it-under-its-locks), `TestR_AUTHZ_026_AssignNamesTheExpectedOwner`)
- 🟢 F-33: the e2e sign-ins wait out the break-glass rotation hold (`test/e2e/rotationhold`, `submitLocalForm` in
  `web/e2e/support.ts`, [Q-AUTH-194](../spec/identity-and-authentication.md#q-auth-194--the-e2e-sign-ins-wait-out-the-break-glass-rotation-hold));
  verified on kind inside a forced rotation (the first sign-in waited 33 s, 19 journeys green)

**Exit criteria of M4 and their evidence (2026-11-03)**
- 🟢 E4.1 (against the issuer double, Q-CI-25): `TestCIPipeline` of the set `ci` (Test and Release job `e2e-ci`, per change)
- 🟢 E4.2: the negative cases of R-CI-004 in `internal/ci/subject` and `test/integration/ci` (`exchange_errors_test.go`), the
  algorithm-confusion token (Q-CI-24), the key rotation against the double (`keys_test.go`)
- 🟢 E4.3: `replay_test.go` (two replicas, single use off and on)
- 🟢 E4.4: `TestCIPipeline`'s fork case (`cross_project_source_denied`; with `read_only` the publish gets 403)
- 🟢 E4.5: `TestCIPipeline`'s dry-run comparison over the five claim sets
- 🟢 E4.6: `TestCITokens` (401 after expiry on three replicas, a revocation in force within 37 to 42 ms)
- 🟢 E4.7: the set `ci-ha` in the dispatched run 41575201390 of `HA and Performance (nightly)` (drain, Valkey stopped with every
  replica ready and `degraded: [valkey]`, single use across a Sentinel failover and `FLUSHALL`, a web pod deleted under 50
  jobs); the restore part waits for M5 (E5.12)

**The owner's requests during the run (2026-11-02)**
- 🟢 Migrations are immutable once on `main`, released or not: a committed file is never edited, renamed or removed, and a
  defect is fixed forward with a new migration of the class R-HA-018 gives it. `make compat-check` (job `compat`) reads the
  history of `main` and fails on a change, with the file, the commit and the rule in the message; its allowlist
  `historicalEdits` names the two commits from before the rule ([Q-ARCH-283](../spec/architecture.md#q-arch-283--is-a-migration-on-main-immutable-and-what-does-the-check-hold-to),
  `TestR_ARCH_026_MigrationsOnMainAreNeverEdited`). The cause: the dev cluster had migrated from `main` before
  `00019_projects.sql` was rewritten into expand steps, and every rollout then failed on the checksum mismatch; its database was
  reset. The owner: pre-alpha, that reset was an accepted cost; from the completion of this plan on, reliability of installed
  data is priority 1
- 🟢 The Browse screen reinvented: a real two-pane browser over the full height with a compact tree toolbar (filter as you
  type, filter panel with chips, sort), a details pane with the selection's path and actions, folders as the shared data
  table, clear empty states (R-UI-010, R-UI-011; owner: "the worst design I have ever seen";
  [Q-UI-98](../spec/api-and-ui.md#q-ui-98--the-file-browser-is-two-panes-with-a-toolbar-and-a-details-header)); built,
  625 web tests and axe in English and German, light and dark, against a stub API; the journeys `02` and
  `03` are green on kind (`make e2e SET=access-long`, 20 journeys, 2026-11-03)
- 🟢 The manual uses the width of the screen (phase 8, beta documentation; Q-UI-97)

**Findings of the run (2026-11-02, while writing the manual against the contracts; status per item)**
- 🟢 HIGH (design) · `claims.Frozen` carried one `Project` for all inline grants, while a token can unite the match of the
  system tier with matches of several project tiers (R-CI-009). Now one contribution per matched mapping with its tier, its
  project, its inline grants and its caps, and a versioned bundle (W2, Q-CI-17 amended); W5 evaluates per contribution
- 🟢 The snippet operation (R-API-035, R-UI-035) had no project route, so a project administrator could not get a mapping's
  snippet; `GET /projects/{key}/ci/issuers/{issuer}/snippets/{kind}` answers it for a mapping of the project's tier, and the
  spec says the operation is MVP for R-UI-035 while R-CI-027 stays P1 (W3)
- 🟢 R-AUTHZ-026 "Deleting a project" and its table row did not name CI mappings as a blocker; both do now, and the delete
  answers 409 `project_not_empty` while a project has CI mappings (W3)
- 🟢 The spec listed three save-time warnings, `internal/ci/reason` six more; R-CI-010 lists all nine now
- 🟢 A rule violation of a management write: every violation in `errors[]` with its pointer and code, a rule the spec names
  also as the problem's `code` (Q-CI-30)
- 🟢 `ca_bundle_ref`: `file:/abs/path` or `env:TROVE_CIC_<NAME>`; the installation's trust always applies and the bundle
  only adds to it (Q-CI-31)
- 🟢 The exchange: a `ttl` below `ci.token.min_ttl` answered `subject_token_expires_too_soon`, which names the wrong cause; an
  empty `subject_token` had no stated reason; `ci.exchange.enabled: false` answering 404 was only in the code; the 405 of
  R-CI-007 was not in OpenAPI (W4). A requested `ttl` below the floor is the caller's `ttl_below_minimum` at step 1 and an empty
  `subject_token` a missing parameter, `malformed_request` ([Q-CI-41](../spec/gitlab-ci-oidc.md#q-ci-41--the-reason-of-a-request-that-cannot-succeed-by-its-own-parameters)),
  and R-CI-007 and the OpenAPI operation state the 404 and the 405 (`TestR_CI_007_TheEndpointsOtherAnswers`)
- 🟢 The reference pipeline's error line (R-CI-028) printed no `requestId`, which the administrator needs to find the refusal;
  the error line of the examples, the snippet and the manual prints `error`, `error_description`, `reason` and `requestId`, and
  R-CI-028 has the acceptance criterion (W3)
- 🟢 The closing revocation of the reference pipeline, `- printf 'Authorization: Bearer %s\n' …`, is a YAML mapping, not a
  command: a plain scalar with a colon and a space in it. GitLab refuses such a `script` entry. The examples, the manual and the
  snippet write it as a block scalar (`- |`), and a test parses the snippet as YAML (W3)
- 🟢 `TestR_CI_003_ARotationCausesOneFetchAndRandomKidsCauseNone` failed in 2 of 4 runs: the lookups caused no extra read (the key source lets at most one re-read per `cluster.epoch_poll_interval` through), but PostgreSQL flushed the 10 reads of the preceding rotation into `pg_stat_user_tables` between 0.9 s and 1.6 s late, on either side of the test's 1.5 s wait, so they landed in the measured window; the test now waits 2.5 s (`statsFlushWait`) before each reading and the bound is unchanged
- 🟢 Two implementations of R-CI-003's key rules: W1's `internal/ci/keys_jwks.go` (built while the shared leaf did not exist)
  and W2's `jws.ParseJWKS`; one of them goes, so the key status and the validation cannot disagree (W4). The key store stores and
  reads its sets through `jws.ParseJWKS` and selects with `jws.Candidates`, its own rules are gone
  ([Q-CI-42](../spec/gitlab-ci-oidc.md#q-ci-42--one-implementation-of-the-key-rules-the-shared-leaf), `TestR_CI_003_TheKeyStatusAndTheValidationAgree`)
- 🟢 Test and Release run 41525937899 failed in `integration (shard b)`: since W1, `trove migrate` applies `ci.issuers` on
  every run, and the test helper `testenv.DeclareRoleBindings` built a bare configuration without the CI caches' SafetyTTL
  (`cache: "ci_issuers" needs a SafetyTTL`); the helper sets them as `config.Load` does
- 🟢 `TestR_CI_003_AFetchHoldsNoTransactionOpenAndAnUnchangedSetLeavesTheEpoch` failed in 2 of 3 runs after the merge of W3:
  its sampler counted every connection that passed through `idle in transaction` for under a millisecond between two
  statements of an ordinary transaction (a job insert, a gauge read); it counts the connections in that state for more than
  1 s now, which a transaction held over the 4 s fetch still is (8 of 8 runs pass)
- 🟢 W3 declared two package-level slices in `internal/ci` against R-ARCH-032 (the architecture test caught it at the merge);
  both are functions now
- 🟢 Deleting an issuer with `?cascade=true` deleted its mappings' principals before it revoked the issuer's tokens; a token
  row belongs to its mapping's principal (`ON DELETE CASCADE`), so the rows went without `ci.token.revoked` and its journal
  entry. The tokens are revoked first now (`TestR_CI_002_ACascadeDeleteRevokesTheTokensBeforeTheMappingsGo` found 0 events
  before the fix)
- 🟢 A second GitLab project that publishes byte-identical content for an existing npm version gets 201, the registry's
  answer to a retried publish, without the ownership check; nothing is written (observed by W5, not checked further) — no defect: the answer
  acknowledges the retry of a publish whose bytes the registry already holds, writes nothing, and gives the second project
  neither a version nor the name
- 🟢 `make image` failed with "no space left on device": the build context took in `.claude/worktrees`, the worktrees of
  the development sessions with their tool binaries; `.dockerignore` leaves `.claude` out
- 🟢 The owner's dev cluster runs the current main again (owner, 2026-11-02, option a): its `trove` schema was dropped and the
  migration Job built it afresh; the database's earlier content is gone
- 🟢 `make dev-up` on an existing cluster installed a newly loaded `guidedtraffic/trove:dev` without rolling the pods: the tag
  and the pod spec were unchanged. `deploy/dev/up.sh` now puts the loaded image's ID into the pod annotation `trove-dev/image-id` of
  both roles, so the pods roll exactly when the image changed and a second run with the same image changes nothing (checked on the
  dev cluster: a changed image rolled `web` and `worker`, the next `make dev-up` left the pods alone; W7)
- ➡️ `TestR_CI_021_TheAddressBudgetIsOneAcrossReplicas` holds R-CI-021's "1,000 requests within 5 s" as a hard bound; on a
  CPU-limited CI runner the flood may need longer, which would fail the test without a defect (W4's note; watched) — moved to the open findings as [F-34](promt.md#8--open-findings)
- 🟢 R-CI-029's "every replica stays ready and reports `degraded: [valkey]`" while Valkey is stopped has no assertion in the
  integration tier (W7) — asserted by `TestCIHAValkeyStopped` (set `ci-ha`, W7)
- 🟢 Test and Release run 41549404703 failed in `compat`: W4 typed the exchange's `ttl` as a string with a pattern, a breaking
  change against v0.13.0 (R-ARCH-025), which the local check missed because the tag `v0.13.0` was not fetched; `ttl` keeps the
  released `integer`, and the handler takes a JSON number or a string of digits as before
- 🟢 Test and Release run 41549404703 failed in `unit tests` at `TestFlightLockSubscriptions` ("fifty waiters of one flight
  cost one SUBSCRIBE", R-ARCH-033): the test released the flight after a fixed 100 ms sleep, and a waiter goroutine that a
  loaded runner had not yet scheduled made its first `GET` after the `DELIFEQ`, which `Wait` documents as `gone` (the flight
  code keeps its promise). The test, and its two siblings with the same shape, now wait until every waiter's first `GET` has
  been counted before they release
- 🟢 Test and Release run 41553486196 failed in `unit tests` at `TestStallTimeout` ("every successful chunk moves the deadline,
  so a slow but steady transfer completes", R-ARCH-005: the body arrived cut at 70 bytes); the server keeps its promise, the
  test left no margin: net deadlines are absolute and a read whose goroutine is not scheduled before the deadline fails even with
  the bytes in the socket, and the test's 100 ms chunk gap against a 300 ms stall timeout (the cut came 0.3 s after chunk 7, the
  run took 0.94 s) did not survive a 200 ms pause of a loaded runner; the gap is now a sixteenth of the stall timeout and the
  transfer lasts three stall timeouts, both derived from the configured timeout
- ➡️ `TestR_CI_003_StaleAndUnreachable` failed once in W3's full run of `test/integration/ci` while three agents shared
  the stack, and passed alone and in the next full run; watched, not yet reproduced — moved to the open findings as [F-35](promt.md#8--open-findings)
- 🟢 Test and Release run 41518863868 failed in `e2e on kind (access-long)`: the journey `06` clicked "Refresh now" by a
  substring match, and the new task `ci-keys-refresh` added a "Run task ci-keys-refresh now" button to the same screen; the
  locator is exact now
- 🟢 The dispatched run 41568514247 of HA and Performance (nightly) failed `TestCIHAPodDelete` in `e2e-ci on kind (ci-ha)`
  (drain, Valkey stopped, single use and FLUSHALL passed): 15 of the 50 jobs got `Post "https://oidcstub.trove.svc:8444/mint": EOF`
  and the other 35 ran 444 exchanges. No exchange was refused; the test infrastructure failed. The jobs minted their ID tokens at the
  same moment, each over a new TLS connection (`DisableKeepAlives`) through the one `kubectl port-forward` to the double's control
  port, and the CI runner is CPU-limited. The double is not the limit (verified: 400 simultaneous new TLS mints through a
  port-forward to the double on kind all answered 200, and `TestControlAPICarriesConcurrentMints` has 200 on a new connection each
  under `-race`); the burst of 50 new forwarded streams is the one thing the path has that those do not. Not reproduced on the
  laptop, so the stream creation of `kubectl port-forward` on the starved runner is the likely link, not proven. Now `mintAll`
  mints the 50 tokens before the window, 6 at a time over kept-alive connections, and fails the test on a refused mint, so the
  measured window holds trove's exchanges only and the criterion is unchanged (every exchange 200, no reset, no 5xx, no retry).
  `make e2e SET=ci-ha` twice on kind: 403.7 s and 380.3 s, 628 exchanges by 50 jobs both times, all 200. The nightly run
  after this change is the closing evidence

**Code review of the phase (2026-11-03; status per item)**
- 🟢 HIGH · "Overwrite" in the conflict dialog of the mapping editor and the issuer form sends the whole stale form with the
  new `If-Match`, so another administrator's narrowing (a removed `deploy`, a removed audience) is undone without being shown
  (`mapping-editor.ts`, `issuer-form.ts`, the shared resolver builds merge patches; R-CI-002, R-UI-042) — the resolver hands
  the stored record to `send`, and the `PUT` body after "Overwrite" is that record plus only the members the user changed
  (`overwriteBody`, Q-UI-101); two specs prove the other administrator's narrowing survives
- 🟢 MEDIUM · An issuance record whose token row went with a deleted mapping's principal shows `active: true` and no
  `revokedAt` until it expires; `?active=false` returns it with `"active": true` (`ci_issuances.sql`, `issuance.go`).
  Fixed (Q-CI-52): the database computes one `state` (`active`, `revoked`, `expired`, `removed`) for the column and the
  filter, a missing token row is never active, and the representation, OpenAPI and the issuance list show why;
  `TestR_CI_020_ARecordWhoseTokenWentWithItsMappingIsNotActive`
- 🟢 MEDIUM · The bulk revoke confirms the count of all applied filters but sends only issuer, project ID, pipeline, job and
  mapping, so it revokes every active token of that mapping across projects and time (`issuance-list.ts`) — the revoke is
  disabled while a filter it cannot carry is applied and the note names it, and the day filters are the local day (Q-UI-102)
- 🟢 MEDIUM · Browse: revealing a selection while a load of the same folder runs loops in microtasks and freezes the tab
  (`browse.ts` `findChild`/`loadChildren`) — a node keeps the promise of its load in flight and `findChild` awaits it; the
  spec hung the runner before the fix
- 🟢 MEDIUM · A project's view of an issuance strips the other tiers' grants but keeps `grantedScope` and `requestedScope`,
  which list them, and the claim snapshot of jobs it only contributed to (`issuances.go` `onlyProject`). Fixed (Q-CI-52): the
  view omits both scope strings, the person, the subject token's ID and the client and trims the same claims from the snapshot;
  `TestQ_CI_26_AProjectsRecordShowsItsOwnMappingsOnly` and its integration twin check the fields
- 🟢 MEDIUM · The UI offers a project administrator no CI snippet, although the project route exists (`mapping-editor.ts`,
  `ci-api.ts`) — `CiApi.snippet` takes the project and reads `getProjectCiSnippet`; the editor offers the button to a project
  administrator whose `managePolicies` switch is on
- 🟢 MEDIUM · The manual, the README, OpenAPI (26 `501` responses) and the contract page still say the federation answers 501
  or is not built (W7 closes the documentation) — closed by W7: the notes and the 501
  responses are gone and the pages describe the built screens
- 🟢 MEDIUM (UI part of the line above) · The UI's "not available yet" card was dead code with misleading text — `CiError`, its
  strings and the `ci_not_implemented` message are removed; every CI screen shows the shared error notice
- 🟢 LOW (backend) · A just-created or re-enabled issuer is refused on a lagging replica until the cache's SafetyTTL (fix wave A) — a miss re-reads the issuer at most once per replica and
  poll interval (Q-CI-51)
- 🟢 LOW · The CI editors keep no draft across a re-authentication (R-UI-042) — the mapping editor and the issuer form keep the
  write body in `DraftStore` on `401 reauthentication_required`, register it for "Copy draft as JSON" and restore it once
- 🟢 LOW · Browse keeps a reloaded node's old size and kind — a node read again takes the new meta, a node of another kind is
  replaced, and the `nodes` map drops what left a folder with its subtree
- 🟢 LOW · The claim tester has no form view (R-UI-036) — a JSON / Form switch edits the top-level claims as rows (Q-UI-103)
- 🟢 MEDIUM · Disabling or deleting a mapping scans every issuance of the issuer within the retention while the issuer row is
  locked `FOR UPDATE`, so its exchanges wait into 503 (`CiMappingHasIssuances`). Fixed (Q-CI-53): the question reads
  unexpired records only (`expires_at > now()`, `ci_issuances_expiry`); the plan is in `ci_issuance_plans_test.go`
- 🟢 MEDIUM · The other part of the same finding, the two revocation statements `CiIssuanceTokensOfMapping` and
  `CiIssuanceTokensOfIssuer`, is fix wave A's — fix wave A bounded both to
  unexpired records
- 🟢 LOW · The "audience removed on A" subtest did not reach the in-transaction re-validation. Fixed: a test of its own seeds B's
  issuer copy first, stops B's change log with Valkey down, and shows `audience_invalid` from the row B re-reads in the
  transaction (`TestR_CI_007_AnAudienceRemovedOnAIsRefusedOnBFromTheRowReadInTheTransaction`, the stale counter is 1)
- 🟢 LOW · The list endpoints of the issuances sorted all records per page without a project filter, tested `grants` with `@>`
  without a GIN index and read `audit_event` by `token_id` for the usage summary. Fixed (Q-CI-53): migrations 00029 to
  00031 (three `CONCURRENTLY` indexes) and the issuer filter as an equality on its ID; the usage summary stays without an
  index on the partitioned `audit_event` (window on the primary key, plan checked)
- 🟢 MEDIUM · Disabling or deleting a mapping or an issuer scans every issuance of the issuer within the retention (no index
  on `mapping_ids`, no expiry bound) while the issuer row is locked `FOR UPDATE`, so its exchanges wait into 503 — fixed:
  `CiIssuanceTokensOfMapping` and `CiIssuanceTokensOfIssuer` read the unexpired records only through the expiry index
  (`TestR_CI_017_ARevocationReadsOnlyTheUnexpiredRecords`, 3,000 expired records beside 20,000 live tokens)
- 🟢 LOW · The "audience removed on A" subtest does not reach the in-transaction re-validation; the CI editors keep no draft
  across a re-authentication (R-UI-042); Browse keeps a reloaded node's old size and kind; the claim tester has no form view
  (R-UI-036) — all parts fixed: the re-validation's own test (wave B),
  the drafts, the reloaded node and the claim tester's form view (UI wave), the lagging replica (wave A)
- 🟢 LOW · A just-created or re-enabled issuer is refused on a lagging replica until the cache's SafetyTTL — fixed: a miss or a
  disabled hit of the issuer copy re-reads the issuers at most once per replica and `cluster.epoch_poll_interval` (Q-CI-51,
  `TestR_CI_003_ALaggingReplicaLearnsANewIssuerWithinThePollInterval`)

**Security audit of the phase (2026-11-03; status per item)**
- 🟢 HIGH · A project-tier mapping reaches the tokens of every job it matches, against R-CI-009's "tiers combine by union": its
  `maxTtlSeconds` bounds every token (1 s blocks every exchange with `ttl_below_minimum`), a render failure of its template fails
  the whole exchange after the system tier contributed, and disabling or deleting it revokes every token whose issuance lists
  it, the system tier's included (`select.go`, `exchange_tx.go`, `ttl.go`, `revoke.go`, `CiIssuanceTokensOfMapping`) — fixed
  (Q-CI-47): a project tier's render failure, bound (`expiresAt`, bundle version 2) and disable or delete reach its own
  contribution only, the system tier owns its tokens, and the evaluator drops a project's ended or inactive contribution
  (`tiers_test.go`; gaps H-424, H-425, H-427)
- 🟢 MEDIUM · `trove migrate` changes a declared issuer's `issuer` URL and `kind` in place under the same issuer ID, which the
  API refuses as `immutable`: pins, package owners (`ci:<issuer>:project:<id>`) and unpinned private-issuer mappings move to the
  new instance, the old key set stays valid until the next refresh, nothing is revoked (`issuers_declared.go`) — fixed
  (Q-CI-48): the migration stops, names both values and asks for a new name (`TestR_CI_001_ADeclaredIssuerKeepsItsIssuerURL`)
- 🟢 MEDIUM · The project's issuance view keeps `grantedScope` and `requestedScope` of every tier (also a code review finding;
  fixed with it, Q-CI-52)
- 🟢 MEDIUM · Stored mappings make every exchange expensive inside its transaction: up to 1,000 mappings per issuer shared by all
  tiers, each evaluated to its end with a trace and its regexes compiled twice per evaluation, unbounded literal sizes, while the
  exchange holds `FOR SHARE` on the issuer row — fixed (Q-CI-49): `ci.mappings.max_per_project`, size bounds of a condition, the
  conditions compiled in the mapping set, and `Select` without trace on the copy before the transaction (gap H-426)
- 🟢 LOW · Project key prefixes overlap (`ab-` matches `ab-c-release`), so H-413's probe reaches another project's names; a changed
  `jwks_uri` or `discovery_url` keeps the old key set until the next refresh; switching `public_issuer` to true does not re-check
  the unpinned mappings; H-418's key-existence oracle is reachable by any project administrator; `ci.replay.retention_after_expiry:
  0` leaves a single-use replay window of the clock skew between a replica and the database — fixed (Q-CI-50, Q-CI-51): keys
  cannot overlap (Q-AUTHZ-176) and the prefix is now checked before the lookup, a new key location fails closed and is fetched,
  an unpinning change is `409 mappings_unpinned`, an unknown scope key is `scope_not_granted`, the retention is at least 1 m

**Phase**
- 🟢 Contracts: schema (migrations `00026` to `00028`), packages (`internal/ci`, `claims`, `subject`, `reason`), every
  operation of R-API-035 and the project routes in OpenAPI (answering 501 until built), configuration `ci.*`, the audit
  events, change areas, limiters, tasks, metrics, and the seam between the CI token and the evaluator (`claims.Frozen` on
  the token, the gate for CI tokens) ([ci-oidc.md](../developer/ci-oidc.md), Q-CI-17 to Q-CI-22, ADR-147); the GitLab-shaped
  issuer double `test/oidcstub` (Q-CI-23, Q-CI-24)
- 🟢 GitLab as trusted issuer: discovery, signing keys, key rotation; several GitLab instances side by side (R-CI-005).
  Built (W1): the issuer API, the key fetch through the egress policy with the issuer's own CA, the shared key row and the
  per-replica copy, the unknown-`kid` re-read and refetch throttles, `max_stale`, `ci-keys-refresh` on workers
  (`test/integration/ci`, [ci-federation.md](../security/ci-federation.md), Q-CI-30 to Q-CI-32); the criteria that
  needed the exchange and the revoker pass since W4 and W5, and end to end in the set `ci` (W7)
- 🟢 GitLab issuers declared in the Helm values (`config.ci.issuers`), applied by `trove migrate`, shown in trove and
  read-only there ([Q-CI-16](../spec/gitlab-ci-oidc.md#q-ci-16--ci-issuers-provisioned-through-the-helm-values-are-read-only-in-trove), owner 2026-11-02); built (`ApplyDeclared` in `trove migrate`, `TestR_CI_001_*` in `test/integration/ci`); removing an
  issuer that has issuance records disables it and revokes its tokens (W5), and a changed URL or kind stops the migration (Q-CI-48)
- 🟢 Token exchange: GitLab ID token (`id_tokens:` with trove as audience) in, short-lived CI token (`trv_cit_`) out;
  `CI_JOB_TOKEN` is not accepted (owner 2026-11-02; its fallback stays P2, R-CI-022). Built (W4): `POST /api/v1/auth/oidc/token` with
  form and JSON bodies, the scope, the lifetime, the re-check of the issuer and the mappings `FOR SHARE` in one transaction with the
  replay set and the restore cut-off, the issuance record, the events and metrics, the cluster-wide budgets per address and per
  project and the aggregated denials (Q-CI-41 to Q-CI-43, H-418 to H-420); verified by `test/integration/ci` (`exchange*_test.go`,
  `replay_test.go`, `ratelimit_test.go`) against the issuer double on one to three replicas with Valkey running and stopped; the real clients on kind, a Sentinel failover and `FLUSHALL` during exchanges and a `web` pod deleted under 50 exchanging jobs were W7's and passed (`TestCIPipeline`, `TestCIHASingleUse`, `TestCIHAPodDelete`, [e2e.md](../developer/e2e.md#the-ci-tests))
- 🟢 Audit of the exchange: which pipeline (instance, group, project, branch, pipeline, job) signed in when, as issuance
  records with a tab per issuer in the admin UI (R-CI-020). Evidence: `TestCIPipeline` (the issuance of each job of the reference
  pipeline shows `projectId`, `ref` and `sha`; the publish's audit entry `artifact.deployed` carries the issuance's `token_id`;
  `token.revoked` with the reason `owner`), the journey `13-ci` (the Issuances tab, the detail, the bulk revoke); see
  [e2e.md](../developer/e2e.md#the-ci-tests)
- 🟢 Admin manual: a runbook page for GitLab CI (register an instance in the Helm values, the `id_tokens:` block, a
  mapping, verify with the dry run, read the issuances, troubleshoot refusals) (owner 2026-11-02), written while the phase is
  built (owner, 2026-11-02), with three how-tos: how a project administrator lets the pipelines of a GitLab project write
  to the project's repositories; how a project administrator gives a whole GitLab instance or group read access to the
  whole project or to one repository; how a pipeline gets its trove token. Documented in
  [admin/gitlab-ci.md](../manual/admin/gitlab-ci.md) and [user/gitlab-ci-pipelines.md](../manual/user/gitlab-ci-pipelines.md);
  W7 removed the "not available yet" notes and the 501 paragraphs, added the section on the screens, checked the labels against
  `web/src/app/features/ci` and the nuget.config of the pipeline against the run (Q-CI-44), and removed the "not released yet"
  wording of the related pages ([ci-oidc.md](../developer/ci-oidc.md#the-manual-pages))
- 🟢 Project administrators manage the CI mappings of their project (owner, 2026-11-02; Q-CI-26): a mapping tier per
  project, grants only over the project's repositories and policies, the save-time rules unchanged; issuers stay the
  system administrators'. Built (W3): the project routes with the narrowing, the bounds, the dry run and the snippet of the
  tier, `project_not_empty` for a project with mappings (`TestQ_CI_26_*`); the project's issuances (W5, Q-CI-52) and the journey `13-ci` (W7)
- 🟢 Identity mappings: conditions on project, branch, protected ref, environment; templated grants. The claim engine is
  built (W2: conditions with their limits and trace, glob, RE2, templates with escaping per context, grants with the
  ownership default, the scope grammar, the cross-project cap per mapping, every save-time rule and warning, the project
  tier's rules; Q-CI-27 to Q-CI-29); the subject-token validation of R-CI-004 and the shared JWS leaf `internal/auth/jws`
  the login uses too. The mapping store and API are built (W3: the principals, `mappings_version`, the policy binding, the
  per-replica copy, `Select`, the dry run, the snippet; Q-CI-35 to Q-CI-37, `TestR_CI_009_*` in `test/integration/ci`); the criteria
  that needed the exchange pass since W4 and in the set `ci` (W7); a project tier changes only its own contribution (Q-CI-47)
- 🟢 Package ownership: a CI project writes only the packages it owns, reads what it is granted. Built (W5): a CI token's rights per
  contributing mapping (policies, listed groups, frozen inline grants; the cap `read` of one mapping caps only its own; a project tier
  bound to its project's repositories and policies; never `manage`; the scope as ceiling), the claim of a name for the GitLab project
  on its first publish also where ownership is off, `403 package_owned_by_other_owner` for every other project, an administrator adding
  `ci:<issuer>:project:<id>`, the revocation of CI tokens by mapping, issuer, bulk, one token and the token itself within 2 s on every
  replica with and without Valkey, the issuance API with a project's view, the two retention tasks (Q-CI-38 to Q-CI-40; H-415 to H-417).
  Verified by the integration tests of `test/integration/ci` with tokens issued through `IssueCITx`; and end to end through the
  exchange with the real npm and .NET clients in the set `ci` (E4.1, W7)
- 🟢 Admin UI: CI issuers, mappings, dry run and claim tester; the issuances, bulk revoke and the project's CI screens
  (`/admin/ci…`, `/admin/projects/:key/ci…`, Q-UI-99, Q-UI-100; 713 web tests). Against the real backend by the journey `13-ci`
  (an administrator registers the issuer double with its CA, creates the reference mapping in the editor, tests a claim set with a pasted
  token decoded in the browser, reads the issuance and revokes it; a project administrator manages a mapping of its project; axe in
  both colour schemes; part of `TestE1_11Journeys`). The axe check first found the issuance dialog's scrolling blocks without
  keyboard access (`scrollable-region-focusable`); they are tab stops now, and `13-ci` is green on kind with the other 19 journeys
  (`make e2e SET=access-long`, 2026-11-03)
- 🟢 Reference pipeline: `.gitlab-ci.yml` publishing an npm package and pushing a NuGet package: the Examples of the
  specification, run as written by `TestCIPipeline` (`make e2e SET=ci`); the first run found that the Example's `dotnet nuget push` needs
  the push source in `nuget.config` (Q-CI-44)
- 🟢 GitLab's behaviour validated against the owner's unused GitLab instance and recorded as fixtures of the issuer
  double; no integration or e2e test runs against a real GitLab or gitlab.com (owner, 2026-11-02; Q-CI-25); recorded on a local GitLab 19.0.1-ee because
  the owner's instance cannot create projects; recording again there is [F-36](promt.md#8--open-findings)
- 🟢 Integration tests: invalid signatures, wrong audience, expired tokens, fork merge requests: every reason of R-CI-008 the MVP
  reaches, through the endpoint with status, `error` and `reason` (`TestR_CI_008_EveryReasonOfTheErrorTable`), the recorded fork merge
  request run in the parent (`TestR_CI_012_ForkPipelines`), 2,000 forged tokens beside a legitimate project, and a grep of every answer
  and log line for token material; `configuration_changed` and the three P2 reasons are proven or deferred as Q-CI-43 says
- 🟢 E2E tests against the issuer double for npm and NuGet (the reference pipeline's exchange and client steps), with a `web` node
  drained during the run and again with Valkey stopped; no test runs against a real GitLab. `make e2e SET=ci` on every change (E4.1,
  E4.4 to E4.6: protected tag and unprotected branch with the real npm and .NET clients, the fork's merge request, the dry run against the
  issuances, expiry and the 2 s revocation, the tasks on workers) and `make e2e SET=ci-ha` nightly (E4.7: a drained node, Valkey stopped
  with every replica ready and `degraded: [valkey]`, 250 single-use tokens across a Sentinel failover and `FLUSHALL`, a `web` pod deleted
  under 50 exchanging jobs); the results are in [e2e.md](../developer/e2e.md#the-ci-tests) ([Q-CI-25](../spec/gitlab-ci-oidc.md#q-ci-25--gitlabs-behaviour-is-validated-against-a-real-instance-the-tests-run-against-the-double), owner 2026-11-02)
- 🟢 GitLab's behaviour validated by hand on GitLab 19.0.1 and recorded as fixtures that the issuer double reproduces: ID tokens of 16
  cases, the key set and its caching, `CI_JOB_TOKEN`'s shape (`make oidc-fixtures`, `test/oidcstub/testdata`); a fork merge request
  opened by a parent member runs in the parent, no claim pins an ancestor group (H-27)

## Phase 5b · trove Bastion (M13)

Details: [roadmap M13](../coverage/roadmap.md#p1-platform-milestones) · spec: [bastion.md](../spec/bastion.md)

The owner moved the Bastion into the beta on 2026-11-02, before vulnerability scanning, so that phase 7 hardens it with
everything else. Its main purpose (owner): an exposed artifact instance that gives external parties read access to released
content, caches artifacts and takes as much load as possible off the main installation. It is exposed to the internet and
to denial-of-service attempts.

- 🟢 Spec page [bastion.md](../spec/bastion.md) first: deployment profile, trust between Bastion and main installation, the cache, and
  what the Bastion refuses. Decided by the owner on 2026-10-31: the Bastion asks the main installation whether a
  credential is valid and what it may read, and keeps the answers for a short, bounded time; it holds no copy of
  users, tokens or policies. Decided by the owner on 2026-11-02: the Bastion's own credential towards the main
  installation is a token that a trove administrator of the main installation issues for the Bastion (read only, nothing
  else); a client certificate is not its identity, because a TLS-terminating reverse proxy in front of the main
  installation would not pass it on. The Bastion connects to the main endpoint of the main installation (owner,
  2026-11-02): the Bastion replaces the customer endpoint of the earlier plans. Settled on the page: the bound (R-BAS-020,
  R-BAS-026), the interface on the main installation (R-BAS-008 to R-BAS-017), the renewal of the Bastion's token
  (R-BAS-006), and the state it keeps beyond its cache (answers in memory and, optionally, signed in Valkey; R-BAS-020,
  R-BAS-034). Catalog entries it pulls forward: the chained remote repository (RMT-040, RMT-041, RMT-044) and the read-only
  deployment profile with its trust between source and instance (DIST-024, DIST-025), raised to MVP with the page; RMT-042,
  RMT-043, RMT-045 and RMT-046 stay P2 (Q-BAS-47)
- 🟢 A second Helm chart (`trove-bastion`) for this instance type (owner, 2026-11-02) with its own Ingress, Service, NetworkPolicy and TLS Secret; horizontal replicas
  without PostgreSQL of its own if the design allows, otherwise the smallest state that keeps the replicas consistent
- 🟢 Same authentication as the main installation: personal, service-account and CI tokens and the same login, with
  revocation reaching the Bastion within a stated bound; permissions and exposure rules of the main installation
  enforced on every request, cached content included
- 🟢 Package cache: immutable content (tarballs, `.nupkg`) kept and served without asking the main installation again,
  metadata revalidated against it with conditional requests; collapsing of concurrent misses; behavior while the main
  installation is unreachable (stale metadata, cached content) as for remote repositories
- 🟢 Read-only for package content (owner, 2026-10-31): publish, unpublish and tag writes answer in each protocol's shape and
  point at the main installation; client setup snippets for a Bastion
- 🟢 Observability: cache hit rate, bytes served from the cache, upstream requests to the main installation
- 🟢 Integration and e2e tests: npm and NuGet clients against a Bastion on kind in front of a main installation, cold and
  warm cache, main installation unreachable, token revoked at the main installation, drain of a Bastion replica
- 🟢 What a caller reads (owner, 2026-11-02): there is no release step of its own. The right is the token's: a caller reads
  through the Bastion exactly what its token may read on the main installation, and read access behaves the same on both
- 🟢 How the Bastion fetches (owner, 2026-11-02): with the caller's token. On a cache miss it forwards the caller's token,
  the main installation decides and serves, and the Bastion caches the bytes; a cached artifact is served to another
  caller only after the main installation said that this caller may read it (an answer kept for the bound). The
  Bastion's own token identifies the Bastion and may only have tokens checked; it reads no package
- 🟢 How fast a revocation reaches the Bastion (owner, 2026-11-02): the Bastion keeps the main installation's answers for a
  configurable period (some minutes) and follows a revocation feed of the main installation every few seconds, so a
  revoked token or a narrowed right is dropped within seconds; widening a right may take the period
- 🟢 The main installation unreachable (owner, 2026-11-02): answers the Bastion already holds stay valid for a configurable
  grace period (proposed 15 minutes) beyond their normal period; callers with a valid answer get cached artifacts and
  possibly stale metadata, a token the Bastion has not seen gets `503`, and after the grace period it serves nothing
- 🟢 The cache (owner, 2026-11-02): a small part of the artifacts, never the main installation's object store, which the
  Bastion cannot read; its backend is chosen in the chart, a bucket of the Bastion's own shared by its replicas or a local
  volume per replica, with size limit, eviction by last access and the metadata revalidation period configurable
- 🟢 Shared state (owner, 2026-11-02): Valkey is optional. With it, the replicas share blocked addresses, rate-limit and
  failed-sign-in counters, the coordination of fetches and the main installation's answers; without it, each replica works
  alone with the budgets divided by the number of replicas; losing Valkey costs performance, never correctness (INV-3).
  With a local volume per replica, the chart can route by path so that each artifact lives on one replica
- 🟢 Anonymous reads (owner, 2026-11-02): as on the main installation; the Bastion admits anonymous requests exactly when
  the main installation does. Because an anonymous request costs an attacker nothing, the Bastion absorbs misses itself:
  a negative cache for names the main installation does not have, a budget of concurrent requests to the main
  installation that misses cannot exceed, and stricter per-address limits for requests without a credential
- 🟢 Audit of downloads (owner, 2026-11-02): the Bastion reports the downloads it serves from its cache to the main
  installation in batches (who, what, when, address; origin Bastion), under the same `audit.record_downloads` switch; it
  buffers them, bounded, while the main installation is unreachable and sends them later; events lost to a full buffer are
  counted and reported
- 🟢 No web interface (owner, 2026-11-02): the Bastion serves the package clients and raw downloads with tokens only; no
  login, no session, no UI
- 🟢 Configured only through its Helm chart (owner, 2026-11-02): the Bastion has no settings held anywhere else; every value
  (main installation, its token, cache, periods, limits, blocking, trusted proxies) is a chart value, and a change is a
  rollout
- 🟢 Blocking in two stages (owner, 2026-11-02): the Bastion itself enforces the rules that need trove's knowledge (failed
  authentication, misses, block duration, a block list shared through Valkey); its chart can put a coarser per-address
  limit in front at HAProxy Ingress, which can be switched off and is set wider than the Bastion's own. Volumetric attacks
  on bandwidth are the network provider's, not the Bastion's
- 🟢 The URL space of the main installation (2026-11-02): the Bastion holds no repository list; every repository of the main
  installation is reachable under the same paths, and the main installation renders documents for the Bastion's host
  (the Bastion sends its base URL as the forwarded host; the main installation lists the Bastion in `trusted_proxies` and
  `additional_base_urls`), so the Bastion carries no format knowledge
- 🟢 Metadata (owner, 2026-11-02): the main installation marks a document that is the same for every caller who reads the
  whole repository; the Bastion serves that copy for the metadata period to each caller for whom the main installation
  confirmed a full read of the repository (an answer kept like the others, under the revocation feed). A filtered
  document is never shared: it goes to the main installation per caller with a conditional request. Artifacts are shared
  after the main installation said the caller may read them
- 🟢 The Bastion's token (owner, 2026-11-02): renewed by hand with an overlap: an administrator issues a second token before
  the first expires, it reaches the Bastion's Secret through GitOps, the old one is revoked; the Bastion reloads a changed
  Secret without a restart, exports the remaining lifetime as a metric and warns in its log from a chart-set period before
  expiry (proposed 30 days). The Bastion holds no right to write Secrets or to issue tokens
- 🟢 Interface version (owner, 2026-11-02): the Bastion interface of the main installation (token checks, revocation feed,
  audit intake) has a version of its own, separate from the release number, raised only when that interface changes,
  which after the stable release is rare. Main installation and Bastion talk only when they speak the same interface
  version; on a mismatch the Bastion treats the main installation as unreachable (the grace period, then nothing) and
  raises a Prometheus alert (a metric plus a rule its chart ships)
- 🟢 The main installation counts the real client: the Bastion stops abuse before a request reaches the main installation,
  and the main installation lists the Bastion's addresses in `server.trusted_proxies`, so its per-address limits apply to
  the client and not to the Bastion
- 🟢 Internet exposure (owner, 2026-11-02): rate limits per client address, automatic blocking of addresses that flood it,
  a timeout for an address after too many failed authentication attempts, and a configurable cache (size, retention,
  metadata revalidation)

## Phase 6 · Vulnerability scanning (M4b)

Details: [roadmap M4b](../coverage/roadmap.md#m4b--vulnerability-scanning) · spec: [vulnerability-scanning.md](../spec/vulnerability-scanning.md)

The owner decided on 2026-10-31 that trove scans its stored packages itself and shows a vulnerability dashboard in the
beta. The phase comes before the HA phase, so that phase 7 hardens the scanner with everything else. The testing rule above
applies to the npm and NuGet parts of this phase.

**Engines**
- 🟢 Advisory mirror on `worker`: the OSV archives of npm and NuGet (GitHub Advisory Database, OpenSSF malicious packages), full and incremental sync with conditional and ranged requests, integrity and shrink guards, egress rules, sources status
- 🟢 Offline import for air-gapped installations from a generic repository
- 🟢 Known-exploited marking from the CISA catalog
- 🟢 Matching by package identity with each format's version order, as an optional interface of the frozen format contract; public advisories never applied to claimed names, claimed-name matches as a security alert
- 🟢 Findings: severity from CVSS, triage statuses and history, re-evaluation when the advisory data changes, rescan on demand, retention
- 🟢 Triage instance-wide by the `auditor` role, which sees every repository's metadata and findings but downloads, uploads
  and changes no artifact ([Q-AUTHZ-172](../spec/authorization.md#q-authz-172--the-auditor-reads-every-repositorys-metadata-and-triages-but-downloads-nothing), owner 2026-11-02)
- 🟢 Download blocking: malicious versions by default, every other block set and lifted in triage on a confirmed finding, no severity threshold (owner, 2026-10-31); on every route that serves a version, in the one item-lookup query; report mode for the whole installation

**Formats**
- 🟢 npm: `npm audit`, `pnpm audit` and `yarn npm audit` answered from trove's data on local, remote and virtual repositories; no package name leaves the installation
- 🟢 NuGet: `VulnerabilityInfo` from trove's data on every repository class, so `dotnet restore` reports NU1901 to NU1904
- 🟢 Blocked downloads in npm's and NuGet's error shapes

**API and UI**
- 🟢 Vulnerability report: counts by severity, trend, filterable and sortable list, finding detail, bulk triage, CSV export; per repository through its filter
- 🟢 Vulnerabilities tab on the package page; sources screen with the offline commands
- 🟢 Permissions: findings only where the caller can read the package; triage with the capability `vulnerabilities:triage`

**Tests**
- 🟢 Integration tests against PostgreSQL, Valkey with Sentinel and S3 with a test advisory source: sync paths and faults, the matching corpus per format, the finding lifecycle, blocking on every route, the API with permission-narrowed callers
- 🟢 E2E tests: npm, pnpm and Yarn audits and a blocked install, `dotnet restore` audit warnings and a blocked restore, through local, remote and virtual repositories on several `web` replicas with random distribution; browser journeys of the report, triage, blocking and export
- 🟢 E2E: a full sync drained mid-run finishes on another `worker`; the set once with Valkey stopped
- 🟢 Performance bounds V-1 to V-9 with 100,000 stored versions: `TestR_VULN_028_PerformanceRun` reports all nine (the run `vuln-perf` of HA and Performance (nightly)); four bounds are missed or not measured precisely, open as [F-43](promt.md#8--open-findings)

**Exit criteria of M4b and their evidence (2026-11-03)**
- 🟢 E4b.1: `test/integration/vulnsync` (30 tests: full and incremental sync, `304`, ranges on and off, every fault, the guards and
  `acceptShrink`, the offline import without a network request, release and resume with the attempt unchanged); the node drain of a
  full sync `TestVulnLong_R_VULN_004_FullSyncDrainedMidRun` of the set `vuln-long`: green twice locally, and in the nightly run
  41739280012 every assertion of the criterion passed (queued again, ended on a second worker from its checkpoint after 5
  batches) before its cleanup failed on the test source's control connection ([F-44](promt.md#8--open-findings))
- 🟢 E4b.2: the corpus `internal/vuln/match` (22 real records, results equal to osv-scanner v2.7.0's offline mode) and the per-format
  table tests; the claims rule and `TroveVulnerabilityClaimedNameMalicious` in `test/integration/vulnfindings` and the chart's promtool
  tests
- 🟢 E4b.3: `test/integration/vulnfindings` (21 tests and the fix-wave cases), `TestR_VULN_015_E4b3_E4b4_TheFlowFromTheAdvisorySourceToTheBlockedDownload`
  of `test/integration/vulnflow`, the reopen and bulk triage with their audit events in `test/integration/vulnapi`
- 🟢 E4b.4: `test/integration/vulnblock` (every route, HEAD, Range and conditional requests, the remote miss without an upstream
  request, the cross-replica dismissal, Valkey stopped, `querycount_test.go`: the scanning adds no statement to a download); the
  blocked installs, malicious and in triage, of every gated npm client row and the blocked restore of .NET 8, 9 and 10 in the set `vuln`
- 🟢 E4b.5: npm, pnpm 11 and 12 and Yarn 4 audits through `npm-local`, `npm-remote` and `npm-all` with zero upstream and advisory-source
  requests, NU1903, NU1904 and `NuGetAuditSuppress` (set `vuln`); the recorded public answer against trove's in
  `TestR_VULN_017_TheReferenceLockfileMatchesThePublicAnswer` (67 real advisories of a control body, identical identifiers and severities)
- 🟢 E4b.6: the four browser journeys with axe across three `web` replicas (set `vuln`); the `@team-a/*` narrowing of list, counts,
  trend and export in `test/integration/vulnapi`
- 🟢 E4b.7: `test/integration/vulnrestore` on the state a restore leaves (Q-VULN-58; the restore of the backup job itself is phase 7)
- 🟢 E4b.8: the set with Valkey stopped (`vulnblock`, the `vuln` set's R-VULN-027 cases, `TestVulnLong_R_VULN_027_ClientsWithValkeyStopped`)
  passes; the performance run reports V-1 to V-9 (2026-11-04: V-1 7.1 min, V-2 2.1 min, V-3 0.16 min, V-6 131 ms, V-7a 37 ms, V-9
  0.33 GiB within their targets; V-4, V-5b, V-7b and V-8 not, F-43); the nightly run 41748522448 reports them on the CI runner, where
  V-1 did not finish within 40 min (F-43)
- 🟢 E4b.9: every MVP requirement of the page has a passing test that names it: R-VULN-001 to R-VULN-021 and R-VULN-024 to
  R-VULN-027 in the unit, integration and chart reports ([implemented.txt](../coverage/implemented.txt)); R-VULN-022, R-VULN-023 and
  R-VULN-029 in the end-to-end set `vuln` (journeys and the acceptance suite, which the coverage generator does not read, as for
  R-BAS-049); R-VULN-028 in the nightly run `vuln-perf`

## Phase 6a · Task areas

Decided by the owner on 2026-11-04: trove is cut into task areas, as roles of one binary that run as Deployments of
their own, with network seams only at trust or load boundaries; the first seam is an upstream fetch service, the only
role that contacts upstream registries
([ADR-153](../decisions.md#adr-153--trove-is-cut-into-task-areas-roles-of-one-binary-network-seams-only-at-trust-or-load-boundaries)).
Network policies are ingress rules between the chart's pods, each with its port
([ADR-154](../decisions.md#adr-154--network-policies-are-ingress-rules-between-the-charts-pods-each-with-its-port)).
The phase comes before phase 6b, so that the mirror's background passes and its upstream contacts land in their task
areas.

- 🟢 Principle of the cut (ADR-153) and the network-policy rule (ADR-154)
- ⚪ The roles of the cut: which task areas, which route families and queues each serves (proposed to the owner)
- ⚪ The fetch service: which upstream contacts go through it, the internal API over mutual TLS, streaming of a miss,
  collapse and client-first priority in one place, its drain
- ⚪ Specification rewritten: INV-2, R-ARCH-001, R-HA-002, R-HA-062 and R-HA-042 (its egress part, Q-HA-10 and H-41
  removed under ADR-154); ADR-013 and ADR-104 revised
- ⚪ The roles in the binary (`SubsystemsFor` and the wiring areas) and in the chart: a Deployment, Service, disruption
  budget, autoscaler and ingress rule per role, path routing, worker pools by queue
- ⚪ Tests: the role table in the architecture test, chart tests per role, integration tests of the fetch seam, e2e with
  every role deployed, a drain per role

## Phase 6b · Remote full mirror

Details: [roadmap phase 6b](../coverage/roadmap.md#phase-6b--remote-full-mirror) · spec: [repositories-and-proxy.md, Mirror mode](../spec/repositories-and-proxy.md#mirror-mode)
(R-REPO-050 to R-REPO-059), [npm](../spec/formats/npm.md#mirror-discovery) and [NuGet](../spec/formats/nuget.md#mirror-discovery) mirror
discovery

The owner's request of 2026-11-02: pull-through stays the default of a remote repository; a remote can also be set to full
mirror, in which trove downloads and keeps everything it can discover at the upstream on its own. The phase comes before
phase 7, so that the failure suite covers the background sync too. Its engine waits for phase 6a: until then
`mode: mirror` is refused with `not_applicable` (R-REPO-050).

- 🟢 Spec first: the mode per remote repository (`pullThrough`, the default, or `mirror`); what "discoverable" means per
  format (NuGet: the upstream's catalog or its package listing; npm: the upstream's package list or change feed, where it
  has one); what the include and exclude patterns of the remote bound; storage and quota bounds, because a mirror of a
  public registry is terabytes; the catalog's pull-replication entries (API-082 to API-090) it pulls forward or leaves
- ⚪ Background synchronisation on `worker`: a job per mirrored remote, resumable from a checkpoint, continued after a
  drain, a restart or a failover, and started again on its own after an error
- ⚪ Upstream throttling: `429`, `503` with `SlowDown` and every other rate-limit answer are honoured (`Retry-After`, back-off
  with jitter, fewer concurrent fetches), and the sync resumes by itself when the upstream allows it again
- ⚪ Client requests first: a request from a client is served before the background sync; the sync uses a lower-priority
  share of the remote's fetch budget that client fills pre-empt
- ⚪ Status: progress, last complete pass, errors and rate-limit waits per mirrored remote in the API and the admin UI, and
  as metrics; the mirror's fills audited like cache fills
- ⚪ Integration tests against the counting test upstreams answering `429` and `SlowDown`; e2e: a mirror pass while clients
  install, with the clients' requests served first

## Phase 7 · HA hardening, backup and Helm (M5)

Details: [roadmap M5](../coverage/roadmap.md#m5--ha-hardening-and-helm-ga) · spec: [ha-and-helm.md](../spec/ha-and-helm.md), [backup-and-restore.md](../spec/backup-and-restore.md)

**High availability**
- ⚪ Failure suite: pod crash, PostgreSQL switchover and failover, Valkey failover and outage, S3 outage, node loss
- ⚪ Node drain under npm and NuGet load with zero failed requests; the NuGet load (`TestNuGetHA`) and R-ARCH-024's drain
  criterion move here from phase 4 ([Q-NUGET-73](../spec/formats/nuget.md#q-nuget-73--the-nuget-part-of-the-drain-test-moves-to-phase-7-with-the-other-long-legs))
- 🟡 npm load in the drain and failover tests reliably green (moved from phase 3, owner 2026-11-01). Diagnosis of
  2026-11-01: `make e2e SET=npm-ha` passes on a 17-CPU kind cluster (drain: 40,894 k6 requests, 0 failures, every job at
  attempt 1); with the kind nodes held to 6 CPUs the drain gets 1,062 `503`s, all `memory_slots` shedding of Q-ARCH-240 (no
  OOM, heap 111 MB), and the PostgreSQL switchover fails too. The scenario needs about 12 cores. In CI run 41322837920 the
  whole runner stalled for 40 s (worker loop silent, every backend timing out at once), the edge had no ready backend, leases
  expired and the reaper counted the attempts (no counting defect). CPU profile of a `web` pod: 57 % in the virtual tarball
  path, 35 % TLS writes, 30 % per-tarball PostgreSQL reads (`TarballFacts`, `items.read`, `proxy.getEntry`, `Tombstoned`).
  Open: a per-replica cache of the immutable tarball facts, shedding by memory in use instead of a worst case per request
  (after measuring a held download), the full settle deadline for the release writes at shutdown, and a load the runner
  can carry
- ⚪ The drain criterion of the security audit (R-NPM-038, moved from phase 3): the `web` pod that forwards an `npm audit`
  to a provider answering after 20 s is deleted 1 s in; `npm audit` succeeds and no pod gets `SIGKILL`
- ⚪ The default of `health.shutdown_drain_delay` (5 s, proposed) set from the endpoint-removal time of the target Ingress measured under load; the dev profile needs 15 s on the CI runners (Actions run 41241903588)
- ⚪ The mirror sync of phase 6b under the failure suite: a `worker` drained and lost mid-pass, the upstream throttling
- ⚪ The Bastion under the failure suite: a Bastion replica drained and lost, the main installation unreachable and
  failing over, a rolling upgrade of either side
- ⚪ Vulnerability scanning under the failure suite: sync and evaluation jobs across drains and failovers, blocks and triage after a restore (E5.13)
- ⚪ Rolling upgrade from the previous pre-release with mixed versions
- ⚪ Performance targets met in two nightly runs in a row

**Backup and restore**
- ⚪ Backup job (CronJob): consistent database snapshot, blobs copied incrementally to the backup S3 target
- ⚪ Restore job into an empty installation
- ⚪ Revocation journal: a restore never brings back revoked tokens, removed permissions or narrowed exposure
- ⚪ E2E: backup taken during publishing restores with every npm and NuGet file digest-correct

**Helm chart and release**
- ⚪ Chart complete: values reference and schema, autoscaling, network policies, monitoring integration
- ⚪ Security documentation in [docs/security](../security/README.md) and security review
- 🟡 Release pipeline: `Release Docker & Helm` publishes the image `guidedtraffic/trove` (Docker Hub) with provenance and SBOM attestations and the chart (GitHub Pages) with each release ([releasing.md](../developer/releasing.md#the-release-pipeline)); it publishes every release since the switch to the organisation's CI standard. Open: E5.8, the beta candidate installed by digest from there passing E2.1's smoke scenarios
- ⚪ Beta candidate published

## Phase 8 · Walkthrough and beta release

Details: [roadmap: owner walkthrough](../coverage/roadmap.md#owner-walkthrough)

- ⚪ Guided walkthrough of the running beta candidate with the owner:
  - ⚪ OIDC sign-in, users, groups, tokens, permission policies
  - ⚪ npm and .NET clients against local, remote and virtual repositories, with nuget.exe and the .NET SDK on Windows and
    the Visual Studio 2022 checklist of R-NUGET-027 (E3.1, moved from phase 4,
    [Q-NUGET-72](../spec/formats/nuget.md#q-nuget-72--the-windows-row-of-e31-and-the-visual-studio-checklist-run-in-the-owner-walkthrough-not-on-a-ci-runner))
  - ⚪ GitLab reference pipeline
  - ⚪ Vulnerability report, a blocked malicious version, and the npm and .NET audits from trove's data
  - ⚪ Node drain and PostgreSQL and Valkey failover under load
  - ⚪ Backup by the CronJob and restore into an empty namespace
- ⚪ Objections from the walkthrough worked off
- ⚪ All exit checks green again
- 🟡 Beta documentation: installation guide, configuration reference, known limitations. The first edition of the
  [manual](../manual/README.md) (7 user and 12 administrator pages, among them installation, configuration and known
  limitations) is written against the code of 2026-11-01, embedded into the binary and shown behind the "?" of the UI
  (R-API-054, R-UI-050, owner 2026-11-01). Open: the pages follow every later phase, and the walkthrough reviews them
  - 🟢 The manual uses the width it has (owner, 2026-11-02; [Q-UI-97](../spec/api-and-ui.md#q-ui-97--the-manuals-article-takes-the-width-of-its-column-only-the-prose-keeps-a-measure)): on a wide screen the article and its tables widen instead of
    a narrow column with words broken mid-word in table cells; prose up to 110 characters a line, tables that are still too wide scroll in their own box
- 🟡 Design language of the owner's example screens in both colour schemes (Q-UI-88, owner 2026-11-01): palette, shell and
  theme button built and in the component tests; the primary colour is cogpit's blue and green marks positive status only
  (Q-UI-96, owner 2026-11-02), every pair held at WCAG AA by a palette test; open: the browser tier's colour-contrast check (light and dark) in CI and the
  owner's review at the walkthrough
- ⚪ **Beta release** published

---

## Test matrix

| Area | Integration tests | E2E tests | Drain and failover load |
|---|---|---|---|
| Foundation (storage, coordination) | 🟢 | — | 🟢 |
| Access core (login, tokens, permissions) | 🟢 | 🟢 | 🟢 |
| **npm** | 🟢 | 🟢 | 🟡 |
| **NuGet (.NET)** | 🟢 | 🟡 | ⚪ |
| Projects (delegated administration) | 🟢 | 🟢 | — |
| GitLab CI OIDC | 🟢 | 🟢 | 🟢 |
| trove Bastion | 🟢 | 🟢 | 🟢 |
| Vulnerability scanning | 🟢 | 🟢 | 🟡 |
| Backup and restore | ⚪ | ⚪ | — |
