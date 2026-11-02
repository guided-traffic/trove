# Configuration

This page explains where trove reads its configuration from, how configuration keys map to environment variables and Helm values, how the configuration is validated at start-up, which changes need a restart and which apply while trove runs, and which settings you change in the UI instead of the file. It closes with the important keys of each area and their defaults.

## Where configuration comes from

trove reads one YAML file, `trove.yaml`, and then the environment. Every key has a built-in default unless it says otherwise, so a file only needs the keys you change.

| Source | Precedence | Notes |
|---|---|---|
| Built-in defaults | lowest | The defaults below and in the full reference |
| The configuration file | overrides the defaults | `--config <file>`, else the variable `TROVE_CONFIG_FILE`, else `/etc/trove/trove.yaml`. A file you name must exist; a missing default file is not an error. |
| Environment variables | override the file | `TROVE_<KEY PATH>`, see below |
| Command-line flags | highest | `--role` sets `process.role`, `--log-level` sets `log.level` |

With the Helm chart, the `config` value is rendered into the ConfigMap `<fullname>-config` and mounted at `/etc/trove/trove.yaml` in every pod and in the migration Job. Environment overrides go into `extraEnv` (or `web.extraEnv`, `worker.extraEnv`, `migrations.extraEnv`). The chart manages some keys itself and fails the render when you set them inside `config`: the file paths of every mounted Secret, `storage.spool.dir`, the encryption key list, `server.tls` and `server.ops_tls` while `tls.enabled` is on (from the `tls` values) and `server.endpoints` (from the `endpoints` values). It also sets a few variables from the pod, such as the memory limit (`TROVE_RUNTIME_MEMORY_LIMIT_BYTES`) and the termination grace period.

