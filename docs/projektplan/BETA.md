# trove · Project plan to the beta release

**Status:** ⚪ not started · 🟡 in progress or partly done · 🟢 done
**Last updated:** 2026-10-29

The way from zero to the beta release. The beta is trove with two package registries, **npm** and
**NuGet (.NET)**, each with local, remote (pull-through) and virtual repositories, running on the
production topology the owner requires: HA PostgreSQL, HA Valkey with Sentinel, S3, separate `web`
and `worker` roles, node drains without outage, and a backup job. Everything after the beta
(further registries such as Ansible Galaxy, PyPI, Docker, and platform breadth) is in
[FULL.md](FULL.md).

The beta release is what the [roadmap](../coverage/roadmap.md) builds in M0 to M5, after the
owner walkthrough, published as `1.0.0-beta.1`. Details, exit criteria and requirement IDs of
every phase are in the roadmap section linked under each heading.

## Where we are

| Phase | Result | Status |
|---|---|---|
| [0 · Specification](#phase-0--specification) | Feature catalog, specification, decisions | 🟢 |
| [1 · Foundation](#phase-1--foundation-m0) | Skeleton, HA-shaped local stack, roles, drain-safe shutdown | ⚪ |
| [2 · Access core](#phase-2--access-core-m1) | Login, tokens, permissions, repositories, admin UI | ⚪ |
| [3 · npm registry](#phase-3--npm-registry-m2) | npm local, remote, virtual · pre-release 0.1 | ⚪ |
| [4 · NuGet registry](#phase-4--nuget-net-registry-m3) | NuGet v3 local, remote, virtual · pre-release 0.2 | ⚪ |
| [5 · GitLab CI OIDC](#phase-5--gitlab-ci-oidc-m4) | CI jobs get scoped tokens without stored secrets · pre-release 0.3 | ⚪ |
| [6 · HA, backup, Helm](#phase-6--ha-hardening-backup-and-helm-m5) | Drains and failovers without errors, backup and restore · beta candidate | ⚪ |
| [7 · Walkthrough and beta](#phase-7--walkthrough-and-beta-release) | Owner review, objections fixed · **beta release** | ⚪ |

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
- ⚪ Go module (Go 1.27.3) with pinned tools, task runner, architecture test
- ⚪ Angular 22 workspace; UI embedded in the Go binary
- ⚪ CI pipeline: lint, unit tests, integration tier, image build, chart lint, one version per tool
- ⚪ Coverage generator as a Go tool in the repository

**Data and storage**
- ⚪ PostgreSQL connection classes, transaction-mode pooler support, migration runner and migration Job
- ⚪ S3 blob store: spooling, hashing, content-addressed commit, multipart transfer, garbage collection
- ⚪ S3 conformance check against the local S3 store

**Coordination across replicas**
- ⚪ Job queue with leases, claimed only by `worker` replicas
- ⚪ Change log and epochs in PostgreSQL for cache invalidation
- ⚪ Valkey client with Sentinel discovery, TLS, ACL and degraded mode

**Server and deployment**
- ⚪ Server shell: roles `web`, `worker`, `all`; configuration; middleware; error model
- ⚪ Drain-safe shutdown: readiness off, endpoint delay, in-flight requests finish, jobs released
- ⚪ Helm chart skeleton: one Deployment per role, Services, PodDisruptionBudgets, topology spread
- ⚪ Local HA-shaped stack: PostgreSQL with standby, Valkey with three Sentinels, S3 store
- ⚪ Metrics, JSON request log, health and readiness endpoints

**Exit checks**
- ⚪ PostgreSQL and Valkey failover under load without a failed request
- ⚪ Node drain under load without a failed request
- ⚪ No state between requests: test profile with random distribution across replicas

## Phase 2 · Access core (M1)

Details: [roadmap M1](../coverage/roadmap.md#m1--access-core)

**Identity and tokens**
- ⚪ OIDC login with a test identity provider, sessions, CSRF protection, logout
- ⚪ Break-glass administrator and bootstrap commands
- ⚪ Users, groups (from the login provider's claims) and service accounts
- ⚪ Personal and service-account tokens (`trv_pat_`, `trv_sat_`): scopes, lifetime, revocation
- ⚪ Brute-force protection with cluster-wide counters

**Permissions**
- ⚪ Permission policies: principals, repositories, name patterns, actions
- ⚪ Evaluation engine with caches that stay correct across replicas
- ⚪ Audit of logins, token changes, policy changes and denied requests

**Repositories and files**
- ⚪ Repository model and management API (local, remote, virtual)
- ⚪ Generic local repositories and raw file transfer under `/files/`
- ⚪ API conventions: errors, cursor pagination, ETags, idempotency keys, rate limits

**Admin UI**
- ⚪ Sign-in, repository list and creation wizard, file browser
- ⚪ Users, groups, service accounts, personal tokens
- ⚪ Permission policy editor with "why can X do Y" explainer
- ⚪ Audit log and system status

**Tests**
- ⚪ Integration tests: login, tokens, permission evaluation, raw transfer
- ⚪ E2E tests: browser journeys across replicas with random distribution

## Phase 3 · npm registry (M2)

Details: [roadmap M2](../coverage/roadmap.md#m2--npm) · spec: [formats/npm.md](../spec/formats/npm.md)

**Engines shared by all registries**
- ⚪ Format contract and conformance kit
- ⚪ Proxy engine: upstream settings, request collapsing through Valkey, metadata and negative caches, stale metadata while the upstream is down
- ⚪ Virtual engine: member order, merged metadata, deployment target, namespace claims, protection against dependency confusion

**npm**
- ⚪ Local: publish, package documents, dist-tags, unpublish, whoami, ping
- ⚪ Remote: proxy of the public npm registry, tarball URL rewriting, versions removed upstream
- ⚪ Virtual: merged package documents and dist-tags, writes through the deployment target
- ⚪ Clients: npm, pnpm, Yarn Classic, Yarn Berry
- ⚪ Package UI: package list and detail, README, install panel, client setup snippets

**Tests**
- ⚪ npm integration tests: protocol over HTTP, conformance kit, nightly fuzzing of package documents
- ⚪ npm e2e tests: install and publish with npm, pnpm, Yarn Classic and Yarn Berry through local, remote and virtual repositories
- ⚪ npm e2e: cold cache with three clients at once, upstream down, Valkey stopped, replica killed mid-install
- ⚪ npm load joins the drain and failover tests

**Release**
- ⚪ Pre-release 0.1

## Phase 4 · NuGet (.NET) registry (M3)

Details: [roadmap M3](../coverage/roadmap.md#m3--nuget) · spec: [formats/nuget.md](../spec/formats/nuget.md)

**NuGet v3**
- ⚪ Local: service index, package content, registrations, search, push, unlist, relist, delete
- ⚪ Symbol package push (`.snupkg`)
- ⚪ Remote: proxy of nuget.org with resource URL rewriting
- ⚪ Virtual: merged package content, metadata and search; publishing through the deployment target
- ⚪ Authentication: token as password in `nuget.config`, token as API key for push
- ⚪ Clients: .NET CLI, nuget.exe; setup instructions for Visual Studio
- ⚪ Package UI and client setup snippets for .NET
- ⚪ Format contract frozen for all later registries

**Tests**
- ⚪ NuGet integration tests: protocol over HTTP, conformance kit, SemVer 2 and ID normalization
- ⚪ NuGet e2e tests: `dotnet restore`, `dotnet nuget push` and nuget.exe through local, remote and virtual repositories
- ⚪ NuGet e2e: cold cache with three clients at once, push and delete across drains and retries, Valkey stopped
- ⚪ NuGet load joins the drain and failover tests

**Release**
- ⚪ Pre-release 0.2

## Phase 5 · GitLab CI OIDC (M4)

Details: [roadmap M4](../coverage/roadmap.md#m4--gitlab-ci-oidc) · spec: [gitlab-ci-oidc.md](../spec/gitlab-ci-oidc.md)

- ⚪ GitLab as trusted issuer: discovery, signing keys, key rotation
- ⚪ Token exchange: GitLab ID token in, short-lived CI token (`trv_cit_`) out
- ⚪ Identity mappings: conditions on project, branch, protected ref, environment; templated grants
- ⚪ Package ownership: a CI project writes only the packages it owns, reads what it is granted
- ⚪ Admin UI: CI issuers, mappings, dry run and claim tester
- ⚪ Reference pipeline: `.gitlab-ci.yml` publishing an npm package and pushing a NuGet package
- ⚪ Integration tests: invalid signatures, wrong audience, expired tokens, fork merge requests
- ⚪ E2E tests against a GitLab test instance for npm and NuGet, with a `web` node drained during the run
- ⚪ Pre-release 0.3

## Phase 6 · HA hardening, backup and Helm (M5)

Details: [roadmap M5](../coverage/roadmap.md#m5--ha-hardening-and-helm-ga) · spec: [ha-and-helm.md](../spec/ha-and-helm.md), [backup-and-restore.md](../spec/backup-and-restore.md)

**High availability**
- ⚪ Failure suite: pod crash, PostgreSQL switchover and failover, Valkey failover and outage, S3 outage, node loss
- ⚪ Node drain under npm and NuGet load with zero failed requests
- ⚪ Rolling upgrade from the previous pre-release with mixed versions
- ⚪ Performance targets met in two nightly runs in a row

**Backup and restore**
- ⚪ Backup job (CronJob): consistent database snapshot, blobs copied incrementally to the backup S3 target
- ⚪ Restore job into an empty installation
- ⚪ Revocation journal: a restore never brings back revoked tokens or removed permissions
- ⚪ E2E: backup taken during publishing restores with every npm and NuGet file digest-correct

**Helm chart and release**
- ⚪ Chart complete: values reference and schema, autoscaling, network policies, monitoring integration
- ⚪ Security documentation in [docs/security](../security/README.md) and security review
- ⚪ Release pipeline: image and chart published to `ghcr.io/guided-traffic`
- ⚪ Beta candidate published

## Phase 7 · Walkthrough and beta release

Details: [roadmap: owner walkthrough](../coverage/roadmap.md#owner-walkthrough)

- ⚪ Guided walkthrough of the running beta candidate with the owner:
  - ⚪ OIDC sign-in, users, groups, tokens, permission policies
  - ⚪ npm and .NET clients against local, remote and virtual repositories
  - ⚪ GitLab reference pipeline
  - ⚪ Node drain and PostgreSQL and Valkey failover under load
  - ⚪ Backup by the CronJob and restore into an empty namespace
- ⚪ Objections from the walkthrough worked off
- ⚪ All exit checks green again
- ⚪ Beta documentation: installation guide, configuration reference, known limitations
- ⚪ **Beta release `1.0.0-beta.1`**

---

## Test matrix

| Area | Integration tests | E2E tests | Drain and failover load |
|---|---|---|---|
| Foundation (storage, coordination) | ⚪ | — | ⚪ |
| Access core (login, tokens, permissions) | ⚪ | ⚪ | — |
| **npm** | ⚪ | ⚪ | ⚪ |
| **NuGet (.NET)** | ⚪ | ⚪ | ⚪ |
| GitLab CI OIDC | ⚪ | ⚪ | ⚪ |
| Backup and restore | ⚪ | ⚪ | — |
