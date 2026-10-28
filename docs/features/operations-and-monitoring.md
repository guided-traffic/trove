# Operations, monitoring and configuration

This page covers how a full-featured binary repository manager is run and observed: health and
readiness checks, system information, metrics, the log set and its record formats, the audit
trail, log viewing and forwarding, support bundles, backups, system- and repository-level import
and export, every configuration mechanism (node configuration file, central configuration
descriptor, declarative configuration patches, bootstrap files, identity-service configuration
documents), the general settings an administrator edits in the UI, the mail server, the HTTP front
end, load protection, recurring maintenance jobs, upgrades and migration from another repository
manager. Billing, licensing and hosted-service functions of an operator-run customer portal are
listed near the end as out of scope, one short entry each, followed by one global security switch
that no other page covers.

## Health and system information

### OPS-001 · Ping
Priority: **MVP**

A minimal check that the repository service is up and able to answer. Clients and tooling call it
before real work to confirm that the base URL and credentials are right.

| | |
|---|---|
| Inputs | none |
| Output | `200` with the plain-text body `OK`; `500` when the service is not working |
| Privilege | any caller; anonymous calls are accepted |

The identity service has its own ping with the same plain-text `OK` answer and no authentication,
so each internal service can be checked separately. Whether ping checks database or object-storage
reachability is unspecified.

Interactions: readiness (OPS-002), service status overview (OPS-008).

### OPS-002 · Readiness probe
Priority: **MVP**

A Kubernetes-style readiness check: should this node receive traffic? Its response time also serves
as a latency signal that should be monitored.

| | |
|---|---|
| Inputs | none |
| Output | `200` with a JSON object whose status code field is `OK`; on failure an HTTP error status with a reason |
| Privilege | any caller; anonymous calls are accepted |

Which dependencies readiness checks is unspecified beyond "working properly".

Interactions: liveness coupling (OPS-003, OPS-004); load balancer and rolling upgrade (OPS-073).

### OPS-003 · Liveness probe
Priority: **MVP**

A Kubernetes-style liveness check with the same request and response shape as OPS-002 (`200` with
status `OK`, otherwise an error status with a reason; anonymous calls accepted).

Edge case: liveness does not report when the container can start accepting traffic; that is
readiness. The configuration (OPS-004) defines the actual coupling: liveness starts failing once
readiness has failed for longer than a configured duration, so a node that stays unready is
eventually restarted by the orchestrator.

### OPS-004 · Health-check tuning
Priority: **P1**

Node-level settings in the node configuration file (OPS-040) that control how health is evaluated.

| Setting | Meaning | Default |
|---|---|---|
| Health check interval | How often the node re-evaluates its health | 15 s; values below 3 s are raised to 3 s |
| Fail liveness on long readiness failure | Couples liveness to readiness | on |
| Readiness failure duration | How long readiness may fail before liveness fails as well | 60 s |
| Local service check interval | How often the node's front router checks each local service | 5 s |
| Local service check timeout | Timeout of one such check | 5 s |
| Healthy threshold | Consecutive successful checks before a service counts as healthy | 2 |
| Unhealthy threshold | Consecutive failed checks before a service counts as unhealthy | 2 |
| Topology refresh interval | How often the router reloads the cluster topology from the service registry | 3 s |
| Maximum stale heartbeat | Time since a service's last heartbeat after which it no longer counts as healthy | 30 s |

The front router also exposes an aggregated health operation; setup verification expects it to
report the router's state as healthy. Its full response schema is
unspecified.