Some settings are not configuration at all but data in PostgreSQL, changed through the UI or the management API: repositories, users, groups, service accounts, tokens, permission policies and role bindings, the exposure rules of endpoints, and the runtime authentication settings described [below](configuration.md#settings-in-the-ui-versus-the-configuration-file).

## Environment variables

The variable of a key is `TROVE_` followed by the key path in upper case, with dots replaced by underscores.

| Key | Variable |
|---|---|
| `log.level` | `TROVE_LOG_LEVEL` |
| `database.ssl_mode` | `TROVE_DATABASE_SSL_MODE` |
| `auth.break_glass.password_file` | `TROVE_AUTH_BREAK_GLASS_PASSWORD_FILE` |
| `server.trusted_proxies` (a list) | `TROVE_SERVER_TRUSTED_PROXIES='["10.244.3.17"]'` |
| `log.levels` (a map) | `TROVE_LOG_LEVELS='{"proxy":"debug"}'` |

A list or a map is one key, and its variable takes a JSON value. A `TROVE_` variable that is not a key stops the start with an error that suggests the closest valid variable. A few names are reserved and are not keys: `TROVE_CONFIG_FILE`, `TROVE_POD_NAME`, `TROVE_POD_NAMESPACE`, `TROVE_NODE_NAME` and `TROVE_POOL_NAME`, which the chart sets.

Keys that hold a secret end in `_file` and take a file path only, never the secret itself. trove reads the file where the key says, usually again on every use. With the chart you never set these paths: you name an existing Secret under `secrets` and the chart mounts it and fills in the path.

## Validation at start-up

Every command (`serve`, `migrate`, `admin`) validates the file and the environment before it does anything else, and reports every problem in one pass: an unknown key (with its line and the closest valid key), an unknown `TROVE_` variable, a value of the wrong type or outside its range, and rules across keys, such as a TLS client certificate without its key. `serve` additionally requires a role, the Valkey addresses, the bucket and the encryption keys. A replica with an invalid configuration does not start.

Check a configuration without starting anything:

```shell
trove admin config validate --config trove.yaml                    # table: key, value, source, variable
trove admin config validate --config trove.yaml --format json      # the same as JSON
```

The output shows every key with its effective value and its source (`default`, `file`, `env`, `flag:<name>` or `file:<path>`); secrets are redacted. One rule is checked only where the sign-in is built and therefore not by this command: `server.base_url` must suit the configured OIDC providers.

With the chart, `values.schema.json` checks the chart's own values during `helm install` and `helm upgrade`, but the `config` subtree is checked by trove itself. With the default `migrations.mode: hook`, the migration Job loads the same file first, so a misspelled key in `config` stops `helm install` or `helm upgrade` at the Job, before any Deployment changes. Read the Job's log to find the key.

## What needs a restart and what applies live

trove reads the configuration file and the environment once, at start. A change to either needs new pods; the chart rolls the pods by itself when the rendered `trove.yaml` or the list of encryption key names changes. Files that hold credentials and certificates are read again while trove runs, so rotating a Secret needs no restart.

| What changes | When it takes effect |
|---|---|
| Any key in `trove.yaml` or in the environment | At the next start of the process; the chart rolls the pods |
| The encryption keys (`secrets.encryption_key_files`, `secrets.active_key_id`) | At the next start; the keys are read once. Adding or removing a key name in `secrets.encryption.keys` rolls the pods |
| Listener certificates (`server.tls`, `server.ops_tls`, endpoint certificates) | Live: polled every `reload_interval` (10 s by default) once the kubelet has updated the mounted Secret |
| PostgreSQL credentials and TLS files | Live: read again for every new connection |
| Valkey and Sentinel credentials, Valkey TLS files | Live: credentials on every connection attempt, TLS files on every handshake; connections are renewed every `valkey.max_conn_lifetime` (30 min) |
| Object-store credential files (`credentials.source: static`) | Live: read again at most once per second |
| Object-store CA (`storage.s3.ca_file`) and outbound CA files (`proxy.outbound.tls.ca_files`) | Live: used for the next connection |
| OIDC client secret file | Live: read again when the file changes |
| Break-glass password file | Live: checked every `auth.break_glass.check_interval` (10 s); a rotation refuses the local sign-in for `auth.break_glass.rotation_hold` (2 min) and ends the break-glass sessions |
| Metrics bearer token file (`metrics.bearer_token_file`) | Live: read on each scrape |
| Tracing CA file (`tracing.ca_file`) | At the next start; read once |
| Settings changed in **Administration › System › Settings** | Live on every replica within `auth.revocation_bound` (2 s) |

## Settings in the UI versus the configuration file

**Administration › System › Settings** opens **Authentication settings**. It holds the runtime settings, which are stored in PostgreSQL, apply to every replica and survive restarts and upgrades:

| Setting in the UI | What it does | Bounded by the file key |
|---|---|---|
| **Anonymous access** | Lets requests without a credential read what the anonymous principal may read. Off after installation. | `auth.anonymous.allowed`: when `false`, anonymous access stays off whatever the switch says |
| **Token lifetimes** › **Personal tokens**: **Default lifetime (days)**, **Maximum lifetime (days)** | The lifetime a new personal token gets when none is asked for, and the longest one allowed | `auth.tokens.pat.default_ttl`, `auth.tokens.pat.max_ttl` |
| **Token lifetimes** › **Service account tokens**: the same two values | As above, for service-account tokens | `auth.tokens.sat.default_ttl`, `auth.tokens.sat.max_ttl` |
| **Expiry notice** | How many days before expiry a token produces its expiry event | `auth.tokens.expiry_notice` |

The file values are ceilings. Until you save a value in the UI, the file value applies. A value above its ceiling is refused, and if you lower a ceiling in the file later, the lower value wins. Saving needs the `settings:write` capability and a recent sign-in, and writes an audit event. The **Status (read only)** part of the page shows **Identity providers**, the **Break-glass account**, **Sessions** and **Clients**; these come from the configuration file and change only with it.

Endpoints are split the same way: an endpoint's listener, hosts, certificates and accepted credentials are configuration (the chart's `endpoints` values), while its exposure rules are data you manage in the UI. See [Endpoints and exposure rules](endpoints.md).

## Important keys by area

