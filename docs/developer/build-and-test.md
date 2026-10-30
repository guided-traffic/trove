# Build, test and lint

Everything runs through the GNU Makefile at the repository root (GNU Make 3.81 is enough, the version
macOS ships). `make help` lists the targets. This page describes what exists today; targets whose
script another work package provides are marked.

## Tool pins

One version per tool (E0.2, R-ARCH-003). Each pin lives in exactly one place, and the Makefile and CI
read it from there.

| Tool | Pinned in | Notes |
|---|---|---|
| Go | the `go` line of [go.mod](../../go.mod) (`1.27.3`) | `make versions-check` fails when `go version` differs |
| sqlc, oapi-codegen | the `tool` directives of [go.mod](../../go.mod) | run as `go tool sqlc …`, `go tool oapi-codegen …`; `versions-check` compares the running version with the module version |
| golangci-lint, kind, kubectl, Helm, k6, CloudNativePG chart | [tools/versions.env](../../tools/versions.env) | `KEY=value` lines; the Makefile `include`s the file and exports its keys, CI appends it to `$GITHUB_ENV` |
| Container images (PostgreSQL, PgBouncer, Valkey 9.1 (the only line tested while trove is in development), SeaweedFS, the release base image, the dev-profile images) | [tools/versions.env](../../tools/versions.env), each with its digest | `docker buildx imagetools inspect <tag>` prints the digest to pin |
| openssl | `OPENSSL_IMAGE` in [tools/versions.env](../../tools/versions.env), a container image with its digest | never the host's openssl (LibreSSL on macOS, 3.x on Ubuntu): [certs.sh](../../test/integration/certs.sh) and the e2e probes ([e2e.md](e2e.md)) run it with `docker run --rm --user "$(id -u):$(id -g)" -v <dir>:<dir> -w <dir>`, so the files belong to the caller |
| Angular packages, Node.js | `web/package-lock.json`, `web/.nvmrc` | owned by the web workspace, see [web-ui.md](web-ui.md) |

golangci-lint is built from source with `go install` into
`.bin/golangci-lint-<version>-<go version>`. The file name carries both inputs, so a change of the pin
or of the Go toolchain builds a new binary and a stale one is never reused. `.bin/` is ignored by git.
`make versions-check` also compares the installed kind, kubectl, Helm and k6 (when present on `PATH`)
with their pins and fails on a difference; a tool that is not installed is not an error there.

## Targets

