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
| [process.go](../../internal/config/process.go), [server.go](../../internal/config/server.go), [database.go](../../internal/config/database.go), [migrate.go](../../internal/config/migrate.go), [runtime.go](../../internal/config/runtime.go), [valkey.go](../../internal/config/valkey.go), [storage.go](../../internal/config/storage.go), [jobs.go](../../internal/config/jobs.go), [cluster.go](../../internal/config/cluster.go), [cache.go](../../internal/config/cache.go), [secrets.go](../../internal/config/secrets.go), [system.go](../../internal/config/system.go), [health.go](../../internal/config/health.go), [log.go](../../internal/config/log.go), [metrics.go](../../internal/config/metrics.go), [trace.go](../../internal/config/trace.go), [audit.go](../../internal/config/audit.go), [maintenance.go](../../internal/config/maintenance.go), [outbound.go](../../internal/config/outbound.go) | one section each (`Process`, `Server`, `Database`, `Migrate`, `Runtime`, `Valkey`, `Storage`, `Jobs`, `Cluster`, `Cache`, `Secrets`, `System`, `Health`, `Log`, `Metrics`, `Tracing`, `Audit`, `Maintenance`, `Proxy` with `Outbound`); the owner of a subsystem edits only its file |
| [schema.go](../../internal/config/schema.go) | `Describe`: flattens the tree into `Key`s, checks tags, derives environment names |
| [load.go](../../internal/config/load.go) | `Load`: defaults, file, environment, flags; strict matching; secret resolution; validation |
| [rules.go](../../internal/config/rules.go), [value.go](../../internal/config/value.go), [units.go](../../internal/config/units.go) | `validate` tag rules, text and YAML value parsing, `ByteSize` |
| [secret.go](../../internal/config/secret.go), [report.go](../../internal/config/report.go) | the `Secret` type; `Report`, `Validator`, `CommandValidator`, `Command`, `Error` |
| [effective.go](../../internal/config/effective.go), [jsonschema.go](../../internal/config/jsonschema.go), [schemagen](../../internal/config/schemagen/main.go), [schema.json](../../internal/config/schema.json) | redacted view, JSON Schema generator and its committed output |

Sections that exist in the specification but have no owner yet are not in the tree: `backup`, `retention`,
`auth`, `authz`, `ci`, `proxy` (apart from `proxy.outbound`), `npm`, `nuget`, `api`, `ui`, `support`,
`config_as_code`, `webhooks`, `mail`, `content_types`. Strict validation rejects a file that sets them
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
`[]string`, `map[string]T`, nested structs, and `config.Secret`. Lists and maps are one key; their
environment value is JSON.

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
  TLS private keys. The consumer reads the file on every use, so a rotated Secret applies without a
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
