# Releasing

Every push to `main` and every pull request runs the checks ([CI](#ci)). Once every job of a push
run on `main` passed, Semantic Release publishes a GitHub release ([release flow](#release-flow)),
and [`build.yml`](../../.github/workflows/build.yml) builds the product from that tag: the image
`guidedtraffic/trove` on Docker Hub, and the Helm charts `trove` and `trove-bastion` in the chart repository on GitHub Pages
([the release pipeline](#the-release-pipeline)). What the release contains and how the image is
built: [`Containerfile`](../../Containerfile).

## CI

[`release.yml`](../../.github/workflows/release.yml) ("Test and Release") runs on every push to `main`,
every pull request to `main` and on demand. A newer push cancels the running run of its ref. Every
check is a job of its own, with its own make target, so it runs the same locally. Every job runs on the
organisation's self-hosted runners, which are ephemeral pods (Ubuntu 24.04, Docker) without `make` or a C
compiler. A job therefore installs what it calls: `make` and the C compiler (`build-essential`), `file`
and `jq` where a step uses them, the Docker Compose plugin for the integration stack, Go from `go.mod`,
Node.js from `web/.nvmrc`, and the pinned helm, kubectl, kind and Docker Buildx; the chart job also fetches promtool
at its pin (`make promtool`, below).

| Job | Make target | Runs |
|---|---|---|
| versions (E0.2) | `make versions-check` | go, node (`web/.nvmrc`), sqlc, oapi-codegen, golangci-lint, oasdiff (when built), promtool (when fetched), kind, kubectl, helm, k6 and docker buildx against their pins. The job installs the pinned helm, kubectl, kind and buildx first, so the runner's copies are not the ones checked |
| Code Linting | `make lint` | golangci-lint (pinned; its binary is cached by version and Go version) with the linters of [.golangci.yml](../../.golangci.yml), then `go vet` |
| GoSec Security Scan | `make gosec` | golangci-lint with only gosec enabled, the settings and the accepted findings of [.golangci.yml](../../.golangci.yml) |
| Vulnerability Check | `make vuln` | govulncheck (pinned) over the module |
| Cyclomatic Complexity | `make cyclo` | gocyclo (pinned), fails above `CYCLO_THRESHOLD`; the job summary lists the 20 most complex functions with `make -s cyclo-report` |
| generate (E0.1) | `make generate-check`, then `make nuget-reference-check` | `go generate`, then `git diff --exit-code` and a check for generated files that are not committed; then the NuGet reference solution of [perf/nuget](../../perf/README.md), regenerated from the counting upstream's package set and compared with the committed files (`go run ./test/nugetstub/cmd/nugetstub reference -check`, [Q-ARCH-279](../spec/architecture.md#q-arch-279--how-are-the-nuget-sdk-pins-and-the-reference-solution-kept-in-step)) |
| nuget vectors (R-NUGET-003) | `make nuget-vectors-check` | regenerates the golden vectors of the NuGet version, range and ID rules with NuGet.Versioning in the pinned .NET SDK image (`NUGETVECTORS_SDK_IMAGE`, `NUGET_VERSIONING_VERSION`) and compares them with the committed file; needs Docker |
| build without Node.js (E0.1) | `make build` | with every Node.js executable off `PATH`; `file` must say *statically linked*, `go version -m` must record `CGO_ENABLED=0` |
| unit tests | `make test` | the race detector; uploads `.out/test-unit.json` (`test-unit`) and `.out/coverage/unit.out` (`profile-unit`) |
| web (Angular) | `make web-ci`, `make web-lint`, `make web-test`, `make web-embed` | `npm ci` (fails when `package.json` and the lockfile disagree), ESLint and the R-UI-046 rules, the Angular unit tests, the production build embedded into `internal/webui/dist` |
| Malware Scan (source code) | `make malware-scan` | ClamAV (`clamscan --recursive --infected`) over the tree, without `.git`, `node_modules` and the build outputs; any infected file fails; uploads `clamav-report` |
| chart (E0.7) | `make chart-lint`, `make chart-test` | `helm lint` and `helm template` over every value set of both charts (`deploy/helm/trove/ci`, `deploy/helm/trove-bastion/ci`, the dev profile), the render assertions of [test/chart](../../test/chart) for both charts, and the `promtool check rules` / `promtool test rules` run over the Bastion's PrometheusRule ([rules_test.yaml](../../deploy/helm/trove-bastion/tests/rules_test.yaml)); `make chart-test` fetches promtool into `.bin` at `PROMTOOL_VERSION` of [tools/versions.env](../../tools/versions.env), checked against the SHA-256 of the platform; uploads `.out/test-chart.json` (`test-chart`) |
| image | `make image IMAGE=trove:ci` | the [Containerfile](../../Containerfile) for the runner's architecture, loaded into Docker; the image must run as `65532:65532` (R-HA-033) |
| Container Scan (Trivy, built image) | `make image IMAGE=trove:scan`, then Trivy | Trivy (pinned `TRIVY_VERSION`) on the built image: fixed OS and library vulnerabilities of CRITICAL or HIGH, secrets and misconfigurations; SARIF uploaded as `trivy-container-report` |
| integration (Valkey 9.1, shard a, b) | `make integration-up`, `make test-integration SHARD=<a\|b>`, `make integration-down` | the integration tier against the compose stack on TLS, on Valkey 9.1 only (no older line is guaranteed while trove is in development); two legs with a stack each, shard `a` the packages `coordination`, `npm`, `repository` and `proxy`, shard `b` every other one; uploads `test-integration-<shard>` and `profile-integration-<shard>` |
| S3 conformance (MinIO) | `make minio-up`, `make s3-conformance`, `make minio-down` | the conformance tests of R-STOR-035 against MinIO, built from source at `MINIO_COMMIT` and served over TLS only; the server log is uploaded on failure |
| e2e-npm on kind (npm) | `make image VERSION=dev`, `make dev-up`, `make e2e SET=npm`, `make dev-down` | the npm acceptance matrix (eight real clients, [R-NPM-045](../spec/formats/npm.md#r-npm-045--client-acceptance-matrix)) and E2.2 to E2.12 on the scatter profile with the counting test upstream; gates every change, own cluster name and edge port (8082). A dispatch with `closing=true` adds the leg `npm-closing` (port 8083): the matrix and the cold install with Valkey stopped, and the read-only subset against the public registry ([Q-NPM-46](../spec/formats/npm.md#q-npm-46--what-marks-the-run-that-closes-the-phase)); the legs `npm-ha` and `npm-ha-crash` left this job on 2026-11-01 (owner): `npm-ha` runs nightly and on dispatch in [HA and Performance (nightly)](#ha-and-performance-nightly) (port 8087), `npm-ha-crash` (three force-deletes) runs by hand only; for them the step budget is 80 minutes and the job 120, and they are E2.11: drain variant A, the PostgreSQL switchover and the Valkey failover under the npm load, and the pod crash with three force-deletes ([e2e.md](e2e.md#drain-and-failover-under-npm-load)); a closing dispatch runs both as one leg `npm-ha-closing` with the 20-minute pod crash, the R-ARCH-027 report and the 5-minute Valkey outage ([Q-HA-67](../spec/ha-and-helm.md#q-ha-67--where-and-how-often-the-scenarios-under-npm-load-run)); uploads `e2e-npm-report-<set>`, `e2e-npm-artifacts-<set>` and on failure the cluster state |
| fuzz npm (closing run only) | `make fuzz-npm FUZZ=<target> FUZZTIME=30m` | three legs (`FuzzR_ARCH_028_Match`, `FuzzR_ARCH_028_Inspect`, `FuzzMatch`) of 30 minutes each; runs only on a dispatch with `closing=true`; uploads the failing input |
| fuzz nuget (closing run only) | `make fuzz-nuget NUGET_FUZZ_TARGETS=<pkg>:<target> NUGET_FUZZTIME=30m` | six legs of 30 minutes, one per target of `NUGET_FUZZ_TARGETS`: the readers `nupkg:FuzzR_ARCH_028_Inspect` and `nuspec:FuzzR_NUGET_005_Nuspec`, and the version and ID rules `version:FuzzNormalize`, `version:FuzzCompare`, `version:FuzzRange`, `id:FuzzID`; they run side by side ([Q-ARCH-278](../spec/architecture.md#q-arch-278--how-is-the-nuget-fuzz-run-of-the-closing-run-laid-out)); runs only on a dispatch with `closing=true`; uploads `fuzz-nuget-<pkg>-<target>` with the failing input |
| e2e-nuget on kind (nuget) | `make nuget-clients-image`, `make image VERSION=dev`, `make dev-up`, `make e2e SET=nuget`, `make dev-down` | the NuGet acceptance suite (R-NUGET-027, E3.1 to E3.8 as they land): the .NET 10 SDK and nuget.exe on Mono in the client pods, the scatter profile and the counting NuGet test upstream; gates every change, own cluster name and edge port (8088). The clients image is built by its own step and not again by the set (`TROVE_E2E_NUGET_CLIENTS_IMAGE_BUILD=0`). A dispatch with `closing=true` adds the leg `nuget-closing` (port 8089): the tests named `TestNuGetClosing...`, the additions of the closing run. The nightly leg `nuget-ha` (port 8090) is in [HA and Performance (nightly)](#ha-and-performance-nightly). A set whose expression names no test yet passes with a notice, as `nuget-closing` and `nuget-ha` do today ([Q-ARCH-276](../spec/architecture.md#q-arch-276--how-does-an-e2e-set-that-names-no-test-pass-and-when-does-it-fail)); uploads `e2e-nuget-report-<set>`, `e2e-nuget-artifacts-<set>` and on failure the cluster state ([e2e.md](e2e.md#the-nuget-clients)) |
| e2e-ci on kind (ci) | `make npm-clients-image`, `make nuget-clients-image`, `make image VERSION=dev`, `make dev-up`, `make e2e SET=ci`, `make dev-down` | the reference pipeline of GitLab CI OIDC (R-CI-028, E4.1 and E4.4 to E4.6): the steps of the Examples with the real npm and .NET clients in the client pods against the issuer double, the scatter profile; gates every change, own cluster name and edge port (8091). Both clients images are built by their own steps (`TROVE_E2E_NPM_CLIENTS_IMAGE_BUILD=0`, `TROVE_E2E_NUGET_CLIENTS_IMAGE_BUILD=0`). The long legs are the set `ci-ha` of [HA and Performance (nightly)](#ha-and-performance-nightly); uploads `e2e-ci-report-ci`, `e2e-ci-artifacts-ci` (with the job logs `ci-job-*.log`) and on failure the cluster state ([e2e.md](e2e.md#the-ci-tests)) |
| e2e on kind (push, access-long) | `make image VERSION=dev`, `make dev-up`, `make e2e SET=<set>`, `make dev-down` | the dev profile on a kind cluster, one leg per set, every change: `push` and `access-long` (E1.1, E1.11 with the Playwright journeys, E1.13; the only leg that installs Node.js and the browser). The set `failover` (E0.13, E1.8) left this job on 2026-11-01 (owner) and runs nightly and on dispatch in [HA and Performance (nightly)](#ha-and-performance-nightly); the legs and those of e2e-npm build their own image and wait for no other job; each leg has its own cluster name and edge port; uploads the reports, `e2e-artifacts-<set>`, and on failure the cluster state |
| workflow files (actionlint) | `actionlint` (pinned `ACTIONLINT_VERSION`) | every file of `.github/workflows`, with [.github/actionlint.yaml](../../.github/actionlint.yaml) |
| compatibility (R-ARCH-025, R-ARCH-026) | `make compat-check` | the OpenAPI document against the latest `v0.*` release with the pinned oasdiff (`--fail-on ERR`), the migration files: only additions that continue the numbering, and no change of a migration that `main` has committed (the history of `main`); needs the history and the tags (`fetch-depth: 0`, `fetch-tags: true`); see [Compatibility](#compatibility) |
| coverage generator (links and rules) | `go run ./internal/tools/coverage -check`, then `-links` | the generated pages match their rules; every relative link and anchor resolves |
| coverage evidence (implemented.txt) | `go run ./internal/tools/coverage -check -tests …` over the reports of this run | [implemented.txt](../coverage/implemented.txt) equals what the reports imply; needs `unit`, both `integration` shards and `chart` |
| Combined Coverage Report | `make coverage-report` | merges the unit and integration profiles; the job summary and the badge `coverage-badge` |
| Release Tooling | `make test-release-tooling` | `npm ci`, `npm audit signatures`, and [hack/verify-release-tooling.mjs](../../hack/verify-release-tooling.mjs), which renders release notes with the installed plugins |
| Semantic Release | – | needs every job above; runs only on a push to `main`; publishes the release ([release flow](#release-flow)) |

The workflows pin their actions by major version tag (`actions/checkout@v7`), the way the
organisation's other repositories do. Two actions are pinned by a release tag instead:
`aquasecurity/trivy-action@v0.36.0` and `renovatebot/github-action@v46.3.10`. Third-party actions that
run with the release secrets are pinned by tag too: the owner decided so on 2026-10-31 after the risk
was named ([ADR-130](../decisions.md#adr-130--the-organisations-ci-and-release-standard)). A tag can be
moved, and a moved tag runs in the job that names it with that job's secrets
([H-325](../security/data-and-secret-flow.md#h-325--a-moved-action-tag-runs-in-a-job-that-holds-release-secrets)).

### Coverage

The unit tier and each integration line write a profile to `.out/coverage`: `unit.out` and
`integration-<line>.out`, with `-covermode=atomic -coverpkg=./...`, so each counts for every package
it reaches. [`test/coverage/report.sh`](../../test/coverage/report.sh) merges them: a block counts once,
covered when any profile covered it. It writes `coverage.txt` (`go tool cover -func`), `summary.md`
for the job summary, and [`coverage.json`](../../.github/badges/coverage.json) (the README badge; green
from 80 %, brightgreen from 90 %, yellow from 70 %, orange from 60 %, red below). Semantic Release
commits the badge with the release.

The requirement evidence of [docs/coverage](../coverage/README.md) is a different thing: the
`coverage evidence` job checks that `implemented.txt` equals the requirement IDs the test reports of
the run prove. A refresh of the file is `make coverage-refresh`, which runs both integration lines.

### Cyclomatic complexity

`CYCLO_THRESHOLD` in the [Makefile](../../Makefile) is a ratchet, as in cogpit: it is the complexity of
the most complex function of the module, so `make cyclo` passes today and a change that adds a function
above it fails. The threshold of valkey-operator is 15. Today 40 functions of the module are above 15
(`make cyclo-report` lists them; tests and the generated code are not counted). A change that
simplifies the most complex function lowers the threshold to the new maximum in the same commit.

### Malware scan

ClamAV scans the source tree (`make malware-scan`). The job installs the `clamav` package and updates
the database with `freshclam` first. The scan excludes `.git`, `node_modules` and the build outputs
`.out`, `.bin`, `.dev` and `bin`. The report is `.out/clamav-report.txt`; the job fails when it does
not end with `Infected files: 0`, so a scan that did not finish is a failure too.

### Lint and GoSec

Both run through the same pinned golangci-lint (`GOLANGCI_LINT_VERSION`), with the linter settings of
[.golangci.yml](../../.golangci.yml). An accepted gosec finding carries its reason in the code
(`//nolint:gosec // reason`) or in the configuration; the standalone gosec binary does not read those
annotations, so the repository runs gosec through golangci-lint and has one gosec version.

### Compatibility

`make compat-check` runs the tests of [test/compat](../../test/compat) (build tag `compat`). They hold two promises of
the specification to the latest release:

| Requirement | What the check accepts | Implemented by |
|---|---|---|
| [R-ARCH-025](../spec/architecture.md#r-arch-025--versioning-and-compatibility-policy) | `api/openapi.yaml` has no error-level change against the baseline: `oasdiff breaking --fail-on ERR` | oasdiff, pinned as `OASDIFF_VERSION` in [tools/versions.env](../../tools/versions.env) and built into `.bin/` by the Makefile; the fixtures of [testdata/openapi](../../test/compat/testdata/openapi) show that a removed operation fails and an added one passes |
| [R-ARCH-026](../spec/architecture.md#r-arch-026--schema-migrations-and-the-compatibility-gate) | Since the baseline, every change of `internal/meta/postgres/migrations` is an addition (`git diff --name-status --no-renames`), and the new numbers continue the sequence | [migrations_test.go](../../test/compat/migrations_test.go): fixtures for an edited, a renamed and a removed file, and for the numbering |
| [R-ARCH-026](../spec/architecture.md#r-arch-026--schema-migrations-and-the-compatibility-gate), [Q-ARCH-283](../spec/architecture.md#q-arch-283--is-a-migration-on-main-immutable-and-what-does-the-check-hold-to) | On the history of `main`, a migration file that `main` has committed is never changed, removed or renamed after its first commit on `main`; a pull request is checked on its own branch too. Only the commits named in `historicalEdits` are exceptions | [migrations_history_test.go](../../test/compat/migrations_history_test.go): `migrationHistoryProblems` on a fixture repository (an edit, a removal, a rename, a branch edit, an allowlisted and an unlisted edit, a stale entry, a branch that predates a file) and on the repository itself (`TestR_ARCH_026_MigrationsOnMainAreNeverEdited`) |

The baseline is the newest `v0.*` tag whose commit is not HEAD's. On the release commit itself HEAD would be compared with
itself, so the check then compares with the release before it ([ADR-139](../decisions.md#adr-139--compatibility-is-checked-against-the-latest-release-in-ci)).
A clone without tags has no baseline, and the tests fail rather than pass. Locally run `git fetch --tags` first. The
`compat` job of [release.yml](../../.github/workflows/release.yml) checks out with `fetch-depth: 0` and `fetch-tags: true`,
and Semantic Release needs it, so a breaking change or a changed migration blocks the release.

The history check reads `main` (the local branch, or `origin/main` where the checkout has none) and the checked-out HEAD. A migration that `main` has committed may not change after its first commit on `main`, so a change in a pull request fails
on the pull request's own branch, and a push to `main` fails on `main`. The exceptions are the commits that `historicalEdits` in
[migrations_history_test.go](../../test/compat/migrations_history_test.go) names, two of them today, from before the rule. A
failure names the file, the commit and the rule: fix forward with a new migration ([database.md](database.md#how-to-fix-a-migration-that-is-on-main)).

There is no waiver. R-ARCH-025 promises additive changes only within `/api/v1`, so a breaking change there is a new API
version, not a flag in CI. The checker's own `--version` reports `main` for a module install, so the test reads the module
version with `go version -m` and compares it with the pin: a binary of another version fails the test.

Not covered by this job: the other surfaces of R-ARCH-025 (configuration keys, Helm values, metric names, error codes) and
its acceptance test of a `helm upgrade` from N-1 to N and a `helm rollback`. Nothing in CI checks those.

### Running a job locally

Run from the repository root. Each line is the command the job runs.

```sh
make versions-check                     # the tools on PATH against the pins
make lint gosec vuln cyclo generate-check build
make test                               # unit tests, race detector, .out/coverage/unit.out
make web-ci web-lint web-test web-embed # Node.js from web/.nvmrc
make malware-scan                       # needs clamscan on PATH
make chart-lint chart-test              # both charts; chart-test fetches promtool at its pin into .bin
make image IMAGE=trove:ci               # the Containerfile, host architecture
make integration-up test-integration integration-down
make minio-up s3-conformance minio-down # needs Docker and the MinIO source build
make coverage-report                    # merges .out/coverage
make test-release-tooling               # npm ci, npm audit signatures, the release notes check
go run ./internal/tools/coverage -check -links
git fetch --tags && make compat-check   # R-ARCH-025 and R-ARCH-026 against the latest v0.* release
make image VERSION=dev && make dev-up && make e2e SET=push && make dev-down
make image VERSION=dev && make dev-up && make e2e SET=npm && make dev-down   # TROVE_DEV_HTTP_PORT=8080 is the port of the committed reference lockfile; any port works
make fuzz-npm FUZZ=FuzzMatch FUZZTIME=1m
make nuget-vectors-check                # needs Docker; make nuget-vectors writes the file
make nuget-reference-check              # perf/nuget/reference against a regeneration (no Docker)
make fuzz-nuget NUGET_FUZZTIME=1m       # the six NuGet fuzz targets, one minute each (CI: 30 minutes each, closing run only)
```

### The run that closes a phase

The roadmap reserves a few checks for "the full run of Test and Release on the commit that closes the phase"
(M2: the npm scenario list and the cold-cache install with Valkey stopped, the read-only subset against the public registry,
the 30-minute fuzz run of npm's `Match` and `Inspect`). Nothing in a push or a pull request marks that commit, so a person starts
the run ([Q-NPM-46](../spec/formats/npm.md#q-npm-46--what-marks-the-run-that-closes-the-phase)):
*Actions, Test and Release, Run workflow*, the closing commit as the ref, `closing` ticked; or
`gh workflow run release.yml --ref main -f closing=true`. Every job of the workflow runs, except that the `e2e` job no longer has the `failover` set (owner, 2026-11-01): the closing run does not start [HA and Performance (nightly)](#ha-and-performance-nightly), so a closing commit whose criteria need the failover set needs that run too. Plus the legs `e2e-npm (npm-closing)` and
the three `fuzz npm` legs, the leg `e2e-nuget (nuget-closing)` and the six `fuzz nuget` legs of 30 minutes each ([Q-ARCH-278](../spec/architecture.md#q-arch-278--how-is-the-nuget-fuzz-run-of-the-closing-run-laid-out)), and the legs `e2e-npm (npm-ha)` and `e2e-npm (npm-ha-crash)` run as one leg `npm-ha-closing`, with the 20-minute pod crash, the R-ARCH-027 report and the Valkey outage under npm load. A dispatch does not release (Semantic Release runs on `push`), and a failed closing leg means the phase
is not closed. Not in this workflow yet: the run of the NuGet criteria against nuget.org that R-NUGET-027 names for the closing
run (the remote and symbol criteria); the closing run covers the NuGet legs above and nothing against the public registry for NuGet.

`make generate-check` runs `git diff --exit-code`, so it fails on a tree with uncommitted changes for
reasons unrelated to generation. `make dev-up` needs an amd64 host or the emulator of
the arm64 note of [local-profile.md](local-profile.md).

### HA and Performance (nightly)

The long tests run here and not on every change (owner, 2026-11-01), so a slow leg no longer holds the release. Per change,
Test and Release runs `push` and `access-long` (job `e2e`), `npm` (job `e2e-npm`) and `nuget` (job `e2e-nuget`). This workflow runs the
`failover` set (job `e2e`, E0.13 and E1.8), the `npm-ha` leg (job `e2e-npm`, E2.11), `npm-ha-soak` and the `nuget-ha` leg (job
`e2e-nuget`, Q-ARCH-277), beside the latency job. Dispatch it with `gh workflow run "HA and Performance (nightly)" --ref main`.
Locally, from the repository root, after `make image VERSION=dev` and `make dev-up`: `make e2e SET=failover`, `make e2e SET=npm-ha`,
`make e2e SET=npm-ha-crash` (the three-force-delete crash, which has no CI job), `make e2e SET=npm-ha-soak` or `make e2e SET=nuget-ha`,
then `make dev-down`; the latency tests need `make integration-up && make test-performance`.
A red long leg does not hold the release: it is recorded as an open finding instead (owner, 2026-11-01).

[`performance.yml`](../../.github/workflows/performance.yml) ("HA and Performance (nightly)") runs every night at 02:17 UTC and on demand
(`workflow_dispatch`), on the self-hosted runners. It brings up the integration stack (`make integration-up`), runs
`make test-performance` and stops the stack. That target runs `TestChangePropagation`, `TestJobPickup`, `TestR_NPM_010_ThousandVersions` and
`TestIdempotentCreate` with `TROVE_PERF_GATE=1`, which enforces the p99 bounds of P-13 (100 ms) and P-14 (1 s) with Valkey available, R-NPM-010's
10 ms for a cached document of 1,000 versions and R-API-014's 1.5 s for a concurrent duplicate; it then runs `TestR_EP_013_CeilingPerformance` of `internal/authz` the same way
(the 1 ms p99 of one decision with 1,000 exposure rules, in the CPU time of the deciding thread, [Q-ARCH-250](../spec/architecture.md#q-arch-250--a-p99-bound-in-microseconds-is-a-nightly-gate-and-the-per-change-test-asserts-allocations-and-the-median)); the per-change integration job asserts delivery, the p50 and looser bounds only
([Q-ARCH-220](../spec/architecture.md#q-arch-220--p-13-starts-its-clock-at-the-commit-and-its-p99-is-enforced-in-the-nightly-performance-run)).
The report `test-performance` is kept for 30 days. Beside it, the job `npm-ha-soak` runs `make image VERSION=dev`, `make dev-up` and
`make e2e SET=npm-ha-soak` on a kind cluster: R-HA-052's pod crash with ten force-deletes over 20 minutes under the npm load, then the npm
load scripts of R-ARCH-027 against one replica (`TestNPMPerf`, reported, not gated); the three-force-delete crash runs by hand only, and the closing run runs the ten force-deletes
in its leg `npm-ha-closing` ([Q-HA-67](../spec/ha-and-helm.md#q-ha-67--where-and-how-often-the-scenarios-under-npm-load-run)). Its reports
`e2e-npm-report-npm-ha-soak` and `e2e-npm-artifacts-npm-ha-soak` are kept for 30 days. A failed night is not wired to an issue or a
notification: look at the Actions list. The other targets of R-ARCH-027 (`perf/`) are not part of this workflow.

Job `e2e-nuget` runs `make e2e SET=nuget-ha` on a kind cluster (edge port 8090, cluster `trove-nuget-ha-<run id>`, reports
`e2e-nuget-report-nuget-ha` and `e2e-nuget-artifacts-nuget-ha`, kept 14 days). No test is named `TestNuGetHA...` yet, so the set passes
with a notice and the job checks nothing beyond the cluster coming up ([Q-ARCH-276](../spec/architecture.md#q-arch-276--how-does-an-e2e-set-that-names-no-test-pass-and-when-does-it-fail)).
The nightly run therefore costs a cluster bring-up and proves no NuGet behavior under load until the drain and failover tests land.

Job `e2e-ci` runs `make e2e SET=ci-ha` on a kind cluster (edge port 8092, cluster `trove-ci-ha-<run id>`, reports `e2e-ci-report-ci-ha` and
`e2e-ci-artifacts-ci-ha`, kept 14 days): E4.7, the reference pipeline while the node of a `web` replica is drained and again with Valkey
stopped (every replica ready, `"degraded":["valkey"]`), single-use exchanges across a forced Sentinel failover and `FLUSHALL`, and a `web`
pod deleted gracefully under 50 exchanging jobs ([e2e.md](e2e.md#the-ci-tests)). By hand: `make dev-up && make e2e SET=ci-ha`.

### Renovate

[`renovate.yml`](../../.github/workflows/renovate.yml) runs self-hosted Renovate every day at 02:00
Berlin time, with an installation token of the organisation's GitHub App (secrets `APP_CLIENT_ID` and
`APP_PRIVATE_KEY`). The configuration is [renovate.json](../../renovate.json).

Every Renovate branch runs the whole Test and Release pipeline on the organisation's nine shared runners, so the
number of branches is bounded (owner, 2026-10-31):

- Branches are created and updated only before 6:00 on Mondays (`schedule`, `updateNotScheduled: false`); the daily
  run outside that window changes no branch, but it still closes pull requests whose update is gone.
- Every minor, patch and digest update that no other rule groups is in one pull request, `non-major dependencies`;
  the groups below keep their own. At most three Renovate branches and pull requests exist at a time, at most two
  are opened per hour.
- A major update gets its branch only after it is ticked in the Dependency Dashboard issue.
- Security updates are not held by the schedule (`vulnerabilityAlerts`, `osvVulnerabilityAlerts`).

- Go modules, the Go version (`go.mod`, grouped with `golang.org/x/*`) and GitHub Actions merge on
  their own once CI passes; majors wait for approval and review.
- The pins of [tools/versions.env](../../tools/versions.env) are read by custom managers: a
  `# renovate:` comment directly above a tool or chart pin names its datasource and package; image lines
  are read by their digest, and tag and digest move together. The k6 image and `K6_VERSION` are one
  group, because [deploy/dev/e2e.sh](../../deploy/dev/e2e.sh) refuses a mismatch.
- The two .NET SDK pins of the NuGet tests, `NUGETVECTORS_SDK_IMAGE` and `E2E_NUGETCLIENTS_SDK_IMAGE` (both
  `mcr.microsoft.com/dotnet/sdk`), are one group, `.NET SDK (NuGet tests)`, so one pull request moves both; `make versions-check`
  fails when they differ ([Q-ARCH-279](../spec/architecture.md#q-arch-279--how-are-the-nuget-sdk-pins-and-the-reference-solution-kept-in-step)).
- The release tooling of [package.json](../../package.json) (semantic-release and its presets) is one
  group and never merges on its own. The Angular packages of `web/package.json` are one group, reviewed.
- Every automerged dependency update is a `fix` commit (valkey-operator's policy), so under
  [.releaserc.json](../../.releaserc.json) each of them is a patch release. See
  [Release flow](#release-flow).
- TypeScript is held below 6.1 by a rule of its own (`allowedVersions`, [ADR-140](../decisions.md#adr-140--typescript-is-held-below-61-until-angulars-peer-range-admits-7x)):
  the Angular 22 packages and typescript-eslint admit no 7.x, so Renovate does not open that update.
- The Containerfile has no literal image or toolchain version; its build arguments come from
  `go.mod`, [web/.nvmrc](../../web/.nvmrc) and `tools/versions.env`, so there is nothing for the
  dockerfile manager to update.

## Release flow

[`.releaserc.json`](../../.releaserc.json) (preset `conventionalcommits`, releases from `main`), the
tooling pinned in [package.json](../../package.json) and [package-lock.json](../../package-lock.json):

1. Commits follow [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/): `feat:` makes a
   minor release, `fix:` and `perf:` a patch release, other types none. A breaking change (`!` or a
   `BREAKING CHANGE:` footer) makes a minor release too, while trove is on major version 0
   (`releaseRules`).
2. Once every job of a push run on `main` passed, the job Semantic Release takes a token of the
   organisation's GitHub App (`permission-contents: write`), runs `npm ci` and `npm audit signatures`,
   puts the coverage badge in place (the artifact `coverage-badge`) and runs `npx semantic-release`: the
   version from the commits since the last tag, the notes, the badge committed as
   `chore(release): <version> [skip ci]` with the notes in its body, the tag `v<version>` on that commit,
   and the GitHub release. `[skip ci]` keeps the release commit from starting a run.
3. The release's `published` event starts [the release pipeline](#the-release-pipeline), because the
   App's token made the release: one made with `GITHUB_TOKEN` starts no workflow.

A release that commits the badge moves `main`: `git pull --rebase` before the next push.

**Releases stay on major version 0** until the owner decides on 1.0 ([ADR-124](../decisions.md#adr-124--releases-stay-on-major-version-0)).

### When no release comes

Semantic Release needs every job of the run: one failed or cancelled job skips it. A newer push cancels
the running run (`cancel-in-progress`), which loses nothing: the next green run releases every commit
since the last tag.

- **A job failed:** `gh run list --branch main --limit 3`, then for the newest commit's run
  `gh run view <id>` and `gh run rerun <id> --failed`. Nothing re-runs by itself.
- **The release is published but its image or chart is not:** the run of that tag in
  `Release Docker & Helm` (`gh run list --workflow "Release Docker & Helm"`), then
  `gh run rerun <id> --failed`. The job pushes the same tags again.
- **semantic-release stopped after the tag** (a tag without a GitHub release, no pipeline): a re-run
  publishes nothing, its commit being behind `main`. Finish by hand, as Semantic Release would:

  ```sh
  git notes --ref semantic-release-v<version> add -f -m '{"channels":[null]}' v<version>
  git push origin refs/notes/semantic-release-v<version>
  git log -1 --format=%B <release commit> | tail -n +3 > notes.md
  gh release create v<version> --verify-tag --title v<version> --notes-file notes.md --latest
  ```

  A person's token starts the pipeline as the App's does, with that person as the release's author.

## The release pipeline

[`build.yml`](../../.github/workflows/build.yml) ("Release Docker & Helm") runs on `release: published`.
The tag is the version: `v0.1.0` gives the image the tags `0.1.0`, `0.1` and `0` (the `latest` tag
belongs to pushes to `main`, not to releases), and the `version` and `appVersion` `0.1.0` of both charts. Every
job checks out the tag.

| Job | Runner | What it does |
|---|---|---|
| Build Docker Image | self-hosted | [docker/build-push-action](https://github.com/docker/build-push-action) builds [Containerfile](../../Containerfile) for `linux/amd64,linux/arm64` and pushes `guidedtraffic/trove` to Docker Hub with the tags of [docker/metadata-action](https://github.com/docker/metadata-action); provenance and SBOM attestations; the build arguments are the pins (`NODE_VERSION` from [web/.nvmrc](../../web/.nvmrc), `GO_VERSION` from [go.mod](../../go.mod), `RUNTIME_BASE_IMAGE` from [tools/versions.env](../../tools/versions.env)) and the version, the commit and the commit's date for the binary's version line |
| Scan and attach the SBOM | same | the SPDX SBOM by anchore/sbom-action with `SYFT_VERSION`, uploaded to the release; a Docker Scout CVE report |
| Release Helm Chart to GitHub Pages | same | `deploy/helm/trove` and `deploy/helm/trove-bastion` with `version` and `appVersion` set to the release (and the image tag in the `values.yaml` of `trove`), packaged together with one `helm package`, added to the chart repository's `index.yaml` on the `gh-pages` branch in one push, and `trove-<version>.tgz`, `trove-bastion-<version>.tgz` and `index.yaml` attached to the release |

**The Containerfile** has three stages. `web` runs `npm ci` and `npm run build:embed` on the Node.js of
`web/.nvmrc`; `build` cross-compiles the binary for the target platform on the build platform
(`CGO_ENABLED=0`), so neither architecture is emulated; the final stage is the distroless base of
`RUNTIME_BASE_IMAGE`, the binary and nothing else, as UID 65532. `make image` builds the same file for the
host architecture.

**The chart repository** is `https://guided-traffic.github.io/trove/`, served from the `gh-pages`
branch: every release adds the packages of both charts and the merged `index.yaml`. The release's assets are the
two packages of that release and the index. The Bastion chart has no version of its own: its `version` and `appVersion`
are the trove version of the release (R-ARCH-025, R-BAS-045), so one release publishes `trove` and `trove-bastion` at the same
number, and a Bastion chart is installed with the image of its `appVersion` (its `image.tag` is empty, which means
`appVersion`; the release job leaves that line alone). The chart's defaults point at the image: `image.registry:
docker.io`, `image.repository: guidedtraffic/trove` ([values.yaml](../../deploy/helm/trove/values.yaml)),
checked by [test/chart/image_test.go](../../test/chart/image_test.go). The release job rewrites the line
`  tag:` of the `values.yaml` of the chart `trove`; the test fails when a second line of that shape appears.

Not in the pipeline: a chart SBOM, a chart attestation, and any signature. Only the image carries
attestations: a provenance and an SBOM attestation in its image index.

## Secrets and settings

The pipeline uses three secrets of the organisation, which reach this repository without a setting here:
`DOCKERHUB_PAT` (the Docker Hub access token of the user `guidedtraffic`) for the image, and
`APP_CLIENT_ID` and `APP_PRIVATE_KEY` for the GitHub App that Semantic Release and Renovate act as.

One setting is the owner's to make: the `gh-pages` branch that holds the chart repository, which GitHub
Pages serves at `https://guided-traffic.github.io/trove/`.

## Not in place yet

- **No signatures.** The image carries a provenance and an SBOM attestation, not a keyless signature. The
  chart carries neither a signature nor an attestation.
- **Docker Buildx of the local machine** is the one of Docker Desktop, not the pinned version, until it
  is replaced ([make versions-check](../../Makefile) reports it).
- **Dependency updates are patch releases.** Each automerged update is a `fix` commit.
- **The cyclomatic complexity threshold** is the ratchet of today, not the 15 of valkey-operator.
