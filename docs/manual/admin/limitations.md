# Known limitations

This page lists what this version of trove does not do, and the known weak spots an administrator should plan around. Each entry says what is missing and, where one exists, what to do instead. Nothing here is a promise about later versions.

## Package formats

- **Two formats, and NuGet in parts.** trove serves generic files and npm packages. For NuGet (.NET) a local repository takes a push, an unlist, a relist and a delete and serves its service index; the downloads, the package documents and the search of NuGet answer `501` until they are built, and NuGet has no remote or virtual repositories yet. Container images, Python, Maven and every other package format are not supported. Paths such as `/v2/` are reserved and answer `404`.
- **Generic repositories are local only.** There are no remote or virtual generic repositories.
- **npm commands that are not implemented.** `npm login` and `npm adduser`, `npm search`, `npm token`, `npm access`, `npm org`, `npm team`, `npm deprecate`, staged publishing and trusted publishing through OIDC. These requests answer `404`, or `501` for a `POST`. Instead of `npm login`, put a trove token into `.npmrc` as the client setup of the repository shows.
- **npm 6 audits.** The legacy audit endpoints answer `501`; npm 7 or later, pnpm and Yarn use the bulk endpoint, which works.

## CI pipelines and automation

- **No CI token exchange.** GitLab CI jobs cannot exchange their ID token for a short-lived trove token; the `trv_cit_` token type and the `ci_exchange` route family are reserved and have no working route. Use a service-account token with a narrow scope and an expiry, stored as a masked CI variable.
- **No webhooks.** The `webhook-admin` role and the webhook audit category exist, but there are no webhooks to configure.

## Vulnerability scanning

- trove does not scan stored packages, keeps no advisory data, blocks no malicious versions and has no vulnerability report.
- `npm audit`, `pnpm audit` and `yarn npm audit` work through remote and virtual npm repositories by forwarding the audit to the remote's upstream or to its configured provider. The answer is that provider's, passed on unchanged. Local repositories and endpoints with exposure rules or without cache fill answer `501`.
- Names of packages that the remote has never fetched are withheld from the forwarded audit and reported as clean. npm prints a notice about withheld names; pnpm and Yarn do not. Install through trove before you audit, and watch `trove_npm_audit_names_withheld_total`.

## Backup and restore

- There is no backup or restore. `trove backup` answers "not implemented yet", the **Backups** link on the System status screen leads to a placeholder, and the chart renders no backup CronJob or restore Job.
- Until then, protect the PostgreSQL database with your database platform's backups, the bucket with your object store's own protection, and keep the Secret with the stored-secret keys (`secrets.encryption`): values sealed with a key that is lost cannot be opened. trove has not tested a restore from such copies.
- A database restored to an earlier point brings back what was removed after that point: revoked tokens, removed permissions and role bindings, and narrowed exposure rules. Review and revoke them after a restore.

## Upgrades and high availability

- **Rolling upgrades with replicas of two versions running side by side are not verified yet.** The System status screen shows such a state, but it has not been tested under load.
- **Memory under drains.** In test runs, `web` pods with a 1 GiB memory limit were killed for exceeding it under heavy npm install load during a node drain. The cause is not fixed. Keep the chart's default limit of 2 GiB or more.
- **Drain delay.** The default `health.shutdown_drain_delay` of 5 s has not been measured against a production ingress controller under load; the test cluster needs 15 s. If clients see connection errors during drains, raise it.
- **Chart gaps.** The chart renders no ServiceMonitor (see [Monitoring and operations](operations.md)), no HorizontalPodAutoscaler, no Gateway API route (only a `BackendTLSPolicy`), and no egress NetworkPolicy: the pods' outgoing traffic is not restricted by the chart.
- **One database and one Valkey user per installation.** Two installations in one PostgreSQL database delay each other through shared locks. Two installations that use one Valkey instance with the same user can read each other's keys.
- **No maintenance mode.** There is no mode that stops writes while reads continue. The **System status** screen does not show a maintenance row; it would appear, with the mode, once the mode exists. `GET /api/v1/system/info` carries `maintenanceMode`, which is always `normal`.

## Web UI

