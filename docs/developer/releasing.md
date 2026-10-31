# Releasing

Every push to `main` and every pull request runs the checks ([CI](#ci)). Once every job of a push
run on `main` passed, Semantic Release publishes a GitHub release ([release flow](#release-flow)),
and [`build.yml`](../../.github/workflows/build.yml) builds the product from that tag: the image
`guidedtraffic/trove` on Docker Hub, and the Helm chart in the chart repository on GitHub Pages
([the release pipeline](#the-release-pipeline)). What the release contains and how the image is
built: [`Containerfile`](../../Containerfile).

## CI

[`release.yml`](../../.github/workflows/release.yml) ("Test and Release") runs on every push to `main`,
every pull request to `main` and on demand. A newer push cancels the running run of its ref. Every
check is a job of its own, with its own make target, so it runs the same locally. Every job runs on the
organisation's self-hosted runners, which are ephemeral pods (Ubuntu 24.04, Docker) without `make` or a C
compiler. A job therefore installs what it calls: `make` and the C compiler (`build-essential`), `file`
and `jq` where a step uses them, the Docker Compose plugin for the integration stack, Go from `go.mod`,
Node.js from `web/.nvmrc`, and the pinned helm, kubectl, kind and Docker Buildx.

| Job | Make target | Runs |
|---|---|---|
| versions (E0.2) | `make versions-check` | go, node (`web/.nvmrc`), sqlc, oapi-codegen, golangci-lint, kind, kubectl, helm, k6 and docker buildx against their pins. The job installs the pinned helm, kubectl, kind and buildx first, so the runner's copies are not the ones checked |
| Code Linting | `make lint` | golangci-lint (pinned; its binary is cached by version and Go version) with the linters of [.golangci.yml](../../.golangci.yml), then `go vet` |
| GoSec Security Scan | `make gosec` | golangci-lint with only gosec enabled, the settings and the accepted findings of [.golangci.yml](../../.golangci.yml) |
| Vulnerability Check | `make vuln` | govulncheck (pinned) over the module |
| Cyclomatic Complexity | `make cyclo` | gocyclo (pinned), fails above `CYCLO_THRESHOLD`; the job summary lists the 20 most complex functions with `make -s cyclo-report` |
| generate (E0.1) | `make generate-check` | `go generate`, then `git diff --exit-code` and a check for generated files that are not committed |
| build without Node.js (E0.1) | `make build` | with every Node.js executable off `PATH`; `file` must say *statically linked*, `go version -m` must record `CGO_ENABLED=0` |
| unit tests | `make test` | the race detector; uploads `.out/test-unit.json` (`test-unit`) and `.out/coverage/unit.out` (`profile-unit`) |
| web (Angular) | `make web-ci`, `make web-lint`, `make web-test`, `make web-embed` | `npm ci` (fails when `package.json` and the lockfile disagree), ESLint and the R-UI-046 rules, the Angular unit tests, the production build embedded into `internal/webui/dist` |
| Malware Scan (source code) | `make malware-scan` | ClamAV (`clamscan --recursive --infected`) over the tree, without `.git`, `node_modules` and the build outputs; any infected file fails; uploads `clamav-report` |
| chart (E0.7) | `make chart-lint`, `make chart-test` | `helm lint` over every value set, the render assertions of [test/chart](../../test/chart); uploads `.out/test-chart.json` (`test-chart`) |
| image | `make image IMAGE=trove:ci` | the [Containerfile](../../Containerfile) for the runner's architecture, loaded into Docker; the image must run as `65532:65532` (R-HA-033) |
| Container Scan (Trivy, built image) | `make image IMAGE=trove:scan`, then Trivy | Trivy (pinned `TRIVY_VERSION`) on the built image: fixed OS and library vulnerabilities of CRITICAL or HIGH, secrets and misconfigurations; SARIF uploaded as `trivy-container-report` |
| integration (Valkey 9.1) | `make integration-up`, `make test-integration`, `make integration-down` | the integration tier against the compose stack on TLS, on Valkey 9.1 only (no older line is guaranteed while trove is in development); uploads `test-integration` and `profile-integration` |
| S3 conformance (MinIO) | `make minio-up`, `make s3-conformance`, `make minio-down` | the conformance tests of R-STOR-035 against MinIO, built from source at `MINIO_COMMIT` and served over TLS only; the server log is uploaded on failure |
| e2e-npm on kind (npm) | `make image VERSION=dev`, `make dev-up`, `make e2e SET=npm`, `make dev-down` | the npm acceptance matrix (eight real clients, [R-NPM-045](../spec/formats/npm.md#r-npm-045--client-acceptance-matrix)) and E2.2 to E2.12 on the scatter profile with the counting test upstream; gates every change, own cluster name and edge port (8082). A dispatch with `closing=true` adds the leg `npm-closing` (port 8083): the matrix and the cold install with Valkey stopped, and the read-only subset against the public registry ([Q-NPM-46](../spec/formats/npm.md#q-npm-46--what-marks-the-run-that-closes-the-phase)); the leg `npm-ha` (port 8084, every change; step budget 80 minutes, job 120) is E2.11: drain variant A, the PostgreSQL switchover, the Valkey failover and the pod crash under the npm load, then the R-ARCH-027 report ([e2e.md](e2e.md#drain-and-failover-under-npm-load)); a closing dispatch runs it as `npm-ha-closing` with the 5-minute Valkey outage ([Q-HA-67](../spec/ha-and-helm.md#q-ha-67--where-and-how-often-the-scenarios-under-npm-load-run)); uploads `e2e-npm-report-<set>`, `e2e-npm-artifacts-<set>` and on failure the cluster state |
| fuzz npm (closing run only) | `make fuzz-npm FUZZ=<target> FUZZTIME=30m` | three legs (`FuzzR_ARCH_028_Match`, `FuzzR_ARCH_028_Inspect`, `FuzzMatch`) of 30 minutes each; runs only on a dispatch with `closing=true`; uploads the failing input |
| e2e on kind (push, failover) | `make image VERSION=dev`, `make dev-up`, `make e2e SET=<set>`, `make dev-down` | the dev profile on a kind cluster, one leg per set: `push` (every change) and `failover` (E0.13, E0.4, E0.6); each leg has its own cluster name and edge port; uploads the reports, `e2e-artifacts-<set>`, and on failure the cluster state |
| workflow files (actionlint) | `actionlint` (pinned `ACTIONLINT_VERSION`) | every file of `.github/workflows`, with [.github/actionlint.yaml](../../.github/actionlint.yaml) |
| coverage generator (links and rules) | `go run ./internal/tools/coverage -check`, then `-links` | the generated pages match their rules; every relative link and anchor resolves |
| coverage evidence (implemented.txt) | `go run ./internal/tools/coverage -check -tests …` over the reports of this run | [implemented.txt](../coverage/implemented.txt) equals what the reports imply; needs `unit`, `integration` and `chart` |
| Combined Coverage Report | `make coverage-report` | merges the unit and integration profiles; the job summary and the badge `coverage-badge` |
| Release Tooling | `make test-release-tooling` | `npm ci`, `npm audit signatures`, and [hack/verify-release-tooling.mjs](../../hack/verify-release-tooling.mjs), which renders release notes with the installed plugins |
| Semantic Release | – | needs every job above; runs only on a push to `main`; publishes the release ([release flow](#release-flow)) |

The workflows pin their actions by major version tag (`actions/checkout@v7`), the way the
organisation's other repositories do. Two actions are pinned by a release tag instead:
`aquasecurity/trivy-action@v0.36.0` and `renovatebot/github-action@v46.3.10`.

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

### Running a job locally

Run from the repository root. Each line is the command the job runs.

```sh
make versions-check                     # the tools on PATH against the pins
make lint gosec vuln cyclo generate-check build
make test                               # unit tests, race detector, .out/coverage/unit.out
make web-ci web-lint web-test web-embed # Node.js from web/.nvmrc
make malware-scan                       # needs clamscan on PATH
make chart-lint chart-test
make image IMAGE=trove:ci               # the Containerfile, host architecture
make integration-up test-integration integration-down
make minio-up s3-conformance minio-down # needs Docker and the MinIO source build
make coverage-report                    # merges .out/coverage
make test-release-tooling               # npm ci, npm audit signatures, the release notes check
go run ./internal/tools/coverage -check -links
make image VERSION=dev && make dev-up && make e2e SET=push && make dev-down
make image VERSION=dev && make dev-up && make e2e SET=npm && make dev-down   # TROVE_DEV_HTTP_PORT=8080 is the port of the committed reference lockfile; any port works
make fuzz-npm FUZZ=FuzzMatch FUZZTIME=1m
```

### The run that closes a phase

The roadmap reserves a few checks for "the full run of Test and Release on the commit that closes the phase"
(M2: the npm scenario list and the cold-cache install with Valkey stopped, the read-only subset against the public registry,
the 30-minute fuzz run of npm's `Match` and `Inspect`). Nothing in a push or a pull request marks that commit, so a person starts
the run ([Q-NPM-46](../spec/formats/npm.md#q-npm-46--what-marks-the-run-that-closes-the-phase)):
*Actions, Test and Release, Run workflow*, the closing commit as the ref, `closing` ticked; or
`gh workflow run release.yml --ref main -f closing=true`. Every job of the workflow runs, plus the legs `e2e-npm (npm-closing)` and
the three `fuzz npm` legs, and the leg `e2e-npm (npm-ha)` runs as `npm-ha-closing`, with the Valkey outage under npm load. A dispatch does not release (Semantic Release runs on `push`), and a failed closing leg means the phase
is not closed.

`make generate-check` runs `git diff --exit-code`, so it fails on a tree with uncommitted changes for
reasons unrelated to generation. `make dev-up` needs an amd64 host or the emulator of
the arm64 note of [local-profile.md](local-profile.md).

### Performance (nightly)

[`performance.yml`](../../.github/workflows/performance.yml) ("Performance (nightly)") runs every night at 02:17 UTC and on demand
(`workflow_dispatch`), on the self-hosted runners. It brings up the integration stack (`make integration-up`), runs
`make test-performance` and stops the stack. That target runs `TestChangePropagation` and `TestJobPickup` with `TROVE_PERF_GATE=1`, which
enforces the p99 bounds of P-13 (100 ms) and P-14 (1 s) with Valkey available; the per-change integration job asserts delivery and the p50 only
([Q-ARCH-220](../spec/architecture.md#q-arch-220--p-13-starts-its-clock-at-the-commit-and-its-p99-is-enforced-in-the-nightly-performance-run)).
The report `test-performance` is kept for 30 days. A failed night is not wired to an issue or a notification: look at the Actions list.
The other targets of R-ARCH-027 (`perf/`) are not part of this workflow.

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
- The release tooling of [package.json](../../package.json) (semantic-release and its presets) is one
  group and never merges on its own. The Angular packages of `web/package.json` are one group, reviewed.
- Every automerged dependency update is a `fix` commit (valkey-operator's policy), so under
  [.releaserc.json](../../.releaserc.json) each of them is a patch release. See
  [Release flow](#release-flow).
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
belongs to pushes to `main`, not to releases), and the chart `version` and `appVersion` `0.1.0`. Every
job checks out the tag.

| Job | Runner | What it does |
|---|---|---|
| Build Docker Image | self-hosted | [docker/build-push-action](https://github.com/docker/build-push-action) builds [Containerfile](../../Containerfile) for `linux/amd64,linux/arm64` and pushes `guidedtraffic/trove` to Docker Hub with the tags of [docker/metadata-action](https://github.com/docker/metadata-action); provenance and SBOM attestations; the build arguments are the pins (`NODE_VERSION` from [web/.nvmrc](../../web/.nvmrc), `GO_VERSION` from [go.mod](../../go.mod), `RUNTIME_BASE_IMAGE` from [tools/versions.env](../../tools/versions.env)) and the version, the commit and the commit's date for the binary's version line |
| Scan and attach the SBOM | same | the SPDX SBOM by anchore/sbom-action with `SYFT_VERSION`, uploaded to the release; a Docker Scout CVE report |
| Release Helm Chart to GitHub Pages | same | `deploy/helm/trove` with `version` and `appVersion` set to the release and the image tag in `values.yaml`, packaged, added to the chart repository's `index.yaml` on the `gh-pages` branch, and `trove-<version>.tgz` and `index.yaml` attached to the release |

**The Containerfile** has three stages. `web` runs `npm ci` and `npm run build:embed` on the Node.js of
`web/.nvmrc`; `build` cross-compiles the binary for the target platform on the build platform
(`CGO_ENABLED=0`), so neither architecture is emulated; the final stage is the distroless base of
`RUNTIME_BASE_IMAGE`, the binary and nothing else, as UID 65532. `make image` builds the same file for the
host architecture.

**The chart repository** is `https://guided-traffic.github.io/trove/`, served from the `gh-pages`
branch: every release adds its package and the merged `index.yaml`. The release's assets are the
package of that release and the index. The chart's defaults point at the image: `image.registry:
docker.io`, `image.repository: guidedtraffic/trove` ([values.yaml](../../deploy/helm/trove/values.yaml)),
checked by [test/chart/image_test.go](../../test/chart/image_test.go). The release job rewrites the line
`  tag:` of `values.yaml`; the test fails when a second line of that shape appears.

Not in the pipeline: a chart SBOM, a chart attestation, and any signature. Only the image carries
attestations: a provenance and an SBOM attestation in its image index.

## Secrets and settings

The pipeline uses three secrets of the organisation, which reach this repository without a setting here:
`DOCKERHUB_PAT` (the Docker Hub access token of the user `guidedtraffic`) for the image, and
`APP_CLIENT_ID` and `APP_PRIVATE_KEY` for the GitHub App that Semantic Release and Renovate act as.

One setting is the owner's to make: the `gh-pages` branch that holds the chart repository, which GitHub
Pages serves at `https://guided-traffic.github.io/trove/`.

## Not in place yet

- **The workflows have not run on GitHub.** actionlint passes at the pin, and the make targets they call
  were run locally, except the two named below. The first run on GitHub is the first test of each job's
  runner setup.
- **The e2e legs, the malware scan and the Trivy scan have not run here.** The e2e legs need the amd64
  emulator on an arm64 host, `clamscan` is not installed on the machine that wrote the change, and Trivy
  needs the built image in the local Docker.
- **No signatures.** The image carries a provenance and an SBOM attestation, not a keyless signature. The
  chart carries neither a signature nor an attestation.
- **Docker Buildx of the local machine** is the one of Docker Desktop, not the pinned version, until it
  is replaced ([make versions-check](../../Makefile) reports it).
- **Dependency updates are patch releases.** Each automerged update is a `fix` commit.
- **The cyclomatic complexity threshold** is the ratchet of today, not the 15 of valkey-operator.
