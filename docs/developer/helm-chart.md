# Helm chart

The chart lives in [deploy/helm/trove](../../deploy/helm/trove). This page covers its layout, what
it renders, how its values reach the pods, how it is validated and tested, and how to extend it.
The second chart, `trove-bastion`, has its own section at the end: [The Bastion chart](#the-bastion-chart).
The requirements are in [ha-and-helm.md](../spec/ha-and-helm.md) (R-HA-002, R-HA-011, R-HA-016, R-HA-019,
R-HA-027 to R-HA-037, R-HA-042, R-HA-056, R-HA-060 to R-HA-064; the Jobs also R-BAK-012) and [endpoints.md](../spec/endpoints.md) (R-EP-008); the dev profile that installs it is described in
[local-profile.md](local-profile.md).

## Scope

Everything the chart renders is real and tested. It does not render what later phases add, and `values.schema.json` refuses their values
(`additionalProperties: false`), so a value of a later phase fails before anything renders instead
of being silently ignored.

| In the chart | Not in the chart yet |
|---|---|
| `api`, `packages` and `worker` Deployments, always (R-HA-002); path pools, worker pools and endpoint pools per role (R-HA-062, R-EP-008); the bridge `web` with `web.enabled` (R-HA-064); see [The cut into roles](#the-cut-into-roles) | the Gateway the chart creates (`gateway.create`, R-HA-053), `contentOrigin` |
| role Services `<fullname>-api` and `<fullname>-packages`, pool Services, ops Service `<fullname>-ops`, `sessionAffinity: None` (R-HA-037) | `TroveBackupCheckFailed` of R-HA-045 (the gauge of the platform check task `backup-check`, R-HA-023, is not exported), see [The PrometheusRule](#the-prometheusrule) |
| PodDisruptionBudget and HorizontalPodAutoscaler per Deployment, topology spread with `nodeTaintsPolicy: Honor` (R-HA-032) | |
| one NetworkPolicy per Deployment and per Job (migration, backup, restore), ingress only (R-HA-042, ADR-154) | |
| PrometheusRule with 30 alerts: the sixteen rows of R-HA-045 whose metrics the binary exports, `TroveRevocationJournalBacklog` and the six alerts of the backup runs (R-BAK-014), the three of the vulnerability scanning, `TroveMirrorFailing` and `TroveMirrorStale` (R-OPS-046, Q-OPS-160, Q-OPS-171), `TroveBridgeLeftOn` (R-VULN-024, Q-HA-92) and `TroveBlobMissing` (R-HA-024, Q-STOR-152), `metrics.prometheusRule.*` | |
| migration Job as `pre-install,pre-upgrade` hook, with hook ConfigMap and ServiceAccount (R-HA-019); not rendered while `restore.enabled` | the post-restore Job (`postRestore.*`, R-HA-025) |
| backup CronJob `<fullname>-backup` and restore Job `<fullname>-restore`, each with its ServiceAccount and a policy that admits nothing (R-HA-060, R-BAK-012), see [The backup and restore Jobs](#the-backup-and-restore-jobs) | |
| configuration file `trove.yaml`, Secret wiring including the login secrets `secrets.oidc` and `secrets.breakGlass`, downward-API variables (R-HA-030); `secrets.databaseBackup`, `secrets.backupTarget`, `secrets.backupTargetCA` | `trustedCA`, `bootstrap` |
| restricted-PSS security contexts, probes, spool and `/tmp` volumes (R-HA-033 to R-HA-035, R-HA-061) | `initContainers`, `sidecars`, `lifecycle`, Service types beyond the three in the schema |
| Ingress with the path rules of the roles (R-HA-037); HTTPRoute of the default endpoint (`gateway.*`, R-HA-038, see [The HTTPRoute](#the-httproute)); ServiceMonitor (`metrics.serviceMonitor.*`, R-HA-045, see [The ServiceMonitor](#the-servicemonitor)); TLS on trove's listeners (R-HA-063); the values reference of the spec (R-HA-031, see [The values reference](#the-values-reference)) | the HTTPRoute of further endpoints (R-EP-008); profiles `small`, `medium`, `large` (R-HA-050) |
| further endpoints: Services, Ingress, optional pool per role, certificates (R-EP-008), see [Endpoints](#endpoints) | the validation rows of the features in the right column |

Gaps in what is there:

- **The `config` subtree is free-form.** R-HA-029 wants it validated against the schema the binary
  emits (`internal/config/schema.json`, not generated yet). The schema types only the keys the
  chart reads itself; a typo elsewhere in `config` fails at runtime (strict validation in the
  binary), not at `helm install`. When the binary emits its schema, `values.schema.json` takes it
  as the `config` property.
- **The container is started with `args: [serve, --role, <role>]` and no `command`.** It relies on
  the image's `ENTRYPOINT` being the trove binary.
- **No kubeconform run** over the rendered output yet; the tool is not pinned.

## The cut into roles

The chart renders the roles of ADR-153 and ADR-155 ([roadmap](../coverage/roadmap.md#phase-6a--task-areas), WP6a.3). Every template that
renders something per Deployment ranges over one list, `trove.units` ([_units.tpl](../../deploy/helm/trove/templates/_units.tpl)): a
map per Deployment with its `name`, `role`, `pool`, `kind` (`main`, `pool`, `endpoint`, `bridge`), effective values `r`, and for a worker
pool its `queues`. The Deployment, its PodDisruptionBudget, HorizontalPodAutoscaler and NetworkPolicy therefore cannot disagree on
name, labels or selector.

| Object | Rendered | Spec |
|---|---|---|
| Deployments | `<fullname>-api`, `<fullname>-packages`, `<fullname>-worker`; path pools `<fullname>-<role>-<pool>`; worker pools `<fullname>-worker-<pool>`; endpoint pools `<fullname>-packages-ep-<name>` and `<fullname>-api-ep-<name>` (one per role the surface holds) | R-HA-002, R-HA-062, R-EP-008 |
| Services | `<fullname>-api` and `<fullname>-packages` on 8080; one per path pool; `<fullname>-ops`; per further endpoint `<fullname>-ep-<name>` and, for a surface of both roles, `<fullname>-api-ep-<name>`; worker pods have none | R-HA-002, R-HA-028, Q-EP-71 |
| Edge | per host the `Prefix` rules `/` and `/api/v1` to `api`, `/api`, `/files`, `/v2`, `/.well-known` to `packages`, and the paths of the path pools before them ([_edge.tpl](../../deploy/helm/trove/templates/_edge.tpl), `trove.edgeRules`); a further endpoint over one role is routed whole, one over both by the rows of its families | R-HA-037, Q-HA-71 |
| PDB, HPA | one per Deployment; the HPA (`autoscaling/v2`, CPU and memory targets, `metrics`, `behavior`) replaces `spec.replicas` | R-HA-032 |
| NetworkPolicy | one per Deployment, named as it, a rule per port its pods open; none for egress | R-HA-042, Q-HA-76 |
| Login secrets | `secrets.oidc[]` and `secrets.breakGlass` in `api` pods only (main, pools, endpoint pools) and the bridge's `web` pods | R-HA-030, Q-HA-79 |
| Environment | `TROVE_POOL_NAME` on every pool, `TROVE_JOBS_QUEUES` (a JSON list) on a worker pool, `TROVE_JOBS_EXCLUDE_QUEUES` on the main worker beside pools; the two preflight counts on the migration Job | R-HA-030, R-HA-016, Q-HA-82 |
| Upgrade bridge | three steps, each of which changes either the edge or the backends: `web.enabled: true` also renders `<fullname>-web`, the Service `<fullname>` and the edge `/` (step 1); `web.routed: false` beside it renders the edge of the target shape while the bridge keeps running (step 2, `trove.bridgeRouted` in `_edge.tpl`); `web.enabled: false` removes the bridge (step 3). The endpoint pools of the roles and a two-role endpoint's `<fullname>-api-ep-<name>` render beside the bridge, the BackendTLSPolicies cover `<fullname>` and the role Services for the whole bridge (`trove.edgeBackendServices`); a `lookup` guard refuses the upgrade that moves the edge (the Ingress `<fullname>` or an endpoint Service still on the bridge) while a serving Deployment of the target shape (api, packages, path pools, endpoint pools) is not ready, and step 3 while the edge is still on the bridge; `TroveBridgeLeftOn` reports a bridge left on | R-HA-064, Q-HA-73, Q-HA-90, Q-HA-92, Q-HA-130, Q-HA-132 |

A **pool** is the role's values with the entry's keys on top, key by key (`trove.poolValues`, so `false` and `0` override, and
`autoscaling` is inherited, Q-HA-84). A path pool opens the ports of the endpoints without a pool, like the main Deployment, but
only the default endpoint's edge routes its `paths` (Q-HA-83). The names of pools match `^[a-z][a-z0-9-]{0,19}$` and may not start
with `ep-`; `trove.validatePools` checks names, paths inside the role's families, queues and duplicates.

The **connection budget** (R-HA-016): `trove.connectionCounts` sums `maxReplicas` (or `replicaCount`) plus `maxSurge` over every
Deployment, serving roles and worker apart, and passes `TROVE_DATABASE_PREFLIGHT_EXPECTED_SERVING_REPLICAS` and
`..._WORKER_REPLICAS` to the migration Job; `NOTES.txt` prints the resulting `max_connections` (231 with the defaults, 311 while the
bridge runs). The key `expected_serving_replicas` is the binary's (WP6a.1); until it lands, `trove admin config validate` over
the rendered environment names it as unknown.

A path pool takes `routed: false` to render without its route, the first of two applies that add a pool (Q-HA-90). With an autoscaler a Deployment renders the live `spec.replicas` read through `lookup`, so switching the autoscaler on keeps the replicas (Q-HA-91). A provider of `config.auth.oidc.providers` that has a client secret and no entry in `secrets.oidc` fails the render (Q-HA-93).

The **guard** of the bridge ([_validate_roles.tpl](../../deploy/helm/trove/templates/_validate_roles.tpl)) reads Deployments with
`lookup`, which returns nothing under `helm template` and in these tests. The chart test checks that the rendering does not
depend on it; the refusal is verified against a cluster (the upgrade e2e test, `helm upgrade --dry-run=server`), see Q-HA-86. While
the bridge runs, its `web` pods hold the secrets of both roles
([H-518](../security/roles-and-trust-boundaries.md#h-518--the-bridge-and-the-development-role-hold-both-halves-of-the-cut)).

The HTTPRoute of R-HA-038 loops over the same table, see [The HTTPRoute](#the-httproute).

## Layout

```text
deploy/helm/trove/
├── Chart.yaml              apiVersion v2; version = appVersion = 0.0.0-dev (the release pipeline, build.yml, rewrites both)
├── values.yaml             defaults; a value is commented only where its name does not say it
├── values.schema.json      draft-07, additionalProperties false for everything the chart owns
├── tests/rules_test.yaml   promtool test rules for the PrometheusRule (not packaged)
├── ci/                     value sets the tests render (not packaged, see .helmignore)
│   ├── required-only.yaml      only the values R-HA-031 names as required
│   ├── ha-ingress.yaml         every optional Secret, Ingress with TLS, ephemeral spool
│   ├── single-node.yaml        no enforcement, one replica per role, standalone Valkey
│   ├── migrations-disabled.yaml  migrations.mode disabled, minAvailable budget, image digest
│   ├── monitoring.yaml         the PrometheusRule on, the vulnerabilities section the chart reads (the rule tests render this set)
│   ├── endpoints.yaml          two further endpoints on the main packages pods, own Ingress classes, certificates by SNI
│   ├── endpoints-pool.yaml     an endpoint with a pool of its own
│   ├── roles-pools.yaml        a path pool of api and of packages, two worker pools, an autoscaler, a mixed endpoint with pools
│   ├── bridge.yaml             web.enabled: the first step of the upgrade across the cut
│   └── s3-provisioner.yaml     bucket, region, endpoint and credentials from one provisioner Secret
└── templates/
    ├── _helpers.tpl        names, labels, image, byte and duration parsing, spool budget, config
    ├── _secrets.tpl        the Secret mounts of a pod, as one list used for volumes and mounts
    ├── _pod.tpl            probes, topology spread, downward-API variables
    ├── _validate.tpl       fail-fast validation (R-HA-029), endpoint validation (R-EP-008)
    ├── _units.tpl          the Deployments as data (trove.units), surface roles, endpoints per pod, replica counts
    ├── _edge.tpl           the edge's path rules (trove.edgeRules), read by the Ingresses and the BackendTLSPolicies
    ├── _validate_roles.tpl roles, pools, autoscalers, spool and memory budgets, the bridge guard
    ├── _deployment.tpl     one Deployment of a unit
    ├── _endpoints.tpl      endpoint defaults, trove.yaml block, mounts, S3 settings from a Secret
    ├── _notes.tpl          the text of NOTES.txt
    ├── validate.yaml       renders nothing; includes the validation
    ├── deployments.yaml    one Deployment per unit
    ├── services.yaml       the role and pool Services, <fullname> in the bridge, <fullname>-ops
    ├── pdb.yaml, hpa.yaml  one PodDisruptionBudget and, with autoscaling.enabled, one HorizontalPodAutoscaler per Deployment
    ├── endpoint-services.yaml, endpoint-ingress.yaml   per further endpoint
    ├── NOTES.txt           endpoints and warnings
    ├── networkpolicy.yaml  one ingress policy per Deployment and the migration Job's
    ├── prometheusrule.yaml the PrometheusRule (only with metrics.prometheusRule.enabled); the alerts are _rules.tpl
    ├── _rules.tpl          the alerts as a YAML list (trove.rules.all) and their names (trove.rules.names)
    ├── configmap.yaml      <fullname>-config (trove.yaml)
    ├── serviceaccount.yaml
    ├── migrate.yaml        hook ConfigMap, hook ServiceAccount and the migration Job
    ├── ingress.yaml        only with ingress.enabled
    └── extra-objects.yaml  extraObjects, rendered with tpl
```

| File | Responsibility |
|---|---|
| `_helpers.tpl` | `trove.fullname` (release name, or `<release>-trove`, cut at 50 characters so derived names stay DNS labels), `trove.labels` / `trove.selectorLabels`, `trove.image` (digest wins over tag), `trove.bytes` and `trove.seconds` (parse sizes and durations; they fail on anything else, so a typo never becomes a wrong budget), `trove.spoolBudget`, `trove.config` |
| `_secrets.tpl` | `trove.secretMounts` builds one list per scope (`runtime` for the Deployments, `migrate` for the Job); `trove.secretVolumes` and `trove.secretVolumeMounts` render it |
| `_units.tpl` | `trove.units`, the one list of Deployments; `trove.unitEndpoints` (which endpoint ports a pod opens, R-EP-002); `trove.poolValues`; `trove.connectionCounts` |
| `_validate.tpl`, `_validate_roles.tpl` | every `fail` of R-HA-029, each message naming the offending value |
| `deployments.yaml` | one loop over `trove.units`, so no role can drift from another; the same `trove.deployment` renders a pool |
| `migrate.yaml` | the Job and its hook resources; renders nothing with `migrations.mode: disabled` |

## What the chart renders

`<fullname>` is the release name, or `<release>-trove` when the release name does not contain
`trove`; `fullnameOverride` wins.

| Kind | Name | Notes |
|---|---|---|
| Deployment | `<fullname>-api`, `<fullname>-packages` | `trove serve --role api` / `--role packages`; ports `http` 8080 and `ops` 8081 (and the ports of the further endpoints they serve) |
| Deployment | `<fullname>-worker` | `trove serve --role worker`; port `ops` 8081 only |
| Deployment, Service (path pools only) | `<fullname>-api-<pool>`, `<fullname>-packages-<pool>`, `<fullname>-worker-<pool>` | per entry of `api.pools`, `packages.pools`, `worker.pools` |
| Deployment, Service | `<fullname>-web`, `<fullname>` | `web.enabled` only: `--role web`, the bridge of R-HA-064 |
| Service | `<fullname>-api`, `<fullname>-packages` | port `http` 8080, select the main pods of their role only, `sessionAffinity: None` |
| Service | `<fullname>-ops` | port `ops` 8081, selects every pod labelled `trove/serve: "true"` (`api`, `packages`, `worker`, `web`) |
| ConfigMap | `<fullname>-config` | `trove.yaml`, mounted at `/etc/trove/trove.yaml` |
| ServiceAccount | `<fullname>` | `automountServiceAccountToken: false` |
| PodDisruptionBudget | one per Deployment, named as it | `maxUnavailable: 1`, `unhealthyPodEvictionPolicy: AlwaysAllow` |
| HorizontalPodAutoscaler | one per Deployment, named as it | `autoscaling.enabled` of the role or pool; `autoscaling/v2` |
| NetworkPolicy | one per Deployment, named as it, and `<fullname>-migrate` (hook) | `networkPolicy.enabled`, ingress only, see [Network policy](#network-policy) |
| ConfigMap, ServiceAccount (hook, weight -10) | `<fullname>-migrate` | same `trove.yaml` as `<fullname>-config` |
| Job (hook, weight 0) | `<fullname>-migrate` | `trove migrate up`, which also applies `config.authz.role_bindings` (R-AUTHZ-010; `TestR_AUTHZ_010_DeclaredRoleBindingsReachTheMigrationJob`); `backoffLimit` 2, deadline 1800 s, TTL 86400 s |
| Service, Ingress; pool Deployments, PodDisruptionBudgets, HorizontalPodAutoscalers `<fullname>-<role>-ep-<name>` | `<fullname>-ep-<name>` (and `-api`) | per further endpoint, see [Endpoints](#endpoints) |
| Ingress | `<fullname>` | `ingress.enabled`; per host the path rules of R-HA-037 (all `Prefix`, port name `http`), `/` to `<fullname>` in the bridge; backend-over-TLS annotations per controller, see [TLS](#tls) |
| BackendTLSPolicy | one per Service the default Ingress names, named as the Service | `tls.backendTLSPolicy.enabled` only; Gateway API CRD, see [TLS](#tls) |
| anything | from `extraObjects` | |

The chart renders no Secret, no Role or RoleBinding, no PersistentVolumeClaim, no StatefulSet and
no CRD (R-HA-028, R-HA-033, R-HA-061); the tests check each.

Labels on every object: `app.kubernetes.io/name`, `instance`, `version`, `managed-by`,
`component` (`api`, `packages`, `worker`, `web`, `migrate`, `ops`, `config`, `ingress`, `serviceaccount`) and `helm.sh/chart`.
The pods of `api`, `packages`, `worker` and `web` also carry `trove/pool` (selector label: the pool's name, `ep-<name>` for an
endpoint pool, the empty value for the main Deployments, so that a pool never overlaps their selectors or a role Service's;
selector labels are immutable) and `trove/serve: "true"` (what the ops Service selects).

### Pod template

- `checksum/config` (hash of the rendered `trove.yaml`) and `checksum/encryption-keys` (hash of the
  key *names*) roll the pods when either changes. Secret contents are never hashed.
- `enableServiceLinks: false`, `automountServiceAccountToken: false`.
- Container: restricted-PSS security context, UID/GID 65532, read-only root file system.
- Probes (R-HA-034) on the named port `ops`: startup and liveness `GET /healthz`, readiness
  `GET /readyz`; each can be disabled or replaced (`probes.<name>.custom`).
- No `preStop` hook: the drain delay lives in the process (R-HA-011).
- Tolerations for `not-ready` and `unreachable` with `nodeFailureTolerationSeconds` (60).
- Topology spread (R-HA-032): `kubernetes.io/hostname` (`DoNotSchedule`) and
  `topology.kubernetes.io/zone` (`ScheduleAnyway`), both `maxSkew: 1`, `nodeTaintsPolicy: Honor`,
  `matchLabelKeys: [pod-template-hash]`. Non-empty `topologySpreadConstraints` replace them and
  carry their own `labelSelector`.
- Volumes (R-HA-061): `spool` (`emptyDir` with `sizeLimit`, or a generic ephemeral volume with
  `ReadWriteOnce` or `ReadWriteOncePod`), `tmp` (`emptyDir`), the config ConfigMap, the Secrets.

### Environment

The chart sets these variables and no others (R-HA-030):

| Variable | Value | Pods |
|---|---|---|
| `TROVE_POD_NAME`, `TROVE_POD_NAMESPACE`, `TROVE_NODE_NAME` | downward API (`metadata.name`, `metadata.namespace`, `spec.nodeName`) | every trove pod |
| `TROVE_RUNTIME_MEMORY_LIMIT_BYTES` | `resourceFieldRef limits.memory` | every Deployment |
| `TROVE_RUNTIME_TERMINATION_GRACE_PERIOD` | `<terminationGracePeriodSeconds>s` | every Deployment |
| `TROVE_POOL_NAME` | the pool's name, `ep-<name>` for an endpoint pool | the pods of a pool (R-HA-062, R-EP-002) |
| `TROVE_JOBS_QUEUES` | the pool's `queues` as a JSON list, `["vuln"]` | a worker pool |
| `TROVE_JOBS_EXCLUDE_QUEUES` | the sorted union of the worker pools' queues as a JSON list | the main `worker` while a pool lists a queue |
| `TROVE_STORAGE_S3_BUCKET`, `..._REGION`, `..._ENDPOINT` | `secretKeyRef` into `secrets.s3.existingSecret`, only for the keys named by `secrets.s3.bucketKey`, `regionKey`, `endpointKey` (the settings of an S3 provisioner's Secret; not credentials) | every Deployment |
| `TROVE_STORAGE_SPOOL_MAX_BYTES` | spool size minus `spool.headroom`, in bytes; not set when `config.storage.spool.max_bytes` is set | every Deployment |
| `GOMAXPROCS` | `resourceFieldRef requests.cpu`, only when the role has no CPU limit | every Deployment |
| `TMPDIR` | `/tmp` | every trove pod |
| `TROVE_DATABASE_PREFLIGHT_EXPECTED_SERVING_REPLICAS`, `..._WORKER_REPLICAS` | `serving_max` and `worker_max` of R-HA-016 | migration Job |

`extraEnv` and `extraEnvFrom` append to this list. The binary's strict validation accepts the
`TROVE_` names above as schema keys or as reserved variables (R-OPS-028).

### Configuration file

`trove.config` (in `_helpers.tpl`) renders `config` merged with the keys the chart manages. The
chart-managed keys win, and setting one inside `config` fails the render naming it
(`config.storage.spool.dir`, `config.database.password_file`, `config.secrets.active_key_id`, …).

### Secrets

Every Secret is pre-existing; the chart never creates one. Each is mounted read-only with `items`
(only the named keys), `defaultMode: 0440`, no `subPath`, so a rotation reaches the files, and
never as an environment variable (the one exception is the non-secret S3 settings above, see [S3 from a provisioner's Secret](#s3-from-a-provisioners-secret)). A mount exists only in the pods that read it.

| Value | Keys (default names) | Mount path | Config key set by the chart | Pods |
|---|---|---|---|---|
| `secrets.database` | `username`, `password` | `/var/run/secrets/trove/database/` | `database.username_file`, `database.password_file` | all (the Job uses `secrets.databaseMigration` when set) |
| `secrets.databaseBackup` | `username`, `password` | `/var/run/secrets/trove/database/` (the backup pods' mount of the database login) | `database.username_file`, `database.password_file` | the backup pods only; empty means `secrets.database` |
| `secrets.databaseCA` | `ca.crt` | `/var/run/secrets/trove/database-ca/` | `database.ssl_root_cert_file` | all |
| `secrets.databaseClientCert` | `tls.crt`, `tls.key` | `/var/run/secrets/trove/database-client/` | `database.ssl_cert_file`, `database.ssl_key_file` | all |
| `secrets.encryption` | the names in `secrets.encryption.keys` | `/var/run/secrets/trove/encryption/` | `secrets.encryption_key_files` (ordered), `secrets.active_key_id` (first entry without `.key`) | every Deployment, the same Secret (R-HA-027) and the restore Job, which checks that it opens every key ID of the backup (R-BAK-020); the migration Job and the backup pods never mount it (R-HA-030, R-BAK-012) |
| `secrets.s3` (`source: static`) | `access-key-id`, `secret-access-key` | `/var/run/secrets/trove/s3/` | `storage.s3.credentials.access_key_id_file`, `..._secret_access_key_file` | every Deployment, the backup pods (they read the blob store) and the restore Job (it writes it) |
| `secrets.s3CA` | `ca.crt` (the Secret's `key`) | `/var/run/secrets/trove/s3-ca/` | `storage.s3.ca_file` (also refused inside `config`) | every Deployment, the backup pods and the restore Job; never the migration Job |
| `secrets.backupTarget` (`config.backup.target.credentials.source: static`) | `access-key-id`, `secret-access-key`, `session-token` (with `sessionTokenKey`) | `/var/run/secrets/trove/backup-target/` | `backup.target.credentials.access_key_id_file`, `..._secret_access_key_file`, `..._session_token_file` (all three refused inside `config`) | the backup pods and the restore Job; no Deployment, not the migration Job |
| `secrets.backupTargetCA` | `ca.crt` (the Secret's `key`) | `/var/run/secrets/trove/backup-target-ca/` | `backup.target.ca_file` (refused inside `config`, Q-HA-146) | the backup pods and the restore Job |
| `secrets.valkey` | `username` (omit with `usernameKey: ""`), `password` | `/var/run/secrets/trove/valkey/` | `valkey.username_file`, `valkey.password_file` | every Deployment |
| `secrets.valkeySentinel` | `username` (default: none), `password` | `/var/run/secrets/trove/valkey-sentinel/` | `valkey.sentinel.username_file`, `valkey.sentinel.password_file` | every Deployment |
| `secrets.valkeyCA` | `ca.crt` | `/var/run/secrets/trove/valkey-ca/` | `valkey.tls.ca_file` | every Deployment |
| `secrets.valkeyClientCert` | `tls.crt`, `tls.key` | `/var/run/secrets/trove/valkey-client/` | `valkey.tls.cert_file`, `valkey.tls.key_file` | every Deployment |
| `secrets.breakGlass` | `password` (the Secret's `key`) | `/var/run/secrets/trove/break-glass/` | `auth.break_glass.password_file` (also refused inside `config`); without the Secret `auth.break_glass.enabled: false` | `api` pods (main, pools, endpoint pools) and the bridge's `web`; never `packages`, `worker`, the Job or backup pods (R-AUTH-014, R-AUTH-042, Q-HA-79) |
| `secrets.oidc[]` (`providerId`, `existingSecret`, `key`) | `client-secret` (the entry's `key`) | `/var/run/secrets/trove/oidc-<providerId>/` | `auth.oidc.providers.<providerId>.client_secret_file` (refused inside `config` for a listed provider) | `api` pods and the bridge's `web` only (R-AUTH-004, R-AUTH-042, Q-HA-79) |
| `secrets.metricsToken` (`config.metrics.auth: static`) | `token` | `/var/run/secrets/trove/metrics/` | `metrics.bearer_token_file` | every Deployment |
| `tls.secretName` | `tls.crt`, `tls.key` | `/var/run/secrets/trove/tls/` | `server.tls.certificates[0]` | the pods that load the pair, see [Mounts follow the listeners](#mounts-follow-the-listeners); never the Job |
| `tls.clientCA.secretName` | `ca.crt` | `/var/run/secrets/trove/tls-client-ca/` | `server.tls.client_ca_file`, `server.tls.client_auth` | the pods that open :8080 (`api`, `packages`, their path pools, the bridge's `web`), not an endpoint pool |
| `tls.ops.secretName` | `tls.crt`, `tls.key` | `/var/run/secrets/trove/tls-ops/` | `ops_tls.certificates[0]`, `ops_tls.inherit: false` | every pod that opens the operations listener (every Deployment) |
| `endpoints.<name>.tls.existingSecret`, each of `extraSecrets` | `tls.crt`, `tls.key` | `/var/run/secrets/trove/endpoints/<name>/<n>/` (`n` = position in `tls.certificates`) | `server.endpoints.<name>.tls.certificates` | the pods that serve the endpoint (the main Deployments of the roles its surface holds, or its pools) |
| `endpoints.<name>.tls.clientCaSecret` | `ca.crt` | `/var/run/secrets/trove/endpoints/<name>/client-ca/` | `server.endpoints.<name>.tls.client_ca_file` | the pods that serve the endpoint |

The file *names* inside a mount are fixed (`username`, `password`, `ca.crt`, …); the Secret's key
names are the values (`usernameKey`, `passwordKey`, …). The encryption keys are the exception:
file name equals Secret key, because the file name without `.key` is the key ID (R-ARCH-021).

## TLS

Requirement: R-HA-063 (Q-HA-32); the binary's side is R-ARCH-035. The Secret may come from cert-manager or from any other operator that writes `tls.crt`, `tls.key` (and `ca.crt`); the chart only names it. `tls.enabled` defaults to `true`.
The chart never creates the Secret (a cert-manager `Certificate` with `secretName` is the usual
source). `tls.*` describes the default client listener `:8080` and the operations listener
`:8081`. The endpoint feature adds an `endpoints:` list whose entries each carry their own `tls`
block of the same shape; `tls.*` stays the default endpoint and does not change.

<details>
<summary>Values reference</summary>

| Value | Default | Effect |
|---|---|---|
| `tls.enabled` | `true` | `false` renders no TLS wiring: HTTP probes, no `appProtocol`, no mount, no `server.tls` (plain HTTP inside the cluster, H-38) |
| `tls.secretName` | `""` | required while enabled; the render fails naming the key and `tls.enabled: false` |
| `tls.certKey`, `tls.keyKey` | `tls.crt`, `tls.key` | keys in the Secret; the file names in the pod are fixed |
| `tls.minVersion` | `"1.2"` | `server.tls.min_version` (and `ops_tls` when rendered) |
| `tls.reloadInterval` | `10s` | `server.tls.reload_interval` |
| `tls.clientCA.secretName`, `.key`, `.auth` | `""`, `ca.crt`, `require` | mutual TLS on `:8080`: `server.tls.client_ca_file` and `client_auth` (`require` or `verify_if_given`) |
| `tls.ops.secretName`, `.certKey`, `.keyKey` | `""`, `tls.crt`, `tls.key` | own certificate for `:8081`: `ops_tls.inherit: false` |
| `tls.backendTLSPolicy.enabled`, `.caConfigMapName`, `.hostname` | `false`, `""`, `<fullname>.<namespace>.svc` | optional `BackendTLSPolicy` for a Gateway |
| `ingress.backendTLS.controller` | `haproxy` | `haproxy` (HAProxy Ingress, primary), `traefik`, `ingress-nginx` or `manual` |
| `ingress.backendTLS.verify` | `true` | the controller verifies the trove certificate; `false` accepts an unverified connection |
| `ingress.backendTLS.caSecret` | `""` | haproxy, ingress-nginx: Secret in the release namespace with `ca.crt`; required while `verify` |
| `ingress.backendTLS.clientCertSecret` | `""` | haproxy: Secret with `tls.crt` and `tls.key` the controller presents to trove; required with `tls.clientCA` |
| `ingress.backendTLS.serversTransport` | `""` | Traefik: `<namespace>-<name>@kubernetescrd` of a `ServersTransport`; required while `verify` |

</details>

What the chart does with them:

- **Mount.** One directory volume per Secret, `items` for the keys the process reads (`tls.crt`,
  `tls.key`; `ca.crt` of the main Secret is for the edge and for Prometheus, not for this process,
  so it is not projected), read-only, mode 0440, no `subPath`. The kubelet replaces the files with
  an atomic symlink swap on a Secret update; the process polls the content every
  `tls.reloadInterval` and swaps the certificate without a restart. A subPath mount would never
  see the update. Only the pods that load a pair mount it, see below; the migration Job serves no listener (R-HA-030).
- <a id="mounts-follow-the-listeners"></a>**Mounts follow the listeners.** The private key of `tls.secretName` is mounted
  into a pod only when its process loads that pair (`trove.secretMounts`, `_secrets.tpl`): the pods that open :8080
  (`api`, `packages`, their path pools and the bridge's `web`: the client listener of the default endpoint), a pod that serves a further endpoint without a pair of its own, and every pod
  whose operations listener inherits it, which is every pod while `tls.ops.secretName` is empty. With `tls.ops`, the
  `worker` and an endpoint pool whose endpoints each bring `tls.existingSecret` hold no copy of the client listener's key;
  **set `tls.ops` when you run endpoint pools** (the residual credentials of a pool are H-57 in
  [endpoints.md](../spec/endpoints.md)). The client CA (`tls.clientCA`) mounts into the main `web` pods only, because the
  operations listener never carries it and a further endpoint has its own `clientCaSecret`. (A path pool opens :8080 and mounts it too.)
- **Config.** `server.tls` (and `ops_tls`) are chart-managed keys: setting `config.server.tls` or
  `config.ops_tls` fails while `tls.enabled` is true. `checksum/config` follows `min_version` and
  the paths, not the Secret content, so a certificate rotation does not roll the pods.
- **Operations listener.** Without `tls.ops` and `tls.clientCA`, `ops_tls` keeps its default
  (`inherit: true`). With `tls.clientCA`, the chart renders `ops_tls` with `inherit: false` and the
  same certificate pair but no client CA, because the kubelet's probes and Prometheus present no
  client certificate; mutual TLS applies to `:8080` only. With `tls.ops`, `ops_tls` uses that
  Secret.
- **Probes.** `httpGet.scheme: HTTPS` on port `ops`. The kubelet does not verify the certificate.
  A `probes.*.custom` probe replaces the generated one and is on its own.
- **Services.** Both ports declare `appProtocol: https`.
- **NetworkPolicy** is unchanged.
- **ServiceMonitor** (R-HA-063): the chart renders none yet. When monitoring joins, it sets
  `scheme: https` and `tlsConfig` with the CA from the Secret's `ca.crt` and `serverName` equal to
  the Service name.

### Ingress and Gateway

trove speaks HTTPS, so the controller must connect to the backend with HTTPS and should verify
it. The settings differ per controller and the chart sets only those of the controller named in
`ingress.backendTLS.controller`. HAProxy Ingress is the default and the tested target (the
standard edge of the owner's platform); the other rows are rendered and tested for their
annotations, but not run against a live controller. The annotations are inert on a controller
that does not read them, so a wrong `controller` does not break the Ingress, it leaves the
backend on plain HTTP and every request fails with a gateway error.

| `controller` | What the chart renders | What you provide |
|---|---|---|
| `haproxy` | Ingress annotations with the default prefix `haproxy-ingress.github.io/`: `secure-backends: "true"`; with `caSecret`: `secure-verify-ca-secret: <caSecret>` and `secure-sni: <fullname>.<namespace>.svc` (the name is validated against the certificate when a CA is given); with `clientCertSecret`: `secure-crt-secret: <clientCertSecret>` | the CA Secret with key `ca.crt` (a cert-manager Secret carries it, so `caSecret` can equal `tls.secretName`); for `tls.clientCA`, a Secret with `tls.crt` and `tls.key` that the CA signs. The Secrets must be in the release namespace: reading across namespaces is off in HAProxy Ingress unless `cross-namespace-secrets-ca` / `-crt` allow it. |
| `ingress-nginx` | Ingress annotations `nginx.ingress.kubernetes.io/backend-protocol: HTTPS`; with `caSecret`: `proxy-ssl-verify: "on"`, `proxy-ssl-secret: <namespace>/<caSecret>`, `proxy-ssl-name: <fullname>.<namespace>.svc`, `proxy-ssl-server-name: "on"` | the Secret with `ca.crt` (and `tls.crt`/`tls.key` when `tls.clientCA` is set: the controller presents them to trove) |
| `traefik` | Service annotations `traefik.ingress.kubernetes.io/service.serversscheme: https` and `...service.serverstransport` | a `ServersTransport` (a Traefik CRD the chart does not create) with `serverName: <fullname>.<namespace>.svc`, `rootCAs` from the CA, and `certificatesSecrets` for mTLS |
| `manual` | nothing | the backend protocol, through `ingress.annotations` and `service.annotations` of your controller |

The HAProxy Ingress key names are checked against its
[configuration keys](https://haproxy-ingress.github.io/docs/configuration/keys/) page
(section "Secure backend"): `secure-backends`, `secure-crt-secret`, `secure-sni`,
`secure-verify-ca-secret`, `secure-verify-hostname`, plus `backend-protocol` (`h1-ssl`, `h2-ssl`)
as the alternative to `secure-backends`. The chart uses `secure-backends` because it leaves the
HTTP version alone. A changed `--annotations-prefix` of the controller needs `controller: manual`
and your own annotations. With `verify: false` the chart renders only `secure-backends` (or the
nginx and Traefik equivalent): encrypted, but a man in the middle is not detected.

The upstream project retired ingress-nginx in March 2026 (see [ha-and-helm.md](../spec/ha-and-helm.md)
R-HA-037); it stays supported here because installations still run it.

Gateway API: `tls.backendTLSPolicy.enabled: true` renders a `BackendTLSPolicy`
(`gateway.networking.k8s.io/v1`) per Service the default Ingress names (`<fullname>-api`, `<fullname>-packages`, the path pools' Services; `<fullname>` in the bridge), named as the Service and targeting its port `http`, with
`validation.hostname` and a `caCertificateRefs` entry naming a ConfigMap. Gateway API takes a
ConfigMap there, not a Secret, so the chart cannot reference the Secret's `ca.crt`; you create
the ConfigMap (a cert-manager `trust-manager` Bundle does it). The chart renders no HTTPRoute yet (Q-HA-81),
so the policies apply to routes you create yourself.

## Endpoints

`endpoints` (values) is a map of endpoint name to entry; the entry `default` tunes the default endpoint, every other entry
is a further endpoint (R-EP-008, [endpoints.md](endpoints.md)). The commented block in
[values.yaml](../../deploy/helm/trove/values.yaml) lists every key with its default; `values.schema.json` refuses unknown
keys, and the keys `default` may not set (`port`, `baseUrl`, `tls`, `pool`, `ingress`, …).

| Per further endpoint | Object | Notes |
|---|---|---|
| `port`, `baseUrl`, … | `server.endpoints.<name>` in `trove.yaml` | `listen: ":<port>"`; `pool: ep-<name>` when `pool.enabled`; keys that are unset render their defaults (a further endpoint: `surface: [packages]`, `auth` bound SAT only, `exposure: rules`, `cache_fill: false`, `rate_limit_class: external`, `replica_header: never`, `max_in_flight: 1024`) |
| | Service `<fullname>-ep-<name>` and, for a surface over both roles, `<fullname>-api-ep-<name>` | port `ep-<name>` = `port` (target port numeric), `appProtocol: https` under `tls.enabled`, `sessionAffinity: None`; the first selects the pods of the role of the surface's `packages` and `files` families (`api` when it holds neither), the second the `api` pods, each the main Deployment or the pool; `service.type`, `annotations`, `loadBalancerSourceRanges` (Q-EP-71, `trove.surfaceRoles`) |
| `pool.enabled` | Deployment, PodDisruptionBudget, HorizontalPodAutoscaler `<fullname>-packages-ep-<name>` and, with `api` families, `<fullname>-api-ep-<name>` | `serve --role packages` / `--role api`, `TROVE_POOL_NAME=ep-<name>`, label `trove/pool: ep-<name>`, the role's values with the keys of `pool` on top (merged key by key, so `false` and `0` do override); only these Deployments open the port, the main pods do not (Q-EP-70). In the bridge: `<fullname>-web-ep-<name>` with `--role web` |
| `ingress.enabled` | Ingress `<fullname>-ep-<name>` | `className`, per host (the host of `baseUrl`, `hosts`, then `wildcardHost`) `/` to the one Service when the surface lies within one role, else the rows of R-HA-037's table whose families it holds (`/api/v1` and `/` to the `-api` Service), all `Prefix`, backend by port number, `ingress.tls` as given; backend-over-TLS annotations from `ingress.backendTLS`, any key of the endpoint's own `ingress.backendTLS` winning; the HAProxy verified name is the endpoint Service's `<name>.<ns>.svc`, which the endpoint's certificate must cover |
| `networkPolicy.enabled` (global) | a rule in the policy of every Deployment that opens the port | its port from `networkPolicy.from` (empty = anywhere); an endpoint pool's policy holds that rule and 8081, no 8080 (Q-HA-76) |
| `tls.existingSecret`, `extraSecrets`, `clientCaSecret` | mounts and `tls.certificates` | position 0 is `existingSecret`; without it and with `extraSecrets`, position 0 is the listener pair of `tls.secretName`; without any, no block is rendered and the endpoint serves `server.tls` |

The default endpoint's `server.endpoints.default.hosts` is rendered from `config.server.base_url` and
`config.server.additional_base_urls` as soon as there is a further endpoint (unless `endpoints.default.hosts` is set), so a
request that reaches `:8080` for another endpoint's host answers `421` (H-59). A client that reaches the default endpoint by
another name (the in-cluster Service name) needs that name in `endpoints.default.hosts`. NOTES.txt (the template `trove.notes`)
lists each endpoint with its Services, hosts and pods, and warns about an endpoint with `exposure: rules` on the main
Deployments or with an empty `networkPolicy.from`.

Not rendered: `gateway.*` (HTTPRoute, BackendTLSPolicy per endpoint; the default endpoint has none either).

### S3 from a provisioner's Secret

An object-store provisioner writes one Secret with the credentials and the location. `secrets.s3.existingSecret` names it;
`accessKeyIdKey` and `secretAccessKeyKey` name the credential keys (mounted as files with `credentials.source: static`);
`bucketKey`, `regionKey` and `endpointKey` name the keys that become `TROVE_STORAGE_S3_BUCKET`, `..._REGION` and
`..._ENDPOINT` by `secretKeyRef`. The environment wins over the file, so `config.storage.s3.bucket` may stay empty; the
chart's bucket check accepts `bucketKey` instead. [ci/s3-provisioner.yaml](../../deploy/helm/trove/ci/s3-provisioner.yaml)
shows the key names of one provisioner (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `S3_BUCKET`, `S3_REGION`,
`S3_ENDPOINT`). Its endpoint is a host without a scheme, which the binary treats as https. The migration Job does not
read these variables (it uses no object store).

## Network policy

`networkPolicy.enabled` (default `true`, R-HA-042) renders one policy per Deployment, named as the Deployment and selecting its pods by
the selector labels, plus the migration Job's. They restrict **ingress only**, and by the owner's rule (ADR-154) the chart renders
no egress rule and no policy for pods it does not deploy: the pods' egress is the cluster's, and `values.schema.json` has no
`networkPolicy.egress` key ([H-516](../security/egress.md#h-516--the-chart-restricts-no-egress-of-its-pods)). Every rule names
exactly one port.

| Policy | Admits |
|---|---|
| `<fullname>-api`, `<fullname>-packages`, each path pool, the bridge's `<fullname>-web` | TCP 8080 from `networkPolicy.ingress.http.from` (empty = from anywhere, so any ingress controller works wherever it runs); the port of every further endpoint the pods open, from that endpoint's `networkPolicy.from` (empty = from anywhere); TCP 8081 only from `networkPolicy.ingress.ops.from` (default: namespace `monitoring`) and `networkPolicy.probeCIDRs` |
| each endpoint pool (`<fullname>-packages-ep-<name>`, `<fullname>-api-ep-<name>`, `<fullname>-web-ep-<name>`) | the endpoint's port as above and 8081; no 8080 |
| `<fullname>-worker`, each worker pool | TCP 8081 as above; a worker listens on no client port |
| `<fullname>-migrate` | nothing; `ingress: []` |

No rule admits one trove pod from another: replicas never talk to each other and the cut adds no seam between roles. A caller in
another namespace that addresses a role Service directly (a Bastion) reaches 8080 only when `networkPolicy.ingress.http.from` admits it.

With neither an `ops.from` peer nor a `probeCIDRs` entry, the 8081 rule is left out rather than rendered with an
empty `from`, which Kubernetes reads as "from anywhere". Most CNIs exempt kubelet probes from policies; where one does
not, put the node CIDRs in `probeCIDRs`. Verified by render tests only (`TestNetworkPolicy`, `TestRolesNetworkPolicy`); that a CNI enforces
the policy and still passes the probes is not tested here. The policy for the Valkey and Sentinel pods
belongs to whoever runs Valkey (R-HA-042).

`config.server.trusted_proxies` stays empty by default. The values example names the addresses of the ingress
controller pods, never the pod CIDR: every pod in the cluster could otherwise set `X-Forwarded-For` and be taken
for any client. `config.server.forwarded_header` names the one header the edge rewrites (see
[architecture.md](architecture.md#client-address-and-base-url)).

## Validation

Two layers. `values.schema.json` checks types, enums and unknown keys on install, upgrade, lint
and template. `_validate.tpl` checks the rest and fails with a message that names the value. The
tests in `chart_test.go` assert both, row by row.

<details>
<summary>R-HA-029 rows: implemented and not yet</summary>

| Row | State |
|---|---|
| `config.server.base_url`, `config.database.host`, `secrets.database.existingSecret`, `config.storage.s3.bucket` empty (not when `secrets.s3.bucketKey` names the bucket in the S3 Secret) | implemented |
| `secrets.s3.bucketKey`, `regionKey`, `endpointKey` without `secrets.s3.existingSecret` | implemented |
| endpoints: a `port` that is missing, 8080, 8081 or another endpoint's; an invalid name or a Service name over 63 characters; a missing or non-origin `baseUrl`; a host in two endpoints, a wildcard host that covers another endpoint's host or is shared; an invalid wildcard host; `ui` in a further endpoint's `surface`; `extraSecrets`, `clientCaSecret`, `existingSecret` without `tls.enabled`; a pool below two replicas or without a budget under `enforce`; `config.server.endpoints` set (managed) | implemented, each naming `endpoints.<name>.<key>` |
| `credentials.source: static` without `secrets.s3.existingSecret` | implemented |
| `secrets.encryption.existingSecret` or `.keys` empty (prints the creation command of R-HA-027) | implemented |
| `highAvailability.enforce` with fewer than 2 replicas (`replicaCount`, or the autoscaler's `minReplicas`), a disabled PDB, a host spread that is not `DoNotSchedule` or lacks `nodeTaintsPolicy: Honor` | implemented, per Deployment (role, pool, endpoint pool, bridge) and for custom constraints |
| `api`, `packages` or `worker` below 1 replica | implemented, naming `<role>.replicaCount` (`.autoscaling.minReplicas`) |
| an autoscaler without a metric, or whose `maxReplicas` is below `minReplicas` | implemented |
| pools: a repeated name or path prefix or queue, a name starting `ep-` or too long, a path outside the families of the role or equal to the role's own prefixes, a queue that does not exist | implemented, naming `<role>.pools[<name>]` and the value |
| while `<fullname>-web` exists and the edge is still on the bridge (the Ingress `<fullname>` routes to the Service `<fullname>`, or a `<fullname>-ep-<name>` selects the `web` pods): the edge of the target shape (`web.enabled: false` or `web.routed: false`) while `<fullname>-api`, `-packages` or a pool is missing or not ready, and `web.enabled: false` at all; with no edge to read, the target shape while `<fullname>-api` or `-packages` does not exist | implemented with `lookup` (`trove.bridgeFacts`) and a decision on those facts (`trove.bridgeGuard`, tested with given facts in `TestRolesBridge`, Q-HA-132); inert under `helm template` |
| `config.storage.upload.max_size` larger than the spool budget of a `packages` (or `web`) Deployment; the UI's upload limit (`config.ui.deploy.max_upload_bytes`, `config.storage.upload.max_size` when 0) larger than that of an `api` Deployment | implemented, naming both values |
| `terminationGracePeriodSeconds` below `config.health.shutdown_drain_delay` + 15 s | implemented, per Deployment |
| a serving Deployment (`api`, `packages`, `web`, a path pool or an endpoint pool) without `resources.limits.memory`, or with a limit that admits fewer than 64 requests (Q-HA-70); a limit that is not an integer with an optional binary or decimal unit | implemented, naming the limit and the values the request bytes come from |
| Sentinel mode without addresses or `primary_name`; fewer than 3 addresses under `enforce`; standalone under `enforce` | implemented |
| `secrets.valkey.existingSecret` empty | implemented |
| `secrets.oidc[]`: an invalid or repeated `providerId`, an empty `existingSecret`, a `providerId` without a provider of that ID in `config.auth.oidc.providers`, `client_secret_file` set in `config` for it; `config.auth.break_glass.password_file` set | implemented, each naming the value |
| `config.valkey.tls.mode: disable` without `allow_insecure` | implemented |
| `podDisruptionBudget` with both `minAvailable` and `maxUnavailable` | implemented |
| a chart-managed key set inside `config` | implemented (secret file paths, `storage.spool.dir`) |
| `config.metrics.auth: static` without `secrets.metricsToken` | implemented |
| `spool.headroom` not smaller than the spool; sizes that are not quantities | implemented |
| the spool budget of a worker Deployment that claims the queue `vuln` (the main worker unless a pool lists it, or that pool) below `config.vulnerabilities.sources.osv.max_archive_size` while `config.vulnerabilities.enabled` (default true) and the OSV mode (default `online`) is not `off` (R-VULN-025) | implemented, naming the archive limit and `worker.spool.sizeLimit` |
| a name in `metrics.prometheusRule.disabledAlerts` that is no alert of the chart | implemented, listing the names |
| `tls.enabled` without `tls.secretName` (names the key and the opt-out); `ingress.enabled` with TLS and verification but no `caSecret` (haproxy, ingress-nginx) or `serversTransport` (traefik); `tls.clientCA` with haproxy and no `clientCertSecret`; the same three rules for the merged `ingress.backendTLS` of each further endpoint with `ingress.enabled` (`endpoints.<name>.ingress.backendTLS.caSecret`, `.serversTransport`, `.clientCertSecret` with the endpoint's `tls.clientCaSecret`); `tls.clientCA`, `tls.ops`, `tls.backendTLSPolicy` without `tls.enabled`; `tls.backendTLSPolicy.enabled` without `caConfigMapName` | implemented |
| `backup.enabled` (or `restore.enabled`) without `config.backup.target.bucket`; a CronJob name over 52 characters | implemented, naming the value (and `fullnameOverride`) |
| `backup.schedule` outside the dialect of trove's cron parser (six or seven fields, `?`, `L`, `W`, `#`, day of week `7`, `@every`, `@reboot`, `TZ=`, `CRON_TZ=`, an unknown macro) | implemented, the reason named; a value out of range and an expression that never fires are Kubernetes's at apply time |
| the target on the blob store's bucket and endpoint (the endpoints compared as the binary does, no scheme meaning https) without `allow_same_bucket`, or with one prefix containing the other | implemented, naming both locations |
| `config.backup.target.credentials.source: static` without `secrets.backupTarget.existingSecret` | implemented, naming both |
| `restore.enabled` without `restore.backupId` (its form is the command's to check); with a replica count above 0 or an autoscaler on any role, pool, endpoint pool or the bridge | implemented, naming `restore.enabled` and the value; the replica minimums do not apply meanwhile (Q-HA-148) |
| a chart-managed key of the backup set inside `config` (`backup.manual_run`, `backup.target.ca_file`, the three credential files) | implemented |
| pooler (`session_host`, control endpoint), the Gateway the chart creates, `contentOrigin` | with those features |
| `gateway.enabled` without the Gateway API (`.Capabilities` lacks `gateway.networking.k8s.io/v1/HTTPRoute`; with `helm template` pass `--api-versions gateway.networking.k8s.io/v1/HTTPRoute`), without `gateway.parentRefs`, or with `gateway.timeouts.backendRequest` above `gateway.timeouts.request` (a request timeout of `0s` is no timeout and exceeds nothing); `metrics.serviceMonitor.namespace` other than the release namespace while `tls.enabled` (the CA Secret is read from the ServiceMonitor's namespace) | implemented; a timeout that is no Gateway API duration is the schema's |
| a `ReadWriteMany` spool, `--set ingress.path=…`, any unknown key | schema |

</details>

## The HTTPRoute

`gateway.enabled` renders one HTTPRoute `<fullname>` ([httproute.yaml](../../deploy/helm/trove/templates/httproute.yaml), R-HA-038) for the
default endpoint: `gateway.parentRefs` (required), `hostnames` (default the host of `config.server.base_url`), and one rule per row of
`trove.edgeRules`, the table the Ingress reads, each a `PathPrefix` match to the Service of its role on `service.port`, with
`gateway.timeouts` and `gateway.filters` on every rule and `gateway.extraRules` appended. In the bridge the one rule is `/` to `<fullname>`.
The Ingress and the HTTPRoute may run together. The BackendTLSPolicies are `tls.backendTLSPolicy`'s and do not depend on `gateway.enabled`.
The check for the Gateway API is a capability, so every offline render of a set with `gateway.enabled` passes
`--api-versions gateway.networking.k8s.io/v1/HTTPRoute`: `templateArgs` in the chart tests and [lint.sh](../../test/chart/lint.sh) do, and
`helm lint`, which takes no API versions, runs with `--set gateway.enabled=false` on a set that enables it ([ci/gateway.yaml](../../deploy/helm/trove/ci/gateway.yaml)).
Not built: the HTTPRoute of further endpoints (R-EP-008) and the Gateway the chart creates (R-HA-053); the criteria with Envoy Gateway in
kind (200 MB `npm publish` through the route) are not part of the render tests.
Test: `TestR_HA_038_GatewayAPIExposure`.

## The ServiceMonitor

`metrics.serviceMonitor.enabled` renders one ServiceMonitor `<fullname>` ([servicemonitor.yaml](../../deploy/helm/trove/templates/servicemonitor.yaml)):
the port `ops` of `<fullname>-ops`, path `/metrics`, `role` and `pool` relabelings first (from the pod labels `app.kubernetes.io/component`
and `trove/pool`), then `metrics.serviceMonitor.relabelings`; with `tls.enabled` `scheme: https` and a `tlsConfig` that verifies
`<fullname>-ops.<namespace>.svc` (the certificate of the operator must carry it) against `ca.crt` of `tls.ops.secretName` or `tls.secretName`,
read from the ServiceMonitor's namespace; with `config.metrics.auth: static` the bearer token of `secrets.metricsToken`. Test:
`TestServiceMonitor`, which deliberately does not carry the ID R-HA-045: the requirement is not complete (`TroveBackupCheckFailed`
waits for its gauge, see [The PrometheusRule](#the-prometheusrule)), and a test named for it would count it as implemented.

## The backup and restore Jobs

Requirements: R-HA-060 and R-BAK-012; the commands are `trove backup create` and `trove backup restore` ([backup.md](backup.md)). They are
commands of the binary and not roles (Q-ARCH-342): the pods serve nothing, open no port, have no probe, carry no `trove/serve` label (no
Service and no Valkey policy selects them) and mount no volume but `/tmp`.

| Object | Rendered | Notes |
|---|---|---|
| CronJob `<fullname>-backup` ([backup.yaml](../../deploy/helm/trove/templates/backup.yaml)) | with `backup.enabled` (default `true`) | `trove backup create --trigger schedule`, plus `--adopt-target` with `backup.adoptTarget`. `concurrencyPolicy: Forbid` is fixed; `suspend` is `backup.suspend` or `restore.enabled` |
| Job `<fullname>-restore` ([restore.yaml](../../deploy/helm/trove/templates/restore.yaml)) | with `restore.enabled` | `trove backup restore <restore.backupId>`, `--skip-revocation-journal` with `restore.skipRevocationJournal`, then `restore.extraArgs`. An ordinary Job, not a hook. After a failure for good the operator deletes it and runs the same `helm upgrade` again, which creates it anew (R-HA-060) |
| ServiceAccounts `<fullname>-backup`, `<fullname>-restore` | with their Job; `backup.serviceAccount.*`, `restore.serviceAccount.*` | no token; the annotations carry a workload identity, which is how the target's and the blob bucket's rights differ per Job (R-BAK-015) |
| NetworkPolicies `<fullname>-backup`, `<fullname>-restore` | with their Job and `networkPolicy.enabled` | `policyTypes: [Ingress]` and `ingress: []`; the Jobs need no rule, and the target is reached outbound, which the chart never restricts (ADR-154). The counters a backup pod exports on its operations port for clusters that scrape Jobs are therefore not reachable unless the operator adds a policy |
| the pod and the failure policy ([_backup.tpl](../../deploy/helm/trove/templates/_backup.tpl)) | `trove.jobPod`, `trove.jobPodFailurePolicy` | one template for both Jobs. The policy ignores `DisruptionTarget` first, so a drain or a preemption never consumes `backoffLimit`, then fails the Job on exit 2, 3, 4 and 6 (R-BAK-013); 75 and 1 are retried. `safe-to-evict: "false"` is always set |

Secrets are scoped by the argument `scope` of `trove.secretMounts` ([_secrets.tpl](../../deploy/helm/trove/templates/_secrets.tpl)):
`backup` (the login `secrets.databaseBackup` or else `secrets.database`, the CA of the database, the blob store's read credential and CA, the
target) and `restore` (the migration login or else `secrets.database`, the blob store, the target, and the key ring that the restore checks
before it loads anything). Valkey is in neither. The files of the target are chart-managed config keys (`trove.config`), so the shared
`<fullname>-config` names them and only the two scopes have anything at those paths. The Jobs take no global `extraEnv`, `extraVolumes` or
`extraEnvFrom` (Q-HA-149): what a Job mounts is what its values and its scope say.

**TLS.** The Jobs open no listener, so they mount none of the listener material. Every link they open is TLS-capable like the replicas':
the database with `verify-full` and `secrets.databaseCA` and `databaseClientCert`, the blob store with `secrets.s3CA`, the target with
`secrets.backupTargetCA` (Q-HA-146) or the system trust store. All of them are directory mounts without `subPath`, so a rotated Secret
reaches the files; whether a running process re-reads a CA is the binary's, and a Job lives hours at most.

**Restore.** While `restore.enabled` is set the chart renders no migration Job (and its ConfigMap, ServiceAccount; the policy stays, without
the hook annotations), suspends the CronJob and refuses a replica or an autoscaler on any role, pool, endpoint pool or the bridge, naming
`restore.enabled` and the value. The replica minimums (at least one, at least two under `highAvailability.enforce`) do not apply meanwhile,
because nothing could satisfy both (Q-HA-148). The operator's sequence is in R-HA-060; the cluster side of it (a drain during the copy, the
restored installation) is the end-to-end suite's.

**Names.** The CronJob controller names a Job `<CronJob>-<10 digits>`, so the CronJob name is limited to 52 characters and the chart fails
when `<fullname>-backup` exceeds it. `kubectl create job --from=cronjob/<fullname>-backup <name>` copies the arguments, so a manual run
records `trigger: schedule`; `config.backup.manual_run.*` (chart-managed, rendered with the CronJob) names it for the UI.

**Dev profile.** [values-dev.yaml](../../deploy/dev/values-dev.yaml) sets the target to the bucket `trove-backups` of the profile's SeaweedFS
(a bucket of its own beside `trove-blobs`), static keys from the secret of the blob store, the dev CA, the schedule `0 */6 * * *` and
`config.backup.expected_interval: 6h`.

## The values reference

The values block of [R-HA-031](../spec/ha-and-helm.md) is the reference; `TestR_HA_031_ValuesReference` ([values_reference_test.go](../../test/chart/values_reference_test.go))
holds it to [values.yaml](../../deploy/helm/trove/values.yaml): every key of values.yaml is in the block with the same default, every key of the
block is in values.yaml unless the feature that owns it is not rendered yet (`unbuiltPrefixes` in the test: the post-restore Job, the chart-created
Gateway, `contentOrigin`, `bootstrap`, `trustedCA`, Service tuning, the extension points, and the keys under `config`), and
[values.schema.json](../../deploy/helm/trove/values.schema.json) declares every key. A value added to the chart is added to the block in the
same change; a feature that arrives takes its prefix out of `unbuiltPrefixes`.

## The PrometheusRule

`metrics.prometheusRule.enabled` renders one PrometheusRule `<fullname>`, group `<fullname>`: every alert of
[_rules.tpl](../../deploy/helm/trove/templates/_rules.tpl) except `disabledAlerts`, then `extraRules`, with `labels` on the object. The
rule holds 30 alerts, in this order: `TroveReplicasNotReady`, `TroveDatabaseUnreachable`, `TroveObjectStoreUnreachable`,
`TroveObjectStoreUnreachableSingleReplica`, `TroveDatabasePoolSaturated`, `TroveControlConnectionWaits`, `TroveJobsStuck`, `TroveLeaseLost`,
`TroveMixedVersions`, `TroveSpoolNearlyFull` (rows of R-HA-045's table), `TroveVulnerabilityDataStale`, `TroveVulnerabilitySyncFailing`,
`TroveVulnerabilityClaimedNameMalicious` (R-VULN-024), `TroveMirrorFailing`, `TroveMirrorStale` (R-OPS-046, Q-OPS-160, Q-OPS-167),
`TroveBridgeLeftOn` (a `web` replica registered for 24 hours, Q-HA-92), `TroveBlobMissing` (a read found the object of a blob missing within the
hour, R-HA-024, Q-STOR-152), `TroveGCStale`, `TroveRevocationJournalBacklog` (R-HA-054), `TroveValkeyUnavailable`, `TroveValkeyUnavailableSingleReplica`,
`TroveValkeySentinelsDegraded`, `TroveValkeyFailoverFlapping`, `TroveValkeyPrimaryDisagreement` (rows of the table), `TroveBackupStale`,
`TroveBackupFailed`, `TroveBackupRunningLong`, `TroveBackupGlobalHoldLong`, `TroveBackupSourceDamage` and `TroveBackupVerifyFindings` (R-BAK-014). Severities are in the spec's table (`critical`:
`TroveReplicasNotReady`, `TroveDatabaseUnreachable`, `TroveObjectStoreUnreachable`, `TroveValkeyUnavailable`, `TroveValkeyPrimaryDisagreement`,
`TroveVulnerabilityClaimedNameMalicious`, `TroveBlobMissing`, `TroveBackupStale`, `TroveBackupFailed`, `TroveBackupSourceDamage`,
`TroveBackupVerifyFindings`; the others `warning`).

**Not rendered.** `TroveBackupCheckFailed` reads `trove_backup_check_ok{check}`, which the platform check task `backup-check` (R-HA-023,
R-OPS-019) would export; no code registers that gauge, and a metric nobody writes would make an alert that never fires look like one that
works. When the task exists, the alert, a test pair, a row of `alertSeverities` in
[prometheusrule_test.go](../../test/chart/prometheusrule_test.go) and the ID R-HA-045 on `TestPrometheusRule` arrive together;
`TestPrometheusRule` fails if the alert is rendered earlier.

**The alerts of the backup runs** read the gauges that every `api` and `worker` replica computes from `backup_run` in PostgreSQL
(R-BAK-014), so a finished backup pod need not be scraped. The two timestamps have no series until a run of that outcome exists
(Q-OPS-180), and the expressions handle the absence: `TroveBackupFailed` is `max(last_failure) > (max(last_success) or vector(0))`, silent
without a failure and firing for a first backup that failed; `TroveBackupStale` is `time() - max(last_success) > 1.5 * max(expected_interval)`
or, when no success series exists, `max(absent(last_success))` while the gauge of the expected interval has existed for a whole
`config.backup.expected_interval` (`offset`, Q-OPS-182), so a young installation is silent and one that never succeeded fires one interval
after the replicas first reported. The window is a literal in the rule, rendered from `config.backup.expected_interval` (default `24h`, parsed
by `trove.seconds`), the same value the replicas export; the chart has no threshold of its own. An `interrupted` run leaves no failure
series, so a drain followed by a successful retry never fires `TroveBackupFailed`.

**Names.** The expressions are the specification's, and the binary exports every metric they read under that name (Q-HA-135): the
spool gauges are `trove_spool_used_bytes` and `trove_spool_free_bytes`, the pool metrics are in
[internal/meta/postgres/metrics.go](../../internal/meta/postgres/metrics.go) and
[internal/app/wire_gauges.go](../../internal/app/wire_gauges.go), the queue ages in the `JobQueueGauges` query. `TestPrometheusRule` fails
for an expression that reads a `trove_*` name no code under `internal/` registers.

- `TroveReplicasNotReady` reads kube-state-metrics: `min by (deployment)` of `kube_deployment_status_replicas_available` for the Deployments
  `<fullname>-(api|packages|worker|web)(-.*)?`, the main Deployments, their pools (`<fullname>-<role>-<pool>`) and the bridge; a Deployment
  that is scaled to one replica by choice fires too.
- Every selector carries `namespace="<release namespace>"`, so two installations in one Prometheus do not blend (Q-VULN-55). A scrape
  through the Prometheus operator always sets that label.
- The stale threshold is `config.vulnerabilities.stale_after` (default `24h`, parsed by `trove.seconds`), the key the binary reads; a
  `stale_after` that arrives only through `extraEnv` is not seen.
- A rule file renders no object when every alert is disabled and `extraRules` is empty.
- To add an alert: append it to `trove.rules.all` and its name to `trove.rules.names`, add a test that fires and one that stays silent to
  [tests/rules_test.yaml](../../deploy/helm/trove/tests/rules_test.yaml) (`TestR_VULN_024_PromtoolRules` fails otherwise; the expected
  annotations are the rule's text, so a changed text fails the test until the test file follows), a row to `alertSeverities` in
  [prometheusrule_test.go](../../test/chart/prometheusrule_test.go) and a row to R-HA-045.

[tests/rules_test.yaml](../../deploy/helm/trove/tests/rules_test.yaml) is rendered from `ci/monitoring.yaml` as release `trove` in the
namespace `default` and run with the pinned promtool (`make chart-test` fetches it, see [The Bastion chart](#the-bastion-chart)).

## Value sets and tests

`ci/*.yaml` are the value sets every requirement is asserted against. Each names `tls.secretName` or sets `tls.enabled: false` with a comment (`single-node.yaml`). The dev profile's
[values-dev.yaml](../../deploy/dev/values-dev.yaml) is rendered alongside them (E0.7). Adding a
file to `ci/` adds it to every per-set assertion with no code change.

| Command | What it does |
|---|---|
| `make chart-lint` → [test/chart/lint.sh](../../test/chart/lint.sh) | `helm lint --strict` and `helm template` for every `ci/` set, `values-dev.yaml`, and `values-dev.yaml` with `values-pooler.yaml` |
| `make chart-test` → [test/chart/test.sh](../../test/chart/test.sh) | the Go package [test/chart](../../test/chart/chart_test.go) with build tag `chart`; writes `.out/test-chart.json` for the coverage generator |
| `go test -tags chart ./test/chart/...` | the same, without the wrapper |
| [test/chart/image_test.go](../../test/chart/image_test.go) | the default image is `docker.io/guidedtraffic/trove` at the appVersion, and `values.yaml` has one `  tag:` line, which the release pipeline rewrites |

Both need only `helm` at the version in `tools/versions.env` (the test binary and `lint.sh` fail
with the install command when it differs) and no cluster. The tests run `helm template` and parse
the output; they are named after the requirement they verify (`R-HA-002 …`, `R-HA-061 …`), which
is where the coverage generator reads the IDs from.

| Test (subtest prefix) | Asserts |
|---|---|
| `R-HA-028` | lint passes for every set; no Secret, RBAC, CRD or subchart; names follow the fullname rule; standard labels |
| `R-HA-002` | every Deployment runs the `--role` its name says (`api`, `packages`, `worker`; `web` only with the bridge), never `all`; `api`/`packages`/`worker` replicas below 1 fail naming the key; each role Service selects the main pods of its role; the pool label is in the selectors |
| `R-HA-002`, `R-HA-028`, `R-HA-062` (`TestRolesDeployments`) | Deployment, PodDisruptionBudget and NetworkPolicy per role, pool and endpoint pool; component and pool labels; every selector selects the pods of its Deployment and no other; the queue variables (JSON lists) on the right Deployments; pool inheritance; the autoscaler leaves out `spec.replicas`; the connection counts and the budget in the notes |
| `R-HA-002`, `R-HA-028` (`TestRolesServices`) | each Service selects the pods of exactly its Deployment, sessionAffinity None, ports; the container ports of every Deployment |
| `R-HA-037`, `R-HA-062`, `R-EP-008` (`TestRolesEdge`) | the six fixed `Prefix` rules per host, the pool paths before them, a further endpoint whole or by the rows of its families, a mixed surface opens its port on both main Deployments |
| `R-HA-042` (`TestRolesNetworkPolicy`) | in every value set: ingress only, no `egress`, every rule one port, no empty `from`; per Deployment the ports it admits and their peers; the default policies |
| `R-HA-029`, `R-HA-062` (`TestRolesValidation`) | every pool, autoscaler and spool row fails and names the value |
| `R-HA-064` (`TestRolesBridge`) | `web.enabled` renders `trove-web` (`--role web`), the Service `trove`, the edge `/`, the web endpoint pool, policies; nothing of it without; step 2 (`web.routed: false`) keeps every Deployment and Service of step 1 and step 3 moves no route; a Service step 2 routes to exists in step 1; the BackendTLSPolicies of the bridge; the notes name the step; the guard's decision for given facts of the cluster (`bridgeGuardOf`: the edge on the bridge or moved, a target not ready or missing, step 3 before step 2, no readable edge); the rendering does not depend on the guard |
| `R-HA-061` | no `ReadWriteMany`, `ReadOnlyMany`, PVC, StatefulSet, `volumeClaimTemplates`, hostPath; a `ReadWriteMany` spool fails schema validation; only the spool and `/tmp` are writable |
| `R-HA-029` | one subtest per implemented row fails and names the value; the schema refuses unknown keys and wrong types |
| `R-HA-029` and `R-HA-027` | without the encryption Secret the error prints the `kubectl create secret` command |
| `R-HA-027` | all pods mount the same key Secret; ordered ring, first is active; a key-list change rolls the pods |
| `R-HA-030` | file mount, `checksum/config`; Secrets read-only with items, mode 0440, no `subPath`, never in the environment; per-pod scoping; the config paths match the mounts; the environment set; service links off |
| `R-HA-033` | every pod and container of every set satisfies the restricted Pod Security Standard; UID 65532 |
| `R-HA-034`, `R-ARCH-024` | probes as the table of R-HA-034; ports; no `preStop` |
| `R-HA-011` (`TestChart`) | the chart half: grace period per role, its environment variable, above the drain delay |
| `R-HA-011` (`TestShutdownBudgetDerivation`) | builds the binary and runs `trove admin config validate` over the rendered file and environment of `packages` and `worker`: `server.shutdown_timeout` equals grace - drain - 5 s and its source is `derived` |
| `R-HA-042` (`TestNetworkPolicy`) | the policies of the roles: ports, peers, selectors that match the pods, no `from` meaning "anywhere" on 8081, `enabled: false` renders none |
| `R-HA-030` (`TestMigrationJobSecrets`) | the migration Job mounts no key ring; `api`, `packages` and `worker` do |
| `R-ARCH-006` (`TestTrustedProxiesExample`) | the `trusted_proxies` example is no pod-wide range and the default list is empty |
| `R-HA-035`, `R-HA-036` | spool `sizeLimit` and budget; `GOMAXPROCS` rule; default resources |
| `R-VULN-025` (`TestR_VULN_025_SpoolHoldsOneArchive`) | the worker spool must hold one archive: the defaults pass, a larger `max_archive_size` fails naming both values (also in mode `repository`), the headroom counts, mode `off` and a disabled scanning are not checked, the worker's `TROVE_STORAGE_SPOOL_MAX_BYTES` follows its own `sizeLimit` |
| `R-VULN-025` (`TestR_VULN_025_ConfigurationPassesThrough`) | the `vulnerabilities` section reaches `trove.yaml` as set; `ci/monitoring.yaml` passes `trove admin config validate` with the other sets |
| `R-VULN-024` (`TestR_VULN_024_PrometheusRuleValues`) | no rule by default; the alerts (the three of the vulnerability scanning, the two of the mirror, `TroveBridgeLeftOn`, `TroveBlobMissing`) and their severities; the `labels`; the threshold follows `stale_after`; every selector is scoped to the namespace; `disabledAlerts` and `extraRules`; a misspelt alert name fails; all alerts disabled renders nothing |
| (no ID: `TestPrometheusRule`) | R-HA-045 is not complete (`TroveBackupCheckFailed` waits for the gauge of `backup-check`): the rule is rendered with `enabled` and carries the `labels`, none by default or with `enabled: false`; the 30 alerts and their severities, `for` durations, summaries and descriptions; the role regex follows `<fullname>`; `TroveBackupCheckFailed` and `trove_backup_check_ok` are not rendered; every `trove_*` name an expression reads is registered by code under `internal/`; `disabledAlerts` |
| `R-VULN-024` (`TestR_VULN_024_PromtoolRules`) | `promtool check rules` and `promtool test rules` over the rendered rules, and one firing and one silent test per alert; the backup alerts' tests are `TroveBackupStale` at 90 minutes after the last success (and for a backup that never ran), `TroveBackupFailed` for a failure after the last success and for a first failure (silent after an interrupted run), `TroveRevocationJournalBacklog` and the others |
| `R-HA-060`, `R-BAK-012` (`TestR_HA_060_BackupCronJob`) | the CronJob's fields, `Forbid`, the pod failure policy (an eviction ignored first, then 2, 3, 4, 6), `restartPolicy: Never`, no `activeDeadlineSeconds` by default, the grace period, the autoscaler annotation, `TMPDIR` and the one `emptyDir`, the memory limit, no port or probe, its own ServiceAccount, the values |
| `R-HA-060` (`TestR_HA_060_RestoreJob`) | the Job (not a hook), the arguments, the failure handling, the account, no migration Job, the suspended CronJob, the roles at zero, pools and the bridge |
| `R-HA-029` (`TestR_HA_029_BackupValidation`) | every backup and restore row of the table fails and names the value; the schedules are checked against `jobs.ParseSchedule` |
| `R-HA-031` (`TestR_HA_031_BackupValuesSchema`, `TestR_HA_031_ValuesReference`) | the schema refuses typos and a grace period below 30 s; the values of the spec block render; values.yaml and the spec block agree |
| `R-HA-030`, `R-BAK-011` (`TestR_HA_030_BackupSecrets`) | which Secret reaches which pod: the target's keys and CA only into the backup pods and the restore Job, no replica and not the migration Job, no Valkey Secret in a Job, the key ring only in the restore Job, the login of each Job; no Secret as a variable; the managed config paths |
| `R-HA-059` (`TestR_HA_059_NoDisruptionBudgetForTheJobs`) | no PodDisruptionBudget selects a Job's pods |
| `R-HA-042` (`TestR_HA_042_JobsAdmitNothing`) | the Jobs' policies: ingress only, an explicit empty list, selecting exactly their pods; no egress in any set |
| `R-HA-029` (memory) | a serving pod (`api`, `packages`, a pool) without a memory limit, or one whose memory slots (`trove.requestBytes` and the three cache budgets, the formula of `internal/app/memory.go`) are fewer than 64, fails; `2G`, `64Mi` and a plain integer render |
| `R-HA-032` | PDB, spread, tolerations, default replica counts and rolling update |
| `R-HA-037`, `R-ARCH-032` | `sessionAffinity: None` everywhere, no affinity annotations, every Ingress rule `Prefix` and never the operations port, `ingress.path` fails naming `path` |
| `R-HA-019` | hook annotations and weights, `podFailurePolicy`, same `trove.yaml`, nothing from Valkey or S3; disabled renders no Job |
| `R-HA-056` | Valkey wiring reaches `api`, `packages` and `worker` only |
| `R-EP-008` (`TestEndpoints`) | two endpoints render two Services (own port, selecting the main packages pods), two Ingresses (class, hosts incl. the wildcard, TLS entry, backend), the port rules in the packages policy, the `server.endpoints` block and the default endpoint's hosts; mounts of the pairs without `subPath`; backend TLS per controller; the pool (Deployment of the role, PDB, Service, spread selector, policy with the operations port, no port on the main pods); NOTES; each validation row |
| `R-HA-030`, `R-HA-029` (`TestS3ProvisionerSecret`) | bucket, region, endpoint by `secretKeyRef`, credentials as files with only their two keys, the Job reads none, the bucket is required unless the Secret carries it |
| `R-HA-030` (`TestTLSMountsFollowTheListeners`) | the listener pair is mounted by the pods that load it: everywhere without `tls.ops`; with it, not into the worker, into an endpoint pool only while an endpoint it serves has no pair of its own; the client CA reaches the main `packages` pods and not an endpoint pool |
| `R-HA-030`, `R-AUTH-014`, `R-AUTH-042` (`TestLoginSecrets`) | `secrets.breakGlass` and `secrets.oidc[]` mount a directory of the Secret read-only without `subPath` into `api` pods only (main, pools, endpoint pools, the bridge's `web`; not `packages`, `worker` or the Job), render the config paths, take a different key, leave `enabled` alone with a Secret and render `auth.break_glass.enabled: false` without; the validation rows; the rendered file passes `trove admin config validate` |
| `R-HA-030` (`TestS3CA`) | `secrets.s3CA` mounts read-only without `subPath` with the one projected key, renders `storage.s3.ca_file`, stays out of the Job; without it nothing is rendered and `config.storage.s3.ca_file` is refused |
| `R-HA-063`, `R-HA-030` (`TestTLS`) | HTTPS probes, mount without `subPath` of exactly the certificate and key, config paths, `appProtocol`; missing `tls.secretName` fails; opt-out renders no TLS wiring; no TLS mount in the migration Job; client CA and `ops_tls` separation; per-controller Ingress settings (HAProxy Ingress default, verification and client-certificate rules); one `BackendTLSPolicy` per role Service; every value set agrees between config and probe scheme |

Not covered by these tests (needs a cluster): that the pods start in a restricted namespace, that
`kubectl exec … touch /x` fails, that the Job finishes before the Deployments roll. The dev
profile covers them once the trove image serves ([local-profile.md](local-profile.md)).

## Extension checklists

**Add a value.**

1. Add it to [values.yaml](../../deploy/helm/trove/values.yaml) with its default; comment it only where its name does not
   say its meaning, unit, range or consequence, and give an `example:` where the default is empty.
2. Add it to [values.schema.json](../../deploy/helm/trove/values.schema.json) with its type and
   enum. The schema is `additionalProperties: false`; a value missing there fails every render.
3. Use it in the template, and add a validation row to `_validate.tpl` if a wrong value must fail.
4. Add a subtest to `chart_test.go` named with the requirement ID.
5. Update the tables on this page.

**Add a Secret.** (For TLS material, see [TLS](#tls).) Add an entry in `trove.secretMounts` (`_secrets.tpl`) with its scope, the config
key it sets in `trove.config`, the value in `values.yaml` and the schema, and the managed-key
check in `_validate.tpl`. Extend `R-HA-030 only the pods that need a Secret mount it`.

**Add an object.** New template file, label it with `trove.labels`, take its name from
`trove.fullname`, and extend `R-HA-028 names follow the fullname rule`. Check `R-HA-061` still
passes: no PVC, no StatefulSet.

**Add a role or pool.** Both roles come from one loop in `deployments.yaml`, `pdb.yaml` and the
validation; extend the loops, not a copy. Selector labels are immutable: a new selector label
needs a migration plan for the existing Deployments.

## The Bastion chart

The second chart, [deploy/helm/trove-bastion](../../deploy/helm/trove-bastion), deploys the Bastion: `trove serve --role bastion`,
a read-only, internet-facing cache in front of a main installation ([spec/bastion.md](../spec/bastion.md), R-BAS-001, R-BAS-002,
R-BAS-025, R-BAS-030, R-BAS-033, R-BAS-038, R-BAS-040 to R-BAS-047; the security view is
[security/bastion.md](../security/bastion.md)). It is separate from the chart `trove` on purpose (Q-BAS-5): no PostgreSQL, no
object store of the main installation, no migration Job, no second role, no Secret, no Role and no RoleBinding. The helper
names are `trove-bastion.*` (`_helpers.tpl`), the idioms are those of the chart `trove`: `<fullname>` (release name, or
`<release>-trove-bastion`, cut at 50 characters), the secret mount list used for volumes and mounts, the fail-fast
validation, restricted pod security, probes on the ops port, `extraObjects` through `tpl`.

```text
deploy/helm/trove-bastion/
├── Chart.yaml              version = appVersion = 0.0.0-dev (build.yml rewrites both with the release version)
├── values.yaml             the block of R-BAS-046, copied there verbatim
├── values.schema.json      draft-07, additionalProperties false outside config; config = the generated role schema (below)
├── chart-managed-keys.yaml generated: the config paths the chart renders and refuses (below)
├── ci/                     value sets the tests render (not packaged, see .helmignore)
│   ├── required-only.yaml      the five values a render needs (Q-BAS-55)
│   ├── bucket-valkey.yaml      bucket backend, Sentinel Valkey, trust bundle, tracing, ServiceMonitor, PrometheusRule
│   ├── persistent-routed.yaml  StatefulSet with a claim template, routing by path, edge-limit whitelist
│   ├── autoscaling.yaml        HPA, bastion.replicas = autoscaling.maxReplicas
│   ├── existing-tls.yaml       tls.existingSecret, no Certificate, emptyDir cache, CA bundle from a Secret
│   └── plaintext.yaml          tls.enabled: false, no edge limit, no Ingress, one replica
├── tests/rules_test.yaml   promtool test rules for the PrometheusRule (not packaged)
└── templates/
    ├── _helpers.tpl        names, labels, image, config merge, secret and ConfigMap mounts, probes, topology spread
    ├── _validate.tpl       fail-fast validation (R-BAS-045), managed-key refusal (R-BAS-002)
    ├── _rules.tpl          the alerts of R-BAS-041 and the list of their names
    ├── _notes.tpl          NOTES.txt
    ├── workload.yaml       Deployment, or StatefulSet with cacheVolume.type: persistent
    ├── configmap.yaml  services.yaml  ingress.yaml  certificate.yaml  networkpolicy.yaml  pdb.yaml  hpa.yaml
    ├── serviceaccount.yaml  servicemonitor.yaml  prometheusrule.yaml  extra-objects.yaml  validate.yaml  NOTES.txt
```

**What it renders.** The names are the table of R-BAS-045 (also in the [README](../../README.md#kubernetes-and-helm)). The workload is
a Deployment, or a StatefulSet with `cacheVolume.type: persistent`, whose claim template gives every pod a volume of its own; the
cache volume of the default `ephemeral` type is a generic ephemeral volume of the pod, `emptyDir` an `emptyDir`, and the bucket
backend mounts none (the spool of fills is `/tmp`). `replicas` is `replicaCount`, and absent with `autoscaling.enabled`, where the
HPA owns it. The NetworkPolicy holds ingress rules only, as the chart `trove`'s (R-BAS-047, ADR-154): the https port from
`networkPolicy.ingress.https.from`, the ops port from `networkPolicy.ingress.ops.from` and `probeCIDRs`. There is no
`networkPolicy.egress` value; the Bastion pods' egress is the cluster operator's
([H-522](../security/bastion.md#h-522--the-chart-restricts-no-egress-of-the-bastion-pods)).

**Configuration (R-BAS-002).** `trove-bastion.config` merges `config` with the keys the chart manages (managed wins):
`bastion.main.token_file`, `bastion.main.ca_file` and `bastion.cache.bucket.ca_file` (the one key of `trustedCA`),
`bastion.signing_key_file`, `bastion.cache.volume.dir` (`/var/cache/trove-bastion`, volume backend), the bucket credential
files (`source: static`), `bastion.replicas`, `valkey.username_file`, `valkey.password_file`, `valkey.sentinel.username_file`, `valkey.sentinel.password_file`
(from `secrets.valkeySentinel`; the Sentinels of a Valkey that requires a password need them, found by the end-to-end run) and `valkey.tls.ca_file` (the Valkey CA,
else the trust bundle) while Valkey is configured, and `server.tls` with the listener pair while `tls.enabled` (`server.ops_tls` keeps
its default, which inherits it). `trove-bastion.validate` refuses a managed key under `config`. The ConfigMap `<fullname>-config` is
mounted at `/etc/trove/trove.yaml` with `subPath`, and `checksum/config` is the hash of the merged text only: the Secrets are
mounted as directories without `subPath`, so a replaced Secret reaches the files (token, TLS, trust bundle are re-read by the
binary) and rolls nothing, while any change of `config` rolls the pods.

**Ingress annotations (R-BAS-033, R-BAS-038, R-BAS-044).** Verified against HAProxy Ingress `v0.16.3` (Q-BAS-56); prefix
`haproxy-ingress.github.io/`:

| Value | Annotations |
|---|---|
| `edgeLimit.enabled` | `limit-rps`, `limit-connections`, `limit-whitelist` (joined `config.bastion.blocking.exempt_cidrs`, omitted while empty) |
| `cacheVolume.routeByPath` | `balance-algorithm: uri`, `assign-backend-server-id: "true"`, `config-backend: hash-type consistent` |
| `tls.enabled` | `secure-backends: "true"`, `secure-sni: <fullname>.<namespace>.svc`, and with `ingress.backendTLS.verify` `secure-verify-ca-secret` (`caSecret`, else the listener's Secret) |

[test/chart/bastion_haproxy_test.go](../../test/chart/bastion_haproxy_test.go) fails when `DEV_HAPROXY_INGRESS_IMAGE` or
`DEV_HAPROXY_INGRESS_CHART_VERSION` of [tools/versions.env](../../tools/versions.env) moves away from the version the names were
read against: verify the keys in the controller's `keys.md`, `haproxy.tmpl` and `annotations/backend.go` at the new tag with `gh api`,
then move the constant. That the controller refuses a flood and pins a path is the e2e suite's (R-BAS-049).

**Validation.** Every row fails `helm template` and names the values: the two URLs (set, `https`), the token Secret, the signing key
with the bucket backend or Valkey, the bucket and its credentials Secret, `cacheVolume.type: persistent` with the bucket backend,
`routeByPath` without the volume backend or without `ingress.enabled`, `edgeLimit.requestsPerSecond` not above
`config.bastion.limits.authenticated.requests_per_second`, `config.server.trusted_proxies` while `ingress.enabled` (behind the Ingress every
request comes from the controller's pods, so without it all callers count as one address, [Q-BAS-130](../spec/bastion.md#q-bas-130--behind-the-ingress-the-chart-requires-the-trusted-proxies);
the message names the value and says to list the controller's pod CIDRs), the spool and the volumes it lives in (below), a certificate source, the
https ingress peer (an empty list means "anywhere"), one trust bundle key, an `ingress.annotations` key the chart renders, a name in `disabledAlerts` that
is no alert, autoscaling bounds and metric, `highAvailability.enforce` (two replicas, a PDB, host spreading `DoNotSchedule` with
`nodeTaintsPolicy: Honor`, no standalone Valkey, three Sentinels), and the managed keys.

**The spool and the volumes** ([Q-BAS-131](../spec/bastion.md#q-bas-131--the-bytes-of-the-fills-in-progress-are-bounded-and-the-chart-sizes-the-volumes-from-the-bound)).
The binary bounds the bytes of the fills in progress at `config.bastion.cache.spool_max_bytes` (2 GiB); the chart makes the volumes agree.
`trove-bastion.bytes` reads a size in the forms of both the role (`2GiB`) and Kubernetes (`2Gi`). With the bucket backend the fills are spooled
into the `tmp` emptyDir: `tmp.sizeLimit` is empty by default and then `spool_max_bytes` plus 256 MiB (`2304Mi`, `trove-bastion.tmpSizeLimit`), and a
`tmp.sizeLimit` below that fails the render ("evicted" is in the message: an emptyDir over its limit evicts the pod). With the volume backend the
fills go below `<cache dir>/tmp`, and `cacheVolume.size` must hold `max_bytes` plus the spool; `tmp` stays 1 Gi. `spool_max_bytes` below
`max_object_bytes` fails as well. The values `tmp.sizeLimit` and `config.bastion.cache.spool_max_bytes` are in the values block of
R-BAS-046; change both places.

**Schema and the `config` subtree.** [values.schema.json](../../deploy/helm/trove-bastion/values.schema.json) is generated once from
the key set of `values.yaml` and edited by hand afterwards (like the one of the chart `trove`) for everything outside `config`, which
is `additionalProperties: false`. The `config` property is not hand-written: it is the schema the binary emits for the role `bastion`,
[internal/config/bastion.schema.json](../../internal/config/bastion.schema.json) without `$schema` and `$id` (the root is draft-07),
spliced in by `go generate ./internal/config` ([schemagen](../../internal/config/schemagen/main.go), flag `-bastion-chart`). So
`make generate` regenerates it after a change of [bastion_role.go](../../internal/config/bastion_role.go), and `make generate-check`
(CI) fails when the committed file differs. A typo, a wrong type or an out-of-range value under `config` fails `helm template`,
`helm lint` and `helm install` with the path of the key. Never edit the `config` property or `chart-managed-keys.yaml` by hand.

The keys the chart renders itself are tagged `chart:"managed"` in `bastion_role.go` and carry `x-trove-chart-managed` in the role
schema (the binary reads them, so the schema has them). The same run writes the topmost managed paths to
[chart-managed-keys.yaml](../../deploy/helm/trove-bastion/chart-managed-keys.yaml), which `templates/_validate.tpl` reads with
`.Files.Get` and refuses with a message naming the key set (`config.<path> is managed by the chart`); the `server.*` paths only while
`tls.enabled`. Adding a key the chart renders into `/etc/trove/trove.yaml`: tag the field, run `make generate`, render it in
`trove-bastion.config` in `_helpers.tpl`; `TestR_BAS_046_EveryRenderedKeyIsInTheRoleSchemaAndManaged` fails for a rendered key that
is missing in the schema or not tagged. `bastion.cache.bucket.credentials.web_identity_token_file` is deliberately not managed: the
chart mounts no token for it, the operator sets the path of the workload identity's token.

Tests: [bastion_schema_test.go](../../test/chart/bastion_schema_test.go) (`TestR_BAS_046_ConfigIsTheGeneratedRoleSchema`,
`TestR_BAS_046_ATypoUnderConfigFailsAtHelm`, `TestR_BAS_002_ChartManagedKeysAreRefusedUnderConfig`,
`TestR_BAS_046_EveryRenderedKeyIsInTheRoleSchemaAndManaged`).

**PrometheusRule and its tests (R-BAS-041).** `_rules.tpl` holds the alerts as a YAML list; `prometheusrule.yaml` drops the names
of `disabledAlerts` and appends `extraRules`. The Valkey alert exists only while `config.valkey.mode` is not `none`, and the replica
alert reads the StatefulSet's metric for a StatefulSet. Selectors carry the release's `namespace` and ops `service`, because one
Prometheus can scrape several Bastions; thresholds come from gauges of the Bastion. [tests/rules_test.yaml](../../deploy/helm/trove-bastion/tests/rules_test.yaml)
has one healthy test in which no alert fires and one test per alert in which it fires; `TestR_BAS_041_PromtoolRules` renders the
rule file from `ci/bucket-valkey.yaml`, runs `promtool check rules` and `promtool test rules`, and fails when an alert lacks either
kind of test. promtool is the release binary at `PROMTOOL_VERSION` of `tools/versions.env` (`go install` of the module is refused by
its `replace` directives): `make promtool` fetches it into `.bin/promtool-<version>` after checking the SHA-256 of the platform,
`make chart-test` depends on it and passes the path as `TROVE_PROMTOOL`, `make versions-check` compares installed copies with the pin.
Running the test by hand needs `TROVE_PROMTOOL`.

**Tests.** `make chart-lint` lints and renders every `ci/` set of both charts; `make chart-test` runs the Bastion's tests next to the
other chart tests:

| Test | Asserts |
|---|---|
| `TestR_BAS_045_RequiredValuesRenderTheResources` | the required values give a Deployment with two replicas, PDB, Ingress with the edge-limit annotations, NetworkPolicy and no Secret, Role, RoleBinding or PVC (in every set); the names; every validation row names its values |
| `TestR_BAS_045_HAProxyAnnotationsArePinnedToTheLocalControllerVersion` | the annotation names against the controller version of the local profile |
| `TestR_BAS_046_ValuesReferenceIsTheSpecBlock` | the key set of `helm show values` equals the block parsed from `docs/spec/bastion.md` (the block is a copy of `values.yaml`) |
| `TestR_BAS_002_ConfigurationOnlyThroughTheChart` | the ConfigMap and its mount, every `config` value in the file, the managed keys, Secret mounts without `subPath`, no Secret in the environment, managed keys refused, the checksum changes with a `config` value and not with the token Secret |
| `TestR_BAS_025_ReplicasAreRenderedIntoTheConfiguration` | `bastion.replicas` is `replicaCount`, or `autoscaling.maxReplicas` with an HPA and no `replicas` field |
| `TestR_BAS_045_TheClientAddressNeedsTheTrustedProxiesBehindTheIngress` | the render fails without `config.server.trusted_proxies` while the Ingress is on, names the value, passes with the Ingress off and with a list |
| `TestR_BAS_030_TheSpoolFitsTheTemporaryVolume` | the derived `tmp.sizeLimit` (`2304Mi`), a set one used as written, a volume too small for the spool or the cache refused, a spool below `max_object_bytes` refused |
| `TestR_BAS_030_CacheVolumeKinds` | persistent gives a StatefulSet with a claim template, ephemeral and `emptyDir`, the bucket backend no volume |
| `TestR_BAS_033_RouteByPath`, `TestR_BAS_038_EdgeLimitIsWiderThanTheBastionsOwn` | the annotations, the refusals naming both values, a user annotation that collides |
| `TestR_BAS_047_NetworkPolicy` | ingress peers and ports; in every set `policyTypes: [Ingress]`, no `egress` key, one port per rule, no empty peer list; `networkPolicy.egress` refused by the schema; the ops rule left out without a peer; `enabled: false` renders none |
| `TestR_BAS_043_PodDisruptionBudgetAndSpreading` | PDB, spread, `highAvailability.enforce`, probes |
| `TestR_BAS_044_TLS`, `TestR_BAS_040_ServiceMonitor` | Certificate or existing Secret, backend TLS annotations, trust bundles, the plaintext switch, the ServiceMonitor |
| `TestR_BAS_041_PrometheusRuleAlerts`, `TestR_BAS_041_PromtoolRules` | the alert names, `disabledAlerts`, scoping; promtool |

**Extension checklists.**

*Add a value.* Add it to [values.yaml](../../deploy/helm/trove-bastion/values.yaml) with its default (commented only where its name does not say it) and to
the values block of [spec/bastion.md](../spec/bastion.md) R-BAS-046 as the same line (the block is a verbatim copy; the key-set test
compares the keys), to
[values.schema.json](../../deploy/helm/trove-bastion/values.schema.json), use it in a template, add a validation row if a wrong value
must fail, and a subtest named with the requirement.

*Add an alert.* Add it to `_rules.tpl` and to `trove-bastion.rules.names`, to the table of R-BAS-041, and a failure test and a healthy
assertion to `tests/rules_test.yaml`; `TestR_BAS_041_PromtoolRules` fails without them.

*Add a Secret.* Add an entry to `trove-bastion.secretMounts` and the file path to `trove-bastion.config`, the managed key to the list in
`_validate.tpl` and to R-BAS-046's list of managed keys.
