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
| Container images (PostgreSQL, PgBouncer, Valkey 9.0 and 9.1, SeaweedFS, the release base image, the dev-profile images) | [tools/versions.env](../../tools/versions.env), each with its digest | `docker buildx imagetools inspect <tag>` prints the digest to pin |
| openssl | `OPENSSL_IMAGE` in [tools/versions.env](../../tools/versions.env), a container image with its digest | never the host's openssl (LibreSSL on macOS, 3.x on Ubuntu): [certs.sh](../../test/integration/certs.sh) and [deploy/dev/valkey-certs.sh](../../deploy/dev/valkey-certs.sh) run it with `docker run --rm --user "$(id -u):$(id -g)" -v <dir>:<dir> -w <dir>`, so the files belong to the caller |
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
| `fmt` | the formatters of `.golangci.yml` (gofumpt, goimports) over `./...`; this rewrites files, so run it on your own working tree |
| `versions-check` | the E0.2 checks listed above |
| `test` | `go test -race ./...`; the `go test -json` stream goes to `.out/test-unit.json`, which the coverage generator reads |
| `integration-up`, `integration-down` | start and stop the integration stack, see below |
| `test-integration` | `go test -tags integration ./test/integration/...` with the environment of `.dev/integration/env`; the JSON stream goes to `.out/test-integration.json` |
| `web` | `cd web && npm ci && npm run build:embed` (the web workspace provides the script) |
| `image` | builds the binary for `linux/<arch>` (host build, with the Angular build embedded) and wraps it in the distroless base of [Dockerfile](../../Dockerfile) |
| `coverage` | `go run ./internal/tools/coverage`, see [coverage.md](coverage.md) |
| `chart-lint`, `chart-test` | call `test/chart/lint.sh` and `test/chart/test.sh`; provided by the chart work package |
| `dev-up`, `dev-down`, `dev-failover`, `dev-valkey-failover`, `dev-valkey-kill`, `dev-valkey-outage` (`DURATION=60s`), `dev-drain`, `e2e` | call `deploy/dev/<name>.sh` (`up`, `down`, `failover`, `valkey-failover`, `valkey-kill`, `valkey-outage`, `drain`, `e2e`); provided by the chart and dev-profile work packages, not implemented in this repository state unless the script exists |

Go commands exclude `web/` from `./...` through the `PKGS` variable: npm packages in
`web/node_modules` can ship Go files, which `./...` would otherwise compile. Plain `go test ./...`
on a checkout with an installed `node_modules` therefore picks them up; use `make test`.

## Test tiers

| Tier | Where | Needs | Run with |
|---|---|---|---|
| Unit | `*_test.go` next to the code | nothing | `make test` |
| Architecture | [internal/archtest](../../internal/archtest) | `go` on `PATH` (it runs `go list -deps -json ./...` and loads the packages with type information) | part of `make test` |
| Integration | [test/integration](../../test/integration), build tag `integration` | the stack below | `make integration-up && make test-integration` |
| End to end | `deploy/dev`, `test/e2e` | a kind cluster | `make dev-up && make e2e` (not yet built) |

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
| `internal/proxy` uses no `pg_advisory*` call | R-ARCH-033 | `CheckSourceBans` |

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

1. creates a throwaway CA and one server certificate in `.dev/integration/certs`
   ([certs.sh](../../test/integration/certs.sh); the certificate covers `localhost`, `127.0.0.1`, `::1`
   and the compose service names; openssl runs from `OPENSSL_IMAGE`, the script needs only Docker);
2. starts [test/integration/compose.yaml](../../test/integration/compose.yaml) with `docker compose up --wait`;
3. creates the buckets (`go run ./test/integration/cmd/seed`);
4. writes `.dev/integration/env`.

`TROVE_TEST_VALKEY_LINE=9.0` or `9.1` (default `9.1`) selects the Valkey line. Switching the line recreates
the stack, because Sentinel rewrote the configuration of the data nodes.