Edge case: the liveness coupling only works when something actually calls the readiness probe
(the orchestrator's readiness check); if readiness is never polled, liveness never sees a long
readiness failure.

Interactions: identity-service heartbeat job (5 s interval, a server counts as stale after 30 s
without heartbeat, both template defaults, OPS-048); service status overview (OPS-008).

### OPS-005 · Version information
Priority: **MVP**

| | |
|---|---|
| Inputs | none |
| Output | JSON with the version string, the revision, and the list of installed optional modules |
| Privilege | any caller; anonymous calls are accepted, but invalid credentials still yield `401` |

### OPS-006 · Application headers for anonymous callers
Priority: **P1**

Responses can carry application headers that identify the server software and the answering node.
A node-level switch decides whether these headers are also sent to unauthenticated and anonymous
callers.

| Setting | Meaning | Default |
|---|---|---|
| Expose application headers | Include server identity and node ID headers in responses to unauthenticated and anonymous calls | on |

Security: with the default, anyone who can reach the service learns the product version and the
node layout of a cluster. The header names are unspecified.

### OPS-007 · System information report
Priority: **P2**

An administrator-only operation that returns general system information as plain text. Its exact
content is unspecified; the "system information" part of a support bundle (OPS-031) lists storage,
system properties, runtime and CPU details and plugin status, which indicates the scope.

| | |
|---|---|
| Output | plain text |
| Privilege | administrator |
| Errors | `401`, `403` |

### OPS-008 · Service status overview
Priority: **P1**

A screen listing every service instance on every node of the deployment. UI location:
administration, monitoring, service status.

| Column | Meaning |
|---|---|
| Service | Name of the service |
| URL | Fully qualified URL of the deployment |
| Last heartbeat | When the instance last reported that it is up |
| Node ID | Identifier of the node |
| Version | Version of the service |
| Status | Current state |
| Details | Additional information |
| Start time | When the instance was started |

After a cluster upgrade or a system import, administrators use this screen to confirm that every
node and service is online (OPS-035, OPS-073). An API for the same data is unspecified.

### OPS-009 · Cluster lock diagnostics
Priority: **P2**

Two read-only operations show which database advisory locks are currently held, so that stuck or
long-held locks can be diagnosed without database access. Self-managed installations only.

| Operation | Behavior |
|---|---|
| Cluster lock snapshot | Lists the answering node's locks first; with clustering on, it also queries every other running member and marks a member that does not answer as `unreachable` |
| Node lock snapshot | Only the receiving node's locks, without asking peers; it backs the cluster fan-out but administrators may call it directly |

Input (both): optional category; when given, only locks of exactly that category are returned.

<details>
<summary>Response content</summary>

| Part | Content |
|---|---|
| Metadata (cluster snapshot only) | When the request was processed, which node answered, fan-out status (enabled, number of members that responded, number unreachable) |
| Summary (cluster snapshot only) | Total held locks after the category filter, nodes that reported, nodes that did not |
| Warnings (cluster snapshot only, omitted when empty) | Machine-readable code and message explaining why the snapshot may be incomplete, for example that fan-out is unavailable in one cluster-authentication mode |
| Per node | Node ID; status `ok` or `unreachable`; list of locks (always empty for an unreachable node) |
| Per lock | Category (example: replication); key within the category (example: a repository key); time the database granted the lock, as epoch milliseconds and as an ISO timestamp; ID and name of the holding thread |

</details>

Behavior and edge cases:

- The snapshot is best effort, not atomic. The responding node comes first; stopped members and
  members with a stale heartbeat are left out rather than marked unreachable, so the summary can
  undercount.
- Lock timestamps are `0` (or the Unix epoch as a timestamp) when unknown and must be excluded when
  sorting by age. When a node's database connection drops and the lock is re-acquired, the
  timestamp resets to the renewal time. The holder thread ID is `0` when unknown; the thread name
  may be null on older nodes although the lock is held.
- Advisory locks live as long as the holder's database session; when a node dies, its session
  ends and the database frees its locks without intervention.
- Locks used by the older table-based synchronization of background jobs are never reported.
- `503` when native advisory locking (PostgreSQL) is not active on the node.

Privilege: administrator. Errors: `401`, `403`, `503`.

Interactions: cluster coordination in [ha-and-deployment.md](ha-and-deployment.md).

### OPS-010 · Outbound connection test
Priority: **P2**

An administrator checks that the system can reach another product or endpoint.

| | |
|---|---|
| Inputs | Endpoint URL (required); user name and password for the test (optional) |
| Output | `200` with a plain-text success message when the endpoint answered `200` |
| Errors | `400` with an error list that carries the endpoint's answer (example: the endpoint returned `404`); `401`; `403` |
| Privilege | administrator; self-managed installations only |

Security: the server sends a request with caller-supplied credentials to an arbitrary URL. The
operation is restricted to administrators and no address filtering is specified; compare
the private-network protection for webhooks (EVT-029 in
[events-and-extensibility.md](events-and-extensibility.md)).

## Metrics

### OPS-011 · Metrics endpoint
Priority: **MVP**

Metrics are exposed in the OpenMetrics / Prometheus text exposition format. The repository service
has one metrics operation per node; the federation service has a separate one (OPS-014). The other
auxiliary services have metric families too (OPS-013), but whether they appear in the repository
service's output or behind per-service operations is unspecified.

| | |
|---|---|
| Output | `text/plain` exposition text |
| Privilege | administrator, or a token with the system-metrics read scope (TOK-014 in [access-tokens.md](access-tokens.md)); an administrator can mint such a read-only token so that a scraper never holds administrator credentials |
| Errors | `401` and `403` with a JSON error list |

Enabling: on self-managed installations two node-level switches must be on (general metrics and
database metrics), and a restart is required. The default is unspecified. Open: either both
switches are off until switched on, or general metrics collection is on in the shipped configuration
template; the spec decides.

Hosted deployments: a separate application-metrics operation with the same format, administrator
only.

Example scrape configuration: a Prometheus job with a unique job name, an administrator user name
and password, and the node URL as target. The federation service ships a Prometheus Operator
ServiceMonitor that can be enabled in its Helm values (example interval 60 s, scrape timeout 15 s).

Edge case: the output carries an extra non-standard comment line per metric ("updated" with a
timestamp) and a millisecond timestamp on each sample.

### OPS-012 · Core metric families
Priority: **MVP**

What the repository service measures. Names are unspecified; the type column shows each family's
metric type.

<details>
<summary>Metric families of the repository service</summary>

| Family | Meaning | Published type |
|---|---|---|
| Home volume usage | Bytes used and bytes free on the volume holding the installation's home directory | gauge |
| Garbage collection | Duration of a run, binaries removed by a run, bytes reclaimed by a run; storage size after a full run (deprecated) | gauge |
| Repository inventory | Number of repositories by class (local, remote, virtual) and package format | counter |
| Runtime memory | Free, maximum and total heap; number of processors | gauge (processors: counter) |
| Runtime threads | Daemon, live and peak thread counts, threads per state; total threads started | gauge; counter for the total |
| HTTP server workers | Busy worker threads, configured maximum, current worker threads | gauge |
| Database pool | Active, idle, maximum and minimum-idle connections | gauge |
| Slow queries | Duration of slow database queries | gauge |
| Outbound HTTP pools | Available, leased, pending and maximum connections per remote repository, reported only for the top repositories by available pool count; how many is a tunable (default 10) | gauge |
| Blob store transfer | Bytes downloaded from and uploaded to each storage provider; successful and failed download and upload operations; binaries downloaded and uploaded per provider | counter |
| Blob store contents | Number of binaries and their total size held by a provider (cache layer); requests for binaries not found | gauge; counter for not-found |
| Local cache layer | Bytes evicted; binaries not cached because they exceed the size limit | gauge; counter |
| Shards | Reachability per shard (1 reachable, 0 unreachable, −1 check timed out); number of shard rebalancing operations required | gauge; counter |
| Signed download redirects | Signed redirect URLs issued and bytes redirected; the byte count is an estimate because the system cannot see whether the client used the URL | counter |
| Object-store HTTP pool | Leased, pending and maximum connections (one cloud storage provider only) | gauge |
| Background thread pools | Threads actively executing tasks; tasks waiting | gauge |
| Thread-local caches | Cache events and fallbacks | counter |
| Release-set deletions | Deleted release-set versions, labelled by actor and kind | counter |
| Release lifecycle | Release sets created and existing, promotions run and completed, median creation, copy, promotion and deletion times, artifacts and property volume per release set, creations per agent (UI, API, command-line tool, system) and per creation method | counter |

Labels: storage-provider families carry the provider's name and its position in the provider
chain; inventory families carry the package format; release-set families carry actor and kind.

</details>

Edge cases: several published types do not match the meaning (totals published as gauges, a
processor count as a counter, medians as counters). The release-set families belong to
[distribution-and-lifecycle.md](distribution-and-lifecycle.md) and are only relevant once that
subsystem exists.

Interactions: garbage collection and storage providers in
[storage-and-blobs.md](storage-and-blobs.md); signed redirects (REPO-019 in
[repositories.md](repositories.md)); remote repositories in
[remote-repositories.md](remote-repositories.md).

### OPS-013 · Auxiliary service metric families
Priority: **P1**

<details>
<summary>Metric families of the auxiliary services</summary>

| Service | Families |
|---|---|
| Identity service | Runtime heap and processors; database pool (active, threads waiting for a connection, idle, maximum pool size, minimum idle); slow queries; HTTP server connection usage; gRPC server streams (cancelled, connected in total, currently connected); gRPC request executor (completed, maximum concurrent, pending, running, total); size of the identity configuration document; home volume used and free; number of authentications; federation publish count and duration (count and sum) |
| Event delivery (webhooks) | Time to prepare a trigger, response time of the target, total trigger time, each as sum and count |
| Scripting hooks | Executions, accumulated CPU time (ms), failures (total and per tenant), accumulated memory, executions in progress, successful executions, reruns, accumulated execution time (ms), number of hooks; several names changed between versions |
| Topology | Cache evictions, failed loads, hits, hit rate, loads, accumulated load time, misses, successful loads; successful and failed database migrations; database connections (active, waiting, idle, total); number of managed tenants |
| Front router | Successful and failed configuration reloads with timestamps of the last success and failure; for entry points, routes and backend services: request counts (plain and TLS), request durations, open connections, request and response bytes; backend retries |
| Observability and connectivity services | Home volume used and free; time and count of self-metrics collection; runtime heap in use, allocated, idle, object count, reserved; share of CPU time spent in garbage collection; goroutine count; CPU usage of the first core sampled over 50 ms; host load averages over 1, 5 and 15 minutes; host memory used and free |
| Scanner | Seconds since the last vulnerability-database sync started, finished persisting and finished impact analysis; total sync running time; scanned artifacts and components per package format; uptime per node |

</details>

A CI pipeline service has no counterpart in this catalog; such a service writes further metrics
(process CPU, script runtime heap segments, host memory, pipelines per project, queue messages,
build node provisioning states, concurrent builds) and JSON event lines for runs, steps, source
syncs, incoming webhooks and integration changes.

Interactions: webhooks (EVT-001) and scripting hooks (EVT-038) in
[events-and-extensibility.md](events-and-extensibility.md); scanner in
[security-scanning-and-curation.md](security-scanning-and-curation.md).

### OPS-014 · Federation metric families
Priority: **Later**

The federation service (see [replication-and-federation.md](replication-and-federation.md)) exposes
its own endpoint (self-managed only) with system and runtime metrics plus the families below.

<details>
<summary>Federation metric families</summary>

| Family | Content |
|---|---|
| Member health | Members per status (full sync running, full sync pending, import in progress, full sync preparing, marked for removal, read-only, generic error, out of sync, inactive); members of self-managed targets in error; disabled members |
| Synchronization | Maximum lag between the oldest handled event and completion; per-repository lag for the highest-lag repositories; events sent to remote instances (labelled by propagation type or error), received from remote instances, removed by filters |
| Full sync | Duration per member; artifacts in the file list; file-list and sort rates (artifacts per second); event propagation rate; failures by error |
| Binary tasks | Tasks by status; tasks pending and executing with lag; handler execution time |
| Auto-healing | Members recovered automatically, by status |
| Database | Failed queries by query; time to add events, load the next batch, send events, and per event |
| HTTP client | Available and leased connections (percent), pending connection requests |
| Configuration integrity | Repositories not in sync with remote members or with the local repository service; totals on both sides; enabled-state mismatches between instances, with the identity service, and between member state and the remote identity service; deployments whose configuration or trust token is missing |
| Event processing | Failures processing repository events on the source and on the target member |
| Partition management | Time between partition creations for events and blobs; sub-partition counts per table |

Labels: tenant, source deployment URL, source repository key, remote member deployment URL and
repository key, the combined source and remote key pair, member status, error, query, binary task
status, table name.

An older federation mechanism publishes: repositories and mirrors whose lag exceeds a threshold
(the threshold is a tunable), the maximum lag in milliseconds, disabled members, and repositories
per status (healthy, full sync pending, full sync running, out of sync, exhausted, disabled).

</details>

Edge case: the two lag metrics report seconds although their help text may still say
milliseconds. Example alerts: lag above 300 s, rate of full-sync failures, members in an error
status, available connections below 20 %.

### OPS-015 · Metrics log output
Priority: **P2**

Besides the endpoint, each service periodically writes its metrics to a metrics log file, and
writes event-driven metrics (for example one garbage collection run) deduplicated to a separate
metrics-events log. Contents listed for the repository service's metrics log: disk used and free,
process CPU time, runtime memory and processors, database pool, outbound HTTP pools, and the
scanner's sync and inventory figures.

| Setting | Meaning | Default |
|---|---|---|
| Metrics collection enabled (repository and identity services) | Write metrics to the metrics logs | on (template) |
| Collection interval (repository and identity services) | Seconds between collections | 60 s |
| Excluded providers | Metric providers to skip | none |
| Platform-dependent line endings | Use the platform's line endings instead of Unix style | on |
| Usage metrics (identity service) | Collect usage data into a usage log | on, every 60 s |
| Event service metrics | Gather metrics in the event service | off; interval 10 s |
| Observability metrics logs | Write and ship metrics logs | off; interval 10 s |
| Bundled metrics shipper | Push metrics to an Elasticsearch instance (URL, user, password, TLS verification mode full, certificate or none) | off |

Interactions: log rotation (OPS-024).

### OPS-016 · Runtime management beans
Priority: **Out of scope**

The Java-based repository service also exposes Java management beans (repositories with artifact
count and size, federation lag, executor pools, storage size, database data source, HTTP pools,
event rates, log-shipping queues). This is specific to a Java runtime; the same facts belong in the
metric families (OPS-012).

## Logs

### OPS-017 · Log set and file naming
Priority: **MVP**

Every service writes a fixed set of logs into its log directory; each file name starts with the
service name and a dash, followed by the kind of log.

| Log | Content |
|---|---|
| Service log | Main application log of the service (OPS-018) |
| Request log | Every HTTP request (including gRPC) the service received (OPS-019) |
| Outbound request log | Requests the service initiated for remote repositories and replication (OPS-020) |
| Console log | Combined console output of all services of a node in one file; on Windows one console log per service |
| Front router access log | JSON record per request through the router, including service-to-service calls (OPS-021) |
| Security access log | Security-relevant events of the repository service (OPS-022) |
| Audit trail | Changes to users, groups, permissions and tokens (OPS-023) |
| Further logs | Import and export log (OPS-034), migration log of an upgrade (OPS-074), metrics logs (OPS-015), a troubleshooting log of the event service |

Each log has a rolling policy that compresses rotated files and moves them into an archive
subdirectory (OPS-024). Several services can additionally write their application and request
logs to standard output (per-service switch; in the template on for application logs and off for
request logs).

Edge case: the request log is not a basis for data-transfer accounting; a hosted deployment counts
transfer separately (OPS-083).

### OPS-018 · Service log record format
Priority: **MVP**

| Field | Meaning |
|---|---|
| Timestamp | UTC, millisecond precision, RFC 3339 form |
| Service identifier | Short code of the service that wrote the record |
| Level | Log level |
| Trace ID | Identifies one request across services |
| Source location | Class (or component) and line that wrote the record |
| Thread | Thread name; `main` for services not written in Java |
| Message | Text |

Formats: plain text (default) or JSON, selectable per service; a per-service switch adds caller
information (default off). The console pattern colours the service identifier and level; colours
should be off on Windows.

### OPS-019 · Request log
Priority: **MVP**

One record per completed incoming request, pipe-separated, always with the same number of columns;
a missing value leaves its column empty.

| Column | Meaning |
|---|---|
| Timestamp | When the request completed and was logged; UTC, millisecond precision |
| Trace ID | Request trace identifier |
| Remote address | Caller IP address (IPv4 or IPv6) |
| User name | Authenticated user, or `anonymous` |
| Method | HTTP method in upper case |
| URL | Request URL relative to the service |
| Status | HTTP status returned |
| Request length | Bytes received (for example an uploaded file); `-1` when the client did not send a length |
| Response length | Bytes sent (for example a downloaded file); `-1` when unknown, for example with chunked encoding |
| Duration | Processing time in milliseconds |
| User agent | Client user agent |

Settings of the front router's request logging:

| Setting | Meaning | Default |
|---|---|---|
| Verbose | Add more information per request; slight overhead; changeable at runtime | off |
| Token ID logging | With verbose on, add the ID of the token used for each request | off |
| Forwarded-for logging | Log the value of a forwarded-for header | off; header `X-Forwarded-For` |

Security: the forwarded-for value is whatever the client or the nearest proxy put there.

### OPS-020 · Outbound request log
Priority: **MVP**

One record per request that the system sent to an upstream on behalf of a remote repository or a
replication job. Same conventions as OPS-019.

| Column | Meaning |
|---|---|
| Timestamp | Completion time, UTC, millisecond precision |
| Trace ID | Trace identifier of the originating request |
| Remote repository | Key of the remote repository |
| User name | User on whose behalf the request ran, or `anonymous` |
| Method | HTTP method in upper case |
| Remote URL | Full upstream URL |
| Status | HTTP status from the upstream |
| Request length | Bytes sent; `-1` when unknown |
| Response length | Bytes received; `-1` when unknown |
| Duration | Milliseconds |

Interactions: remote repositories in [remote-repositories.md](remote-repositories.md); replication
in [replication-and-federation.md](replication-and-federation.md).

### OPS-021 · Front router access log
Priority: **P2**

The front router of each node writes one JSON object per request, including traffic between
internal services.

| Field | Meaning |
|---|---|
| Backend address | Internal address the request was forwarded to |
| Client address | Caller address as received, usually IP and port |
| Response size | Bytes of the response body |
| Status | HTTP status |
| Duration | Processing time in nanoseconds |
| Method | HTTP method in upper case |
| Path | Relative request path |
| Start time | Start of processing, UTC, nanosecond precision |
| Trace header | Full distributed-trace header value |
| User agent | Client user agent |
| Time, level, message | Completion time, `info`, empty message |

### OPS-022 · Security access log
Priority: **MVP**

The repository service records security-relevant events with their source address and context:
accepted and rejected logins, downloads, browsing and deployments.

| Field | Meaning |
|---|---|
| Timestamp | UTC |
| Trace ID | Request trace identifier |
| Response and action | `ACCEPTED` or `DENIED`, followed by the action |
| Repository path | Optional `repository:path` |
| Message | Optional system message |
| User and address | User name (or `anonymous`) and IP address |
| Authentication type | Optional: API key or token |

Actions: annotate, download, deploy, delete, search, login, configuration change, create, update,
approve, form submit, property added, property removed, property updated, build create, build
promote, build delete.

UI location: administration, system logs, where the log can be viewed and downloaded (OPS-027).

Edge cases: the timestamp is either an ISO timestamp or a space-separated date with a comma before
the milliseconds; the exact format is unspecified. A hosted deployment omits accepted and denied
download entries because the request log already holds them.

### OPS-023 · Audit trail of security changes
Priority: **MVP**

The identity service records every change to security entities.

Recorded operations: create, update and delete of users, groups, permission policies and access
tokens; updates of the identity-service configuration (newer versions).

| Field | Meaning |
|---|---|
| Date | Timestamp with millisecond precision and zone offset |
| Trace ID | Request trace identifier |
| User IP | Address of the acting user; `unknown` when an internal service token acted |
| User | Acting user name; `unknown` when an internal service token acted |
| Logged principal | Identity of the internal service that performed the operation against the identity service |
| Entity name | Name of the changed entity (user, group, permission policy, ...) |
| Event type | `C` create, `U` update, `D` delete |
| Entity kind | User, group, permission or token (short codes) |
| Data changed | JSON describing what was added or changed; passwords appear masked |

Permission changes encode actions as single letters: read, annotate, deploy/cache, delete/overwrite,
manage.

| Setting | Meaning | Default |
|---|---|---|
| Audit trail enabled | Record audit events | on |
| Maximum number of files | Rolling file count | 10 |
| Maximum file size | Size per file | 100 MB (1 GB in total) |
| Identity configuration change logging | Log changes to the identity-service configuration; recommended to keep on | on |

Operation: the audit switch is changed through the identity service's runtime configuration patch
(a YAML fragment inside a JSON body); administrator with an access token; self-managed only.

Edge cases: two different letters are in use for annotate, so the letter set is not reliable; the
spec decides which one applies. No operation for reading the audit trail is
specified; it is a file.

Interactions: auditor role (IAM-006 in [identity-and-access.md](identity-and-access.md)); hosted
collection and streaming of audit logs (OPS-092, OPS-093).

### OPS-024 · Log rotation and archiving
Priority: **P2**

Per-log rotation settings in the node configuration file (OPS-040).

| Setting | Meaning | Default |
|---|---|---|
| Maximum size | Size in MB at which a file is rotated | 25 MB (front router request log: 100 MB; UI service application and request logs: 10 MB) |
| Maximum files | Rotated files to keep | 10 (package metadata service: 100) |
| Compress | Compress rotated files | on |
| Keep last uncompressed | Rotated files left uncompressed | 1 |
| Maximum age | Days to keep rotated files; overrides the file count where both are set | 365 days for several auxiliary services; 0 for the front router logs |
| Console log | Combine all service console output into one file | on |

Edge cases:

- The combined console log is rotated hourly by a scheduled job only on some installation types;
  archive installations, Mac and Windows, and hand-built container compositions get no rotation.
  On those installations, switching the console log off or adding external rotation is
  recommended.
- Logs of the embedded servlet container are outside the system's control and need external
  rotation; copy-and-truncate rotation can lose lines written in between.

### OPS-025 · Log level configuration
Priority: **P1**

Each logger has a level (error, warning, info, debug, trace).

| Service kind | Where | Takes effect |
|---|---|---|
| Java-based services | Per-service logging configuration file | Reloaded within seconds, no restart |
| Other services | Logging section of the node configuration file, with optional per-package levels | Restart; several router and event-service levels are marked changeable at runtime |

Interactions: temporary override through the API (OPS-026); verbose import/export logging
(OPS-034).

### OPS-026 · Temporary logger level override
Priority: **P1**

Raises logger levels in memory for a limited time, for example to collect debug output while
investigating an issue, without touching configuration files.

| Operation | Inputs | Output | Notes |
|---|---|---|---|
| Set temporary levels | Map of logger name to level (1 to 100 loggers); duration in seconds | `200` with a confirmation text | Levels revert to the previous values when the duration ends; propagated to all cluster nodes |
| Clear temporary levels | none | `200` with a confirmation text | Restores the previous levels on all nodes before the timer ends |
| Get level | Logger name (path parameter) | The level: trace, debug, info, warn, error or fatal | Answered by the receiving node only |

Privilege: administrator. Errors: `400` when no logger is given, more than 100 loggers are given,
or the duration is negative; `401`; `403`; `500`. Self-managed installations.

Edge cases:

- A second set request must not be sent while one is active; clear first, then set again.
- Unknown logger names are accepted without error, so a typo is only fixed by clearing or waiting
  for the timer.
- Get for an unknown logger returns the root logger's configured level.

### OPS-027 · System log viewer
Priority: **P2**

UI location: administration, monitoring, system logs. The administrator picks a service, a node and
a log file; the view shows the tail of the file, refreshes it every few seconds and can be paused
and resumed; the file can be downloaded. Self-managed installations only. The view should not be
left open needlessly because it costs server resources.

### OPS-028 · Live log API
Priority: **P2**

Programmatic tailing of logs, used by a plugin of the companion command-line tool.

| Operation | Inputs | Output |
|---|---|---|
| Live log configuration | none | Available logs, cluster nodes, refresh rate in milliseconds |
| Live log data | Log ID (required); file size seen previously (optional) | Next content chunk (at most 300 KB), the file's last-modified timestamp, the response timestamp, the file's total size |

| Setting | Meaning | Default |
|---|---|---|
| Live logs enabled | Turns both operations on | must be switched on (default unspecified) |
| Refresh rate | Interval clients should poll at | unspecified; example 10 s |
| Concurrent sessions per user | Limit on parallel use of the operations; whether it counts sessions per user or concurrent users in total is unspecified | unspecified; example 10 |
| Read block size | Chunk size in KB | 300 |

Privilege: unspecified beyond `403` for callers without the necessary permission; a token with
the live-logs read scope grants access (TOK-014 in [access-tokens.md](access-tokens.md)). Errors:
`400` when the log ID is missing or empty, `401`,
`403`, `500`, each with a JSON error list.

Edge cases: only request logs are offered; how the optional file size selects the chunk is
unspecified. A hosted deployment treats the feature as deprecated and offers only the main request
log.

### OPS-029 · Syslog forwarding
Priority: **P2**

Java-based services can forward their logs to a syslog daemon by adding a syslog appender (syslog
host, facility, message pattern) to the logging configuration and attaching it to the root logger;
a restart is required. The receiving daemon must accept network input over UDP or TCP (port 514 in
the example).

### OPS-030 · Log analytics integrations
Priority: **P2**

A clustered deployment spreads its state over 25 or more log files. Ready-made integrations collect
all of them into an external analytics tool: a Fluentd-based unified logging layer, New Relic, Splunk, Prometheus with Grafana, Datadog and Sumo Logic. The repository
service also has built-in log appenders that ship access, console, request and traffic logs to Sumo
Logic in batches. Per appender, the runtime management beans (OPS-016) expose read-only figures:
queue capacity and current fill, the remaining-capacity threshold below which trace and debug
records are discarded, batch size limit and current batch size, the batch quiet period in
milliseconds, and the category header sent with each batch. How these appenders are configured,
and the configuration details of the individual integrations, are unspecified here.

Interactions: hosted log streaming (OPS-093).

## Support

### OPS-031 · Support bundle
Priority: **P1**

Collects diagnostic information into one archive for a support case. Self-managed installations
only.

UI location: administration, support page, "create new support bundle". Fields: service, and
check boxes for the items below. Bundles are stored in an internal system repository and can be
downloaded from there.

| Item | Meaning | Default |
|---|---|---|
| Name, description | Free text | none |
| Configuration files | Files that affect the system's behavior | on |
| System information | Storage, system properties, runtime and CPU details, plugin status | on |
| Slowest database queries | The ten slowest queries (PostgreSQL, MySQL, MariaDB, SQL Server) | off |
| Logs | Include logs | on |
| Log start date | First day (date only) | the day before the end date |
| Log end date | Last day (date only) | today |
| Thread dumps: count | Number of dumps | 1 |
| Thread dumps: interval | Milliseconds between dumps | 0 |

| Operation | Inputs | Output |
|---|---|---|
| Create bundle | Fields above | Bundle ID plus, per service, the service ID and the location of its part in the internal repository |
| Get bundle metadata | Bundle ID | Name, description, ID, the parameters used, creation time, status |
| List bundles | none | Bundles (ID, name, description, creation time, status) and their count |
| Download bundle | Bundle ID | Archive stream |
| Delete bundle | Bundle ID | `204` |

Privilege: administrator. Errors: `400` on a hosted deployment; `404` for an unknown bundle; `400`
when a download fails.

Behavior and edge cases:

- In a cluster one bundle covers all nodes. Aggregation across services is cancelled after a
  timeout (default 1 h in the front router and in the event service).
- Logs are selected by file modification time, not by the timestamps inside them.
- Thread dumps and logs are resource-intensive and can make bundles large.
- The identity service's part contains a snapshot of its configuration without licenses (OPS-048).
- Who besides administrators can read the internal repository that stores the bundles is
  unspecified; bundles carry configuration files and logs.

## Backup, import and export

### OPS-032 · Scheduled system backups
Priority: **P2**

Named, scheduled backups of the whole system (configuration, security, binaries and metadata) to a
server directory, in the system export format (OPS-034). Any number of backups can be defined, each
with its own schedule and repository selection. UI location: administration, services, backups.

<details>
<summary>Backup settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Enabled | Backup runs on its schedule | unspecified |
| Backup key | Unique logical name | none; required |
| Schedule | Cron expression | none; example every 12 hours |
| Next run | Read-only: next scheduled run | — |
| Target path | Server directory; each run creates a subdirectory named by timestamp; must lie outside the installation's home directory (enforced in newer versions) | none |
| Notify administrators on errors | Email all administrators when a run hits a problem (needs OPS-060) | unspecified |
| Excluded repositories | Repositories left out | empty |
| Exclude new repositories | Repositories created later are not added automatically | unspecified |
| Verify free space | Check that the target has room; otherwise abort and log | unspecified |
| Incremental | Back up only changes since the last run into one fixed directory (named "current"), usable with incremental file-system backup tools | unspecified |
| Retention period | Hours to keep a backup before it is cleaned up; non-incremental backups only | unspecified |
| Zip archive | Write the backup into a zip archive; slow and CPU-intensive | unspecified |

</details>

Behavior and edge cases:

- Backups are not deduplicated: every artifact is written with its metadata, so a backup can be
  larger than the system's storage.
- Restore is a system import (OPS-035).
- The service log marks the start and the successful end of the underlying full export.
- The retention cleanup may also remove files it did not write, so nothing else may be kept in the
  target directory.
- In a cluster any node may run a backup. Either all nodes get write access to a shared target, or
  background tasks are pinned to one node through the node setting for background-task affinity
  ("any" by default; "none" keeps the node free of background jobs). With a node-local target path
  the backup lands on whichever node happened to run it.
- Each backup and system export also leaves a timestamped identity-service backup file in a backup
  folder; deleting old ones by hand is recommended (the identity service keeps at most 10, template
  default).
- Sizing guidance: built-in backups below 100 GB; between 100 and 500 GB with close monitoring or
  component backups; above 500 GB or one million artifacts only component backups (OPS-033),
  because runs otherwise overlap their own schedule.

### OPS-033 · Component backup and disaster recovery
Priority: **P1**

For larger installations, backing up the components separately is recommended instead of running
system backups:

| Component | How | Notes |
|---|---|---|
| Database | Replication or periodic snapshots | Take the snapshot before copying the binaries, and close in time, so neither side references something missing |
| Binary store | Snapshots, rsync to a dedicated mount, or object storage with versioning | Binaries are named by checksum; without the database they cannot be identified |
| Installation home | Snapshot | Configuration, properties, logs |
| Master key | Separate, secure copy | AES key that encrypts shared data in the database; without it every encrypted password is lost on restore. Not the same key as the configuration-secret key of IAM-045 in [identity-and-access.md](identity-and-access.md) (see OPS-034) |

A system export with content excluded (OPS-034) produces a metadata-only companion, but is not
recommended as the primary mechanism at scale because export and restore time grow with the number
of artifacts.

Disaster recovery options: active-passive or active-active pairs kept in sync by federated
repositories (up to ten deployments) or by push and pull replication; for two-way sync with
replication, a virtual repository per site combines a local repository and a remote repository
pointing at the other site. Repository sync does not carry users, groups, permissions or tokens;
those need identity federation. Other configuration must be kept in step separately:

| Configuration | How it is kept in step |
|---|---|
| Central configuration descriptor | API or bootstrap (OPS-042) |
| Directory and single sign-on settings | Authentication-provider bootstrap file (OPS-047) |
| Advanced runtime properties | By hand or orchestration tooling |
| Node configuration and storage configuration | By hand (they contain node-specific values) |
| New repositories | Created on both sites through the API |

Filestore sharding adds redundancy at write time (no more than ten shards) but does not replace
backups.

Interactions: [replication-and-federation.md](replication-and-federation.md),
[storage-and-blobs.md](storage-and-blobs.md), [ha-and-deployment.md](ha-and-deployment.md).

### OPS-034 · System export
Priority: **P2**

Exports the whole system — configuration, security entities, binaries and metadata — to a directory
on the server. The format is the backup format. Self-managed installations only. UI location:
administration, import and export, system.

<details>
<summary>Export options</summary>

| Option | Meaning | Default returned by the settings-template operation |
|---|---|---|
| Export path | Target directory on the server (the UI offers a server file browser) | — (required) |
| Include metadata | Export repository metadata; the UI phrases it as "exclude metadata"; Maven 2 metadata is exported either way | on |
| Exclude content | Leave binaries out | off |
| Maven 2 compatible | Add Maven 2 repository metadata and checksum files | off |
| Create archive | Write a zip archive (slow and CPU-intensive) | off |
| Incremental | Incremental export | off |
| Bypass filtering | Meaning unspecified | off |
| Verbose | Log at debug level into the separate import/export log (recommended) | off |
| Fail on error | Abort on the first error | on |
| Fail if empty | Fail when there is nothing to export | on |

</details>

| Operation | Inputs | Output |
|---|---|---|
| Get export settings template | none | The options with their defaults |
| Export system | Options | Plain-text export log |

Privilege: administrator. Errors: `400` on a hosted deployment; `401`; `403`; `500`.

Edge cases: container-image repositories need their metadata, so metadata must not be excluded
when they are present. Two different keys are involved: the key that encrypts secrets in the stored
configuration (IAM-045) is written into backups and full exports when it sits in its default
location, and a later full import puts it back and switches that encryption on with it; the
separate master key that encrypts shared data in the database is not specified as part of the
export and has to be copied by hand before an import (OPS-035). Large exports take long; the UI
suggests navigating away and watching the logs.

### OPS-035 · System import
Priority: **P2**

Restores a system export or backup from a server directory. Users, groups, permissions and tokens
are imported and kept.

| Option | Meaning | Default returned by the settings-template operation |
|---|---|---|
| Import path | Source directory on the server | — (required) |
| Include metadata | Import repository metadata | on |
| Exclude content | Import neither binaries nor metadata, only builds and configuration (UI) | unspecified |
| Verbose | Debug-level import/export log | off |
| Fail on error | Abort on the first error | on |
| Fail if empty | Fail when the source is empty | on |

| Operation | Inputs | Output |
|---|---|---|
| Get import settings template | none | The options with their defaults |
| Import system | Options | Plain-text import log |

Privilege: administrator. Self-managed installations only. Errors: `400`; `401`; `403`; `406` when
the request's content type is not supported; `500`. A tunable can switch system import off.

Behavior and edge cases:

- The source's master key (AES, 128 or 256 bit) must be copied to the target first, on single
  nodes and clusters alike; it encrypts the shared data in the database.
- Import between different product versions is discouraged.
- The import files must be readable by, and owned by, the service account.
- If a zip import fails, extract the archive and import the directory.
- Cluster: keep one node running and import there; on every other node remove the node-local
  identity, security, UI and plugin configuration, copy the master key, then start the node; check
  the result on the service status screen (OPS-008). Dependent services (distribution, scanner, CI
  pipelines) must be restarted after an import.
- An import into an instance that takes part in repository federations drops every remote member
  so the import can finish. Recovery: re-establish trust, then on each remote side remove the
  imported member and add it again, which starts a full sync.

### OPS-036 · Repository export
Priority: **P2**

Exports the data and metadata of one repository, or of all repositories, to a server directory.
Self-managed installations only. UI location: administration, import and export, repositories.

| Field (UI) | Meaning |
|---|---|
| Source repository | One local repository or all repositories |
| Export path | Target directory on the server |
| Exclude metadata | Leave repository metadata out (Maven 2 metadata is unaffected) |
| Maven 2 compatible | Add Maven 2 metadata and checksum files |
| Verbose | Debug-level import/export log |

API operation "export repository with metadata": action (fixed value "repository"), target path
(required), repository key (required), exclude metadata (default off), Maven 2 format, verbose. It
creates two directories, one with the data and one with the metadata, and returns a plain-text
log. Privilege: administrator.

Edge case: the API export does not produce the layout that repository import (OPS-037) expects.

### OPS-037 · Repository import
Priority: **P2**

Fills one or all repositories from a previous export or from any directory in Maven 2 layout.

| Field (UI) | Meaning |
|---|---|
| Target repository | One local repository or all repositories |
| Server path | Source directory on the server (self-managed only) |
| Zip file | Upload a zipped repository instead (available to all users, including hosted) |
| Exclude metadata | Leave repository metadata out |
| Verbose | Debug-level import/export log |

API operation: server path (required; a single repository's folder or the parent folder holding
all repositories), repository key (optional; empty means all), metadata flag (0 or 1, default 1),
verbose flag (0 or 1, default 0). Output: plain-text import log. Privilege: administrator.

Layout: a single-repository import expects the content at the root of the folder or zip; an
all-repositories import expects one folder per repository whose name equals the target repository
key.

Edge cases: container-image repositories need their metadata. A local Maven client cache can be
imported into a local repository so that already-downloaded artifacts become available on the
server.

### OPS-038 · Identity-service configuration export and import
Priority: **P2**

| Operation | Behavior | Output |
|---|---|---|
| Export identity configuration | Writes a timestamped JSON backup of the identity-service configuration into a backup folder on the server | `202` |
| Import identity configuration | Loads a backup file that the operator placed under a fixed name in the identity service's configuration directory; only that file is read | `200`; `400` invalid input |

Privilege: administrator. Self-managed installations only. The authentication-provider bootstrap
content (OPS-047) can be retrieved through the export.

### OPS-039 · Instance migration by export and import
Priority: **P2**

Two procedures move an installation to a new server:

| Procedure | Steps |
|---|---|
| Short downtime | Disable garbage collection on both servers; copy the binary store; take the old server off the network; system export with content excluded; stop the old server; final rsync of the binary store; full system import on the new server; switch DNS or traffic; re-enable garbage collection |
| Near-zero downtime | Disable garbage collection on both; copy the binary store; system export with content excluded; full import on the new server; configure push replication from every local repository of the old server to the new one with delete sync off; switch traffic; run all replication jobs to carry over the delta; stop the old server; re-enable garbage collection |

Interactions: OPS-034, OPS-035, push replication in
[replication-and-federation.md](replication-and-federation.md).

## Configuration

### OPS-040 · Node configuration file
Priority: **MVP**

Each service reads one YAML configuration file at startup: a shared section for all services of the
node and one section per service that overrides the shared values for that service. The file is
treated as configuration as code: it can be prepared outside and injected at startup (in Helm
installations it is rendered from the chart values or provided as a secret).

Behavior:

- Value precedence: environment variables, then the service's section, then the shared section,
  then built-in defaults.
- Changes take effect on restart.
- Passwords and similar secrets written in plain text are encrypted on first read and the encrypted
  value is written back into the file; this is the only case in which the system modifies the file.
- Two templates ship with the system: a basic one (identical to a fresh file) and a full one listing
  every key with its default; commented-out values in the templates are the defaults.
- Indentation must use spaces; tabs make the file fail to load.
- Custom environment variables can be passed to all services or to one service; variables with the
  system's reserved prefix are ignored with a warning.

<details>
<summary>Operationally relevant keys (other keys belong to the pages of their subsystems)</summary>

| Setting | Meaning | Default |
|---|---|---|
| Node ID | Unique node identifier | generated at startup |
| Node IP and name | Address and name of the node | resolved by the startup script |
| Cluster member | Node is part of a high-availability cluster | unspecified |
| Background-task affinity | `any`: the node may run background jobs (garbage collection, scheduled replication, backup, ...); `none`: only a small set of system jobs | any |
| Service start timeout | Seconds to wait for the application server to come up | 60 |
| HTTP connector worker threads | Maximum request threads | 200 |
| Maintenance connector | Separate port with its own small thread pool for health checks issued by the front router | 5 threads, accept queue 5 |
| HTTPS connector | Additional TLS listener with certificate and key files | off |
| Database connection pool | Maximum open, maximum idle, minimum idle connections | 100, 10, 1 |
| HTTP client request timeout | Upper bound for internal HTTP requests | 30 s |
| Front router outbound proxy | Proxy URLs for all outgoing HTTP and HTTPS requests of the node's front router, plus hosts reached directly; localhost is always reached directly | none |
| Key file wait timeout | How long a starting service waits for the master and join key files to appear | 120 s |
| Skip default administrator | Do not create the default administrator account on first start | off |
| UI session refresh | Interval after which the UI service's session token is refreshed | 30 min |
| Expose application headers | See OPS-006 | on |
| Logging, rotation | See OPS-018, OPS-024 | — |
| Health checks | See OPS-004 | — |
| Metrics | See OPS-011, OPS-015 | — |
| Run-as user and group | Account the service runs as (archive installations installed as a service) | unspecified |

</details>

Interactions: advanced runtime properties (OPS-041); join and master keys (IAM-043, IAM-045 in
[identity-and-access.md](identity-and-access.md)); webhook delivery settings
([events-and-extensibility.md](events-and-extensibility.md)); deployment in
[ha-and-deployment.md](ha-and-deployment.md).

### OPS-041 · Advanced runtime properties
Priority: **P2**

A second file holds advanced tunables of the repository service as key-value properties, as an
alternative to passing them as runtime options of the hosting process; in Helm installations they
are passed as extra runtime options. Changes need a restart. The mechanism is advanced and
typically unnecessary, and must not be confused with an internal properties file the system
maintains itself. Tunables of directory and single-sign-on providers moved from this
file into the identity-service configuration (OPS-048). Examples of tunables named elsewhere on this
page: the number of repositories reported in outbound-pool metrics (OPS-012), garbage collection
worker threads and full-run frequency (OPS-065), the load-protection mode (OPS-064), switching off
system import (OPS-035).

### OPS-042 · Central configuration descriptor
Priority: **P2**

The repository service keeps its global configuration as one document (XML, described by a
published schema).

Behavior:

- A descriptor file found at first start is loaded, then renamed, and from then on the configuration
  lives in the database, so database backups include it. On every start the current configuration
  is also written out as a backup file.
- An import file with a fixed name, found at startup, overrides the global configuration (newer
  versions need a companion import file for repository configuration). Used to copy configuration
  to another instance.
- Repository configuration and directory, single-sign-on and similar authentication-provider
  configuration were moved out of the descriptor into their own APIs and files. The repository
  configuration is written out periodically as a JSON snapshot file, which is the file the
  companion import above is made from.
- A separate security import file, found at startup, overrides all security settings; changing
  security through the descriptor API is deprecated.

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Get configuration | none | Full descriptor as XML | administrator |
| Save configuration | Full descriptor as XML | `200`; `400` invalid XML | administrator |

UI location: administration, advanced, configuration descriptor (an editor for the full document).
Self-managed installations only. A wrong document can leave the system unusable, so a backup first
is recommended.

### OPS-043 · Declarative configuration patch
Priority: **P1**

Configuration as code for a running system: an administrator sends a YAML document that creates,
updates and deletes configuration elements in one request; one change or many can go into the same
document.

| | |
|---|---|
| Inputs | YAML document (`application/yaml`) |
| Output | `200` with a plain-text result |
| Errors | `400` invalid input; `401`; `403` |
| Privilege | administrator; self-managed installations only |

Document structure: `key: entity` pairs. Top-level keys name configuration sections (OPS-044);
inside a section, entities are keyed by their identifier, for example a repository key.

Edge cases:

- Merge semantics are unspecified. The request is a partial update and deletion needs an explicit
  null (OPS-045), which implies that unnamed elements and fields keep their values. A named field is
  overwritten, including fields with far-reaching effects such as a repository's package format;
  a backup first is recommended.
- A document without any elements deletes all local, remote and virtual repositories, which
  contradicts plain merge semantics for that case.
- Some fields are accepted with `200` but not applied, for example the SAML user-name attribute,
  which is silently dropped.

### OPS-044 · Configuration patch sections
Priority: **P1**

<details>
<summary>Sections of the declarative configuration document</summary>

| Section | Content |
|---|---|
| Local, remote, virtual and federated repositories | One entry per repository key with that repository's settings (owned by [repositories.md](repositories.md), [remote-repositories.md](remote-repositories.md), [replication-and-federation.md](replication-and-federation.md)) |
| Push replications | One entry per replication key: source repository, schedule, target URL, credentials, include and exclude path prefixes, proxy, socket timeout, event-based replication, enabled, sync deletes, sync properties, sync download statistics, distributed checksum storage |
| Pull replications | One entry per replication key: remote repository, schedule, include and exclude path prefixes, event-based replication, enabled, sync deletes, sync properties |
| General | Base URL (OPS-051), UI upload size limit (OPS-057), date format and server name (OPS-053), global offline mode |
| Folder download | Settings of OPS-058 |
| Global replication blocking | Block push, block pull (OPS-059) |
| System message | Enabled, message, title, title colour as hex value (OPS-055) |
| Trash can | Enabled, retention days (ART-036 in [artifacts.md](artifacts.md)) |
| Property sets | Per set: properties with predefined values (each with a default flag), closed list, multiple choice, visibility (ART-026 in [artifacts.md](artifacts.md)) |
| Proxies | Per proxy key: host, port, credentials, NTLM host and domain, hosts it may redirect to, default flag, services it is the default for (RMT-048 in [remote-repositories.md](remote-repositories.md)) |
| Mail server | Settings of OPS-060 |
| Security | Anonymous access; lock policy (enabled, failed attempts); password encryption policy for clients (required, supported, unsupported); password expiration (enabled, maximum age, email reminder); directory servers and directory group settings; Crowd; SAML; OAuth providers; trusted-proxy single sign-on (owned by [authentication-providers.md](authentication-providers.md) and [identity-and-access.md](identity-and-access.md)) |
| Backups | Per backup key: schedule, excluded repositories, retention hours, zip archive, enabled, exclude new repositories, mail on error (OPS-032) |
| Maven indexer | Enabled, schedule, included repositories (OPS-071) |
| Scanner integration | Enabled |
| Garbage collection | Schedule (OPS-065) |
| Storage quota | Enabled, limit percentage, warning percentage (OPS-066) |
| Unused cached artifact cleanup | Schedule (OPS-065) |
| Virtual repository cache cleanup | Schedule (OPS-065) |

</details>

Schedules in this document and on the maintenance screens use a six-field cron syntax with a
seconds field and `?` (Quartz style), for example every 12 hours.

### OPS-045 · Deleting elements through a patch
Priority: **P1**

A key set to `null` or `~` deletes the element: a whole entity (for example a repository) or a
single field (its value is cleared).

Edge cases: within repository entries only string-typed fields can be cleared with null; other
fields need a value of their type. The package-format field cannot be cleared.

### OPS-046 · First-start bootstrap file
Priority: **P1**

An alternative to the first-run wizard (UI-007 in [ui-inventory.md](ui-inventory.md)): a YAML file
placed in the configuration directory before the first start sets up the basics. Any subset may be
given; the rest is configured in the wizard or later.

| Item | Meaning |
|---|---|
| License key | Out of scope (OPS-078) |
| Base URL | OPS-051 |
| Proxies | One or more outbound proxy definitions, including the default flag and the services it applies to |
| Default repositories | Package formats for which default repositories are created |

Loading rules: the file is applied only when no repositories exist yet, and only for items that are
not already configured by other means (base URL, proxy, license); after loading, the file is
deleted. Whether the system was bootstrapped by wizard or by file, it stores a timestamped copy of
its initial configuration, which can be renamed and placed on another instance to bootstrap it the
same way. Directory, Crowd and trusted-proxy single-sign-on settings moved out of this file into
the authentication-provider bootstrap file (OPS-047).

### OPS-047 · Authentication-provider bootstrap file
Priority: **P2**

A YAML file in the identity service's configuration directory pre-configures authentication
providers: trusted-proxy single sign-on, one or more directory servers, directory group settings,
Crowd, one or more SAML configurations. It is loaded on the next restart and deleted after
loading; like the first-start file (OPS-046) it is meant for fresh instances, and another file of
the same kind already present may take precedence over it. Configurations with the same name are
overwritten. Newer versions apply changes to this file without creating a new configuration. Its
content can be retrieved through the identity configuration export (OPS-038). It is also the
supported place for the SAML user-name attribute that the configuration patch drops (OPS-043).

Interactions: [authentication-providers.md](authentication-providers.md).

### OPS-048 · Identity-service configuration documents
Priority: **P2**

The identity service is configured through YAML documents in its configuration directory:

| Document | Effect |
|---|---|
| Template | Read-only reference of every key with recommended defaults and sample values; must never be loaded (it carries a marker that blocks import) |
| Patch | Merged into the current configuration on the next restart; all other settings stay |
| Import | Replaces the entire configuration on the next restart; must contain everything to keep |

The current configuration is stored in the database. Previous versions are recorded in a history
table (older versions instead wrote timestamped files, and reverting meant renaming one to the import
name and restarting). Retained versions: 10 (template default). A schema version inside the document
triggers a migration when it is not current. Some keys can also be changed at runtime through a
patch operation that takes a YAML fragment inside a JSON body (example: the audit trail switch,
OPS-023).

Content areas: password and lock policies, multi-factor authentication, token defaults and limits,
authentication options and caches, directory, OIDC and SAML client tuning, TLS between nodes,
scheduled tasks (expired-token revocation, heartbeats), cluster synchronization retries,
environment and project limits, identity federation, an outbound proxy, delegation rules for the
manage action, the event client, and application integrations (OPS-049).

Operators should keep their own copies of patch and import files outside the directory.

Interactions: [identity-and-access.md](identity-and-access.md), [access-tokens.md](access-tokens.md),
[authentication-providers.md](authentication-providers.md).

### OPS-049 · Application integrations
Priority: **P2**

Third-party applications obtain OAuth client credentials from the system. UI location:
administration, general management, manage integrations, with tabs for OIDC integrations
(CIOIDC-001 in [oidc-ci-integration.md](oidc-ci-integration.md)), a source-hosting application, and
external applications.

Creating an application integration: unique application name; type (a certified collaboration
integration with a predefined callback URL, or a custom integration with its own callback URL);
description; then "generate client ID and secret".

Enabling the feature needs three steps and a restart: a flag in the identity configuration
(default off), at least one integration template (ID, display name, redirect URI, scope — only the
"applied permissions of the user" scope is supported — and an internal flag, default off, that hides
the template from the UI), and a UI feature switch.

Release cycles of integrations follow the third-party product; upgrading an integration may require
re-registering with that product.

### OPS-050 · MIME type mapping
Priority: **P1**

A system-wide table maps file extensions to content types for downloads and for UI behavior.

| Attribute | Meaning |
|---|---|
| Type | Unique MIME type name (required) |
| Extensions | Comma-separated extensions mapped to it (required) |
| Index | Index the content for archive search (archive types only) |
| Archive | The type is a browsable archive |
| Viewable | The UI may show it as text |
| Syntax | Highlighter syntax for viewable types |
| Icon | Display icon style |

Behavior: the first entry whose extensions contain the file's extension wins; files with an unknown
or no extension get `application/octet-stream`. Changes need a restart. Some types are used
internally (for example the checksum type) and must not be changed carelessly. A reserved property
on an individual artifact overrides the content type of its downloads.

Interactions: MIME-type mismatch blocking on remote repositories (RMT-023 in
[remote-repositories.md](remote-repositories.md)); archive search (SRCH-005 in
[search-and-query.md](search-and-query.md)).

## General settings

### OPS-051 · External base URL
Priority: **MVP**

The externally visible base URL of the system. Without it, generated URLs are built from the
context URL the embedded server reports for the incoming request. When the system sits behind a
reverse proxy or load balancer, an administrator sets it so that links and redirects point at the
public address. It also serves operations that have no request to derive an address from, for
example generated emails.

| | |
|---|---|
| Set through | UI general settings (UI-044 in [ui-inventory.md](ui-inventory.md)), first-run wizard, bootstrap file (OPS-046), configuration patch (OPS-044), dedicated operation |
| Dedicated operation | Plain-text body with the URL; `200` with a confirmation text; `400` invalid URL; administrator; self-managed only |

Edge cases: in a cluster the base URL must be the load balancer's URL; upgrade verification checks
this (OPS-073). Behind a reverse proxy it should match the public server name (OPS-061). Single sign-on
requires it (AUTHN-002 in [authentication-providers.md](authentication-providers.md)).

### OPS-052 · Per-request base URL override
Priority: **P1**

A dedicated request header supplies an alternative base URL for one request, so the system can
answer under several host names. Best practice is to let the reverse proxy set it to the scheme,
host and port the client used. The header name is unspecified.

Security: who may send the header is not restricted; any caller who reaches the service directly
can make it generate links and redirects to a host of their choice. The proxy should overwrite the
header on every request.

### OPS-053 · Server name, date format and help
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Server name | Name in the title of every page | unspecified |
| Date format | Format of dates in the UI; mandatory | unspecified; example `dd-MM-yy HH:mm:ss z` |
| Help component | Context-sensitive help at the top right of the UI | unspecified |

UI location: UI-044 in [ui-inventory.md](ui-inventory.md).

### OPS-054 · Logo
Priority: **P2**

A custom logo at the top left of every screen, either uploaded (at most 100 MB; the file is copied
into the UI configuration directory) or referenced by URL.

### OPS-055 · System banner message
Priority: **P1**

A message shown at the top of every screen, for example to announce maintenance.

| Setting | Meaning | Default |
|---|---|---|
| Enabled | Show the message | off (example) |
| Title | Title of the message | none |
| Title colour | Colour of the title (hex value in the configuration document) | unspecified |
| Message | Text; links are written as `[<URL>, <link text>]` | none |

### OPS-056 · Login notice
Priority: **P2**

A dialog shown before login that users must acknowledge before they can log in. Settings: enabled,
title, type (info, warning, error, success — selects the icon), message. Defaults unspecified.

Interactions: login screen (UI-006 in [ui-inventory.md](ui-inventory.md)).

### OPS-057 · Repository-manager general settings
Priority: **P1**

UI location: administration, repository settings, general settings. Administrator privilege.

<details>
<summary>Settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| UI upload size limit | Maximum size in MB of a file uploaded through the web UI; `0` means unlimited | unspecified; example 100 |
| Items per browser page | Maximum items (files and folders) shown per batch at one level of the tree or directory browser; a "load more" entry fetches the next batch | unspecified |
| Global offline mode | No remote repository contacts its upstream, whatever the per-repository offline flag says; only cached and local content is served (RMT-031 in [remote-repositories.md](remote-repositories.md)) | unspecified; example off |
| Global replication blocking | OPS-059 | off |
| Archive search indexing | Index the entries of deployed archives for archive search (SRCH-005 in [search-and-query.md](search-and-query.md)); costs much storage; off on a hosted deployment | unspecified |
| Folder download | OPS-058 | — |
| Incomplete release-set cleanup period | Hours after which a partially distributed first-generation release set counts as incomplete and its temporary files may be removed; `0` disables the cleanup (see [distribution-and-lifecycle.md](distribution-and-lifecycle.md)) | unspecified |
| Trash can | Enabled, retention in days, "empty trash can" action (ART-036 in [artifacts.md](artifacts.md)) | unspecified; example 14 days |

</details>

### OPS-058 · Folder download limits
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Enable folder download | Folders can be downloaded as archives | unspecified |
| Folder download for anonymous users | Also for anonymous callers | unspecified |
| Maximum size | Maximum folder size in MB | unspecified |
| Maximum number of files | Maximum artifacts below one folder | unspecified |
| Maximum parallel folder downloads | Concurrent folder download requests | unspecified |

Interactions: ART-015 in [artifacts.md](artifacts.md); UI-015 in [ui-inventory.md](ui-inventory.md).

### OPS-059 · Global replication blocking
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Block replications | Master switch for the two settings below | off |
| Block push replication | Push replication stops regardless of repository configuration | unspecified |
| Block pull replication | Pull replication stops regardless of repository configuration | unspecified |

Interactions: [replication-and-federation.md](replication-and-federation.md).

### OPS-060 · Mail server
Priority: **P1**

Outgoing email for notifications. UI location: administration, general management, mail server.
Self-managed only; a hosted deployment uses a predefined mail server and hides the screen.

| Setting | Meaning | Default |
|---|---|---|
| Enabled | Send email notifications | unspecified |
| Host | Mail server host; mandatory | none |
| Port | Mail server port; mandatory | none; example 25 |
| User name, password | Authentication at the mail server | none |
| From | Sender address of all mails | unspecified |
| Subject prefix | Prefix for every subject | unspecified |
| Link URL | Base URL used for links in mails | unspecified |
| Use TLS | TLS when connecting | unspecified |
| Use SSL | Secure connection when connecting | unspecified |
| Send test email | Recipient address for a test message | — |

Mail is sent for, among others: change notifications to followers (ART-046 in
[artifacts.md](artifacts.md)), backup warnings and errors (OPS-032), license violations, project
storage quota notifications (IAM-061 in [identity-and-access.md](identity-and-access.md)),
password expiry reminders and token expiry notifications (TOK-023 in
[access-tokens.md](access-tokens.md)).

## HTTP front end and load

### OPS-061 · Reverse proxy configuration generator
Priority: **P2**

The system generates configuration snippets for an NGINX or Apache reverse proxy from a set of
fields, and offers them for download. Self-managed installations only. UI location: administration,
general, HTTP settings.

<details>
<summary>Fields</summary>

| Field | Meaning | Default |
|---|---|---|
| Server provider | None (embedded server), NGINX or Apache | unspecified |
| Internal host name | Host the proxy uses to reach the system; `localhost` when co-located | unspecified |
| Internal application port | Direct access to the repository service (REST API and downloads) | from node configuration |
| Internal router port | Access to the platform services and the UI | from node configuration |
| Public server name | Name users use to reach the system | none |
| Server name expression | Wildcard expression of accepted host names (for subdomain addressing) | unspecified |
| Internal and public application context | Context paths behind and in front of the proxy | unspecified |
| Use HTTP / use HTTPS | Listeners to generate | unspecified |
| HTTP port | Public HTTP port | 80 |
| HTTPS port | Public HTTPS port | 443 |
| TLS certificate and key paths | Files referenced in the snippet | none |
| Container registry method | OPS-062 | unspecified |

</details>

| Operation | Output | Privilege |
|---|---|---|
| Get reverse proxy configuration | The fields above | administrator; other users with relevant permissions receive only the method, the ports and the repositories they may see |
| Update reverse proxy configuration | `200`; `400` invalid | administrator |
| Get snippet | The generated NGINX configuration as text | administrator |

In a cluster the generator adds a load-balancing section that spreads requests over the nodes.

### OPS-062 · Container registry addressing method
Priority: **P1**

How container clients reach individual repositories through the front end. The choice also sets the
registry name or port in each container repository's advanced settings.

| Method | Behavior |
|---|---|
| Subdomain (recommended) | The repository key is the first DNS label of the host name; requires a wildcard certificate covering that many levels |
| Repository path | The repository key is the first path segment |
| Port per repository | Each repository gets its own port on the public server name; recommended together with one virtual repository that aggregates the others |
| Direct access | Clients talk to the embedded server without a proxy |

Interactions: container format page under [formats/README.md](formats/README.md).

### OPS-063 · Reverse proxy deployment rules
Priority: **P1**

Rules for running behind a reverse proxy:

- Co-locate the proxy with the system and bind the system exclusively to the proxy host; set the
  base URL (OPS-051) to the public server name.
- Pass the base-URL override header (OPS-052) from the proxy.
- Proxies whose address matches an "internal proxies" pattern are trusted for client-address
  headers; the default pattern covers 10/8, 192.168/16, 169.254/16 and 127/8, but not 172.16/12.
- Caching binaries in the proxy is not supported (stale metadata, upload problems); use a chained
  remote repository instead.
- Request buffering in the proxy delays large uploads (the proxy forwards only after the client has
  finished); switch it off and use HTTP/1.1 towards the system.
- Mutual TLS is terminated at the proxy, which forwards the verified client certificate in a header;
  the proxy must strip that header from every incoming request and set it only after successful
  verification (AUTHN-034 in [authentication-providers.md](authentication-providers.md)).
- Container registries require a reverse proxy for some addressing methods (OPS-062).

### OPS-064 · Load protection
Priority: **P2**

Protects a node from one activity monopolizing the request worker threads, the most common cause of
outages. Each incoming request is classified into one or more activities; the system tracks the
share of worker threads each activity holds relative to the configured maximum.

| Mode | Rejects requests? | When it acts |
|---|---|---|
| Simulation (self-managed default) | no | Never; records what would have been throttled |
| Reactive | yes, `429` | Only while the node is in an outage state. Open: that state is either exhausted worker threads with high request latency, or all worker threads busy with the connection queue full; the spec decides |

<details>
<summary>Activities and thresholds</summary>

| Activity | Threshold (share of worker threads) |
|---|---|
| Requests waiting for the scanner to clear unscanned artifacts | 80 % |
| Downloads waiting for a scanner decision | 80 % |
| Requests waiting for upstream curation to approve a cached package | 80 % |
| Concurrent uploads to one repository | 80 % |
| Concurrent downloads and uploads of one package format | 85 % |
| Low-priority database-bound requests | 80 % |
| General upload and download activity | 90 % |
| Requests to one API module (for example the npm API) | 90 % |
| Requests to one endpoint | 80 % |
| Requests from one user | 80 % |

</details>

Behavior:

- Only the offending activity receives `429`; other requests continue. One request can count
  towards several activities at once (for example one user and one API module).
- Reactive throttling of the affected activities lasts about 20 minutes, then they return to their
  previous mode.
- When all threads are busy, the log names every activity holding more than 20 % of the threads;
  the mode is logged at startup.
- The mode is a runtime tunable (OPS-041); changing it needs a restart. A hosted deployment runs with
  protection on by default.
- Clients should retry `429` with backoff; clients that do not retry may fail builds during spikes.

## Maintenance

### OPS-065 · Maintenance jobs and schedules
Priority: **P1**

UI location: administration, repository settings, maintenance. Each job shows its cron schedule
(OPS-044 syntax), the next run time and a "run now" action. A hosted deployment runs these jobs for
the customer.

| Job | Behavior | Default schedule |
|---|---|---|
| Binary garbage collection | Removes binaries no longer referenced by any path. With the trash can on, a faster strategy removes trash older than the retention period and deletes a binary when no other path references its checksum; it runs on several worker threads (tunable, example 3); every 20th run (tunable) is a full run that also removes binaries that became unreferenced otherwise. A tunable skips ordering in full runs for speed. Resource-intensive. | every 4 hours |
| Unused cached artifact cleanup | Removes remote-repository cache entries marked as unused (RMT-021 in [remote-repositories.md](remote-repositories.md)); recommended at most once or twice a day, outside working hours; the schedule and "run now" exist only on self-managed installations | unspecified |
| Virtual repository cache cleanup | Deletes aggregated metadata (Maven descriptors) cached by virtual repositories that is older than 168 hours | unspecified |

Edge cases: queued background jobs can be listed (ART-051 in [artifacts.md](artifacts.md)). Garbage
collection itself is specified in [storage-and-blobs.md](storage-and-blobs.md).

### OPS-066 · Storage quota
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Quota control enabled | Watch disk usage | off (example) |
| Storage limit | Percentage of disk space that may be used before deployments are rejected | unspecified |
| Storage warning | Percentage at which a warning is issued | unspecified |

Behavior: above the limit every deployment is rejected with `413` and the maintenance screen shows
that datastore disk usage is too high; above the warning level a warning is logged and shown. The
partition checked is the one holding the binary store, or the local cache directory when binaries
live in the database or in object storage.

Related controls that limit disk use, owned elsewhere: maximum unique snapshots (REPO-025) and
container tag limits (REPO-027) in [repositories.md](repositories.md); unused cached artifact
cleanup (RET-036 in [retention-and-cleanup.md](retention-and-cleanup.md)); project storage quotas
(IAM-061 in [identity-and-access.md](identity-and-access.md)); scripted cleanup through the search
API ([search-and-query.md](search-and-query.md)).

### OPS-067 · Prune unreferenced data
Priority: **P1**

A manual maintenance action removes binary files and empty folders in the binary store that no
database record references (left over, for example, after file-system permission errors or a full
disk). It does not remove empty folders in the repository tree; those disappear asynchronously when
the last item below them is removed. Supported for object-storage providers in newer versions.

### OPS-068 · Compact the embedded database
Priority: **Out of scope**

With the embedded database, a maintenance action reclaims allocated but unused space; it may not
complete while storage is busy. A system that always uses an external PostgreSQL has no embedded
database.

### OPS-069 · SHA-256 backfill job
Priority: **P2**

A background job computes SHA-256 checksums for binaries stored before the system computed them.

| Operation | Inputs | Default |
|---|---|---|
| Start backfill | Batch threshold | 10 |
| | Query limit | 100 |
| | Pause while waiting for the cluster (ms) | 5000 |
| | Pause between batches (ms) | 5000 |
| | Force the job onto one node ID | none |
| Stop backfill | Pause (ms), which must match the value used at start | 5000 |

Both return a plain-text "scheduled" confirmation; start answers `400` on a hosted deployment (no
such error is specified for stop); administrator; self-managed only. The job can also be configured
through runtime tunables. A system that computes SHA-256 at deploy time needs it only for API
compatibility (ART-010 in [artifacts.md](artifacts.md)).

### OPS-070 · Package metadata re-index
Priority: **P2**

When the package metadata service is inconsistent, the repository service re-sends its metadata
events for given repositories or folders, recursively.

| Input | Meaning | Default |
|---|---|---|
| Paths | Repository keys or folder paths (required) | — |
| Asynchronous | Return immediately | on |
| Delete first | Remove the stored metadata of those paths before re-indexing | off |

Output: `200`, or `202` when accepted asynchronously; `400` invalid paths. Administrator. Edge case:
requesting a plain-text response yields `406`.

### OPS-071 · Maven index calculation
Priority: **P2**

The system offers Maven indexes for IDE integrations. Indexes of remote repositories are fetched
from the upstream when it provides one, otherwise computed from the cached artifacts; local and
virtual repositories get computed indexes.

| Setting | Meaning | Default |
|---|---|---|
| Enabled | Run on schedule | off (example) |
| Schedule | Cron expression | none |
| Next run | Read-only | — |
| Run now | Immediate run | — |
| Included / excluded repositories | Which repositories the next run indexes; "clear all" empties the included list | none |

Security: an index exposes the content of a repository to everyone who can read the index,
regardless of path-level permissions. Computing indexes for large local or virtual repositories is
resource-intensive. On a hosted deployment only dedicated servers have it.

## Upgrade and migration

### OPS-072 · Upgrade compatibility policy
Priority: **MVP**

| Rule | Content |
|---|---|
| Within a major version | In-place upgrade directly to any later minor version; no intermediate versions |
| Across major versions | A dedicated migration procedure, not an in-place upgrade; a table defines the minimum source version for each target version; companion services must be upgraded to compatible major versions together |
| Installation method | Upgrade with the same method used for installation (Helm, containers, Linux packages, archive) |
| Flow | Back up data and database; read the release notes; download; stop; install; (cross-major only) migrate and verify the configuration; start; check the logs |
| Downtime | A single node is unavailable during the upgrade; a cluster upgraded node by node stays available (OPS-073) |
| Helm | The chart refuses to upgrade when the required join and master keys cannot be resolved |

Cross-major migration translates the old configuration into the new file format, keeps only values
(comments are lost), writes a migration log, and keeps the old administrator accounts, which may be
deleted afterwards. The reverse proxy configuration must be regenerated.

### OPS-073 · Rolling upgrade of a cluster
Priority: **MVP**

Procedure:

1. Take the first node out of the load balancer and wait until it is idle (request log quiet, no
   running background tasks).
2. Upgrade and start it. It migrates the database schema on startup.
3. Put it back; repeat for every other node, each after a graceful shutdown.
4. Verify: log in through the load balancer, check the service status screen (OPS-008), check that
   the base URL is the load balancer's URL, open each node directly, and look for the node ID in the
   system log.

While the nodes run different versions, the cluster restricts itself to uploads and downloads and
blocks schema and repository configuration changes; version-mismatch exceptions in the logs are
expected until the last node is upgraded.

Edge case: with a shared network file system, only the binary data folder may be shared; the front
router's data folder must be unique per node.

### OPS-074 · Schema and data migrations on upgrade
Priority: **MVP**

- On the first start of a new version, database converters change the schema; this is expected and
  not an error.
- Some upgrades also start a background data migration (for example moving package metadata into
  the package metadata service) on first start. It causes no downtime but limits search results
  while it runs; the UI shows a notice; an interrupted migration continues where it stopped on the
  next start; its pace is a thread count (1 or less switches it off); its status can be read from a
  status table in the database. Benchmark: about 60,000 to 67,000 package versions per hour with 5
  threads.

### OPS-075 · Migration from another repository manager
Priority: **P2**

A separate migration tool, run on the host of another (widely used) repository manager, moves its
content into the system. Free and commercial editions of that product are supported.

<details>
<summary>What is migrated</summary>

| Migrated | Not migrated |
|---|---|
| Hosted and proxy repositories with their configuration | Build information and CI data |
| Repository metadata and artifacts with checksums | Product-specific plugins of the source |
| Users; roles as groups; privileges as permission policies | Repository routing rules |
| Directory connections and groups | Group repository configuration (see edge cases) |

Privileges: only repository-view privileges and content-selector privileges are migrated; of the
selector conditions, only path equality, path prefix and their disjunction are carried over, other
conditions are dropped while the privilege itself is migrated.

</details>

Stages, run in this order or all at once: configure (target URL and token or administrator
credentials; source URL, working directory and credentials; a default password for migrated users
with at least eight characters including a digit, a lower-case and an upper-case letter) → read the
source configuration → create repositories → migrate artifacts → migrate security. The stages can be
re-run to bring over later additions; with the newer source major version only entities added since
the previous run are carried over, with the older one everything is migrated again.

<details>
<summary>Options and editable mapping files</summary>

| Option | Meaning | Default |
|---|---|---|
| Skip security | Do not read users, groups and privileges | off |
| Repository selection | Names or wildcards of repositories to migrate | all |
| Skip existing | Skip artifacts that already exist; when a readable artifact with the same checksum exists, copy it on the server instead of transferring | off |
| Force | Migrate repositories regardless of their recorded status | off |
| Print failures | Write the list of failed artifacts to a file | off |
| Include patterns | Only artifacts under these paths | all |
| Asset list from file | Take the artifact list from a prepared file | off |
| Created after / downloaded after | Only artifacts created or last downloaded after a date; "created after" wins when both are given | none |
| Threads | Parallel workers | half the CPU cores |
| Log level | | INFO |
| Checksum calculation | Compute checksums during upload (more memory) | off |
| Convert external users and groups to internal | | off |
| Rename rules | Regular-expression rules for repository, user, group, directory and permission names; names starting with a non-letter get a prefix, whitespace becomes a dash | prefix rule |
| Python index path rewrite | Ordered source/target regular-expression rules applied to paths in Python repositories, to move content away from the root paths the target reserves for its own index. Without rules, directory-style index entries are skipped (the target rebuilds its index) and packages keep their original path, which the target may reject if it collides with a reserved root. With rules, a package already present at its original path from an earlier run is not uploaded again | none |

Editable files after reading the configuration: repository name mapping, security name mapping,
repository list (remove an entry to skip it), security entities (remove an entity and every
reference to it; passwords are base64-encoded).

</details>

Status and resumption (kept only for the newer source major version): each repository is recorded
as not started (no status), in progress or completed. An aborted run resumes with the artifacts not
yet present; completed repositories are skipped; a delta run uses "force" with "skip existing", or
the operator resets a repository's status by hand.

Edge cases:

- A same-named target repository of a different type makes artifact migration fail; same-named
  artifacts are overwritten; same-named users are overwritten.
- Container repository names are lower-cased.
- If the source stores binaries in object storage, the target needs temporary space of at least the
  largest file size times the thread count.
- The summary lists group repository configuration as not migrated, while the generated repository
  list contains virtual repositories built from the source's groups; which applies is unspecified.

### OPS-076 · Signed release artifacts
Priority: **P1**

Every release binary is signed with an OpenPGP key, and a detached signature is published next to
it, plus the public keys; the Debian package repository of the releases is signed with the same key.
A key change (for example from a 1024-bit key to a 4096-bit RSA key) applies from a cut-over date
on; older releases stay signed with the old key, so verifiers import both keys. Verification:
download binary and signature, import the public keys, verify the signature with a standard OpenPGP
client.

## Billing, licensing and hosted-service functions

These functions belong to an operator-run customer portal, which exists in a hosted and a
self-managed variant. Its dashboard is UI-055 in [ui-inventory.md](ui-inventory.md) and its API
token is TOK-045 in [access-tokens.md](access-tokens.md); the remaining functions follow.

### OPS-077 · License-tier gating
Priority: **Out of scope**

Switching capabilities on or off per paid license tier. A self-hosted product has no license tiers.

### OPS-078 · License management
Priority: **Out of scope**

Installing and inspecting license keys (single node and per cluster node, with expiry and license
hash), license buckets and license export in the portal. No licenses exist in a self-hosted product.

### OPS-079 · Portal accounts and sign-in
Priority: **Out of scope**

Invitation-based portal accounts with password, single-use email codes, social sign-in and an
authenticator-app second factor. Operator-run; no meaning self-hosted.

### OPS-080 · Portal single sign-on
Priority: **Out of scope**

SAML sign-in to the portal per customer domain, with attribute and group-to-role mapping and
just-in-time portal users. Operator-run.

### OPS-081 · DNS domain validation
Priority: **Out of scope**

Proof of domain ownership through a generated DNS TXT record (statuses validated, pending, failed)
before portal single sign-on. Operator-run.

### OPS-082 · Usage explorer
Priority: **Out of scope**

Charts and CSV export of data transfer, storage, consumption, project usage, scans and curated
packages per subscription, deployment or repository. Billing plumbing.

### OPS-083 · Detailed data-transfer logs
Priority: **Out of scope**

Hourly JSON-lines records of every billable upload and download, stored in an internal repository of
the hosted instance. Billing plumbing.

### OPS-084 · Billing
Priority: **Out of scope**

Monthly and prepaid billing views, invoices and payment card management. Billing plumbing.

### OPS-085 · Hosted instance provisioning
Priority: **Out of scope**

Creating hosted platform, edge and archive servers (name, cloud provider, region, initial
administrator) from the portal's topology page. Operator-run.

### OPS-086 · Portal users and roles
Priority: **Out of scope**

Portal-only roles (primary administrator, administrator, finance member, technical member; the
self-managed variant knows only the first two) with invitations valid for 24 hours. Operator-run.

### OPS-087 · Usage alert notifications
Priority: **Out of scope**

Daily or monthly usage-threshold alerts to roles, users or external addresses, with a notification
history. Billing plumbing.

### OPS-088 · Portal audit log
Priority: **Out of scope**

Record of actions attempted in the portal (event type, description, actor, time), kept for one year
and exportable. Operator-run; the system's own audit trail is OPS-023.

### OPS-089 · Support contacts and support portal
Priority: **Out of scope**

Registering named support contacts (personal addresses, time zone) and opening tickets in an
operator-run support portal, to which support bundles (OPS-031) are attached. Paid-support plumbing.

### OPS-090 · Subscription management
Priority: **Out of scope**

Upgrading, downgrading and cancelling subscriptions, switching cloud provider or region, single
sign-on from the system into the portal, and transfer of configuration from a self-managed to a
hosted instance. Billing plumbing.

### OPS-091 · Custom domain names for hosted instances
Priority: **Out of scope**

Uploading TLS certificates and mapping customer domains, including container-registry subdomains,
to hosted instances (limits on certificates and domains). Operator-run networking.

### OPS-092 · Hosted log collection
Priority: **Out of scope**

A hosted service collects audit and request logs into an internal repository, uploaded every 24
hours or at 25 MB, enabled through an administrator operation. Self-hosted logs go to the
operator's own collection (OPS-030).

### OPS-093 · Hosted log streaming
Priority: **Out of scope**

Near-real-time streaming of request, audit and access logs from hosted instances to commercial
analytics services, scoped per instance group. Operator-run.

### OPS-094 · Hosted network access controls
Priority: **Out of scope**

IP allowlists (at most 4,500 entries, no private ranges), private connectivity to cloud networks and
geolocation restrictions for hosted instances. Operator-run networking.

### OPS-095 · DNS routing policies
Priority: **Out of scope**

One routing host name with manual active-passive failover or geolocation-based routing between
hosted instances (up to 10 policies). Operator-run networking.

### OPS-096 · Preview feature program
Priority: **Out of scope**

Opt-in to pre-release features per subscription under separate terms. Operator program.

### OPS-097 · Deployment registry and usage reporting
Priority: **Out of scope**

Self-managed installations report to an operator-run portal, which lists them with ID, versions,
type and last report time and warns when one has not reported for 48 hours. Operator plumbing.

### OPS-099 · Entitlement connector and offline entitlement registration
Priority: **Out of scope**

A connector service in every self-managed installation registers the license with an operator-run
entitlement server and fetches the entitlements that switch services on and set their expiry. In
air-gapped mode (HA-076 in [ha-and-deployment.md](ha-and-deployment.md)) an administrator-scoped
token starts an offline registration that yields a request token; a connected machine trades it at
the entitlement server for an entitlement file plus a response token; both are uploaded to the
isolated installation. New entitlement files are available 14 days before a contract starts and
must be installed within a 14-day grace period; a contract that reduces features needs two uploads. The
operations are ADMAPI-106 in [api-admin-operations.md](api-admin-operations.md). No entitlements
exist in a self-hosted open product.

### OPS-100 · Contributing-developer count
Priority: **Out of scope**

A command of the companion CLI counts distinct commit authors (by email address) over a number of
months (default 1) across one repository, all repositories of an owner or group, or several Git
servers listed in a file (GitHub, GitLab, Bitbucket), scanning repositories in parallel (default
10) and caching results locally (default 3 days). It reads only the Git hosting API, not the
repository manager, and serves to size per-developer subscriptions. Licensing plumbing.

## Security-related system settings

### OPS-098 · Global read access to build information
Priority: **P2**

A system-wide security switch on the repository-manager security screen. When on, every caller,
anonymous ones included, may view the published modules of every build in the system, overriding
the permissions set on individual builds. Default unspecified. The same screen offers the
"revoke all API keys" action (IAM-044 in [identity-and-access.md](identity-and-access.md)).

Security: switching it on discloses build contents (module and artifact names, dependencies,
environment data captured in the build record) to everyone who can reach the system.

Interactions: build privileges (BLD-012 in [builds-and-ci.md](builds-and-ci.md)); anonymous
access ([identity-and-access.md](identity-and-access.md)).

## Notes for implementers

- Ping, readiness, liveness and version are specified as callable anonymously, and orchestrators
  send no credentials. Whether they keep answering when anonymous access is switched off
  (AUTHN-036) is unspecified; probes must not depend on that switch.
- The configuration patch is a partial update, yet an empty document deletes all local, remote
  and virtual repositories, and `null` or `~` deletes elements (OPS-043, OPS-045). Automation that
  renders patches from templates must never send an empty document, and an implementation should
  state its merge semantics explicitly because they are otherwise only implied.
- Log consumers parse the request and outbound logs by column position: the column count is fixed,
  missing values are empty, unknown lengths are `-1`, and the timestamp is the completion time
  (OPS-019, OPS-020). Structured JSON output must keep the same field set.
- The repository export operation writes data and metadata into two directories, which repository
  import does not accept as-is (OPS-036, OPS-037).
- A temporary logger override must be cleared before the next one is set, and unknown logger names
  are accepted silently (OPS-026).
- During a rolling upgrade a mixed-version cluster accepts only uploads and downloads (OPS-073);
  administrators and automation see configuration changes fail in that window.
- The base-URL override header turns into a link and redirect injection point unless the front end
  overwrites it on every request (OPS-052).
- Published metric types are inconsistent (totals as gauges, medians as counters) and two federation
  lag metrics are in seconds despite their help text (OPS-012, OPS-014). Dashboards depend on names
  and units, so both must be chosen deliberately and kept stable.
- Backup retention cleanup may remove foreign files in the target directory; backups are not
  deduplicated; the target must be outside the installation home (OPS-032).
- Two keys matter for restores: the configuration-secret key travels inside backups and exports
  when it sits in its default location, the master key does not and must be copied by hand before
  an import (OPS-034, OPS-035). A restore runbook that forgets the master key leaves every
  encrypted secret unreadable.
- Load protection answers `429` only to the offending activity (OPS-064); package-manager clients and
  CI scripts that do not retry `429` with backoff fail during spikes.