The tables below list the keys an installation most often sets. Every key with its default, including the Helm values, is in the [full reference](https://github.com/guided-traffic/trove/blob/main/README.md#-full-reference). Durations take units such as `500ms`, `30s`, `1h`; sizes take `KiB`, `MiB`, `GiB` or a plain number of bytes.

<details>
<summary>Server, base URL and client address</summary>

| Key | Default | Meaning |
|---|---|---|
| `server.base_url` | empty | The HTTPS origin users and clients reach, such as `https://trove.example.com`. Empty is for development only and logs a warning every hour; the chart requires it. |
| `server.additional_base_urls` | `[]` | Further origins a trusted proxy may select through the forwarded host and scheme |
| `server.trusted_proxies` | `[]` | Addresses or CIDRs of the edge that sets the forwarding header. Without them, the TCP peer is the client. A `/0` prefix is refused. |
| `server.forwarded_header` | `x-forwarded-for` | The one header family the edge rewrites: `x-forwarded-for` or `forwarded`. The other is never read. |
| `server.listen`, `server.ops_listen` | `:8080`, `:8081` | The client and ops listeners |
| `server.max_in_flight` | `4096` | Concurrent requests per replica; beyond it, and beyond what the memory limit admits, a request gets `503` |
| `server.replica_header` | `authenticated` | When responses carry `X-Trove-Replica`: `authenticated`, `always` or `never` |
| `health.shutdown_drain_delay` | `5s` | How long a stopping `web` replica keeps serving; must exceed the time your ingress controller needs to drop an endpoint |

</details>

<details>
<summary>Database</summary>

| Key | Default | Meaning |
|---|---|---|
| `database.host` | none, required | Host name of the PostgreSQL primary or its pooler; a comma-separated list is allowed |
| `database.port` | `5432` | Port |
| `database.name` | none, required | Database name |
| `database.schema` | `trove` | Schema trove creates and uses |
| `database.session_host` | empty (`host`) | A direct host for the connections that hold session locks (the migration and the job leases), when `host` is a pooler |
| `database.pooler.mode` | `none` | `none`, `session` or `transaction`: the kind of pooler `host` points at |
| `database.ssl_mode` | `verify-full` | `verify-full`, `verify-ca`, `require`; `disable`, `allow` and `prefer` also need `allow_insecure: true` |
| `database.ssl_root_cert_file` | empty (system roots) | CA of the server; the chart sets it from `secrets.databaseCA` |
| `database.pool.max_conns` | `20` | Connections per replica |
| `database.preflight.connection_budget` | `enforce` | The migration Job checks that `max_connections` covers every replica's pools: `enforce` fails, `warn` logs |

</details>

<details>
<summary>Valkey</summary>

| Key | Default | Meaning |
|---|---|---|
| `valkey.mode` | `sentinel` | `sentinel`, or `standalone` for development only |
| `valkey.sentinel.addresses` | `[]`, required in Sentinel mode | `host:port` of each Sentinel |
| `valkey.sentinel.primary_name` | empty, required in Sentinel mode | The name in the Sentinels' monitor line |
| `valkey.sentinel.tls` | `true` | Use TLS for the Sentinel connections too |
| `valkey.address` | empty | The server in standalone mode |
| `valkey.tls.mode` | `verify-full` | `verify-full`, `verify-ca` (needs `ca_file`) or `disable` (needs `allow_insecure`) |
| `valkey.tls.ca_file` | empty (system roots) | CA of the servers; the chart sets it from `secrets.valkeyCA` |
| `valkey.key_prefix` | `trove:` | Prefix of every key trove writes; must end in `:` |
| `valkey.command_timeout` | `500ms` | Time limit of one command |

</details>

<details>
<summary>Object storage</summary>

| Key | Default | Meaning |
|---|---|---|
| `storage.s3.bucket` | empty, required to serve | Bucket of the blobs; with the chart it may come from an S3 provisioner's Secret (`secrets.s3.bucketKey`) |
| `storage.s3.region` | empty | Region; required when the environment names none |
| `storage.s3.endpoint` | empty | Endpoint of an S3-compatible store; empty uses AWS endpoint resolution, a host without a scheme means `https://` |
| `storage.s3.prefix` | `trove` | Key prefix inside the bucket |
| `storage.s3.force_path_style` | `false` | `true` for SeaweedFS, MinIO and Ceph RGW |
| `storage.s3.credentials.source` | `default` | `default` (the SDK's chain, which can reach the node's metadata service), `static` (key files) or `webIdentity` |
| `storage.s3.sse.mode` | `none` | Server-side encryption: `none`, `s3` or `kms` |
| `storage.s3.startup_check` | `true` | Check the store before the first readiness |
| `storage.upload.max_size` | `10GiB` | Largest single upload; must fit the spool of a `web` pod |
| `health.require_object_store` | `false` | `true` makes readiness depend on the object store at all times, not only at start |

</details>

<details>
<summary>Sign-in and sessions</summary>

| Key | Default | Meaning |
|---|---|---|
| `auth.oidc.providers` | `{}` | The OIDC providers, keyed by provider ID: `display_name`, `issuer`, `client_id`, the client secret and claim mapping. See [Sign-in and identities](sign-in-and-identity.md). |
| `auth.login_page.auto_redirect` | `true` | Redirect the sign-in page to the provider when exactly one is configured |
| `auth.sessions.idle_timeout` | `1h` | A browser session ends this long after its last request |
| `auth.sessions.absolute_timeout` | `12h` | A browser session ends this long after sign-in |
| `auth.sessions.max_per_user` | `20` | Concurrent sessions of one user; one more ends the oldest |
| `auth.sessions.recent_auth_window` | `1h` | Actions that create credentials need a sign-in this recent |
| `auth.break_glass.enabled` | `true` | The local administrator account; the chart sets `false` without `secrets.breakGlass` |
| `auth.break_glass.username` | `admin` | Its username |
| `auth.break_glass.show_on_login_page` | `false` | Show the local form on the sign-in page; it is always reachable with `?local=1` |
| `auth.anonymous.allowed` | `true` | Ceiling of the **Anonymous access** switch |
| `auth.revocation_bound` | `2s` | Time within which a revocation or settings change is in force on every replica |
| `auth.clients.basic.enabled` | `true` | Accept tokens in HTTP Basic authentication, which some package clients need |
| `auth.rate_limit.failures_per_address` | `300` | Rejected credentials per minute and client address, across all replicas |
| `auth.rate_limit.exempt_cidrs` | `[]` | Client addresses that are never throttled |

</details>

<details>
<summary>Tokens</summary>

| Key | Default | Meaning |
|---|---|---|
| `auth.tokens.pat.default_ttl` | `2160h` (90 days) | The default lifetime of a personal token, and the ceiling of the value in the UI |
| `auth.tokens.pat.max_ttl` | `8760h` (365 days) | The longest personal-token lifetime, and the ceiling of the value in the UI |
| `auth.tokens.pat.allowed_groups` | `[]` | Empty: every user may create personal tokens; otherwise only members of these groups |
| `auth.tokens.sat.default_ttl`, `auth.tokens.sat.max_ttl` | `2160h`, `8760h` | The same for service-account tokens |
| `auth.tokens.sat.allow_non_expiring` | `false` | Allow administrators to create service-account tokens without expiry |
| `auth.tokens.expiry_notice` | `168h` (7 days) | How long before expiry a token produces its expiry event, and the ceiling of the value in the UI |
| `auth.tokens.retention_after_end` | `2160h` | How long an expired or revoked token's record is kept |
| `auth.tokens.last_used_granularity` | `5m` | A token's last-used time is written at most once per this period (1 second to 24 hours); a longer period means fewer database writes and a less exact time |
| `auth.tokens.max_active_per_principal.pat`, `.sat` | `50`, `20` | Active tokens per user and per service account |

</details>

<details>
<summary>GitLab CI federation</summary>

The federation is being built; until then these keys are accepted and the CI operations answer `501`. [GitLab CI federation](gitlab-ci.md#configuration-keys) lists every key of the section and explains how to register a GitLab instance.

| Key | Default | Meaning |
|---|---|---|
| `ci.issuers` | `[]` | GitLab instances declared as trusted CI issuers (`name`, `kind: gitlab`, `issuer`, and the optional fields of the issuer API in snake_case). The migration Job applies them; they are read-only in the UI. With the chart: `config.ci.issuers`. |
| `ci.token.default_ttl`, `ci.token.max_ttl`, `ci.token.min_ttl` | `15m`, `1h`, `30s` | Lifetime of a CI token; `max_ttl` must not exceed `auth.tokens.cit.max_ttl` (`6h`), or the start fails naming both keys |
| `ci.exchange.enabled` | `true` | The token exchange endpoint |
| `ci.exchange.rate_limit.per_client_ip_per_minute`, `.per_subject_per_minute` | `600`, `300` | Cluster-wide limits per client address and per issuer and project |
| `ci.exchange.rate_limit.exempt_cidrs` | `[]` | Runner networks that skip the per-address limit |
| `ci.signing_keys.max_stale` | `24h` | A key set older than this fails closed with `503` |
| `ci.mappings.max_per_issuer` | `1000` | Mappings of one issuer |
| `ci.replay.retention_after_expiry` | `15m` | How long a used subject token is remembered |
| `ci.issuances.retention` | `8760h` | How long the record of an issued CI token is kept |

</details>

<details>
<summary>UI</summary>

| Key | Default | Meaning |
|---|---|---|
| `ui.readme.max_source_bytes` | `512KiB` | Largest README that is rendered; a longer one is cut |
| `ui.readme.external_image_origins` | `[]` | Exact `https` origins whose images a README may show; every other external image is shown as a link |
| `ui.client_setup.token_env_var` | `TROVE_TOKEN` | The environment variable the client setup snippets read a token from |
| `ui.client_setup.token_ttl` | `720h` | Lifetime of a token the setup dialog creates; at most the personal-token maximum |

</details>

<details>
<summary>Rate limits</summary>

Each class allows `requests_per_second` on average and `burst` at once. The limits are counted across all replicas in Valkey; while Valkey is unavailable, each replica enforces its share.

| Key | Default | Applies to |
|---|---|---|
| `api.rate_limits.enabled` | `true` | Every class below |
| `api.rate_limits.exempt_admins` | `false` | Whether administrators bypass the limits |
| `api.rate_limits.read`, `.write` | `50`/`200`, `10`/`50` | Reading and changing requests to the management API |
| `api.rate_limits.anonymous` | `5`/`20` | Management API requests without a credential, per client address |
| `api.rate_limits.package_read`, `.package_write` | `200`/`1000`, `10`/`50` | Reading and changing requests of the package protocols |
| `api.rate_limits.package_anonymous` | `20`/`100` | Package requests without a credential, per client address |
| `server.endpoint_rate_limits.external` | `50`/`200`, per principal | The class further endpoints use by default |

</details>

<details>
<summary>Logging, metrics, tracing and audit</summary>

| Key | Default | Meaning |
|---|---|---|
| `log.format` | `json` | `json` or `text`; a replica writes its log to standard output |
| `log.level` | `info` | `debug`, `info`, `warn` or `error` |
| `log.levels` | `{}` | Level per component, such as `{proxy: debug}` |
| `log.request.enabled` | `true` | One log record per request |
| `metrics.enabled` | `true` | Serve `/metrics` on the ops listener |
| `metrics.auth` | `none` | `static` requires a bearer token (`secrets.metricsToken` in the chart) |
| `tracing.enabled` | `false` | Export OpenTelemetry spans |
| `tracing.endpoint` | empty | The OTLP endpoint, `https://` unless `tracing.allow_insecure` |
| `tracing.sample_ratio` | `0.1` | Share of requests traced |
| `audit.retention_days` | `365` | How long audit events are kept; `0` keeps them all |
| `audit.record_downloads` | `false` | Record every download as an audit event |

</details>

<details>
<summary>Stored-secret encryption and system</summary>

| Key | Default | Meaning |
|---|---|---|
| `secrets.encryption_key_files` | `[]`, required to serve | The key files; the chart fills it from `secrets.encryption` |
| `secrets.active_key_id` | empty, required to serve | The key that encrypts; every listed key decrypts. The chart uses the first entry of `secrets.encryption.keys`. |
| `system.public_version` | `false` | Show the version to callers without a credential |
| `system.bootstrap_anonymous_read` | `false` | Development only: lets anonymous callers read the system information; refused with an `https` base URL that is not `localhost` |

</details>