- No search over packages and files from the top bar; it searches repositories only. Use the **Packages** list and **Browse**.
- No warning before a session ends. Sessions end after 1 hour idle or 12 hours in total by default (`auth.sessions.idle_timeout`, `absolute_timeout`).
- No trash can: deleted files and packages cannot be restored. The file browser cannot copy or move files or edit properties.
- The repository wizard has no fields for the upstream of a remote repository or for the members of a virtual one; an npm remote is created with the public npm registry as its upstream. Set them through the management API (`/api/v1/repositories/{key}`).
- The token screens cannot bind a token to an endpoint; use the management API or `trove admin token`, see [Endpoints and exposure rules](endpoints.md#bind-a-token-to-an-endpoint).
- The exposure **Preview** judges only the versions you type, although its label says it lists the stored ones when empty, and it never shows which dist-tags stay visible.

## Audit trail

- **Not tamper-evident.** The database owner and any superuser can change or delete audit rows, and in single-role mode so can trove's own database login. Ship the `audit` log stream to a store outside trove's control if you need an unchangeable copy.
- **Reads are not audited.** Viewing the audit log leaves no event; only an export is recorded.
- **Events can be lost when a process is killed.** Denials, token rejections, logins and downloads are written in batches. A replica killed without a normal shutdown (out of memory, node loss) loses the events still in its buffer.
- **Last use of a token is approximate.** It is written in batches and at most once per `auth.tokens.last_used_granularity` (default `5m`); a replica killed without a normal shutdown loses the uses of its last minute. Uses before the upgrade that introduced it are not known.

## Storage

- **No quotas.** A repository has no size limit; an upload is bounded only by `storage.upload.max_size`, the repository's own upload size limit and the spool space of the replica. Grant deploy only to the accounts that publish.

## Sign-in and recovery

- **A break-glass account without a password file starts silently.** With `auth.break_glass.enabled: true` and no password file, trove starts and refuses every local login. The chart turns the account off when `secrets.breakGlass` is empty; outside the chart, check the setting. Test the break-glass login once after you set it up.
- **The break-glass account can be kept locked.** Five wrong passwords lock it for 15 minutes, from any address. Whoever reaches the local login can keep it locked. Restrict `/auth/local/` at the edge, and clear a lock with `trove admin break-glass unlock`.
- **The identity provider can remove the last administrator.** Group sync at sign-in removes memberships the provider no longer reports, also the last one of the administrator group. Keep the break-glass account available for recovery.

## Database, Valkey and object store access

- **Single-role database mode is the chart default.** Without `secrets.databaseMigration`, trove's runtime login owns the schema and may change its structure. Configure a separate migration login to remove that right.
- **Valkey without its own user.** Without a configured Valkey user name (`secrets.valkey.usernameKey` empty), trove connects as the `default` user, which usually holds every right on the instance. Give trove an ACL user of its own where your Valkey setup can create one.
- **Sentinel trust is a warning only.** Sentinels without a password and Valkey certificates verified against the system roots are logged at start, not refused. Set `secrets.valkeySentinel` and `secrets.valkeyCA`.
- **Object-store credentials.** With `storage.s3.credentials.source: default`, the cloud SDK's credential chain can reach the node's metadata service. Use `static` or `webIdentity`.

## TLS and network

- A loaded certificate keeps being served after it expires; only the expiry metric warns. Alert on it as shown in [TLS and certificates](tls-and-certificates.md).
- Client certificates are not checked for revocation (no CRL, no OCSP).
- Connections that are open during a certificate rotation keep the old certificate until they close.
- `tracing.ca_file` and the stored-secret key files are read only at start; a change needs a restart.
- The ops listener has no sign-in of its own; the metrics token (`metrics.auth: static`) is optional. The chart's NetworkPolicy is what keeps others out.
- The client address in logs, audit events and rate limits follows the forwarded header of the proxies in `server.trusted_proxies`. List only proxies that append the real client address.

## Endpoints

- Pods that serve a further endpoint, a pool included, hold the same database, Valkey and object-store credentials as every `web` pod.
- Exposure rules are tied to an endpoint name; an endpoint configured under an old name takes over the old rules.
- npm package documents on an endpoint with rules keep their real `time.created`, `time.modified` and `Last-Modified`, so customers can tell that a package changed.
- A rule on a virtual repository also covers packages of members added later.

## Remote repositories

- A missing upstream secret file, or a stored secret sealed with a key the replica lacks, is noticed at the repository's first request, which answers `502 upstream_secret_unavailable`, not at start-up. Run the repository's upstream test (`POST /api/v1/repositories/{key}/upstream-test`) after rolling out secret files.
- Whoever may configure remote repositories can point any upstream secret of `config_as_code.secrets_dir` at a host of their choice. Mount only the upstream secrets that those administrators may use.
