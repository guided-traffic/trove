# Releasing

Every push to `main` and every pull request runs the checks ([CI](#ci)). Once every job of a push
run on `main` passed, Semantic Release publishes a GitHub release ([release flow](#release-flow)),
and [`build.yml`](../../.github/workflows/build.yml) builds the product from that tag: the image
`guidedtraffic/trove` on Docker Hub, and the Helm charts `trove` and `trove-bastion` in the chart repository on GitHub Pages
([the release pipeline](#the-release-pipeline)); a last job installs those artifacts into a kind cluster and runs the matrix against them
([the post-release check](#the-post-release-check)). What the release contains and how the image is
built: [`Containerfile`](../../Containerfile).

## CI

[`release.yml`](../../.github/workflows/release.yml) ("Test and Release") runs on every push to `main`,
every pull request to `main` and on demand. A newer push cancels the running run of its ref. Every
check is a job of its own, with its own make target, so it runs the same locally. A job runs on one of the
organisation's two self-hosted runner classes or, where it needs more disk than they have, on a GitHub-hosted runner
(owner, 2026-11-05; the table below). Both self-hosted classes start one ephemeral pod per job and discard it
afterwards: the runner (actions-runner 2.338.2, Ubuntu 24.04) and a privileged Docker-in-Docker sidecar (docker 29.9.2-dind)
share the pod's budget, without `make` or a C compiler.

| Class | Label | Pods | CPU request / limit | RAM request / limit (runner and dind together) | Disk: `_work` / Docker | Jobs |
|---|---|---|---|---|---|---|
| wds18-small | `runs-on: self-hosted` | 0–24 | 1 / 6 | 4 GiB / 10 GiB | 10 GiB / 15 GiB | every job of Test and Release except the kind legs, Release Docker & Helm, Renovate |
| wds18-large | `runs-on: large` | 0–6 | 2 / 16 | 8 GiB / 20 GiB | 20 GiB / 30 GiB | the kind legs `e2e` (push, access-long) and `e2e-npm` of Test and Release; `e2e` (failover, drain-roles), `e2e-npm` (npm-ha, ha-suite), `npm-ha-soak`, `e2e-oci` (oci, oci-closing, oci-long), `e2e-nuget` (nuget-ha, [Q-HA-102](../spec/ha-and-helm.md#q-ha-102--the-cpu-of-the-nuget-drain-and-where-its-leg-runs)), `smoke-published` of Release Docker & Helm ([the post-release check](#the-post-release-check)), `e2e-mirror` (the sets mirror-long and mirror, [Q-REPO-211](../spec/repositories-and-proxy.md#q-repo-211--the-nightly-install-time-ratio-of-clients-first-runs-on-the-large-class)) and, after all of them, `latency` and `vuln-perf` ([Q-ARCH-325](../spec/architecture.md#q-arch-325--where-do-the-perf-jobs-run-relative-to-the-kind-legs)) of HA and Performance. The nightly bounds its own large legs to three in three lanes ([Q-ARCH-431](../spec/architecture.md#q-arch-431--how-many-large-legs-of-the-nightly-run-start-at-once-and-in-what-order)); Test and Release starts up to five large legs on a push, unbounded, and is not bounded yet ([Q-ARCH-432](../spec/architecture.md#q-arch-432--does-test-and-release-bound-its-large-legs-the-same-way)) |
| GitHub-hosted | `runs-on: ubuntu-24.04` | GitHub's | 4 vCPU | 16 GB | 108 GB free on `/` after the job deletes the preinstalled SDKs | the kind legs that load the NuGet clients image: `e2e-nuget` of Test and Release, `e2e-ci`, `e2e-bastion`, `e2e-vuln`, in both workflows, and the set `mirror` of `e2e-mirror` in Test and Release; `e2e-upgrade` (HA and Performance only, set `upgrade`, which loads both clients images) |

The measured basis of the nightly assignment ([Q-ARCH-326](../spec/architecture.md#q-arch-326--which-runner-class-runs-which-leg-the-runner-review-of-the-nightly-run)):
the six nightly runs of 2026-11-03 to 2026-11-05 (`gh run view <id> --json jobs`, the `runner_name` and the labels of each job), per leg the class that ran it and the
result. `self` is `self-hosted` (the CPU-limited pods of that time), `hosted` is `ubuntu-24.04`, `large` is `large`; `RED` is a failed job, `–` a leg that
the run did not have. The code changed between the runs (F-43; the drain per role was added in 41902592855), so the table shows what happened, not a cause.

| Leg | 41739280012 (11-03, dispatch) | 41748522448 (11-04 00:40, dispatch) | 41804996351 (11-04 09:08, schedule) | 41902592855 (11-04 20:01, dispatch) | 42002617340 (11-05 09:28, schedule) | 42126196109 (11-05 23:00, dispatch) |
|---|---|---|---|---|---|---|
| `e2e` failover | self ok | self ok | self RED | hosted ok | hosted ok | large ok |
| `e2e` drain-roles | – | – | – | hosted ok | hosted ok | large ok |
| `e2e-npm` npm-ha | self RED | self RED | self RED | hosted RED | hosted RED | large ok |
| `npm-ha-soak` | self ok | self RED | self ok | hosted ok | hosted ok | large ok |
| `e2e-nuget` nuget-ha | self ok | self ok | self ok | hosted ok | hosted ok | hosted ok |
| `e2e-ci` ci-ha | self ok | self ok | self ok | hosted ok | hosted ok | hosted ok |
| `e2e-bastion` bastion | self RED | self RED | self ok | hosted ok | hosted RED (lost, no test run) | hosted ok |
| `e2e-bastion` bastion-long | self ok | self RED | self ok | hosted ok | hosted ok | hosted ok |
| `e2e-vuln` vuln | self ok | self ok | self ok | hosted ok | hosted RED (one `EOF` of the npm test upstream in setup) | hosted ok |
| `e2e-vuln` vuln-long | self RED | self RED | self ok | hosted ok | hosted ok | hosted ok |
| `e2e-mirror` mirror | – | – | – | – | – | hosted ok |
| `e2e-mirror` mirror-long | – | – | – | – | – | large ok |
| `latency` (P-13, P-14) | self ok | self ok | self ok | hosted ok | hosted ok | large ok |
| `vuln-perf` (R-VULN-028) | self RED | self RED | self RED | hosted RED | hosted RED | large RED |

Per class over the six runs: `self` 20 of 33 jobs green, `hosted` 25 of 31, `large` 6 of 7 (its one red job is `vuln-perf`). Reds that no test decided:
the `bastion` of 42002617340 was lost after eleven minutes inside `make e2e SET=bastion`, with no step after it and no log (a runner loss; F-40 in the project plan),
and the `vuln` of 42002617340 failed its first test in the environment's setup, on one `EOF` of `POST /-/stub/publish` to the in-cluster npm test upstream;
the later tests of the set then reported that the environment did not finish. `npm-ha` was red in five of five runs off `large` and green once on `large` (42126196109).

What the assignment rests on, measured on 2026-11-05 on a developer machine (linux/arm64 containers with 6 CPUs, a cold
start, the cgroup's `memory.peak`, page cache included): `lint` 4.7 GiB, `unit` 4.6 GiB, `generate` 3.9 GiB, `web` 3.2 GiB,
`chart` 2.3 GiB, `govulncheck` 2.3 GiB, `build-no-node` 2.1 GiB, `malware-scan` 1.3 GiB; the set `drain-roles` under the npm load
held 9.9 GiB in Docker (kind nodes 7.8 GiB, k6 2.2 GiB) and about 13 GiB of images in the kind nodes. The legs that load
the NuGet clients image (2.3 GB, which `kind load docker-image` copies into each of the four nodes) were evicted on large in
run 42046280787 (`Usage of EmptyDir volume "docker-storage" exceeds the limit "30Gi"`), so they run on GitHub-hosted
runners, where they passed (runs 41894949089, 41902592855); there every such job first deletes the runner's preinstalled
SDKs, without which run 41871333853 ran out of disk. The RAM is
overbooked: the scheduler places a pod by its request, and a job above it (the kind legs, `lint`, `unit`) can be evicted by
the kubelet when several jobs on one node peak at once; such a red job is the node's capacity, not a test. The integration
shards, `compat`, the image builds and the performance jobs were not measured. A job therefore installs what it calls: `make` and the C compiler (`build-essential`), `file`
and `jq` where a step uses them, the Docker Compose plugin for the integration stack, Go from `go.mod`,
Node.js from `web/.nvmrc`, and the pinned helm, kubectl, kind and Docker Buildx; the chart job also fetches promtool
at its pin (`make promtool`, below).

| Job | Make target | Runs |
|---|---|---|
| versions (E0.2) | `make versions-check` | go, node (`web/.nvmrc`), sqlc, oapi-codegen, golangci-lint, oasdiff (when built), promtool (when fetched), kind, kubectl, helm, k6 and docker buildx against their pins. The job installs the pinned helm, kubectl and kind first, so the runner's copies are not the ones checked; buildx comes from [install-buildx](../../.github/actions/install-buildx/action.yml), whose probe keeps the runner's file only when its SHA-256 is the pin, so the buildx checked is the one the CLI resolves |
| Code Linting | `make lint` | golangci-lint (pinned; its binary is cached by version and Go version) with the linters of [.golangci.yml](../../.golangci.yml), then `go vet` |
| GoSec Security Scan | `make gosec` | golangci-lint with only gosec enabled, the settings and the accepted findings of [.golangci.yml](../../.golangci.yml) |
| Vulnerability Check | `make vuln` | govulncheck (pinned) over the module |
| Cyclomatic Complexity | `make cyclo` | gocyclo (pinned), fails above `CYCLO_THRESHOLD`; the job summary lists the 20 most complex functions with `make -s cyclo-report` |
| generate (E0.1) | `make generate-check`, then `make nuget-reference-check` | `go generate`, then `git diff --exit-code` and a check for generated files that are not committed; then the NuGet reference solution of [perf/nuget](../../perf/README.md), regenerated from the counting upstream's package set and compared with the committed files (`go run ./test/nugetstub/cmd/nugetstub reference -check`, [Q-ARCH-279](../spec/architecture.md#q-arch-279--how-are-the-nuget-sdk-pins-and-the-reference-solution-kept-in-step)) |
| nuget vectors (R-NUGET-003) | `make nuget-vectors-check` | regenerates the golden vectors of the NuGet version, range and ID rules with NuGet.Versioning in the pinned .NET SDK image (`NUGETVECTORS_SDK_IMAGE`, `NUGET_VERSIONING_VERSION`) and compares them with the committed file; needs Docker |
| build without Node.js (E0.1) | `make build` | with every Node.js executable off `PATH`; `file` must say *statically linked*, `go version -m` must record `CGO_ENABLED=0` |
| unit tests | `make test` | the race detector; uploads `.out/test-unit.json` (`test-unit`) and `.out/coverage/unit.out` (`profile-unit`) |
| web (Angular) | `make web-ci`, `make web-lint`, `make web-test`, `make web-embed` | `npm ci` (fails when `package.json` and the lockfile disagree), ESLint and the R-UI-046 rules, the Angular unit tests, the production build embedded into `internal/webui/dist`, then `TROVE_UI_BUILT=1 go test -run TestR_UI_001_ThirdPartyNotices ./internal/webui/` on that build (the notices of the bundled software, Q-UI-144) |
| Malware Scan (source code) | `make malware-scan` | ClamAV (`clamscan --recursive --infected`) over the tree, without `.git`, `node_modules` and the build outputs; any infected file fails; uploads `clamav-report` |
| chart (E0.7) | `make chart-lint`, `make chart-test` | `helm lint` and `helm template` over every value set of both charts (`deploy/helm/trove/ci`, `deploy/helm/trove-bastion/ci`, the dev profile), the render assertions of [test/chart](../../test/chart) for both charts, and the `promtool check rules` / `promtool test rules` run over the Bastion's PrometheusRule ([rules_test.yaml](../../deploy/helm/trove-bastion/tests/rules_test.yaml)); `make chart-test` fetches promtool into `.bin` at `PROMTOOL_VERSION` of [tools/versions.env](../../tools/versions.env), checked against the SHA-256 of the platform; uploads `.out/test-chart.json` (`test-chart`) |
| image | `make image IMAGE=trove:ci` | the [Containerfile](../../Containerfile) for the runner's architecture, loaded into Docker; the image must run as `65532:65532` (R-HA-033) |
| SBOM generator (pinned syft, test image) | `make sbom-generator-check` | builds the SBOM generator of the release image and a two-platform test image with it, and asserts that both attestations name the pinned syft; needed by Semantic Release ([below](#sbom-generator)) |
| Container Scan (Trivy, built image) | `make image IMAGE=trove:scan`, `make trivy`, then Trivy | Trivy (pinned `TRIVY_VERSION`) on the built image: fixed OS and library vulnerabilities of CRITICAL or HIGH, secrets and misconfigurations; SARIF uploaded as `trivy-container-report` |
| integration (Valkey 9.1, shard a, b, c) | `make integration-up`, `make test-integration SHARD=<a\|b\|c>`, `make integration-down` | the integration tier against the compose stack on TLS, on Valkey 9.1 only (no older line is guaranteed while trove is in development); three legs with a stack each, shard `a` the packages `ci`, `coordination`, `mirror`, `npm`, `nuget`, `repository` and `proxy`, shard `c` the `oci*`, `deb*`, `registry`, `itempublish`, `vuln*` and `bastion*` packages and the OCI conformance suite, shard `b` every other one; uploads `test-integration-<shard>` and `profile-integration-<shard>` |
| S3 conformance (MinIO) | `make minio-up`, `make s3-conformance`, `make minio-down` | the conformance tests of R-STOR-035 against MinIO, built from source at `MINIO_COMMIT` and served over TLS only; the server log is uploaded on failure |
| e2e-npm on kind (npm) | `make image VERSION=dev`, `make dev-up`, `make e2e SET=npm`, `make dev-down` | the npm acceptance matrix (eight real clients, [R-NPM-045](../spec/formats/npm.md#r-npm-045--client-acceptance-matrix)) and E2.2 to E2.12 on the scatter profile with the counting test upstream; gates every change, own cluster name and edge port (8082). A dispatch with `closing=true` adds the leg `npm-closing` (port 8083): the matrix and the cold install with Valkey stopped, and the read-only subset against the public registry ([Q-NPM-46](../spec/formats/npm.md#q-npm-46--what-marks-the-run-that-closes-the-phase)); the legs `npm-ha` and `npm-ha-crash` left this job on 2026-11-01 (owner): `npm-ha` runs nightly and on dispatch in [HA and Performance (nightly)](#ha-and-performance-nightly) (port 8087), `npm-ha-crash` (three force-deletes) runs by hand only; for them the step budget is 80 minutes and the job 120, and they are E2.11: drain variant A, the PostgreSQL switchover and the Valkey failover under the npm load, and the pod crash with three force-deletes ([e2e.md](e2e.md#drain-and-failover-under-npm-load)); a closing dispatch runs both as one leg `npm-ha-closing` with the 20-minute pod crash, the R-ARCH-027 report and the 5-minute Valkey outage ([Q-HA-67](../spec/ha-and-helm.md#q-ha-67--where-and-how-often-the-scenarios-under-npm-load-run)); uploads `e2e-npm-report-<set>`, `e2e-npm-artifacts-<set>` and on failure the cluster state |
| fuzz npm (closing run only) | `make fuzz-npm FUZZ=<target> FUZZTIME=30m` | three legs (`FuzzR_ARCH_028_Match`, `FuzzR_ARCH_028_Inspect`, `FuzzMatch`) of 30 minutes each; runs only on a dispatch with `closing=true`; uploads the failing input |
| fuzz nuget (closing run only) | `make fuzz-nuget NUGET_FUZZ_TARGETS=<pkg>:<target> NUGET_FUZZTIME=30m` | six legs of 30 minutes, one per target of `NUGET_FUZZ_TARGETS`: the readers `nupkg:FuzzR_ARCH_028_Inspect` and `nuspec:FuzzR_NUGET_005_Nuspec`, and the version and ID rules `version:FuzzNormalize`, `version:FuzzCompare`, `version:FuzzRange`, `id:FuzzID`; they run side by side ([Q-ARCH-278](../spec/architecture.md#q-arch-278--how-is-the-nuget-fuzz-run-of-the-closing-run-laid-out)); runs only on a dispatch with `closing=true`; uploads `fuzz-nuget-<pkg>-<target>` with the failing input |
| e2e-nuget on kind (nuget) | `make nuget-clients-image`, `make image VERSION=dev`, `make dev-up`, `make e2e SET=nuget`, `make dev-down` | the NuGet acceptance suite (R-NUGET-027, E3.1 to E3.8 as they land): the .NET 10 SDK and nuget.exe on Mono in the client pods, the scatter profile and the counting NuGet test upstream; gates every change, own cluster name and edge port (8088). The clients image is built by its own step and not again by the set (`TROVE_E2E_NUGET_CLIENTS_IMAGE_BUILD=0`). A dispatch with `closing=true` adds the leg `nuget-closing` (port 8089): the tests named `TestNuGetClosing...`, the additions of the closing run. The nightly leg `nuget-ha` (port 8090) is in [HA and Performance (nightly)](#ha-and-performance-nightly). A set whose expression names no test yet passes with a notice, as `nuget-closing` and `nuget-ha` do today ([Q-ARCH-276](../spec/architecture.md#q-arch-276--how-does-an-e2e-set-that-names-no-test-pass-and-when-does-it-fail)); uploads `e2e-nuget-report-<set>`, `e2e-nuget-artifacts-<set>` and on failure the cluster state ([e2e.md](e2e.md#the-nuget-clients)) |
| e2e-oci on kind (oci) | `make oci-clients-image`, `make image VERSION=dev`, `make dev-up`, `make e2e SET=oci`, `make dev-down` | the OCI acceptance suite (R-OCI-035, T-16 WP11): the Docker Engine with Buildx and Podman in privileged client pods of the namespace `trove-e2e-clients`, the scatter profile and the counting OCI test upstream ([the OCI tests](e2e.md#the-oci-tests)); runs when the change touches the registry (the scope step lists the paths; a closing dispatch always runs it and adds the leg `oci-closing`), on a large runner, own cluster name and edge ports (8104, 8105). The two clients images are built by their own step and not again by the set (`TROVE_E2E_OCI_CLIENTS_IMAGE_BUILD=0`). The nightly leg `oci-long` (edge port 8106) is job `e2e-oci` in performance.yml: a push cut by the drain of the receiving pod |
| e2e-ci on kind (ci) | `make npm-clients-image`, `make nuget-clients-image`, `make image VERSION=dev`, `make dev-up`, `make e2e SET=ci`, `make dev-down` | the reference pipeline of GitLab CI OIDC (R-CI-028, E4.1 and E4.4 to E4.6): the steps of the Examples with the real npm and .NET clients in the client pods against the issuer double, the scatter profile; gates every change, own cluster name and edge port (8091). Both clients images are built by their own steps (`TROVE_E2E_NPM_CLIENTS_IMAGE_BUILD=0`, `TROVE_E2E_NUGET_CLIENTS_IMAGE_BUILD=0`). The long legs are the set `ci-ha` of [HA and Performance (nightly)](#ha-and-performance-nightly); uploads `e2e-ci-report-ci`, `e2e-ci-artifacts-ci` (with the job logs `ci-job-*.log`) and on failure the cluster state ([e2e.md](e2e.md#the-ci-tests)) |
| e2e-vuln on kind (vuln) | `make npm-clients-image`, `make nuget-clients-image`, `make image VERSION=dev`, `make dev-up`, `make e2e SET=vuln`, `make dev-down` | the vulnerability scanning on kind (R-VULN-029, E4b.4 to E4b.6, the short half of E4b.8): the `vuln` profile (three `web`, two `worker` replicas, the scatter edge, the test advisory source), `npm audit`, `pnpm audit`, `yarn npm audit` and the blocked installs of every gate row, `dotnet restore` of the .NET 8, 9 and 10 SDKs, the replica criteria with Valkey stopped and the browser journeys with axe (the job installs Node.js and the browser); runs when the change touches the scanning (a scope step diffs against the base, the job is green after it otherwise, and Semantic Release needs it); own cluster name and edge port (8095). The long legs are the set `vuln-long` of [HA and Performance (nightly)](#ha-and-performance-nightly); uploads `e2e-vuln-report-vuln`, `e2e-vuln-artifacts-vuln`, `e2e-vuln-playwright-vuln` and on failure the cluster state ([e2e.md](e2e.md#the-vulnerability-tests)) |
| e2e-mirror on kind (mirror) | `make npm-clients-image`, `make nuget-clients-image`, `make image VERSION=dev`, `make dev-up`, `make e2e SET=mirror`, `make dev-down` | the remote full mirror on kind (R-REPO-059, E6b.6, R-REPO-055 and R-REPO-058's e2e criteria): the `mirror` profile (three `api` and three `packages` replicas, the scatter edge, the main worker and a worker pool of two processes that claim the queue `mirror` alone), npm and NuGet remotes in mode mirror over the counting test upstreams, `npm ci` of the reference project and `dotnet restore` of the reference solution through the same remotes while a pass runs (the install within 120 % of its time without the pass, the test upstream's open requests within `maxConcurrentFetches + n - 1 + B`), the status from every `api` pod, two processes that dispatch, a pass with Valkey stopped, a restore with the test upstreams stopped, and the browser journey of the Mirror tab with axe (the job installs Node.js and the browser); runs when the change touches the mirror (a scope step diffs against the base, the job is green after it otherwise, and Semantic Release needs it); own cluster name and edge port (8098). The drain is the set `mirror-long` of [HA and Performance (nightly)](#ha-and-performance-nightly); uploads `e2e-mirror-report-mirror`, `e2e-mirror-artifacts-mirror`, `e2e-mirror-playwright-mirror` and on failure the cluster state ([e2e.md](e2e.md#the-mirror-tests)) |
| e2e on kind (push, access-long) | `make image VERSION=dev`, `make dev-up`, `make e2e SET=<set>`, `make dev-down` | the dev profile on a kind cluster, one leg per set, every change: `push` and `access-long` (E1.1, E1.11 with the Playwright journeys, E1.13; the only leg that installs Node.js and the browser). The set `failover` (E0.13, E1.8) left this job on 2026-11-01 (owner) and runs nightly and on dispatch in [HA and Performance (nightly)](#ha-and-performance-nightly); the legs and those of e2e-npm build their own image and wait for no other job; each leg has its own cluster name and edge port; uploads the reports, `e2e-artifacts-<set>`, and on failure the cluster state |
| workflow files (actionlint) | `actionlint` (pinned `ACTIONLINT_VERSION`) | every file of `.github/workflows`, with [.github/actionlint.yaml](../../.github/actionlint.yaml) |
| compatibility (R-ARCH-025, R-ARCH-026) | `make compat-check` | the OpenAPI document against the latest `v0.*` release with the pinned oasdiff (`--fail-on ERR`), the migration files: only additions that continue the numbering, and no change of a migration that `main` has committed (the history of `main`); needs the history and the tags (`fetch-depth: 0`, `fetch-tags: true`); see [Compatibility](#compatibility). Switched off until the first stable release (`if: false`, not among the needs of Semantic Release; [the switches](#switched-off-until-the-first-stable-release)) |
| coverage generator (links and rules) | `go run ./internal/tools/coverage -check`, then `-links` | the generated pages match their rules; every relative link and anchor resolves |
| coverage evidence (implemented.txt) | `go run ./internal/tools/coverage -claims -tests …` over the unit, Angular, chart, integration and kind-leg reports of this run, and `-carried` over the newest report of each kind leg without one (Q-ROAD-11) | [implemented.txt](../coverage/implemented.txt) claims no requirement the reports do not prove; writes the record and the pages rendered from it, lists the requirements proven for the first time in the job summary and uploads the files as `coverage-evidence` for Semantic Release; needs `unit`, `web`, the `integration` shards, `chart` and the kind legs, and holds `actions: read` for the reports of earlier runs; a failing test in such a report withdraws its requirement from the record without failing the job (Q-ROAD-12) |
| Combined Coverage Report | `make coverage-report` | merges the unit and integration profiles; the job summary and the badge `coverage-badge` |
| Release Tooling | `make test-release-tooling` | `npm ci`, `npm audit signatures`, and [hack/verify-release-tooling.mjs](../../hack/verify-release-tooling.mjs), which renders release notes with the installed plugins |
| Semantic Release | – | needs every job above; runs only on a push to `main`; publishes the release ([release flow](#release-flow)) |

A kind leg that fails keeps its cluster state as the artifact `e2e-cluster-state-<set>` (14 days): per namespace the pods, their
descriptions and the events, and the logs of every pod of `trove` and of the edge (`ingress-controller`) whole, with the kubelet's
timestamps, compressed, plus the previous instance of every container that restarted ([deploy/dev/cluster-logs.sh](../../deploy/dev/cluster-logs.sh),
called by every such step). The logs are whole because [kind.yaml](../../deploy/dev/kind.yaml) raises the kubelet's `containerLogMaxSize`
to 200Mi (`kubectl logs` reads only the current file, so the default 10Mi rotation cut the window of a failure out of the log), and the edge writes an
access line per request with HAProxy's termination state ([haproxy-values.yaml](../../deploy/dev/haproxy-values.yaml)), whose flags name the side
that ended a connection. Read a log with `zcat log-<ns>-<pod>.txt.gz`. The size is the logs' size, about a tenth of it compressed; it is bounded by the
run, not by a tail.

The workflows pin their actions by major version tag (`actions/checkout@v7`), the way the
organisation's other repositories do. Two actions are pinned by a release tag instead:
`aquasecurity/trivy-action@v0.36.0` and `renovatebot/github-action@v46.3.10`. Third-party actions that
run with the release secrets are pinned by tag too: the owner decided so on 2026-10-31 after the risk
was named ([ADR-130](../decisions.md#adr-130--the-organisations-ci-and-release-standard)). A tag can be
moved, and a moved tag runs in the job that names it with that job's secrets
([H-325](../security/data-and-secret-flow.md#h-325--a-moved-action-tag-runs-in-a-job-that-holds-release-secrets)).

### Pinned tools in the jobs

A pin in [tools/versions.env](../../tools/versions.env) is the one source for the jobs and for `make versions-check`. Where a
job downloads a pinned archive from a GitHub release, the download can fail on the release host (504 in run 42465102401 on
2026-11-08, verified in its log; run 42464736969 reported the same), so the Docker Buildx of every job is installed by [.github/actions/install-buildx](../../.github/actions/install-buildx/action.yml)
([Q-ARCH-355](../spec/architecture.md#q-arch-355--how-do-the-ci-jobs-get-their-pinned-tools-without-a-live-release-asset-download-per-job)):

1. the file the Docker CLI resolves (`docker info`: the buildx plugin's `Path`), when its version is the pin and its SHA-256 is the
   pinned digest of the platform: nothing is installed;
2. otherwise the archive cached under `.bin/buildx-<pin>/` (the cache key holds the pin and the digest of the platform);
3. otherwise a download of the release asset with [tools/github-download.sh](../../tools/github-download.sh): with the job's token
   (`GITHUB_TOKEN`, sent to github.com only, [Q-ARCH-356](../spec/architecture.md#q-arch-356--how-do-ci-jobs-download-github-release-assets-while-the-release-host-is-degraded)),
   the bounds of the script: a hard budget of 900 s for the whole download (each curl call is limited to the time left; the step has a
   16-minute timeout, and the `versions` and `chart` jobs, which are 20 minutes long, give the download 600 s), up to three rounds for a
   signed redirect that expires, and an attempt that stalls under 50 KiB/s for 30 s is aborted;
4. the archive is used only when its SHA-256 equals `BUILDX_SHA256_LINUX_AMD64` or `_ARM64` of `tools/versions.env`;
5. the plugin is installed into `${DOCKER_CONFIG:-$HOME/.docker}/cli-plugins`, and the CLI is checked to resolve that file.

The step is skipped where the job's setup-buildx step is skipped (the legs of a dispatch, the scope of a release job), and the
composite refuses an empty version.

The `docker/setup-buildx-action` step that follows runs with `cache-binary: false` and without `version`, so it never
downloads or caches the binary. Its cache of the binary is the entry whose paths broke the restore of run 42465102401: the
action stores paths relative to the workspace, so an entry saved in one workspace layout does not extract in another.

Inventory of the downloads of the workflows, as the development agent checked them on 2026-11-08 (the host named in the log
of run 42465102401 where a row says so):

| Tool | Where the job gets it | Host | Handled |
|---|---|---|---|
| Docker Buildx | [install-buildx](../../.github/actions/install-buildx/action.yml), 23 jobs | github.com release asset | runner copy, cache, bounded retry, SHA-256 (this section) |
| Go 1.27.3 | `actions/setup-go@v7`, 43 uses | github.com `actions/go-versions` release asset (run 42465102401: 504 twice, then success) | not yet: [Q-ARCH-355](../spec/architecture.md#q-arch-355--how-do-the-ci-jobs-get-their-pinned-tools-without-a-live-release-asset-download-per-job) names it as the next application |
| Helm v4.3.1 | `azure/setup-helm@v5` | get.helm.sh (run 42465102401) | action's own download; no failure seen |
| kubectl v1.37.2 | `azure/setup-kubectl@v5` | dl.k8s.io (run 42465102401) | action's own download; no failure seen |
| Node.js | `actions/setup-node@v7` | not checked | action's own download |
| promtool, Trivy (`make promtool`, `make trivy`) | the Makefile's rules | github.com release assets | [tools/github-download.sh](../../tools/github-download.sh) with the job's token in CI; SHA-256; retries and stall bound |
| Trivy action, Docker Scout | the actions | not checked | action's own download |
| kind, golangci-lint, govulncheck, gocyclo, oasdiff, osv-scanner | `go install` in the workflows and the Makefile | the Go module proxy | not a release asset |

`make dev-up` downloads no tool: `deploy/dev/lib.sh` `check_tools` checks kind, kubectl and Helm against their pins and prints the
install command when one is missing. The charts of its releases go into `.dev/charts`: `make dev-charts` ([deploy/dev/charts.sh](../../deploy/dev/charts.sh))
fetches the haproxy-ingress and cloudnative-pg archives, which are GitHub release assets, through `chart_release_tgz` (the release URL of the version,
the digest of [tools/versions.env](../../tools/versions.env), and the job's token when `GITHUB_TOKEN` is set). In CI that is its own step, before the
`make dev-up` step, which does not hold the token; `make dev-up` reads the archives from `.dev/charts`, and fetches a missing one itself, without the token.
The others go through `chart_tgz`, which runs `helm pull`
([deploy/dev/lib.sh](../../deploy/dev/lib.sh)). A failed pull is retried, 20 s apart, up to seven attempts and until 600 s have passed since the first
(`CHART_PULL_ATTEMPTS`, `CHART_PULL_SECONDS`); a version the repository does not list is not retried, and release-smoke's wait loop
runs one attempt per round. The deadline is checked between attempts, so an attempt that is running may end after it. The haproxy-ingress and cloudnative-pg chart archives are
GitHub release assets (the chart indexes point there); in run 42465102401 their pulls got 504 (haproxy after about a minute,
cloudnative-pg after about 5 minutes). Their digests are pinned ([Q-ARCH-356](../spec/architecture.md#q-arch-356--how-do-ci-jobs-download-github-release-assets-while-the-release-host-is-degraded),
[H-589](../security/data-and-secret-flow.md#h-589--release-downloads-send-the-job-token-to-githubcom-two-residuals) for the cloudnative-pg one). The cache of those archives in CI is not in place yet
([Q-ARCH-355](../spec/architecture.md#q-arch-355--how-do-the-ci-jobs-get-their-pinned-tools-without-a-live-release-asset-download-per-job)).

### Coverage

The unit tier and each integration line write a profile to `.out/coverage`: `unit.out` and
`integration-<line>.out`, with `-covermode=atomic -coverpkg=./...`, so each counts for every package
it reaches. [`test/coverage/report.sh`](../../test/coverage/report.sh) merges them: a block counts once,
covered when any profile covered it. It writes `coverage.txt` (`go tool cover -func`), `summary.md`
for the job summary, and [`coverage.json`](../../.github/badges/coverage.json) (the README badge; green
from 80 %, brightgreen from 90 %, yellow from 70 %, orange from 60 %, red below). Semantic Release
commits the badge with the release.

The requirement evidence of [docs/coverage](../coverage/README.md) is a different thing: the
`coverage evidence` job writes `implemented.txt`, `checklist.md` and the generated blocks of the coverage README from the
test reports of the run, and Semantic Release commits them with the release, beside the badge. Nobody refreshes them by
hand; the job fails only when the committed record claims a requirement that no passing test of the run names
([coverage.md](coverage.md#how-ci-feeds-it)). A push that makes no release leaves the record as it is until the next release.

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

### SBOM generator

The job `sbom-generator` runs `make sbom-generator-check` ([check.sh](../../tools/sbom-generator/check.sh)) on every push and pull request, and Semantic Release needs it. It builds the
generator of the release image from `SYFT_IMAGE`, builds a two-platform test image with it as the BuildKit SBOM generator, writes the image to a file (nothing is pushed) and
asserts that both SBOM attestations name `syft-${SYFT_VERSION#v}`. It uses a registry container and a builder of its own on the default bridge network and removes them at the end; its test image has a `RUN` step that resolves `proxy.golang.org`, so a builder whose `RUN` steps cannot resolve names fails here and not in a release. A syft bump
that breaks the generator therefore breaks a pull request or push run, never a release ([Q-HA-166](../spec/ha-and-helm.md#q-ha-166--the-sbom-attestation-of-the-image-comes-from-a-generator-that-runs-the-pinned-syft)).
Locally: `make sbom-generator-check` (Docker with buildx and `jq`).

### Lint and GoSec

Both run through the same pinned golangci-lint (`GOLANGCI_LINT_VERSION`), with the linter settings of
[.golangci.yml](../../.golangci.yml). An accepted gosec finding carries its reason in the code
(`//nolint:gosec // reason`) or in the configuration; the standalone gosec binary does not read those
annotations, so the repository runs gosec through golangci-lint and has one gosec version.

### Compatibility

**Switched off until the first stable release** ([Q-ARCH-340](../spec/architecture.md#q-arch-340--no-backward-compatibility-until-the-first-stable-release), `CLAUDE.md`): the job `compat` has `if: false` and is not
among the needs of Semantic Release, so neither a breaking change of `/api/v1` nor an edited migration fails CI. `make compat-check`
and [test/compat](../../test/compat) stay: lint and vet build them with the tag `compat`, Renovate keeps `OASDIFF_VERSION` fresh,
and the target runs by hand. The rest of this section describes the job as it runs once it is switched on
([the switches](#switched-off-until-the-first-stable-release)).

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

### Switched off until the first stable release

trove keeps no backward compatibility until its first stable release ([Q-ARCH-340](../spec/architecture.md#q-arch-340--no-backward-compatibility-until-the-first-stable-release), `CLAUDE.md`). The mechanisms that hold
compatibility are switched off, not removed, so that each comes back with the edit below. Their code and tests stay built,
linted and run: `test/compat` under the tag `compat`, the strict migration rules in `TestParseSteps`.

| Switch | Where | Off now | To switch on |
|---|---|---|---|
| The job `compat` (R-ARCH-025, R-ARCH-026) | [release.yml](../../.github/workflows/release.yml), job `compat` | `if: false` | Delete the line `if: false` and the comment above it that names Q-ARCH-340 |
| `compat` among the needs of the release | [release.yml](../../.github/workflows/release.yml), job `semantic-release` | `compat` is missing from `needs:`, because a skipped need would skip the release | Put `compat` back into `needs:` after `workflows`, and delete the comment above the job that names Q-ARCH-340 |
| actionlint's acceptance of `if: false` | [.github/actionlint.yaml](../../.github/actionlint.yaml) | the message `constant expression "false" in condition` is ignored in `release.yml` | Delete that ignore line and its comment paragraph |
| The expand/contract rules of the migration files (R-HA-018) | [steps.go](../../internal/meta/postgres/steps.go), `EnforceMigrationPhases` | `false`: every header optional, no statement check by phase, `RequiredSchema()` the highest step | Set it to `true`. Every embedded file must then carry one `-- trove:phase` header and no `DROP`, `RENAME` or `SET NOT NULL` in an expand step, or `TestEmbeddedSteps` fails and `RequiredSchema()` panics; `RequiredSchema()` becomes the newest expand step |
| The nightly upgrade set (R-HA-020, R-HA-064) | [performance.yml](../../.github/workflows/performance.yml), job `e2e-upgrade` | runs only on a dispatch with `-f sets=upgrade` | Put `!inputs.sets \|\| ` back in front of the `contains(...)` of its `if:`, and delete the comment that names Q-ARCH-340 here and in the header of the file |

Before the job `compat` is switched on, three things need a decision, because the checks read what happened while they were off:

- The history check reads all of `main`: every migration edited while it was off fails it. Each such commit is named in
  `historicalEdits` of [migrations_history_test.go](../../test/compat/migrations_history_test.go), or the check is anchored at
  the first stable release's tag.
- The baseline of both release checks is the newest `v0.*` tag whose commit is not HEAD's (`releaseTagPattern` in
  [helpers_test.go](../../test/compat/helpers_test.go)). Switched on right after the first stable release, that release is the
  baseline; if it leaves major version 0, the pattern names the new major.
- The checksum message of `verifyApplied` in [migrate.go](../../internal/meta/postgres/migrate.go) names the remedy before the
  first stable release, a new database; with the checks on, the remedy is a new step that fixes forward.

The notes that say "before the first stable release" in R-ARCH-025, R-ARCH-026, R-HA-018 to R-HA-021, R-HA-064,
[database.md](database.md#migrations), [upgrades.md](../manual/admin/upgrades.md#before-the-first-stable-release) and
[limitations.md](../manual/admin/limitations.md) change with the switches, in the same change.

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
`failover` set (job `e2e`, E0.13 and E1.8), the drain per role (job `e2e`, set `drain-roles`, E6a.6, edge port 8097: the evictions run
under the npm load of `npm-ha` too, so the set starts the npm client pods and the counting test upstream,
[Q-HA-94](../spec/ha-and-helm.md#q-ha-94--the-load-of-the-drain-per-role)), the `npm-ha` leg (job `e2e-npm`, E2.11), `npm-ha-soak` and
the `ha-suite`, `ha-faults` and `ha-pooler` legs (job `e2e-npm`, the failure suite under the npm load and its additions of E5.10), the `upgrade` leg (job `e2e-upgrade`, below; switched off in the nightly run until the first stable release, [the switches](#switched-off-until-the-first-stable-release)), the `nuget-ha` leg (job `e2e-nuget`,
Q-ARCH-277, which starts the npm client pods too), and last the perf jobs `latency` and `vuln-perf`, which start after every kind leg
([Q-ARCH-325](../spec/architecture.md#q-arch-325--where-do-the-perf-jobs-run-relative-to-the-kind-legs)). Dispatch it with `gh workflow run "HA and Performance (nightly)" --ref main`, or one leg of it with `-f sets=` ([below](#dispatching-one-leg)).
Locally, from the repository root, after `make image VERSION=dev` and `make dev-up`: `make e2e SET=failover`, `make e2e SET=drain-roles`, `make e2e SET=npm-ha`,
`make e2e SET=npm-ha-crash` (the three-force-delete crash, which has no CI job), `make e2e SET=npm-ha-soak`, `make e2e SET=nuget-ha`, `make e2e SET=ha-suite`, `make e2e SET=ha-faults`, `make e2e SET=ha-pooler` or `make e2e SET=upgrade`,
then `make dev-down`; the latency tests need `make integration-up && make test-performance`.
A red long leg does not hold the release: it is recorded as an open finding instead (owner, 2026-11-01).

#### Dispatching a branch

`gh workflow run "HA and Performance (nightly)" --ref <branch> -f sets=<set>` runs the workflow file of the branch, and the run is in
the concurrency group of its ref (`${{ github.workflow }}-${{ github.ref }}`, [Q-ARCH-433](../spec/architecture.md#q-arch-433--is-the-concurrency-of-the-nightly-run-per-workflow-or-per-ref)),
so it runs beside main's run instead of replacing main's pending run. Each run bounds its own large legs to three ([Q-ARCH-431](../spec/architecture.md#q-arch-431--how-many-large-legs-of-the-nightly-run-start-at-once-and-in-what-order)):
a branch dispatch adds its own large legs to those of a run on main, and the development agent coordinates the dispatches it starts. Not verified on GitHub yet
that a branch run starts beside a run on main.

#### Dispatching one leg

The input `sets` of a dispatch is a comma-separated list of set names or job keys, without spaces: `gh workflow run "HA and Performance (nightly)" --ref main -f sets=upgrade,ha-suite`
runs the leg `upgrade` and the leg `ha-suite` and no other. A job key selects every leg of its job (`-f sets=e2e-npm` runs `npm-ha` and `ha-suite`), and the perf jobs are
selected by their keys `latency` and `vuln-perf`. An empty list runs everything, as the schedule does, except the leg `upgrade`, which runs only when the
list names it until the first stable release ([the switches](#switched-off-until-the-first-stable-release)); the perf jobs, which need it, run all the same,
because their condition starts with `!cancelled()`. A matrix leg that the list does not name shows as green with its
steps skipped, and its job summary says so; a job without a matrix that the list does not name is skipped. A list with a space fails every matrix leg with an error.
The composite action [select-leg](../../.github/actions/select-leg/action.yml) decides for a matrix leg ([Q-ARCH-324](../spec/architecture.md#q-arch-324--how-is-one-leg-of-the-nightly-run-dispatched)).
The input exists on `main` once the change is merged, because `gh` runs the workflow file of the ref.

#### Order and wall time

The large legs run in three lanes, so at most three of them are active at once, one per worker node of the class
([Q-ARCH-431](../spec/architecture.md#q-arch-431--how-many-large-legs-of-the-nightly-run-start-at-once-and-in-what-order)):
lane 1 is `npm-ha`, `ha-suite`, `ha-faults` then `ha-pooler` (job `e2e-npm`, one leg at a time, [Q-HA-250](../spec/ha-and-helm.md#q-ha-250--where-the-scenarios-of-e510-run-and-in-which-lane)) then `nuget-ha` and `cold-crash` (job `e2e-nuget`, one leg at a time); lane 2 is `drain-roles` then
`failover` (job `e2e`) then `npm-ha-soak`; lane 3 is the job `e2e-vuln` (its `vuln-pooler` leg; its GitHub-hosted legs run beside the lanes), then
`backup`, then `mirror` and `mirror-long` (job `e2e-mirror`, one at a time), then `oci-long`. Each lane job waits for its predecessor with `!cancelled()`,
so an unselected or failed leg does not stop its lane. The estimate, from the durations of run 42571414393 (measured under six-way contention): the lanes take
about 87, 82 and 86 minutes, against 58 minutes for the kind legs before the change. Not measured yet: the wall time in this order.
The perf jobs start after all of the kind legs have finished: `latency` (P-13 and P-14) after the nine kind jobs, and `vuln-perf` after
`latency` ([Q-ARCH-325](../spec/architecture.md#q-arch-325--where-do-the-perf-jobs-run-relative-to-the-kind-legs)). So neither perf job shares the large VM with a kind cluster of this
workflow or with the other perf job (a Test and Release run that starts meanwhile can still share it). The cost is wall time: in run 42126196109 the two jobs took 5 min 17 s and 7 min 22 s, so a nightly ends about 13 minutes after its longest kind
leg. Not measured yet: the wall time of a whole nightly in this order, and the V-4 and V-7b results under it.

The longest kind leg is `e2e-bastion (bastion-long)`: the Bastion's drain, its floods and its failure suite (a replica lost on either backend, the main installation failing over, a rolling upgrade of either side; [Q-BAS-142](../spec/bastion.md#q-bas-142--the-failure-suite-runs-one-load-with-a-miss-loop-and-one-fault-after-the-other)) take about 80 minutes of tests, so its `make e2e` step has 120 minutes and `TROVE_E2E_TIMEOUT=110m`, and the job 180 minutes; the other legs keep 80 and 90.

[`performance.yml`](../../.github/workflows/performance.yml) ("HA and Performance (nightly)") runs every night at 02:17 UTC and on demand
(`workflow_dispatch`, with the input `sets`, [above](#dispatching-one-leg)), on the large runners and, for the legs that load the NuGet clients image, on GitHub-hosted runners ([CI](#ci)). It brings up the integration stack (`make integration-up`), runs
`make test-performance` and stops the stack. That target runs `TestChangePropagation`, `TestJobPickup`, `TestR_NPM_010_ThousandVersions` and
`TestIdempotentCreate` and `TestR_DEB_010_DelayBound` (the reference load of 10,000 package files and ten uploads per second, with and without Valkey, p99 of the delay of a build, Q-DEB-59) with `TROVE_PERF_GATE=1`, which enforces the p99 bounds of P-13 (100 ms) and P-14 (1 s) with Valkey available, R-NPM-010's
200 ms for a miss, 10 ms for a cached hit and 50 ms for a `304` of a document of 1,000 versions ([Q-ARCH-293](../spec/architecture.md#q-arch-293--where-a-wall-clock-target-of-a-functional-test-is-enforced)) and R-API-014's 1.5 s for a concurrent duplicate; it then runs `TestR_BAS_026_AFeedPageOfAThousandEntriesIsAppliedAgainstTwoHundredThousandAnswersWithinTheBound` of `internal/bastion/engine` with the gate (the 5 s of a burst page, R-BAS-026; per change the page is bounded by 100 matching passes over the same answers, [Q-BAS-140](../spec/bastion.md#q-bas-140--where-the-5-s-of-a-burst-page-is-enforced)) and `TestR_EP_013_CeilingPerformance` of `internal/authz` the same way
(the 1 ms p99 of one decision with 1,000 exposure rules, in the CPU time of the deciding thread, [Q-ARCH-250](../spec/architecture.md#q-arch-250--a-p99-bound-in-microseconds-is-a-nightly-gate-and-the-per-change-test-asserts-allocations-and-the-median)); the per-change integration job asserts delivery, the p50 and looser bounds only
([Q-ARCH-220](../spec/architecture.md#q-arch-220--p-13-starts-its-clock-at-the-commit-and-its-p99-is-enforced-in-the-nightly-performance-run)).
The report `test-performance` is kept for 30 days. Beside it, the job `npm-ha-soak` runs `make image VERSION=dev`, `make dev-up` and
`make e2e SET=npm-ha-soak` on a kind cluster: R-HA-052's pod crash with ten force-deletes over 20 minutes under the npm load, then the npm
load scripts of R-ARCH-027 against one replica (`TestNPMPerf`, reported, not gated); the three-force-delete crash runs by hand only, and the closing run runs the ten force-deletes
in its leg `npm-ha-closing` ([Q-HA-67](../spec/ha-and-helm.md#q-ha-67--where-and-how-often-the-scenarios-under-npm-load-run)). Its reports
`e2e-npm-report-npm-ha-soak` and `e2e-npm-artifacts-npm-ha-soak` are kept for 30 days. A failed night is not wired to an issue or a
notification, except the job `vuln-perf` (below): look at the Actions list. The other targets of R-ARCH-027 (`perf/`) are not part of this workflow.

Job `vuln-perf` runs, after `latency` and every kind leg ([Q-ARCH-325](../spec/architecture.md#q-arch-325--where-do-the-perf-jobs-run-relative-to-the-kind-legs)), the performance run of the vulnerability scanning (R-VULN-028, scenarios V-1 to V-9, [perf/vuln](../../perf/vuln)) against the
integration stack with `TROVE_PERF_GATE=1`: it seeds 100,000 stored versions in SQL, imports the 231,000-record npm dump of the test advisory source,
evaluates, and measures; it uploads `perf-vuln.json`, the `go test -json` stream and, when the run failed, the replicas' whole logs
(`perf-vuln-logs/`) as `vuln-perf` (30 days) and, when a scenario is above its target or the run fails, opens an issue that lists the misses
(the job has `issues: write`, no other job has). A scenario that did not finish is listed as `not finished`, and a scenario that needs it
as `not measured` (V-2 and V-9 need V-1, V-3 to V-8 need V-1 and V-2; [perf/README.md](../../perf/README.md#reading-a-run)). It is the only job of the workflow that
opens an issue ([Q-VULN-57](../spec/vulnerability-scanning.md#q-vuln-57--the-performance-run-is-a-test-package-on-the-integration-stack-gated-and-reported-by-the-nightly-workflow)).

Job `e2e-nuget` also runs the leg `cold-crash` (`make e2e SET=cold-crash`, edge port 8110, cluster `trove-cold-crash-<run id>`, after `nuget-ha`, `max-parallel: 1`): `TestColdCrash`, a packages replica force-deleted during a cold `npm ci` and a cold `dotnet restore` of three clients each, then `trove admin storage verify` (E5.2, [Q-HA-220](../spec/ha-and-helm.md#q-ha-220--the-cold-install-crash-of-exit-criterion-e52-kills-one-packages-replica-once-the-upstream-has-served-a-share-of-the-packages-and-the-clients-go-through-the-edge)); it needs the same two clients images and runs under the same budgets.

Job `e2e-nuget` runs `make e2e SET=nuget-ha` on a kind cluster (edge port 8090, cluster `trove-nuget-ha-<run id>`, reports
`e2e-nuget-report-nuget-ha` and `e2e-nuget-artifacts-nuget-ha`, kept 14 days): `TestNuGetHA`, drain variant A, the PostgreSQL switchover
and the Valkey failover under the npm and NuGet load (R-HA-059, R-ARCH-024). It runs on the large class, because the load needs about 16 cores
on the dev cluster ([Q-HA-102](../spec/ha-and-helm.md#q-ha-102--the-cpu-of-the-nuget-drain-and-where-its-leg-runs)); the set loads the
clients images into the control-plane node alone, so the 2.3 GB NuGet image no longer fills the Docker storage of a large pod four times over.
The job builds both clients images in its own steps, `make npm-clients-image` and `make nuget-clients-image`, and the set loads them, because the set starts the npm
client pods too ([Q-ARCH-327](../spec/architecture.md#q-arch-327--how-do-the-legs-that-load-both-client-images-get-them-and-who-builds-them)). The nightly run therefore
costs a cluster bring-up and proves no NuGet behavior under load until the drain and failover tests land.

Job `e2e-upgrade` runs only on a dispatch with `-f sets=upgrade` until the first stable release ([Q-ARCH-340](../spec/architecture.md#q-arch-340--no-backward-compatibility-until-the-first-stable-release)):
the set upgrades the latest release to the commit, which fails once the commit changes a migration the release applied. It runs `make e2e SET=upgrade` on a kind cluster (edge port 8101, cluster `trove-upgrade-<run id>`, reports and artifacts `e2e-upgrade-report-upgrade`,
`e2e-upgrade-artifacts-upgrade` and `e2e-upgrade-playwright-upgrade`, kept 14 days; on failure `e2e-upgrade-cluster-state-upgrade`). The set installs published releases
of the chart from <https://guided-traffic.github.io/trove/> and their images from Docker Hub, loads them into the cluster, upgrades to the image built from the commit, and
runs the npm and NuGet client loops and a browser journey. The job runs on a GitHub-hosted runner with the disk space freed, because it loads both clients images; its
budgets are 120 minutes for the job and 90 for the set. Not verified on this commit: the set is not in [deploy/dev/e2e.sh](../../deploy/dev/e2e.sh) yet, so the leg fails at
`make e2e` until the change that defines it lands.

Set `ha-suite` (job `e2e-npm`, edge port 8100, the budgets of `npm-ha`: 120 minutes for the job, 80 for the set) runs the failure suite under the npm load: the S3 outage,
the loss of a kind worker node with `docker stop`, the loss of the PostgreSQL and of the Valkey primary, and the npm-audit drain. Not verified on this commit: the set is not
in [deploy/dev/e2e.sh](../../deploy/dev/e2e.sh) yet, so its scenarios are the ones the plan names, not the ones the code runs.

Sets `ha-faults` (edge port 8112) and `ha-pooler` (edge port 8111, the pooler profile through `TROVE_POOLER=1`) are legs of the same job with the same budgets: the database at 0 instances for 2 minutes, the Sentinel quorum loss, `FLUSHALL` and a slow Valkey and a Valkey credential mismatch; and a pooler instance deleted, gracefully and forced. [The failure suite ha-faults](e2e.md#the-failure-suite-ha-faults) describes them.

Job `e2e-ci` runs `make e2e SET=ci-ha` on a kind cluster

Job `e2e-ci` runs `make e2e SET=ci-ha` on a kind cluster (edge port 8092, cluster `trove-ci-ha-<run id>`, reports `e2e-ci-report-ci-ha` and
`e2e-ci-artifacts-ci-ha`, kept 14 days): E4.7, the reference pipeline while the node of a `web` replica is drained and again with Valkey
stopped (every replica ready, `"degraded":["valkey"]`), single-use exchanges across a forced Sentinel failover and `FLUSHALL`, and a `web`
pod deleted gracefully under 50 exchanging jobs ([e2e.md](e2e.md#the-ci-tests)). By hand: `make dev-up && make e2e SET=ci-ha`.

Job `e2e-vuln` runs the sets `vuln`, `vuln-long` and `vuln-pooler` (edge ports 8095, 8096 and 8103, clusters `trove-vuln-<run id>`, `trove-vuln-long-<run id>` and `trove-vuln-pooler-<run id>`, reports and
artifacts `e2e-vuln-report-<set>`, `e2e-vuln-artifacts-<set>`, `e2e-vuln-playwright-<set>`, kept 14 days): the short set as in Test and Release, and the long
set, R-VULN-029's drain and E4b.8: a full synchronization of 100,000 npm records drained mid-run (`kubectl drain` of the node that runs the job: it is `queued`
again with its attempt unchanged and another `worker` completes it from its checkpoint), and the client tests once more with Valkey stopped, and E5.13's scanning under failures: a worker killed in the middle of a full synchronization, a PostgreSQL switchover during an evaluation, four blocked versions probed through a node drain (variant A) and the Valkey and PostgreSQL failovers and through drain variants B, C and E of R-HA-059, and the restore drill of R-VULN-026 (a backup of an installation with blocked, dismissed and malicious findings restored into the namespace `trove-restore`, which reads the test advisory source and runs two `packages` replicas, judged before and after its first synchronization). `vuln` and `vuln-long` run on a GitHub-hosted runner (the NuGet clients image, Q-ARCH-326), `vuln-pooler` on a `large` one: it starts no client pods and installs no Node.js. `vuln-pooler` is the block of the same four versions through drain variant D, on the cluster with the poolers of R-HA-058 (`TROVE_POOLER=1`, which `e2e.sh` exports for the set). A red long leg is
a finding and holds nothing ([e2e.md](e2e.md#the-vulnerability-tests)). By hand: `make dev-up && make e2e SET=vuln-long` (and `SET=vuln-pooler`; the cluster stays in the pooler overlay until the next `make dev-up`).

Job `e2e-backup` runs the set `backup` (edge port 8102, cluster `trove-backup-<run id>`, reports and artifacts `e2e-backup-report` (`.out/test-e2e.json`, the set `backup`), `e2e-backup-pitr-report` (`.out/test-e2e-backup-pitr.json`, the set `backup-pitr` that follows it as a step of the job, [the point-in-time drill](e2e.md#the-point-in-time-restore-drill)) and `e2e-backup-artifacts`, kept 14 days, on a `large` runner):
R-BAK-029 and the drill of R-HA-052 on a kind cluster in the profile `backup`: a backup taken while a load publishes, deletes, trashes and collects with a grace period of 0s, restored with the restore Job
into the namespace `trove-restore` (a database, a Valkey and a bucket of their own), two overlapping Jobs, a node drain and a `kill -9` of a backup and of a restore, and the revocation journal across a restore, with E5.12's CI bullets (the leg starts the issuer double, `TROVE_E2E_OIDCSTUB=1`: a disabled CI mapping and a single-use subject token across the restore)
(E5.1, E5.11, E5.12). It takes about 25 minutes and is a long leg: nightly and on dispatch, not per change (owner, 2026-11-01; Q-HA-154). The artifact `e2e-backup-artifacts` holds `backup-timings.json`, the timings of
scenario 9. By hand: `make dev-up && make e2e SET=backup` ([e2e.md](e2e.md#the-backup-tests)).

Job `e2e-mirror` runs the sets `mirror` and `mirror-long` (edge ports 8098 and 8099, clusters `trove-mirror-<run id>` and `trove-mirror-long-<run id>`, reports and
artifacts `e2e-mirror-report-<set>`, `e2e-mirror-artifacts-<set>`, `e2e-mirror-playwright-<set>`, kept 14 days): the short set as in Test and Release, with the install-time ratio of R-REPO-055 enforced (on a `large` runner since
Q-REPO-211: on the 4 vCPUs of a GitHub-hosted runner the pass's own work took the installs' CPU), and the long set (on a `large` runner), R-REPO-058's drain: `kubectl drain` of the node of the process of the
pool `mirror` that runs a pass of 300 packages (the job is `queued` again with its attempt unchanged, the other process of the pool ends the pass, and the test upstream served every
tarball, `concurrency` of them twice at most), followed by the failure suite of R-HA-052 on passes of 100 packages (the process of the pass force-deleted, its node stopped and frozen, the upstream throttling across a drain and a kill, a PostgreSQL switchover and a Valkey failover; about 40 minutes more). A red long leg is a finding and holds nothing ([e2e.md](e2e.md#the-mirror-tests)). By hand: `make dev-up && make e2e SET=mirror-long`.

#### The platform matrix

Job `k8s-matrix` of the nightly is the install half of R-HA-049's acceptance ([Q-HA-274](../spec/ha-and-helm.md#q-ha-274--the-platform-matrix-is-a-light-kind-leg-per-kubernetes-version-and-helm-major-that-installs-the-chart-without-starting-its-pods)):
four legs on a GitHub-hosted runner (`ubuntu-24.04`), `k8s-min-helm4`, `k8s-min-helm3`, `k8s-max-helm4` and `k8s-max-helm3`, each a one-node kind cluster of its own that
[deploy/dev/k8s-matrix.sh](../../deploy/dev/k8s-matrix.sh) creates and removes. The oldest Kubernetes is `K8S_MIN_KIND_NODE_IMAGE` (1.34), the newest `DEV_KIND_NODE_IMAGE`
(1.37, the dev profile's node), Helm 4 is `HELM_VERSION` and Helm 3 `HELM3_VERSION` ([tools/versions.env](../../tools/versions.env); Renovate keeps the minimum on 1.34 and Helm 3 on 3.x).
A leg checks that the API server and the helm are the versions the leg names, lints the chart, has the API server admit six sets of values (`helm install --dry-run=server`: the roles and pools,
the three sizing profiles of R-HA-050, the backup), installs the medium profile with the backup on without waiting (the migration hook is off, no database or image is there and no pod starts),
reads back the fields R-HA-049 names as the reason for Kubernetes 1.31 (PDB `unhealthyPodEvictionPolicy`, `nodeTaintsPolicy`, CronJob `timeZone`, the HPA), upgrades to the small profile
and uninstalls. `gh workflow run "HA and Performance (nightly)" --ref main -f sets=k8s-matrix` runs the four legs, `-f sets=k8s-min-helm3` one. By hand, with the pinned `kind`, `kubectl` and a Helm of the major asked for:
`deploy/dev/k8s-matrix.sh "$K8S_MIN_KIND_NODE_IMAGE" 3`. **Not covered:** no pod starts, so the smoke subset of R-HA-052 does not run on these legs; Kubernetes 1.37 with Helm 4 is the only pair the
failure suite runs on ([H-805](../spec/ha-and-helm.md#h-805--the-platform-matrix-installs-the-chart-and-starts-no-pod)).

### Renovate

[`renovate.yml`](../../.github/workflows/renovate.yml) runs self-hosted Renovate every day at 02:00
Berlin time, with an installation token of the organisation's GitHub App (secrets `APP_CLIENT_ID` and
`APP_PRIVATE_KEY`). The configuration is [renovate.json](../../renovate.json).

Every Renovate branch runs the whole Test and Release pipeline on the organisation's shared runners ([CI](#ci)), so the
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
- **A commit was pushed while the run was in progress, with `[skip ci]` in its message:** that push starts
  no run and cancels none, so the running run passes, but Semantic Release then finds `main` ahead of its
  commit ("The local branch main is behind the remote one") and publishes nothing. GitHub reads the marker
  anywhere in the message. A dispatched run (`gh run … workflow_dispatch`) does not release either: the job
  runs on a push only. Push a new commit without the marker (an empty `ci:` commit when nothing else is due).
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
| Build Docker Image | `self-hosted` ([CI](#ci)) | [docker/build-push-action](https://github.com/docker/build-push-action) builds [Containerfile](../../Containerfile) for `linux/amd64,linux/arm64` and pushes `guidedtraffic/trove` to Docker Hub with the tags of [docker/metadata-action](https://github.com/docker/metadata-action); provenance and SBOM attestations (the SBOM from the generator image of [tools/sbom-generator](../../tools/sbom-generator), built in this job from `SYFT_IMAGE` and served by digest from a throwaway registry container on the default bridge network (reached by its IP; the buildx builder stays on that network too), [Q-HA-166](../spec/ha-and-helm.md#q-ha-166--the-sbom-attestation-of-the-image-comes-from-a-generator-that-runs-the-pinned-syft); the image is pushed by digest without a tag, a step asserts that both platforms' attestations name the pinned syft, and only then `docker buildx imagetools create` adds the tags); the build arguments are the pins (`NODE_VERSION` from [web/.nvmrc](../../web/.nvmrc), `GO_VERSION` from [go.mod](../../go.mod), `RUNTIME_BASE_IMAGE` from [tools/versions.env](../../tools/versions.env)) and the version, the commit and the commit's date for the binary's version line |
| Scan and attach the SBOM | same | the SPDX SBOM written by the image `SYFT_IMAGE` on the release tag (`docker run`, no download from GitHub, [Q-HA-204](../spec/ha-and-helm.md#q-ha-204--the-spdx-file-of-the-release-is-written-by-the-pinned-syft-image-from-the-registry-tag)), uploaded to the release; a Docker Scout CVE report |
| Release Helm Chart to GitHub Pages | same | `deploy/helm/trove` and `deploy/helm/trove-bastion` with `version` and `appVersion` set to the release (and the image tag in the `values.yaml` of `trove`), packaged together with one `helm package`, added to the chart repository's `index.yaml` on the `gh-pages` branch in one push, and `trove-<version>.tgz`, `trove-bastion-<version>.tgz` and `index.yaml` attached to the release |
| Smoke the published release (E5.8) | `large` ([CI](#ci)) | after both jobs above; [below](#the-post-release-check) |

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

**One syft.** `SYFT_VERSION` in [tools/versions.env](../../tools/versions.env) writes both SBOMs of the image: the SPDX file of the release
(the image `SYFT_IMAGE`, run on the registry tag, [Q-HA-204](../spec/ha-and-helm.md#q-ha-204--the-spdx-file-of-the-release-is-written-by-the-pinned-syft-image-from-the-registry-tag)) and the attestation in the image index (the generator of `tools/sbom-generator`, whose
[Containerfile](../../tools/sbom-generator/Containerfile) takes the syft binary from `SYFT_IMAGE`, the same version by digest, and has no version of its own). The scanner that
BuildKit bundles, `docker/buildkit-syft-scanner`, ships an older syft (v1.51.0 in its newest release, 1.12.1, checked on 2026-11-07), which is why it is not used.
`make versions-check` runs [check-pins.sh](../../tools/sbom-generator/check-pins.sh) and fails on a `SYFT_IMAGE` that is not `SYFT_VERSION` by digest, on a default or a
syft image of its own anywhere else in the repository, on a build.yml that reads another version or sets `sbom` to anything but the generator, and on a `syft` on `PATH` that
is not the pin. The generator is served from a registry container of the run (`trove-sbom-registry-<run id>-<attempt>`) on the default bridge network and reached by its IP, with plain HTTP allowed for that address only; the buildx builder takes no network option, because a builder on a user-defined network cannot resolve names in its `RUN` steps (release v0.25.1, run 42433259845) and `network=host` would put them into the runner's namespace. The image is pushed by digest without a tag, and a step runs
[creators.sh](../../tools/sbom-generator/creators.sh) on that digest before `imagetools create` adds the tags (version, major.minor, major, sha, and `latest` on main; the attestations are part of the index and stay); a failure moves no tag, publishes no chart and
attaches no SPDX file, and leaves an untagged image by digest in the repository ([Q-HA-166](../spec/ha-and-helm.md#q-ha-166--the-sbom-attestation-of-the-image-comes-from-a-generator-that-runs-the-pinned-syft)). What keeps that from
happening on a release is the job `sbom-generator` of Test and Release ([below](#sbom-generator)). The `build` job itself first runs on the next release: if its generator step
fails, the release stops before the chart is published, and `gh run rerun <id> --failed` runs it again once the cause is fixed.

Not in the pipeline: a chart SBOM, a chart attestation, and any signature. Only the image carries
attestations: a provenance and an SBOM attestation in its image index.

## The post-release check

The job `smoke-published` of `build.yml` (E5.8, R-HA-047) takes the artifacts as an installer gets them and runs the scenarios of
[Q-HA-165](../spec/ha-and-helm.md#q-ha-165--the-smoke-scenarios-of-e58-are-the-e21-matrix-and-the-client-smoke-test) against them. It runs after `build` and
`release-helm-gh` succeeded, on `large` (a kind leg), up to 120 minutes. Its steps are named by the case they decide:

| Step | Cases that make it red |
|---|---|
| Name the release to check | the version is not `x.y.z` (a dispatch input) |
| Verify the published artifacts | `release not found` (no GitHub release for the version); `chart not published` (the chart index does not list the version after 20 minutes: the release job or GitHub Pages did not publish it); `chart package differs from the index`; `image not published`; `attestation missing` (no provenance or no SPDX SBOM for `linux/amd64` or `linux/arm64`); `release asset missing` (no `guidedtraffic-trove_<version>.spdx.json`) |
| Install the published release | the published chart and digest do not install into the dev profile (a defect of that version, or a Docker Hub fault) |
| Run the scenarios | a red `TestNPMClients` or `TestNPMMatrix` (a defect of that version, or the environment) |
| Check that every pod runs the published digest | `wrong image in the cluster`: a container does not report `@<digest>`, or `helm get values` carries another digest ([the published mode](e2e.md#the-published-mode)) |

The check reads the attestations; it verifies no signature, and no cluster verifies them (H-79). It pushes nothing and touches no branch; it builds only the images the harness always builds for a run (the e2e driver, the counting test upstream and the client images), never the product.
The chart package it installs is the file it verified, and the image is pulled by the digest the tag resolved to, so the install and the checks cannot see different artifacts.
The evidence is the job summary (the tag's commit, digests, the SBOM creator tools of each platform, the matrix per gate row, the wall time of each step) and, for 90 days, the artifacts
`release-smoke-report` (`.out/test-e2e.json`) and `release-smoke-artifacts` (`.out/e2e/`, with `npm-matrix.json`, and `.out/release-smoke/`, with `evidence.json` and `times.tsv`). The edge listens on host port 8101. The GitHub API is called with the job's read-only token; a 404 reads "release not found", an answer of 401, 403 or 429 "refused (authentication or rate limit)", and no answer "cannot be reached".

**A red check leaves the release published** ([Q-HA-163](../spec/ha-and-helm.md#q-ha-163--a-red-post-release-check-leaves-the-release-published-and-is-a-finding-not-a-withdrawal)):
it is a finding about that version, and the fix is a commit that Semantic Release releases under the next version. Infrastructure faults (a Docker Hub rate limit, a kind
cluster that does not start, a lost runner) are re-run with `gh run rerun <id> --failed`, which runs the failed job again and keeps the results of the publish jobs.

**Running it on a published release** (`workflow_dispatch`, input `version`): only this job runs. The publish jobs have `if: github.event_name == 'release'`, so a dispatch
builds no image and pushes no tag, `latest` and `gh-pages` included ([test/chart/release_workflow_test.go](../../test/chart/release_workflow_test.go) fails when a gate goes).
The harness and the dev profile come from the dispatched ref, the chart and image from the release.

```sh
gh workflow run "Release Docker & Helm" --ref main -f version=0.25.0   # example
gh run list --workflow "Release Docker & Helm" --limit 3
gh run view <id> --json jobs --jq '.jobs[] | [.name, .conclusion] | @tsv'   # build and release-helm-gh: skipped
```

Locally, without a cluster: `make release-smoke VERSION=0.25.0` (needs `curl`, `helm`, `jq`, Docker with buildx). Dispatching a workflow file that exists only on a branch is
not verified here; if GitHub refuses it, the change goes to `main` first, which is safe because its commits are `ci:`, `test:` and `docs:` and release nothing.

Open until the first release runs the job: the wait for GitHub Pages (the cap is a plan value, not measured), the matrix's wall time against the 120 minutes, and the image ID that
containerd reports for `guidedtraffic/trove` (the guard assumes the index digest, which it reports for the pinned cert-manager images).

## Secrets and settings

The pipeline uses three secrets of the organisation, which reach this repository without a setting here:
`DOCKERHUB_PAT` (the Docker Hub access token of the user `guidedtraffic`) for the image and for the pulls of the post-release check, and
`APP_CLIENT_ID` and `APP_PRIVATE_KEY` for the GitHub App that Semantic Release and Renovate act as.

One setting is the owner's to make: the `gh-pages` branch that holds the chart repository, which GitHub
Pages serves at `https://guided-traffic.github.io/trove/`.

## Not in place yet

- **No signatures.** The image carries a provenance and an SBOM attestation, not a keyless signature. The
  chart carries neither a signature nor an attestation.
- **The post-release check has not run on a real release.** E5.8 is closed by the owner's walkthrough from the evidence of the first green run after a release and of the
  dispatch runs; until then the job is built and its parts are checked locally, not proven in CI ([the post-release check](#the-post-release-check)).
- **Docker Buildx of the local machine** is the one of Docker Desktop, not the pinned version, until it
  is replaced ([make versions-check](../../Makefile) reports it).
- **Dependency updates are patch releases.** Each automerged update is a `fix` commit.
- **The cyclomatic complexity threshold** is the ratchet of today, not the 15 of valkey-operator.