| Service | Container | Host address | Notes |
|---|---|---|---|
| PostgreSQL 16 (the floor version) | `trove-it-postgres` | `127.0.0.1:15432` | user `trove`, database `trove`, superuser; data on tmpfs; no TLS |
| PgBouncer | `trove-it-pgbouncer` | `127.0.0.1:16432` | `pool_mode = transaction`, a catch-all database entry, `max_prepared_statements = 100`; the admin console is the database `pgbouncer` |
| Valkey primary, replica | `trove-it-valkey-primary`, `trove-it-valkey-replica` | `127.0.0.1:16379`, `:16380` | `port 0`, `tls-port`, `tls-replication yes`, ACL file with the users below |
| Sentinels | `trove-it-valkey-sentinel-{1,2,3}` | `127.0.0.1:26381` to `:26383` | TLS only, password-protected, monitor name `trove`, quorum 2, `down-after-milliseconds 3000` |
| SeaweedFS (S3 gateway) | `trove-it-seaweedfs` | `http://127.0.0.1:18333` | path-style, static credentials; buckets `trove-blobs` (versioning on) and `trove-backups` |

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
| `TROVE_TEST_PG_DSN`, `TROVE_TEST_PG_PGBOUNCER_DSN` | PostgreSQL direct and through PgBouncer |
| `TROVE_TEST_PG_HOST`, `_PORT`, `_PGBOUNCER_PORT`, `_USER`, `_PASSWORD`, `_DATABASE` | the same, split up |
| `TROVE_TEST_VALKEY_LINE` | `9.0` or `9.1` |
| `TROVE_TEST_VALKEY_SENTINELS`, `TROVE_TEST_VALKEY_NODES` | comma-separated `host:port` of the Sentinels and the data nodes |
| `TROVE_TEST_VALKEY_PRIMARY_NAME` | the Sentinels' monitor name, `trove` |
| `TROVE_TEST_VALKEY_USERNAME`, `_PASSWORD` | the least-privilege user |
| `TROVE_TEST_VALKEY_SENTINEL_PASSWORD` | the Sentinels' password |
| `TROVE_TEST_VALKEY_ADMIN_USERNAME`, `_ADMIN_PASSWORD` | the unrestricted user |
| `TROVE_TEST_VALKEY_CA_FILE` | absolute path of the CA certificate |
| `TROVE_TEST_VALKEY_KEY_PREFIX` | `trove:`, the prefix the ACL rule allows |
| `TROVE_TEST_VALKEY_CONTAINERS` | container names, for `docker stop` and `docker start` |
| `TROVE_TEST_S3_ENDPOINT`, `_REGION`, `_ACCESS_KEY`, `_SECRET_KEY`, `_FORCE_PATH_STYLE` | the S3 gateway |
| `TROVE_TEST_S3_BUCKET_BLOBS`, `TROVE_TEST_S3_BUCKET_BACKUPS` | bucket names |

`TROVE_TEST_` is a reserved prefix of the configuration loader (see
[configuration.md](configuration.md)), so sourcing the file in a shell does not make `trove` reject its
environment.

The smoke tests in [test/integration/smoke](../../test/integration/smoke) check one thing per
dependency: PostgreSQL 16 direct (including a session advisory lock), PgBouncer in transaction mode,
Valkey discovery through Sentinel over TLS with the ACL user, TLS-only ports, the example ACL rule
(`ACL LOG` stays empty, commands outside the rule are refused), two forced failovers that a host-side
client follows, and put, get and versioning in SeaweedFS with the AWS SDK. The failover test returns the
stack to its original layout by failing over twice.

Packages under `test/` may import pgx, valkey-go and the AWS SDK directly: the architecture test does not
check them.

`make integration-down` removes the containers, volumes and `.dev/integration/env`; the certificates
stay.

## Known gaps

- No CI workflow exists yet (WP0.2). `make lint`, `make test`, `make generate-check` and
  `make versions-check` are what it will run.
- `make lint` reports findings in packages that other work packages own (`internal/tools/coverage`);
  they are not hidden by an exclusion.
