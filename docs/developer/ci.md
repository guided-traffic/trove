# Continuous integration

Two GitHub Actions workflows run every check that the [Makefile](../../Makefile) and the
[web workspace](web-ui.md) define. [ci.yml](../../.github/workflows/ci.yml) runs on every push to
`main` and on every pull request. [nightly.yml](../../.github/workflows/nightly.yml) runs the S3
conformance tests against MinIO, and reports AWS S3 as skipped until an AWS account exists. In M0
nothing is pushed to a registry and no release is cut; the release pipeline belongs to phase 6.

Status: the workflows are validated with actionlint and by running each target and each shell step
on macOS. They have **not run on a GitHub runner yet**. The first run on `main` is the first real
test of the Linux-only steps (see [Not in place yet](#not-in-place-yet)).

## What has to be in your head first

- **The Makefile is the interface.** Each job calls a `make` target, or a command that the Makefile
  would also run. A failure reproduces with the same command on your machine
  ([Reproduce a job locally](#reproduce-a-job-locally)).
- **Every pin is read from one place.** Go comes from [go.mod](../../go.mod), Node from
  [web/.nvmrc](../../web/.nvmrc), Angular from [web/package-lock.json](../../web/package-lock.json),
  every other tool and image from [tools/versions.env](../../tools/versions.env). Action versions are
  pinned by full commit SHA in the workflow files, with the version in a comment.
- **Evidence is committed.** [docs/coverage/implemented.txt](../coverage/implemented.txt) records
  which requirements have passing tests. CI compares it with the test reports of the run, and fails
  when they differ ([coverage.md](coverage.md)).
- **A skip is never a pass.** The AWS S3 job is reported as skipped, and no check exits green
  because it found nothing to run (see [Nightly](#nightly-workflow)). The rule comes from
  [R-HA-051](../spec/ha-and-helm.md#r-ha-051--local-development-profile).
- **The AWS leg is a trust relationship.** It assumes a role through the repository's OIDC token, so
  the role's trust policy is the control, not a stored key. The security pages are in
  [docs/security/](../security/README.md).

## Workflows and jobs

| Workflow | Job | Runs | What it gates |
|---|---|---|---|
| `ci` | `versions` | `make versions-check`, the Go and Node versions in the log, Node against `web/.nvmrc` | E0.2: one version per tool. Installs the pinned helm, kubectl and kind first, so the runner's copies are not the ones checked |
| `ci` | `lint` | `make lint` (golangci-lint built from its pin, cached by its versioned path) | Static analysis and `go vet` |
| `ci` | `unit` | `make test` (race detector); uploads `.out/test-unit.json` as `test-unit` | Unit tests and architecture tests; the report feeds `coverage-evidence` |
| `ci` | `generate` | `make generate-check` | E0.1: generated code is committed and current |
| `ci` | `build-no-node` | `make build` with every Node.js executable off `PATH`; `file` must say *statically linked*, `go version -m` must record `CGO_ENABLED=0` | E0.1: the binary builds without Node.js |
| `ci` | `web` | `npm ci`, `npm run lint`, `npm test`, `npm run build:embed` in `web/` | E0.2 (npm ci fails when `package.json` and the lockfile disagree), E0.9 (web unit tests) |
| `ci` | `coverage` | `go run ./internal/tools/coverage -check`, then `-links` | E0.8: the generated pages match their rules; every relative link and anchor resolves |
| `ci` | `coverage-evidence` | `go run ./internal/tools/coverage -check -tests` over the four reports of the run | E0.8: `implemented.txt` equals what the reports imply. Needs `unit`, `integration` and `chart` |
| `ci` | `chart` | `make chart-lint`, `make chart-test`; uploads `.out/test-chart.json` as `test-chart` | E0.7: the chart lints and renders as specified |
| `ci` | `image` | `make image IMAGE=trove:ci` (no push), then the image user must be `65532:65532` | R-HA-033's non-root user; the image builds |
| `ci` | `integration` (matrix `9.0`, `9.1`) | `make integration-up`, `make test-integration`, then `make integration-down` always; container logs on failure | E0.5 once the S3 conformance tests exist (they are under `test/integration/`), and every other integration test |
| `ci` | `e2e` | `make image VERSION=dev`, `make dev-up`, `make e2e SET=push`; uploads `.out/test-e2e.json` (`test-e2e`), `.out/e2e/` (`e2e-artifacts`: k6 logs and summaries, pod watch, events, driver output) and, on failure, the cluster state (`e2e-cluster-state`); always `make dev-down`. Needs `chart` and `image` | E0.3, E0.4 (chart part), E0.6, E0.7, E0.11, E0.12, the E0.14 drain, the E0.15 certificate rotation and the E0.16 second endpoint on every change to `main` ([e2e.md](e2e.md)) |
| `ci` | `workflows` | actionlint at `ACTIONLINT_VERSION` over `.github/workflows/` | The workflow files themselves |
| `nightly` | `minio` | Builds MinIO from source at `MINIO_COMMIT`, starts it, seeds the buckets, runs the conformance tests | E0.5 nightly, MinIO leg |
| `nightly` | `e2e-failover` | the same preparation, then `make e2e SET=failover` | E0.13: PostgreSQL switchover, kill and 30 s stop, Valkey failover, kill and 60 s outage under k6 load, with E0.4 and E0.6. Nightly because it needs a runner's CPU for about 20 minutes and k6 at 100 requests/s must not share a small runner with a kind cluster in a pull request |
| `nightly` | `aws-gate` and `aws-s3` | See [Nightly](#nightly-workflow) | E0.5 nightly, AWS leg, reported as skipped |

The `e2e` job has not run on a GitHub runner either. Its design is in [e2e.md](e2e.md): it builds the release
image, brings the dev profile up on kind (HAProxy Ingress, cert-manager, the Valkey operator, the
internal-secrets-operator, CloudNativePG: all pinned in `tools/versions.env` and installed by `up.sh`) and runs the
suite. A prep step before `make dev-up` prints the pins, checks that the runner is amd64 (the two guided-traffic
operator images are published for amd64 only) and that the five chart repositories answer, so a missing repository
fails in seconds and not five minutes into `dev-up`. The `e2e` job and the nightly `e2e-failover` job have the
same prep. Measured on a laptop, `SET=push` takes about 9
minutes and `SET=failover` about 7, each after about 5 minutes of image build and `make dev-up`. The job
sets a 75-minute limit (90 for the nightly one) and dumps the cluster state on failure.

### The shared steps

- [.github/actions/load-pins/action.yml](../../.github/actions/load-pins/action.yml) appends
  `tools/versions.env` to `$GITHUB_ENV`. It refuses any line that is not a comment, a blank line or a
  `KEY=value` line without spaces. Jobs that call a workflow step with a pin (`HELM_VERSION`,
  `GOLANGCI_LINT_VERSION`, `ACTIONLINT_VERSION`, `MINIO_COMMIT`) run it; jobs that only call make do
  not need it, because the Makefile includes the same file.
- Every checkout sets `persist-credentials: false`, and the workflows set `permissions: contents: read`
  at the top. Only the nightly AWS job requests `id-token: write`, for OIDC.
- Test reports are uploaded with `if: always()`, so a failed run still leaves its report.

## Pins

| What | Where | Value |
|---|---|---|
| Go | [go.mod](../../go.mod) `go` line, `actions/setup-go` with `go-version-file` | `1.27.3` |
| Node.js | [web/.nvmrc](../../web/.nvmrc) | `26.5.0` |
| Angular and every npm package | [web/package-lock.json](../../web/package-lock.json) | as locked |
| golangci-lint | [tools/versions.env](../../tools/versions.env) `GOLANGCI_LINT_VERSION` | `v2.15.0` |
| actionlint | [tools/versions.env](../../tools/versions.env) `ACTIONLINT_VERSION` | `v1.7.12` |
| kind, kubectl, Helm | [tools/versions.env](../../tools/versions.env) `KIND_VERSION`, `KUBECTL_VERSION`, `HELM_VERSION` | `v0.32.0`, `v1.36.2`, `v3.21.3` |
| Integration images | [tools/versions.env](../../tools/versions.env) `INTEGRATION_*`, `VALKEY_90_IMAGE`, `VALKEY_91_IMAGE`, `SEAWEEDFS_IMAGE` | by digest |
| Release base image | [tools/versions.env](../../tools/versions.env) `RUNTIME_BASE_IMAGE` | by digest |
| MinIO source | [tools/versions.env](../../tools/versions.env) `MINIO_COMMIT` | **not yet present**: the nightly MinIO job fails until it is added (see [Not in place yet](#not-in-place-yet)) |

kind is installed in CI with `go install sigs.k8s.io/kind@$KIND_VERSION`, helm and kubectl with the
Azure setup actions. golangci-lint and actionlint are built with `go install` at their pins.

<details>
<summary>Action pins (full SHA, version in the comment)</summary>

| Action | Pin | Used by |
|---|---|---|
| `actions/checkout` | `3d3c42e5aac5ba805825da76410c181273ba90b1` (v7.0.1) | every job |
| `actions/setup-go` | `b7ad1dad31e06c5925ef5d2fc7ad053ef454303e` (v7.0.0) | every Go job |
| `actions/setup-node` | `949feb2413d6458794dcd2491c4babbbce0c15c1` (v7.1.0) | `versions`, `web`, `image` |
| `actions/cache` | `55cc8345863c7cc4c66a329aec7e433d2d1c52a9` (v6.1.0) | `lint` |
| `actions/upload-artifact` | `cf430e030ddbb5b0abf93d22962f4752f3646cd9` (v7.0.2) | `unit`, `chart`, `integration`, `minio` |
| `actions/download-artifact` | `9000827ccba6bdab643e8b6fd33ac0654aef8333` (v8.0.2) | `coverage-evidence` |
| `azure/setup-helm` | `9bc31f4ebc9c6b171d7bfbaa5d006ae7abdb4310` (v5.0.1) | `versions`, `chart` |
| `azure/setup-kubectl` | `bda439f5f36b99b262ebdcd3613869bddb064c8e` (v5.2.0) | `versions`, `chart` |
| `aws-actions/configure-aws-credentials` | `e1253824e5c10ff9df46874f81ed3ec929e19cfd` (v6.3.0) | `aws-s3` |

The pins were resolved with `gh api repos/<owner>/<repo>/commits/<tag>` on 2026-10-29. Verified in
the action sources: `azure/setup-kubectl` refuses a custom download URL without a SHA-256 checksum,
and the default URL needs none. I did not confirm whether either action checks the checksum of the
default download, and `azure/setup-helm` showed no checksum handling in the files I read. So the trust
in those two binaries rests on the pinned action and the vendor's HTTPS download, not on a checksum
pinned in this repository.

</details>

## Reproduce a job locally

Run from the repository root. Each row is the command the job runs.

| Job | Local command | Differences from the runner |
|---|---|---|
| `versions` | `make versions-check` | The check compares helm, kubectl, kind and k6 only when they are on `PATH` |
| `lint` | `make lint` | None beyond the host |
| `unit` | `make test` | None beyond the host; the race detector needs a C compiler |
| `generate` | `make generate-check` | `git diff --exit-code` compares against the index. On a tree with uncommitted changes it fails for reasons unrelated to generation |
| `build-no-node` | `make build`; on Linux, `GOOS=linux make build`, then `file bin/trove` and `go version -m bin/trove` | `file` on macOS reports Mach-O, so the static-link check needs the `GOOS=linux` build. The Node-free `PATH` is not reproduced on a Mac, where `node` and `go` often share a directory |
| `web` | `cd web && npm ci && npm run lint && npm test && npm run build:embed` | Node from `web/.nvmrc` (`nvm use`) |
| `coverage` | `go run ./internal/tools/coverage -check` and `go run ./internal/tools/coverage -links` | None |
| `coverage-evidence` | Needs the four reports; see [Keep the evidence record current](#keep-the-evidence-record-current) | Reports come from `make test`, `make chart-test` and both integration lines |
| `chart` | `make chart-lint` and `make chart-test` | Needs helm at `HELM_VERSION` on `PATH` |
| `image` | `make image IMAGE=trove:ci` | Runs for `linux/amd64` on the runner; a Mac builds `linux/arm64` unless `IMAGE_ARCH=amd64` is given |
| `integration` | `make integration-up`, `make test-integration`, `make integration-down` | Needs Docker. Valkey line by `TROVE_TEST_VALKEY_LINE=9.0` or `9.1`. Changing the line recreates the stack |
| `workflows` | `go install github.com/rhysd/actionlint/cmd/actionlint@v1.7.12`, then `actionlint -shellcheck=` from the repository root | CI disables shellcheck, which is not pinned |
| `minio`, `aws-s3` | `.github/scripts/s3-conformance.sh <label>` with the `TROVE_TEST_S3_*` variables set | See [Nightly](#nightly-workflow) |

## Keep the evidence record current

`coverage-evidence` fails when [implemented.txt](../coverage/implemented.txt) differs from what the
reports of the run imply. That is the point of the job: a passing test that proves a requirement
has to change the committed record in the same change.

The refresh needs both Valkey lines, because CI passes the reports of both to the generator in one
invocation, and a requirement proven on one line only is still counted. The intended command is
`make coverage-refresh`. **It does not exist yet**; the Makefile owner has to add it. Until then the
refresh is these commands:

```shell
make test
make chart-test
for line in 9.0 9.1; do
  TROVE_TEST_VALKEY_LINE=$line make integration-up
  TROVE_TEST_VALKEY_LINE=$line make test-integration
  cp .out/test-integration.json ".out/test-integration-$line.json"
done
make integration-down
go run ./internal/tools/coverage -tests .out/test-unit.json .out/test-chart.json \
  .out/test-integration-9.0.json .out/test-integration-9.1.json
```

Then commit `docs/coverage/implemented.txt` and the pages rendered from it. The integration stack
and its volumes belong to the developer running this; `make integration-down` removes them.

## Nightly workflow

[nightly.yml](../../.github/workflows/nightly.yml) runs at 03:23 UTC every day and on demand.

### MinIO leg (`minio`)

1. Fetches `MINIO_REPO` at `MINIO_COMMIT` with `git fetch --depth 1 <sha>`, checks that `HEAD` equals
   the pin, and runs `go build -trimpath` on it. The commit is the last one on `master` before MinIO
   was archived on 2026-04-25 (verified: `7aac2a2c5b7c882e68c1ce017d8256be2feea27f`, committed
   2026-02-12). I built it locally with Go 1.27.3, and the binary reports `DEVELOPMENT.GOGET`.
2. Makes the CA and the S3 server certificate with `test/integration/certs.sh` (openssl from the pinned image;
   the SANs cover `127.0.0.1` and `localhost`), starts the server on `https://127.0.0.1:9000` with random root
   credentials and `--certs-dir` (`public.crt`, `private.key`), checks that a plaintext request is not served
   (INV-20), exports `TROVE_TEST_CA_FILE` and the `https` endpoint, and runs `go run ./test/integration/cmd/seed`,
   which creates the buckets with versioning on. The leg is TLS-only like the rest of the integration tier.
3. Runs [s3-conformance.sh](../../.github/scripts/s3-conformance.sh) with `-run Conformance` over
   `./test/integration/blob/...`. It fails when no package matches, and when no test named
   `Conformance` passed, because `go test` alone reports success for an empty match.

The leg fails until the conformance tests of R-STOR-035 exist. That is the intended state: the
failure says so, and it does not read as a pass.

### AWS S3 leg (`aws-gate`, `aws-s3`)

- `aws-gate` reads the secret `AWS_CI_ROLE_ARN` (a job-level `if` cannot read secrets). When the
  secret is absent, the gate writes a SKIPPED section to the job summary and sets its output
  `configured=false`. The gate itself exits 0.
- `aws-s3` only runs when the output is `true`. When it is skipped, GitHub shows it grey as
  *Skipped*, not green. Nothing in the run reports an AWS result.
- To enable the leg, the owner adds the secret `AWS_CI_ROLE_ARN` (an IAM role that trusts the
  repository's OIDC token) and the variables `AWS_CI_REGION`, `AWS_CI_BUCKET_BLOBS` and
  `AWS_CI_BUCKET_BACKUPS`.
- The leg stops before the tests if the assumed role yields a session token. The test accessors of
  [test/integration/testenv](../../test/integration/testenv) read a static key pair and have no field
  for a session token. Until they have one, the leg cannot pass.

## Not in place yet

- **The first run on a GitHub runner.** Nothing here has run on a runner. Two steps depend on runner
  details I could not check: the Node-free `PATH` trimming in `build-no-node` assumes that `node`
  sits in a directory that does not also hold `go`, `make` or `file`, and the `versions` job assumes
  the Azure setup actions install the pinned helm and kubectl first on `PATH`.
- **The e2e test report in the evidence record.** `coverage-evidence` reads four reports; the e2e report
  (`.out/test-e2e.json`, test names carry the requirement IDs) is not among them, so the requirements it
  proves are not in `implemented.txt` yet. Wiring it needs the coverage generator and the record to
  take a fifth report.
- **Runner capacity of the e2e job.** The suite is sized on a 20-CPU laptop. The runner has 4 vCPUs; if
  k6 drops iterations there, the job fails on `dropped_iterations` instead of passing.
- **Image scanning.** No Trivy or grype job. Adding one needs a pinned scanner version in
  `tools/versions.env`, which this package does not add.
- **`MINIO_COMMIT`.** The pin is not in `tools/versions.env` yet. The nightly MinIO job fails with a
  message naming the file until the line `MINIO_COMMIT=7aac2a2c5b7c882e68c1ce017d8256be2feea27f`
  is added.
- **Coverage badge.** No `.github/badges/coverage.json` exists, so the README has no coverage badge.
- **shellcheck in CI.** The `workflows` job runs actionlint with shellcheck disabled, because the
  runner's shellcheck is not pinned.
- **Cross-platform rebuilds.** The `image` job builds for `linux/amd64` only.
