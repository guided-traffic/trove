# Configuration framework

The schema is one Go struct tree in [internal/config](../../internal/config) (R-ARCH-020). Each key's
tags give its YAML name, default, validation and secret flag. The loader, the environment variable
names, the JSON Schema and the redacted effective view are all derived from the tree by reflection, so
no key can exist outside it. The user-facing rules (file location, precedence, strict validation) are
specified in [operations-and-observability.md](../spec/operations-and-observability.md) R-OPS-028.

## Layout

| File | Content |
|---|---|
| [config.go](../../internal/config/config.go) | the root struct `Config`, one field per section; the `go:generate` line |
| [process.go](../../internal/config/process.go), [server.go](../../internal/config/server.go) with [tls.go](../../internal/config/tls.go) (`server.tls`, `server.ops_tls`), [database.go](../../internal/config/database.go), [migrate.go](../../internal/config/migrate.go), [runtime.go](../../internal/config/runtime.go), [valkey.go](../../internal/config/valkey.go), [storage.go](../../internal/config/storage.go), [jobs.go](../../internal/config/jobs.go), [cluster.go](../../internal/config/cluster.go), [cache.go](../../internal/config/cache.go), [secrets.go](../../internal/config/secrets.go), [system.go](../../internal/config/system.go), [health.go](../../internal/config/health.go), [log.go](../../internal/config/log.go), [metrics.go](../../internal/config/metrics.go), [trace.go](../../internal/config/trace.go), [audit.go](../../internal/config/audit.go), [maintenance.go](../../internal/config/maintenance.go), [outbound.go](../../internal/config/outbound.go) | one section each (`Process`, `Server`, `Database`, `Migrate`, `Runtime`, `Valkey`, `Storage`, `Jobs`, `Cluster`, `Cache`, `Secrets`, `System`, `Health`, `Log`, `Metrics`, `Tracing`, `Audit`, `Maintenance`, `Proxy` with `Outbound`); the owner of a subsystem edits only its file |
| [outbound.go](../../internal/config/outbound.go), [npm.go](../../internal/config/npm.go), [nuget.go](../../internal/config/nuget.go) (M3: `nuget.archive`, `nuget.registration`, `nuget.search`), [ui.go](../../internal/config/ui.go), [config_as_code.go](../../internal/config/config_as_code.go) | the sections of M2: `proxy.*` (inside the `>>> M2 proxy` markers of `Proxy`), `npm`, `ui` (README rendering and client setup) and `config_as_code.secrets_dir`, see [M2 keys](#m2-keys) |
| [schema.go](../../internal/config/schema.go) | `Describe`: flattens the tree into `Key`s, checks tags, derives environment names |
| [load.go](../../internal/config/load.go) | `Load`: defaults, file, environment, flags; strict matching; secret resolution; validation |
| [m1.go](../../internal/config/m1.go), [login.go](../../internal/config/login.go), [auth_identity.go](../../internal/config/auth_identity.go), [authz.go](../../internal/config/authz.go) | the sections of the M1 areas (`auth`, `authz`, `api`, `repositories`); in `auth` the login keys `auth.oidc`, `auth.login_page`, `auth.sessions`, `auth.break_glass` ([login.md](login.md#configuration)). `auth.oidc.providers` is a map of nested structs whose keys left out are filled by `AuthOIDCProvider.Defaulted`, not by `default` tags |
| [endpoints.go](../../internal/config/endpoints.go) | `server.endpoints` and `server.endpoint_rate_limits`: the endpoint entries, their defaults (`ResolveEndpoints`) and validation, see [endpoints.md](endpoints.md) |
| [rules.go](../../internal/config/rules.go), [value.go](../../internal/config/value.go), [units.go](../../internal/config/units.go) | `validate` tag rules, text and YAML value parsing, `ByteSize` |
| [secret.go](../../internal/config/secret.go), [report.go](../../internal/config/report.go) | the `Secret` type; `Report`, `Validator`, `CommandValidator`, `Command`, `Error` |
| [effective.go](../../internal/config/effective.go), [jsonschema.go](../../internal/config/jsonschema.go), [schemagen](../../internal/config/schemagen/main.go), [schema.json](../../internal/config/schema.json), [bastion.schema.json](../../internal/config/bastion.schema.json) | redacted view, JSON Schema generator and its committed outputs (the main tree's and the role bastion's) |
| [bastions.go](../../internal/config/bastions.go), [bastion_role.go](../../internal/config/bastion_role.go) | the main installation's section `bastions`, and the configuration tree of the role bastion (`BastionConfig`, `LoadBastion`), see [Bastion keys](#bastion-keys) |

Sections that exist in the specification but have no owner yet are not in the tree: `backup`, `retention`,
`ci`, `proxy` (apart from the keys listed under [M2 keys](#m2-keys)), `support`,
`config_as_code` (apart from `secrets_dir`), `webhooks`, `mail`, `content_types`. Of `ui`, only the keys under
[M2 keys](#m2-keys) exist. Strict validation rejects a file that sets them
until their owner adds the section. The chart must render only keys the tree knows.

## Tags

| Tag | Meaning |
|---|---|
| `yaml:"name"` | required on every exported field; snake_case. The key path is the dotted chain of names |
| `default:"…"` | the default as text, parsed like an environment value: `10s`, `256MiB`, `true`, `["a"]`. A key without `default` has the zero value (no default) |
| `validate:"…"` | comma-separated rules: `required`, `min=<bound>`, `max=<bound>`, `oneof=a\|b\|c`. Bounds read in the unit of the type (`5s`, `5MiB`, `2`); for strings and collections they bound the length |
| `secret:"file"` | on a string field named `…_file`: a file-only secret, a path that is read again on every use. The loader does not open it |
| `doc:"…"` | description for the JSON Schema |

Supported types: `string`, `bool`, `int`, `int32`, `int64`, `float64`, `time.Duration`, `config.ByteSize`,
`[]string`, lists of flat structs (`server.tls.certificates`), `map[string]T`, nested structs, and `config.Secret`.
Lists and maps are one key; their environment value is JSON (a YAML flow list works too; a bare or comma-separated text is refused naming the variable, so `TROVE_JOBS_QUEUES='["index"]'` is the form of the worker pools' queue list, Q-ARCH-304):
`TROVE_SERVER_TLS_CERTIFICATES='[{"cert_file":"/tls/tls.crt","key_file":"/tls/tls.key"}]'` # example. A list
element has scalar fields with snake_case `yaml` names only (`Describe` refuses anything else), is decoded strictly
(an unknown field is an error), and appears in the JSON Schema as `items` with its properties; `validate:"required"`
on an element field marks it required there, and the section's `Validate` enforces it, because elements are not keys.

Cross-field rules go on the section as `func (s *Section) Validate(r *Report)` (runs on every load,
messages read `<section>.<key> <problem>`). A rule that applies only to some commands is
`ValidateFor(cmd Command, r *Report)`: `serve` requires the role, a Valkey address, the bucket and the key
ring; `migrate` requires none of them.

## Environment variables

`TROVE_` plus the key path, upper-cased, segments joined by `_`: `log.request.enabled` is
`TROVE_LOG_REQUEST_ENABLED`. The names are generated and matched exactly, never split. `Describe` fails
when two keys derive the same name (for example `a.b_c` and `a_b.c`) or a field has no yaml tag; both are
unit tests (R-ARCH-020).

Reserved, not keys: `TROVE_CONFIG_FILE`, `TROVE_POD_NAME`, `TROVE_POD_NAMESPACE`, `TROVE_NODE_NAME`,
`TROVE_POOL_NAME`, `TROVE_CAC_*`, and `TROVE_TEST_*` (the integration environment, see
[build-and-test.md](build-and-test.md)). Any other `TROVE_` variable that is not a key stops the load and
names the closest valid one.

## Secrets

Two shapes:

- **File-only.** A string field `…_file` tagged `secret:"file"`: `database.password_file`,
  `valkey.password_file`, `storage.s3.credentials.secret_access_key_file`, `metrics.bearer_token_file`,
  TLS private keys (`database.ssl_key_file`, `valkey.tls.key_file`, `key_file` of each `server.tls.certificates`
  entry, which the JSON Schema marks `x-trove-secret: file-only`). The consumer reads the file on every use, so a rotated Secret applies without a
  restart. The effective view shows the path, which is not a secret.
- **Value and file.** A field of type `config.Secret` expands to two keys, `<name>` and `<name>_file`.
  The value may come from the environment (`TROVE_<NAME>`); written inline into the YAML file it stops
  the load with an error that names `<name>_file` and the variable. Both set is an error. The loader reads
  the file once; the effective view shows `[REDACTED]` with the source `file:<path>`. No section uses this
  shape yet (the specification's example is `mail.password`); the mechanism is covered by unit tests with
  a synthetic tree.

## Precedence and sources

Defaults, then the file, then the environment, then flags (`--role` is `process.role`, `--log-level` is
`log.level`). The file is `--config`, else `TROVE_CONFIG_FILE`, else `/etc/trove/trove.yaml`. A file named
explicitly must exist; a missing default file is not an error, so a replica can run from defaults and
environment.

Every key has a source: `default`, `file`, `env`, `flag:<name>` or `file:<path>` (a value read from a
secret file). `trove admin config validate [--config <file>] [--format table|json]` prints every key with
its value, source and environment variable; `--format json` has the shape of
`GET /api/v1/system/config`. All problems are reported in one pass; a type error, a range error, an unknown
key (with its line and the closest key) and an unknown variable are all listed together.

## Adding a key

1. Add the field to the section struct with `yaml`, `default`, `validate` and `doc` tags. Use a nested
   struct for a group. Use `time.Duration` and `ByteSize`, never an integer with a unit in its name.
2. A secret path is a `…_file` string with `secret:"file"`.
3. A cross-field rule goes into the section's `Validate`; a command-dependent requirement into
   `ValidateFor`.
4. Run `go generate ./internal/config` (or `make generate`) and commit the changed
   [schema.json](../../internal/config/schema.json). A unit test fails when it is stale.
5. Add a test for the rule. A test that verifies a requirement carries the ID in a subtest name.
6. Update the owning spec page's YAML block and this table of sections when the key is new to the spec.

A new section is a new file next to the others and a field on `Config`.

## JSON Schema

`go generate ./internal/config` writes [schema.json](../../internal/config/schema.json) (draft 2020-12):
`additionalProperties: false` at every level, `required` for keys tagged `required`, `enum` for `oneof`,
`default`, `minimum`/`maximum` for numbers, and the extensions `x-trove-env` (the variable),
`x-trove-secret` (`env-only`, `file` or `file-only`) and `x-trove-type` (`duration`, `bytes`). Durations
are strings, sizes are strings or integers. The chart may reference or copy the file to validate
`config:` values; the loader does not read it.

## Declared role bindings

`authz.role_bindings` ([authz.go](../../internal/config/authz.go), `AuthzRoleBinding`) is a list of structs: `role` and
exactly one of `group`, `user`, `service_account`, in the environment as JSON (`TROVE_AUTHZ_ROLE_BINDINGS`). `Authz.Validate`
checks what can be checked without a database (R-AUTHZ-010): a role of `SystemRoles()`, one principal, a valid name, never
`anonymous` or `authenticated`, no entry twice. Only `trove migrate` reads the list ([authorization.md](authorization.md#role-bindings-from-the-configuration)).

## CI keys

The `ci` section ([ci.go](../../internal/config/ci.go), [ci-oidc.md](ci-oidc.md)): `ci.signing_keys.*`, `ci.exchange.*` with `rate_limit.*`,
`ci.mappings.*`, `ci.token.*`, `ci.replay.*`, `ci.issuances.retention` and the list `ci.issuers` (R-CI-001, Q-CI-16), each with the default of the
specification. `Config.validateCI` refuses `ci.token.max_ttl` above `auth.tokens.cit.max_ttl` and names both keys (R-CI-016), `ci.mappings.max_per_project`
(the mappings of one project's tier on one issuer, default `100`, Q-CI-49) above `ci.mappings.max_per_issuer`, `ci.job_token_fallback.enabled`
(R-CI-022) and bad declared issuers (name, https URL, algorithms, skew, a `max_ttl_seconds` outside `ci.token.min_ttl` to `ci.token.max_ttl`); the
schema bounds `ci.replay.retention_after_expiry` at `1m` at least (Q-CI-51); the retired key `ci.exchange.rate_limit.per_issuer_per_second` is refused in the
file and in the environment with the names of its replacements (`retiredKeys`, [load.go](../../internal/config/load.go), R-CI-021). A list element may hold
scalars, pointers to scalars (a key left out is nil and takes the default of R-CI-001) and lists of strings. Only `trove migrate` applies `ci.issuers`.

## Vulnerability keys

The `vulnerabilities` section ([vulnerabilities.go](../../internal/config/vulnerabilities.go), [vulnerabilities.md](vulnerabilities.md)): `enabled`, `enforcement` (`enforce` or `report`),
`stale_after`, `sources.osv.*` (`mode`, `base_url`, `ecosystems`, `repository`, `full_resync_threshold`, `overlap`, `max_shrink`, `timeout`, `max_archive_size`, `max_record_size`),
`sources.kev.*`, `sources.epss.*` (P1: only `off` is accepted), `matching.public_upstreams` (a map of format ID to hosts, replaced as a whole), `findings.resolved_retention` and
`export.max_rows`, each with the default of R-VULN-025. The four maintenance tasks `vuln-sync-osv`, `vuln-sync-osv-full`, `vuln-sync-kev` and `vuln-findings-retention` are in the default of
`maintenance.tasks`. `Vulnerabilities.Validate` refuses what R-VULN-025's criterion names, each time with the key: `max_shrink` outside 0 to 1, an `ecosystems` entry of no format of this
release (the fixed list `npm`, `NuGet`, spelled as OSV spells them), `mode: repository` without `repository`, a source URL that is not an https URL (INV-20), and an `epss` mode other than
`off`. A source URL is checked also while its source is off, so a typo does not wait for the day the mode changes.

## Bastion keys

Two trees serve the Bastion ([bastion.md](bastion.md), Q-BAS-50).

**The main installation's keys** ([bastions.go](../../internal/config/bastions.go), the `bastions` section; `AuthTokenBST` in
[auth_identity.go](../../internal/config/auth_identity.go)): `bastions.max_registrations` (`16`, 1 to 256), `bastions.trusted_proxies` (`[]`,
addresses or CIDRs as `server.trusted_proxies`, no `/0`; `Bastions.Validate`, `TrustedProxyPrefixes`), `bastions.check.max_repositories`
(`16`), `bastions.feed.lookback` (`5m`, 30s to 1h) and `bastions.feed.max_page` (`1000`), `bastions.downloads.max_batch` (`1000`),
`bastions.downloads.max_age` (`720h`), `bastions.downloads.events_per_second` (`200`) and `events_burst` (`2000`, at least 100, holding `max_batch`
in units of `config.DownloadEventsPerUnit` (100), a window of at most 12 h), `auth.tokens.bst.default_ttl` (`8760h`) and `max_ttl` (`9480h`, `default_ttl` must not exceed it) and
`auth.tokens.max_active_per_principal.bst` (`3`). `bst` in an endpoint's `auth.token_types` stops startup naming the key
([endpoints.go](../../internal/config/endpoints.go), R-BAS-005).

**The role's tree** ([bastion_role.go](../../internal/config/bastion_role.go)): a second root struct `BastionConfig` with the sections `server`
(listen, ops_listen, base_url, trusted_proxies, forwarded_header, max_in_flight, tls, ops_tls), `bastion` (main, answers, feed, cache,
limits, failed_auth, blocking, audit, token, replicas, signing_key_file), `valkey` (the main section's keys with `mode: none` as the default),
`log`, `tracing` (without `incoming_context` and `trusted_peers`: the role never continues a caller's trace context) and `runtime`. It is
loaded by `config.LoadBastion`, which reuses the loader with `Options.Role` set: a key of any other section, in the file or the
environment, stops startup with a message that names the key and the role (`database.host is not a key of the role bastion`), a mapping
is walked so that the leaf is named, and `--role bastion` is the only way to select it (`process.role` is not a key of the tree). The
only flag is `--log-level`. `http` in `bastion.main.url` is refused naming the key; the defaults and ranges are those of R-BAS-046
(`TestR_BAS_001_TheRoleLoadsWithTheMinimalKeysAndTheSpecDefaults`, `TestR_BAS_046_TheRangesOfTheSpecificationAreEnforced`).

Keys the chart renders carry the tag `chart:"managed"` (on a field or a whole struct); `Key.ChartManaged` and the schema node
`x-trove-chart-managed: true` carry it to the chart, which refuses a value under `config` for them (R-BAS-046). The role's JSON
Schema is [bastion.schema.json](../../internal/config/bastion.schema.json), written by the same `go generate ./internal/config` as
`schema.json` (`schemagen -bastion-out`), committed and checked by `make generate-check`; the chart `trove-bastion` includes it as the
schema of its `config` value (R-HA-029's mechanism). The sections the role does not have (`health`, `metrics`) are used at their
defaults through `BastionConfig.HealthSection` and `MetricsSection`; `ServerSection` returns the server section in the form the
listeners read.

## Endpoint keys

`server.endpoints` (a map of endpoint name to entry) and `server.endpoint_rate_limits` are one key each, so their environment
value is JSON (`TROVE_SERVER_ENDPOINTS='{"customers":{"listen":":8443","base_url":"https://packages.example.com"}}'`
# example). A map of structs is decoded strictly like a list of structs, but its elements may be nested (`tls`, `auth`);
`Describe` accepts any `map[string]T`, and the JSON Schema shows the key as an object. The keys, their defaults and
the validation rules are on [endpoints.md](endpoints.md#configuration). Defaults that differ between the default and a
further endpoint are not `default` tags: `Endpoint` uses pointers and nil lists for "unset", `Server.ResolveEndpoints`
applies the defaults of the kind, and the root's `Finalize` writes them into the entries so that `trove admin config validate`
prints every key with its value.

## TLS keys of the listeners

`server.tls` (client listener) and `server.ops_tls` (ops listener), [tls.go](../../internal/config/tls.go). A listener
serves TLS when its `certificates` list is non-empty. Behaviour and rotation: [tls.md](tls.md).

```yaml
server:
  tls:
    certificates: []                    # default: plain HTTP; one or more pairs, SNI picks, the first is the default
    # certificates:                     # example
    #   - cert_file: /var/run/secrets/trove/tls/tls.crt
    #     key_file: /var/run/secrets/trove/tls/tls.key
    client_ca_file: ""                  # default: no client certificates
    client_auth: none                   # default; none | verify_if_given | require (needs client_ca_file)
    min_version: "1.2"                  # default; "1.2" | "1.3"
    reload_interval: 10s                # default; at least 1s
  ops_tls:
    inherit: true                       # default: server.tls's certificates, min_version, reload_interval; no client certificates
    certificates: []                    # default; with inherit false: the ops listener's own pairs
    client_ca_file: ""                  # default
    client_auth: none                   # default
    min_version: "1.2"                  # default
    reload_interval: 10s                # default
```

Rules (`Server.Validate`): every entry names both files, no `cert_file` twice; `client_auth` other than `none` needs
`client_ca_file`, and `client_ca_file` with `client_auth: none` is refused (it would look like mutual TLS and be none);
client certificates need certificates on the listener; with `inherit: true` every other `ops_tls` key must keep its
default (it would be ignored); with `inherit: false` and no certificates the ops listener would be plain HTTP beside a
TLS client listener, which is refused. `database.ssl_cert_file` and `database.ssl_key_file` are set together.

## Memory and load protection keys

No key of its own: the memory guard of step 4 (R-ARCH-005, [architecture.md](architecture.md#stage-4-and-the-memory-guard)) is
sized from keys that exist for other reasons. With the chart, `runtime.memory_limit_bytes` comes from `limits.memory`.

```yaml
runtime:
  memory_limit_ratio: 0.85            # default; the Go memory limit is this × memory_limit_bytes, unless GOMEMLIMIT is set
  memory_limit_bytes: 0               # default: none known, no Go limit and no memory guard
server:
  max_in_flight: 4096                 # default; per endpoint, asked before the memory slots
storage:
  download:
    buffer_size: 256KiB               # default; with hold_back, the buffer one download holds
    hold_back: 64KiB                  # default
  s3:
    http:
      receive_window: 256KiB          # default; 16KiB to 1GiB; the HTTP/2 window per object, Go's default is 4 MiB
```

Memory slots = ⌊(0.8 × Go limit − the bytes the caches hold now) ÷ (class of max(`buffer_size`, 2 × `hold_back`, 4 KiB) + `receive_window` + 128 KiB)⌋,
at least 1 ([Q-ARCH-328](../spec/architecture.md#q-arch-328--admission-charges-the-bytes-a-request-holds-and-the-caches-hold-now-not-a-worst-case-per-request-and-the-caches-budgets)).
A request is charged that much at admission, and a download that knows its size settles to its buffer class, `min(size, receive_window)`
and 128 KiB. The caches count by what they hold, not by their budgets (`cache.rendition_bytes` + `proxy.metadata_cache_bytes` +
`proxy.merged_metadata_cache_bytes` + `proxy.query_cache_bytes`, 648 MiB by default, [Q-ARCH-243](../spec/architecture.md#q-arch-243--the-memory-slots-leave-the-caches-byte-budgets-aside)):
empty caches 1,114 slots for 1 GiB, 278 for 256 MiB and 2,228 for 2 GiB; with the default caches full 77 for a 1 GiB
container and 1,191 for 2 GiB, which is the floor the chart checks. The 80 % and 70 % of the heap gate are constants
([Q-ARCH-240](../spec/architecture.md#q-arch-240--a-replica-admits-what-its-memory-holds-memory-slots-and-a-heap-gate-in-step-4)).

## M2 keys

The keys of the remote engine (`proxy.*` beside `proxy.outbound`), the virtual engine, the npm format and, from M3, the NuGet format
([nuget.go](../../internal/config/nuget.go), `Config.validateNuGetCoupling`; the symbol and vulnerability keys arrive with R-NUGET-021 and R-NUGET-023). The per-repository
settings of a remote repository (timeouts, cache periods, `limits.*`) are in the repository document
([repositories.md](repositories.md)); the keys here are the system-wide values and bounds. Ranges are enforced by the
`validate` tags; the rules across keys are in `Proxy.Validate` and in `Config.validateNPMCoupling`.

```yaml
proxy:
  lock_poll_interval_ms: 1000        # default; 50 to 60000, below fetch_wait_timeout_seconds (R-REPO-026)
  fetch_wait_timeout_seconds: 120    # default; 1 to 3600 (R-REPO-026)
  negative_l1_entries: 100000        # default; 1 to 10000000, per replica (R-REPO-028)
  max_redirects: 5                   # default; 0 to 20 (R-REPO-039)
  max_artifact_bytes: 10GiB          # default; at least 1KiB (R-REPO-040)
  max_metadata_bytes: 256MiB         # default; at least 1KiB (R-REPO-040)
  max_concurrent_fetches: 256        # default; 1 to 100000, per replica (R-REPO-040)
  queue_timeout_seconds: 10          # default; 0 to 600, 0 refuses at once (R-REPO-040)
  global_offline: false              # default (R-REPO-031)
  blocked_content_types:             # default; lower-case type/subtype, no parameters (R-REPO-042)
    - text/html
    - application/xhtml+xml
  metadata_cache_bytes: 128MiB       # default; 0 turns the cache off, per replica: served metadata documents by blob digest (R-REPO-025)
  merged_metadata_cache_bytes: 256MiB  # default; 0 turns the cache off, per replica (R-REPO-020)
  query_cache_bytes: 8MiB            # default; 0 turns the cache off, per replica: answers of pass-through queries, e.g. a forwarded search (R-ARCH-013 step 6, Q-ARCH-270)
  claims:
    remote_refusal: true             # default (R-REPO-023)
npm:
  publish:
    max_tarball_size: 256MiB         # default; at least 1KiB and at most storage.upload.max_size (R-NPM-007)
    max_unpacked_size: 2GiB          # default; at least 1KiB
    max_tar_entries: 100000          # default; 1 to 10000000
    manifest_consistency: reject     # default; reject | warn
    retry_window: 10m                # default; 0s to 24h, 0s turns the acknowledged retry off
  abbreviated_time: true             # default (R-NPM-011)
  document_max_age: 0s               # default; 0s to 24h (R-NPM-013)
nuget:
  archive:
    max_nuspec_bytes: 1MiB           # default; 1KiB to 1MiB, the limit of R-ARCH-028 can only be tightened (Q-NUGET-25)
    max_entries: 65535               # default; 1 to 1000000, what the central directory of a package may declare (Q-NUGET-23)
    max_uncompressed_bytes: 4GiB     # default; 1MiB to 1TiB, the sum of the declared sizes
    max_compression_ratio: 100       # default; 1 to 100, of an entry read into memory
  registration:
    page_size: 64                    # default; 1 to 1000, versions per registration page (R-NUGET-009)
    inline_below: 128                # default; 1 to 100000, a package with fewer visible versions has its pages inlined
  search:
    default_take: 20                 # default; 1 to 1000, at most max_take (R-NUGET-010)
    max_take: 1000                   # default; 1 to 10000
    max_skip: 3000                   # default; 0 to 1000000, a larger skip answers 400
    virtual_merge_window: 1000       # default; 1 to 10000, results per member a virtual repository merges (R-NUGET-018, Q-NUGET-58)
    remote_result_ttl: 60s           # default; 0s to 1h, how long a forwarded search answer is kept (R-NUGET-016, Q-NUGET-16)
ui:
  readme:
    max_source_bytes: 512KiB         # default; 1KiB to 16MiB, a longer README is cut and flagged (R-UI-008)
    external_image_origins: []       # default; exact https origins whose README images are shown, the img-src of the UI's CSP; an origin of this installation is refused
    # external_image_origins: ["https://img.shields.io"]   # example
  client_setup:
    token_env_var: TROVE_TOKEN       # default; the variable the credentials=env snippets read the token from (R-API-030)
    token_ttl: 720h                  # default; the lifetime the setup dialog asks for, at most auth.tokens.pat.max_ttl
config_as_code:
  secrets_dir: /etc/trove/cac-secrets  # default; where a secretRef {"file": name} is read (R-REPO-033, R-OPS-029)
```

The three keys that carry a unit in their name (`lock_poll_interval_ms`, `fetch_wait_timeout_seconds`,
`queue_timeout_seconds`) are integers because the specification names them so (Q-REPO-85); code reads them through
`Proxy.LockPollInterval()`, `Proxy.FetchWaitTimeout()` and `Proxy.QueueTimeout()`. Every other duration is a
`time.Duration`, every size a `ByteSize` (a plain integer is bytes). Not in the tree yet, because their requirements are
P1 or later: `npm.trusted_publishing`. The `npm.audit` forwarding is built, as a setting of each remote repository (the `npm` member
of the repository document, [settings.go](../../internal/formats/npm/settings.go)), not a key of this file. `proxy.global_offline` is only the configuration side of
R-REPO-031; the runtime switch and its table (`proxy_offline_switch`) are not built.
`config_as_code` has only `secrets_dir` so far: the secret references of remote repositories need it, and the keys of the
document mechanism (`files`, `mode`, `lock_managed_objects`, R-OPS-029, P1) arrive with it.
The unit tests are in [m2_test.go](../../internal/config/m2_test.go).

