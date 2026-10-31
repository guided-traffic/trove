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
| [outbound.go](../../internal/config/outbound.go), [npm.go](../../internal/config/npm.go), [ui.go](../../internal/config/ui.go), [config_as_code.go](../../internal/config/config_as_code.go) | the sections of M2: `proxy.*` (inside the `>>> M2 proxy` markers of `Proxy`), `npm`, `ui` (README rendering and client setup) and `config_as_code.secrets_dir`, see [M2 keys](#m2-keys) |
| [schema.go](../../internal/config/schema.go) | `Describe`: flattens the tree into `Key`s, checks tags, derives environment names |
| [load.go](../../internal/config/load.go) | `Load`: defaults, file, environment, flags; strict matching; secret resolution; validation |
| [m1.go](../../internal/config/m1.go), [login.go](../../internal/config/login.go), [auth_identity.go](../../internal/config/auth_identity.go), [authz.go](../../internal/config/authz.go) | the sections of the M1 areas (`auth`, `authz`, `api`, `repositories`); in `auth` the login keys `auth.oidc`, `auth.login_page`, `auth.sessions`, `auth.break_glass` ([login.md](login.md#configuration)). `auth.oidc.providers` is a map of nested structs whose keys left out are filled by `AuthOIDCProvider.Defaulted`, not by `default` tags |
| [endpoints.go](../../internal/config/endpoints.go) | `server.endpoints` and `server.endpoint_rate_limits`: the endpoint entries, their defaults (`ResolveEndpoints`) and validation, see [endpoints.md](endpoints.md) |
| [rules.go](../../internal/config/rules.go), [value.go](../../internal/config/value.go), [units.go](../../internal/config/units.go) | `validate` tag rules, text and YAML value parsing, `ByteSize` |
| [secret.go](../../internal/config/secret.go), [report.go](../../internal/config/report.go) | the `Secret` type; `Report`, `Validator`, `CommandValidator`, `Command`, `Error` |
| [effective.go](../../internal/config/effective.go), [jsonschema.go](../../internal/config/jsonschema.go), [schemagen](../../internal/config/schemagen/main.go), [schema.json](../../internal/config/schema.json) | redacted view, JSON Schema generator and its committed output |

Sections that exist in the specification but have no owner yet are not in the tree: `backup`, `retention`,
`ci`, `proxy` (apart from the keys listed under [M2 keys](#m2-keys)), `nuget`, `support`,
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
Lists and maps are one key; their environment value is JSON:
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

## M2 keys

The keys of the remote engine (`proxy.*` beside `proxy.outbound`), the virtual engine and the npm format. The per-repository
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
P1 or later: `npm.trusted_publishing` and the `npm.audit` forwarding. `proxy.global_offline` is only the configuration side of
R-REPO-031; the runtime switch and its table (`proxy_offline_switch`) are not built.
`config_as_code` has only `secrets_dir` so far: the secret references of remote repositories need it, and the keys of the
document mechanism (`files`, `mode`, `lock_managed_objects`, R-OPS-029, P1) arrive with it.
The unit tests are in [m2_test.go](../../internal/config/m2_test.go).