| Target | What it does |
|---|---|
| `build` | `CGO_ENABLED=0 go build -trimpath` into `bin/trove`, with version, commit and date set through `-ldflags` in [internal/buildinfo](../../internal/buildinfo) |
| `generate` | `go generate` over every package: the configuration schema today, sqlc and oapi-codegen as their packages arrive |
| `generate-check` | `generate`, then `git diff --exit-code` and a check for untracked generated files (E0.1) |
| `lint` | pinned golangci-lint over `./...` (configuration in [.golangci.yml](../../.golangci.yml)), then `go vet` |
| `gosec` | the same golangci-lint with only gosec enabled (CI: GoSec Security Scan) |
| `vuln` | pinned govulncheck over `./...` |
| `cyclo`, `cyclo-report` | pinned gocyclo: fails above `CYCLO_THRESHOLD`; the report lists the most complex functions, tests included |
| `malware-scan` | ClamAV (`clamscan`) over the source tree; needs `clamscan` on `PATH` (CI installs ClamAV) |
| `versions-check` (continued) | also compares the Node.js of `PATH` with `web/.nvmrc` and `docker buildx` with `BUILDX_VERSION` |
| `fmt` | the formatters of `.golangci.yml` (gofumpt, goimports) over `./...`; this rewrites files, so run it on your own working tree |
| `versions-check` | the E0.2 checks listed above |
| `test` | `go test -race ./...` with a coverage profile of the unit tier (`.out/coverage/unit.out`); the `go test -json` stream goes to `.out/test-unit.json`, which the coverage generator reads |
| `integration-up`, `integration-down` | start and stop the integration stack, see below |
| `test-integration` | `go test -tags integration ./test/integration/...` with the environment of `.dev/integration/env`; the JSON stream goes to `.out/test-integration.json`, the profile to `.out/coverage/integration-<line>.out` |
| `minio-up`, `minio-down`, `s3-conformance` | MinIO built at `MINIO_COMMIT`, served over TLS, and the conformance tests against it ([test/integration/minio.sh](../../test/integration/minio.sh)) |
| `coverage-report` | merges `.out/coverage` into `coverage.txt`, `summary.md` and the badge `coverage.json` ([test/coverage/report.sh](../../test/coverage/report.sh)) |
| `test-release-tooling` | `npm ci`, `npm audit signatures`, and [hack/verify-release-tooling.mjs](../../hack/verify-release-tooling.mjs) |
| `web-ci`, `web-lint`, `web-test`, `web-embed` | `npm ci`, the lint script, the Angular unit tests and `npm run build:embed` in `web/`; `web` is `web-ci` and `web-embed` |
| `image` | builds the release image for `linux/<arch>` (`IMAGE`, default `guidedtraffic/trove:dev`) with the [Containerfile](../../Containerfile): the Angular build, the Go build and the distroless base, no Node.js on the host. The binary's version is `VERSION`: the number of a `v0.*` tag on `HEAD` (the `v` stripped), else `dev`, else what the caller passes (CI passes `VERSION=dev`). A commit hash is never a version: the binary would not become ready |
| `npm-stub-image` | builds the counting npm test upstream (`test/npmstub/cmd/npmstub`) for `linux/<IMAGE_ARCH>` on the release base image, tag `NPM_STUB_IMAGE` (default `trove-e2e-npmstub:dev`); see [e2e.md](e2e.md#the-counting-npm-test-upstream) |
| `npm-reference`, `npm-reference-check` | write, or check against a regeneration, the reference lockfile `perf/npm/reference` with the npm of the pinned Node image (`E2E_CLIENTS_NODE_IMAGE`) against the stub; need Docker, see [perf/README.md](../../perf/README.md) |
| `coverage` | `go run ./internal/tools/coverage`, see [coverage.md](coverage.md) |
| `chart-lint`, `chart-test` | call `test/chart/lint.sh` and `test/chart/test.sh`; provided by the chart work package |
| `dev-up`, `dev-down`, `dev-failover`, `dev-valkey-failover`, `dev-valkey-kill`, `dev-valkey-outage` (`DURATION=60s`), `dev-drain`, `dev-valkey-stop`, `dev-valkey-start`, `e2e` | call `deploy/dev/<name>.sh` (`up`, `down`, `failover`, `valkey-failover`, `valkey-kill`, `valkey-outage`, `valkey-stop`, `valkey-start`, `drain`, `e2e`); the Valkey scripts are in [deploy/dev/](../../deploy/dev/); the other scripts of the list were not checked in this pass |

Go commands exclude `web/` from `./...` through the `PKGS` variable: npm packages in
`web/node_modules` can ship Go files, which `./...` would otherwise compile. Plain `go test ./...`
on a checkout with an installed `node_modules` therefore picks them up; use `make test`.

## Test tiers

| Tier | Where | Needs | Run with |
|---|---|---|---|
| Unit | `*_test.go` next to the code | nothing | `make test` |
| Architecture | [internal/archtest](../../internal/archtest) | `go` on `PATH` (it runs `go list -deps -json ./...` and loads the packages with type information) | part of `make test` |
| Integration | [test/integration](../../test/integration), build tag `integration` | the stack below | `make integration-up && make test-integration` |
| End to end | `deploy/dev`, `test/e2e` | a kind cluster | `make dev-up && make e2e`; `SET=access` runs the access-core scenarios (`test/e2e/e1_*`, `m1_env_test.go`). `make dev-valkey-stop` and `make dev-valkey-start` hold a Valkey outage across a criterion of unknown length |

A test that verifies a requirement carries the requirement ID in a subtest name, for example
`t.Run("R-ARCH-002 forbidden import names the edge", …)`. The coverage generator reads the IDs from the
`go test -json` stream ([coverage.md](coverage.md)). Go replaces spaces by underscores in the name; the
ID survives.

### Architecture test

[internal/archtest](../../internal/archtest) turns the structural rules of the specification into tests.
Each rule is a function over the package graph or the source tree, and each has a test that feeds it a
synthetic violation and asserts that the offending edge is named.

| Rule | Requirement | Function |
|---|---|---|
| A lower layer never imports a higher one; every package under `internal/` has a layer | R-ARCH-002 | `CheckLayers` ([layers.go](../../internal/archtest/layers.go) holds the table) |
| pgx only in `meta/postgres` and `cluster`; valkey-go only in `cluster`; the AWS SDK only in `blob/s3` | R-ARCH-002 | `CheckImportBans` |
| `database/sql` and the pgx stdlib adapter nowhere | R-ARCH-026 | `CheckImportBans` |
| Formats import no infrastructure package | R-ARCH-008 | `CheckModuleImportBans` |
| `http.Client{` and `net.Dial` only in `internal/outbound` and `_test.go` files | R-ARCH-002, R-ARCH-022 | `CheckOutbound` |
| No package-level variable of map, slice, pointer or `sync.Map` type outside `cache`, `cluster`, `observability` and the start-up registries | R-ARCH-032 | `CheckPackageState` |
| Nothing below `internal/blob` imports the Valkey client or uses a session-level advisory lock | R-STOR-046 | `CheckStorageValkey`, `CheckSourceBans` |
| `internal/proxy` and `internal/virtual` use no `pg_advisory*` call | R-ARCH-033 | `CheckSourceBans` |

To add a package: give it a layer in `layerTable`. A package under `internal/` without a layer fails the
test and says so. The trees `internal/archtest`, `internal/tools`, `test`, `tools`, `perf`, `web` and
`deploy` are not checked.

Two behaviours of the state check are decisions, not accidents. Variables whose type is an interface (an
error sentinel, `var _ I = (*T)(nil)`) are not state of the forbidden kinds. A pointer to `regexp.Regexp`
is exempt, because a compiled expression is immutable; every other pointer is reported. Generated files
and variables with a `go:embed` directive are skipped.

The fixtures are small modules under [internal/archtest/testdata](../../internal/archtest/testdata);
they are real Go modules, so `go list` and the type loader run on them unchanged.

## Integration stack

`make integration-up` runs [test/integration/up.sh](../../test/integration/up.sh), which

1. creates a throwaway CA and one server certificate per service in `.dev/integration/certs`
   ([certs.sh](../../test/integration/certs.sh); every certificate covers `localhost`, `127.0.0.1`, `::1`
   and the compose service names; openssl runs from `OPENSSL_IMAGE`, the script needs only Docker);
2. starts [test/integration/compose.yaml](../../test/integration/compose.yaml) with `docker compose up --wait`;
3. creates the buckets (`go run ./test/integration/cmd/seed`);
4. writes `.dev/integration/env`.

**Every connection is TLS (INV-20, R-HA-063).** PostgreSQL runs with `ssl=on` and a
[pg_hba.conf](../../test/integration/conf/pg_hba.conf) that has `hostssl` lines only, so a client without TLS is
refused. PgBouncer takes clients with `client_tls_sslmode = require` and talks to PostgreSQL with
`server_tls_sslmode = verify-full` against the stack's CA. The SeaweedFS S3 gateway serves HTTPS only
(`-s3.cert.file`, `-s3.key.file`); a plaintext request to its port is answered with a 400 by the TLS
server and never reaches the S3 handler. Valkey and the Sentinels were TLS-only already. The tests verify the
servers from the host (`sslmode=verify-full`, trove's `ssl_mode: verify-full` with `ssl_root_cert_file`,
`storage.s3.ca_file`) and no integration config uses `allow_insecure`, `allow_insecure_endpoint` or
`ssl_mode: disable`, with two deliberate exceptions that test the refusal itself: the Valkey test
"R-HA-056 the data node accepts TLS only" configures a client without TLS to prove it never connects,
and the PostgreSQL test "R-HA-015 ssl_mode disable without allow_insecure is refused" proves the
configuration check. Every `TestMain` of a suite calls `testenv.Main`, which fails the whole package before
any test when a `TROVE_TEST_*` endpoint is plaintext or unverified or the CA file is missing; the smoke tests
`TestPostgresIsTLSOnly` and `TestS3IsTLSOnly` prove the refusal against the running stack.

What is not covered: the SeaweedFS master, volume and filer talk to each other plaintext inside the one
container (loopback, not published on the host); the Unix socket of PostgreSQL inside its container
uses `trust` for the image's init and `pg_isready`; and the certificates are RSA 2048 signed for 365 days,
fine for a test stack and not a model for production.

`TROVE_TEST_VALKEY_LINE=9.0` or `9.1` (default `9.1`) selects the Valkey line. Switching the line recreates
the stack, because Sentinel rewrote the configuration of the data nodes.

| Service | Container | Host address | Notes |
|---|---|---|---|
| PostgreSQL 16 (the floor version) | `trove-it-postgres` | `127.0.0.1:15432` | user `trove`, database `trove`, superuser; data on tmpfs; TLS only (`hostssl`) |
| PgBouncer | `trove-it-pgbouncer` | `127.0.0.1:16432` | `pool_mode = transaction`, a catch-all database entry, `max_prepared_statements = 100`; the admin console is the database `pgbouncer`; TLS in (`require`) and out (`verify-full`) |
| Valkey primary, replica | `trove-it-valkey-primary`, `trove-it-valkey-replica` | `127.0.0.1:16379`, `:16380` | `port 0`, `tls-port`, `tls-replication yes`, ACL file with the users below |
| Sentinels | `trove-it-valkey-sentinel-{1,2,3}` | `127.0.0.1:26381` to `:26383` | TLS only, password-protected, monitor name `trove`, quorum 2, `down-after-milliseconds 3000` |
| SeaweedFS (S3 gateway) | `trove-it-seaweedfs` | `https://127.0.0.1:18333` | HTTPS only, path-style, static credentials; buckets `trove-blobs` (versioning on) and `trove-backups`; `-volume.max=100` (a bucket is a collection that grows several volumes, so a low count runs out after a few runs of the 500 MiB upload test), set in `compose.yaml` only: `make integration-up` is the one way the stack starts, locally and in CI. |

**How a client on the host follows a failover.** The five Valkey processes run in separate containers
that share one network namespace (the container `trove-it-net`), and every port is published on the host
under the same number. `replica-announce-ip` and `sentinel announce-ip` are `127.0.0.1`, so the addresses
that Sentinel returns are valid inside the stack (replication, monitoring) and on the host (the tests).
There is no name resolution to configure. A process can still be stopped on its own
(`docker stop trove-it-valkey-primary`), which is how a failure is simulated. Stopping `trove-it-net`
takes all five down.

**Valkey users** (test constants, in [conf/users.acl](../../test/integration/conf/users.acl)):
`trove` is the least-privilege user of R-ARCH-031's example rule; `ops` may do everything and serves
replication, Sentinel monitoring and the tests' inspection; the `default` user is off. The Sentinels
carry the password from `.dev/integration/env`.

The environment file holds these variables; [test/integration/testenv](../../test/integration/testenv)
reads them and fails the test with a message that names the variable when one is missing. Integration
tests never skip.

| Variable | Meaning |
|---|---|
| `TROVE_TEST_CA_FILE` | absolute path of the stack's CA certificate (`.dev/integration/certs/ca.crt`) |
| `TROVE_TEST_CERT_DIR` | the directory of the stack's certificates and keys |
| `TROVE_TEST_PG_DSN`, `TROVE_TEST_PG_PGBOUNCER_DSN` | PostgreSQL direct and through PgBouncer, `sslmode=verify-full&sslrootcert=<CA>` |
| `TROVE_TEST_PG_HOST`, `_PORT`, `_PGBOUNCER_PORT`, `_USER`, `_PASSWORD`, `_DATABASE` | the same, split up |
| `TROVE_TEST_VALKEY_LINE` | `9.0` or `9.1` |
| `TROVE_TEST_VALKEY_SENTINELS`, `TROVE_TEST_VALKEY_NODES` | comma-separated `host:port` of the Sentinels and the data nodes |
| `TROVE_TEST_VALKEY_PRIMARY_NAME` | the Sentinels' monitor name, `trove` |
| `TROVE_TEST_VALKEY_USERNAME`, `_PASSWORD` | the least-privilege user |
| `TROVE_TEST_VALKEY_SENTINEL_PASSWORD` | the Sentinels' password |
| `TROVE_TEST_VALKEY_ADMIN_USERNAME`, `_ADMIN_PASSWORD` | the unrestricted user |
| `TROVE_TEST_VALKEY_CA_FILE` | absolute path of the CA certificate (the same file as `TROVE_TEST_CA_FILE`) |
| `TROVE_TEST_VALKEY_KEY_PREFIX` | `trove:`, the prefix the ACL rule allows |
| `TROVE_TEST_VALKEY_CONTAINERS` | container names, for `docker stop` and `docker start` |
| `TROVE_TEST_S3_ENDPOINT` (`https://127.0.0.1:18333`), `_REGION`, `_ACCESS_KEY`, `_SECRET_KEY`, `_FORCE_PATH_STYLE` | the S3 gateway |
| `TROVE_TEST_S3_BUCKET_BLOBS`, `TROVE_TEST_S3_BUCKET_BACKUPS` | bucket names |

`TROVE_TEST_` is a reserved prefix of the configuration loader (see
[configuration.md](configuration.md)), so sourcing the file in a shell does not make `trove` reject its
environment.

**Certificate layout** in `.dev/integration/certs` (keys are world-readable on purpose, the stack protects
nothing):

| Files | Used by |
|---|---|
| `ca.crt`, `ca.key` | the CA; `ca.crt` is `TROVE_TEST_CA_FILE` |
| `server.crt`, `server.key` | the five Valkey processes |
| `postgres.crt`, `postgres.key` | PostgreSQL (copied by [conf/postgres.sh](../../test/integration/conf/postgres.sh) to a directory owned by `postgres` with mode 0600, which the server insists on) |
| `pgbouncer.crt`, `pgbouncer.key` | PgBouncer, client side |
| `s3.crt`, `s3.key` | the SeaweedFS S3 gateway |

Every certificate has the SANs `localhost`, `127.0.0.1`, `::1` and the service names, so one name list
verifies from the host and between containers.

**Helpers for tests.** `testenv.CAFile`, `testenv.CertDir`, `testenv.RootCAs`, `testenv.TLSConfig` and
`testenv.HTTPClient` give a client that trusts exactly the stack's CA; `testenv.PgDSN(tb, database)` and
`testenv.PgBouncerDSNFor` build verify-full DSNs; `testenv.SecureDatabase` and `testenv.SecureS3` set
trove's `database.ssl_mode`/`ssl_root_cert_file` and `storage.s3.endpoint`/`ca_file`. A new suite starts with
`func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }`.

**Rotating a certificate in place.** [rotate-cert.sh](../../test/integration/rotate-cert.sh)
`<postgres|pgbouncer|valkey|s3> [days]` re-issues that service's certificate with a new key and serial from
the existing CA and makes the running service serve it: PostgreSQL by `pg_reload_conf()`, PgBouncer by
SIGHUP, Valkey by `CONFIG SET tls-cert-file` on all five processes, and the S3 gateway by a container restart
(SeaweedFS reads the pair at start only; the script waits for health). The CA does not change, so clients
keep verifying. Pass a short `days` to test expiry. A test shells out to the script from the repository
root, as the Valkey tests do with `docker`.

The smoke tests in [test/integration/smoke](../../test/integration/smoke) check one thing per
dependency: PostgreSQL 16 direct (including a session advisory lock), PgBouncer in transaction mode,
Valkey discovery through Sentinel over TLS with the ACL user, TLS-only ports, the example ACL rule
(`ACL LOG` stays empty, commands outside the rule are refused), two forced failovers that a host-side
client follows, and put, get and versioning in SeaweedFS with the AWS SDK. The failover test returns the
stack to its original layout by failing over twice.

Packages under `test/` may import pgx, valkey-go and the AWS SDK directly: the architecture test does not
check them.

`make integration-down` removes the containers, volumes and `.dev/integration/env`; the certificates
stay. A stack created before the per-service certificates existed is recreated by `make integration-up`
(`certs.sh` sees the missing files and issues a new set).

## Known gaps

- The CI workflow [release.yml](../../.github/workflows/release.yml) calls these targets; it has not run on
  GitHub yet. See [releasing.md](releasing.md#not-in-place-yet).
- `make lint` reports findings in packages that other work packages own (`internal/tools/coverage`);
  they are not hidden by an exclusion.
