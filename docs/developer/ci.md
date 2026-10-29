# Continuous integration

Four GitHub Actions workflows run the checks and publish the artifacts. Every check that the
[Makefile](../../Makefile) and the [web workspace](web-ui.md) define runs in
[ci.yml](../../.github/workflows/ci.yml), on every push to `main` and on every pull request.
[nightly.yml](../../.github/workflows/nightly.yml) runs the S3 conformance tests against MinIO, and
reports AWS S3 as skipped until an AWS account exists. [preview.yml](../../.github/workflows/preview.yml)
publishes every green commit of `main`, and [release.yml](../../.github/workflows/release.yml)
publishes a `v0.*` tag. Both push to ghcr.io and sign what they push
([Preview publication](#preview-publication), [Release pipeline](#release-pipeline)).

Status: the `ci` workflow runs on GitHub runners. The run of `main` at `d153ee3` passed every job,
the e2e job included. The nightly, preview and release workflows are validated with actionlint; the
release workflow's first publication is recorded in [Release pipeline](#release-pipeline).

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
| `nightly` | `minio` | Builds MinIO from source at `MINIO_COMMIT`, starts it over TLS, seeds the buckets, runs the conformance tests with `TROVE_TEST_SCOPE=s3` (only the `TROVE_TEST_S3_*` variables and an optional CA are required; without a scope `testenv` requires the whole stack) | E0.5 nightly, MinIO leg |
| `nightly` | `e2e-failover` | the same preparation, then `make e2e SET=failover` | E0.13: PostgreSQL switchover, kill and 30 s stop, Valkey failover, kill and 60 s outage under k6 load, with E0.4 and E0.6. Nightly because it needs a runner's CPU for about 20 minutes and k6 at 100 requests/s must not share a small runner with a kind cluster in a pull request |
| `nightly` | `aws-gate` and `aws-s3` | See [Nightly](#nightly-workflow) | E0.5 nightly, AWS leg, reported as skipped |
| `preview` | `publish` | A green `ci` run of a push to `main`, or a manual run of `main`: `make image-push` (image `main-<sha12>` and `main`), the chart at `0.<next minor>.0-main.<run>`, both signed and verified | Every green commit of `main` is a preview build; see [Preview publication](#preview-publication) |
| `release` | `resolve`, `versions`, `image`, `chart`, `release-notes` | A `v0.*` tag push, or a manual run from `main` with a tag: the image and the chart at the tag's version, both signed and attested, the digests in the GitHub release notes; see [Release pipeline](#release-pipeline) | R-HA-047 and Q-HA-11: the published artifacts of a release |

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
  at the top. Jobs that need more request it themselves: `id-token: write` for OIDC in the nightly AWS
  job and in the preview and release jobs that sign, `packages: write` and `attestations: write` where
  they publish or attest, and `contents: write` only in the release job that edits the release notes.
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
| MinIO source | [tools/versions.env](../../tools/versions.env) `MINIO_COMMIT` | the commit the nightly MinIO leg builds |
| Cosign | [tools/versions.env](../../tools/versions.env) `COSIGN_VERSION` | `v3.1.4`, the release the owner's chart repository signs with |
| Docker Buildx | [tools/versions.env](../../tools/versions.env) `BUILDX_VERSION` | `v0.38.1`, `docker/setup-buildx-action` `version` in `preview` and `release` |
| Syft | [tools/versions.env](../../tools/versions.env) `SYFT_VERSION` | `v1.55.0`, the SBOM generator of `release` |
| gh | [tools/versions.env](../../tools/versions.env) `GH_VERSION` | `v2.103.3`, built with `go install` by [install-gh](../../.github/actions/install-gh/action.yml) |

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
| `actions/upload-artifact` | `cf430e030ddbb5b0abf93d22962f4752f3646cd9` (v7.0.2) | `unit`, `chart`, `integration`, `minio`, `release` |
| `actions/attest-build-provenance` | `4d101475d8b20a2381f78447822ac1eab6504dd8` (v4.2.2) | `release` (image and chart, pushed to the registry) |
| `docker/setup-buildx-action` | `f87e5991a6d7451dcb8d9637bfbc97413f497069` (v4.4.1) | `preview`, `release` |
| `docker/login-action` | `81c6f93d4bc991e510b342a00b419b3bb4efc5c6` (v4.7.0) | `preview`, `release` |
| `sigstore/cosign-installer` | `6f9f17788090df1f26f669e9d70d6ae9567deba6` (v4.1.2) | `preview`, `release` |
| `anchore/sbom-action/download-syft` | `e22c389904149dbc22b58101806040fa8d37a610` (v0.24.0) | `release` |
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

## Preview publication

[preview.yml](../../.github/workflows/preview.yml) publishes every commit of `main` whose `ci` run
succeeded (and a manual run of `main`'s head) as a preview build. Release builds of `v0.N.0` tags
are a separate pipeline, [release.yml](../../.github/workflows/release.yml) ([Release pipeline](#release-pipeline)); this workflow never publishes a release version.

| Artifact | Name | Notes |
|---|---|---|
| Image | `ghcr.io/guided-traffic/trove:main-<sha12>` and the moving tag `main` | `linux/amd64` and `linux/arm64`, built by `make image-push` (host build of the binaries with the embedded web build, then the distroless wrap) |
| Chart | `oci://ghcr.io/guided-traffic/charts/trove`, version `0.<next minor>.0-main.<run number>`, appVersion `main-<sha12>` | `image.digest` is set to the pushed image's digest before packaging, so one chart version always means one binary |
| Signatures | keyless cosign (`COSIGN_VERSION` in [tools/versions.env](../../tools/versions.env)) on the image and the chart | identity `https://github.com/guided-traffic/trove/.github/workflows/preview.yml@refs/heads/main`, issuer `https://token.actions.githubusercontent.com`; the job verifies both before it ends |

A cluster that follows the previews uses a Flux `OCIRepository` on the chart with `ref.semver`
over the `-main.N` prereleases and a cosign verification of the identity above. The binary of a
preview build reports the version `dev` unless the commit carries a `v0.*` tag (the binary accepts
only `major.minor.patch` or `dev`).

## Release pipeline

[release.yml](../../.github/workflows/release.yml) publishes a release version `v0.MINOR.PATCH`. It
runs on a pushed `v0.*` tag, and on a manual run that names an existing tag. It publishes the image
`ghcr.io/guided-traffic/trove:<version>` for linux/amd64 and linux/arm64, and the chart
`oci://ghcr.io/guided-traffic/charts/trove` with version and appVersion `<version>`
([R-HA-047](../spec/ha-and-helm.md), Q-HA-11). It never publishes `latest` or `main`; those are
preview tags.

**Source and tooling.** The source is the tag. The release tooling (`.github`, `Makefile`,
`Dockerfile`, `tools/versions.env`) comes from the commit the run started on: a tag push uses the
tag's own tooling, a manual run uses the tooling of `main`. So a tag that predates the tooling can
still be published. Each job checks out the tag and the run's commit, and copies the tooling over the
tag's tree before it reads a pin or calls a composite action.

**Gates.** A run stops before it pushes anything unless:

- the tag has the form `v0.MINOR.PATCH`, the only form the binary accepts as a version;
- a manual run starts from `main`, because the signature identity names the branch of a manual run;
- a `ci` run of the tagged commit succeeded, the same rule that `preview.yml` applies to `main`;
- the GitHub release of the tag exists, because its notes are written last;
- the version is not in ghcr.io yet, unless the `overwrite` input is set. The check runs in `image`
  and `chart` after the login, because an anonymous request to a private package answers like a
  missing one.

**Jobs.**

| Job | What it does |
|---|---|
| `resolve` | Names the tag, checks the gates, reads the commit and its date. The build date is the commit's, so one tag gives one binary |
| `versions` | The pins of Helm, cosign and Node against the installed tools, then `make versions-check` (E0.2) |
| `image` | `make image-push`, the target that `preview.yml` uses: the Angular build (`make web`), both binaries, and the buildx push with the labels source, revision, version and licenses. Then the checks: the binary reports the tag and the commit, every asset of the English index is in both binaries, the pushed index has both platforms. Then the keyless signature, the SPDX SBOM (syft, attested with `cosign attest --type spdxjson`), the SLSA provenance, and the verification of all three |
| `chart` | `helm lint`, `helm package --version --app-version`, a check that the package says so, `helm push`, the keyless signature, the SLSA provenance, and the verification of both |
| `release-notes` | Writes the artifact section into the notes of the release |

**Artifacts.**

| Artifact | Reference | Checked by |
|---|---|---|
| Image | `ghcr.io/guided-traffic/trove:<version>`, the digest of the image index | `cosign verify`, `cosign verify-attestation --type spdxjson`, `gh attestation verify` (job `image`) |
| Chart | `oci://ghcr.io/guided-traffic/charts/trove`, the digest of the chart manifest | `cosign verify`, `gh attestation verify` (job `chart`) |
| Release notes | the section between `<!-- release-pipeline:start -->` and `<!-- release-pipeline:end -->`: both digests, the identity and the verify commands | [release-notes.sh](../../.github/scripts/release-notes.sh) replaces that section and nothing else; the rest of the notes is kept as it was |

The chart does not pin the image by digest. Its `image.tag` is empty, so the image is
`<version>`, the appVersion, as R-HA-031 defines it. The image tag is immutable as long as the
overwrite gate holds.

**Signature identity.** The identity is this workflow file at the ref the run started on:
`https://github.com/guided-traffic/trove/.github/workflows/release.yml@refs/tags/v0.1.0` for a tag push,
and `https://github.com/guided-traffic/trove/.github/workflows/release.yml@refs/heads/main` for a manual
run. The issuer is `https://token.actions.githubusercontent.com`. Each job verifies its own signatures
against the identity of its run, and the release notes print that identity in the verify commands:

```shell
cosign verify ghcr.io/guided-traffic/trove@<digest> \
  --certificate-identity https://github.com/guided-traffic/trove/.github/workflows/release.yml@refs/heads/main \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

**Starting a release by hand.** A manual run takes the tag and, after a failure that came after the
push, `overwrite`. It must start from `main`:

```shell
gh workflow run release.yml --ref main -f tag=v0.1.0
gh workflow run release.yml --ref main -f tag=v0.1.0 -f overwrite=true
```

The second form replaces the versions that exist. The replaced digest loses its tag and is not
deleted.

## Not in place yet

- **The Node-free build and the pinned helm on the runner.** The run of `d153ee3` passed
  `build-no-node` and `versions`, so the `PATH` trimming and the Azure setup actions work on the
  runner image as written. The first run of any new step on a runner is still the first test of that
  step.
- **Chart SBOM.** R-HA-047 asks for an SPDX SBOM on every chart artifact. The chart has a signature
  and a provenance attestation, but no SBOM, because a chart carries no packages of its own.
- **Identity for outside verifiers.** R-HA-047 documents the identity with `refs/tags/v`. A manual run
  signs as `refs/heads/main`, which is the only way to build a tag that predates the workflow, so an
  outside verifier that accepts only tags rejects those signatures. Which identities an outside
  verifier accepts is not decided yet.
- **No environment protection.** The release jobs run without a GitHub environment and without a
  required reviewer. Whoever can push a `v0.*` tag, or start a manual run from `main`, can publish.
- **Binaries are checked after the push.** `make image-push` pushes in one step, so the checks of the
  binaries run after the image is in ghcr.io. A failed check leaves the version published, and the
  next run needs `overwrite`.
- **Tests are not run again.** The release relies on the `ci` run of the tagged commit.
- **The e2e test report in the evidence record.** `coverage-evidence` reads four reports; the e2e report
  (`.out/test-e2e.json`, test names carry the requirement IDs) is not among them, so the requirements it
  proves are not in `implemented.txt` yet. Wiring it needs the coverage generator and the record to
  take a fifth report.
- **Runner capacity of the e2e job.** The suite is sized on a 20-CPU laptop. The runner has 4 vCPUs; if
  k6 drops iterations there, the job fails on `dropped_iterations` instead of passing.
- **Image scanning.** No Trivy or grype job. Adding one needs a pinned scanner version in
  `tools/versions.env`, which this package does not add.
- **Coverage badge.** No `.github/badges/coverage.json` exists, so the README has no coverage badge.
- **shellcheck in CI.** The `workflows` job runs actionlint with shellcheck disabled, because the
  runner's shellcheck is not pinned.
- **Cross-platform rebuilds.** The `image` job builds for `linux/amd64` only.
