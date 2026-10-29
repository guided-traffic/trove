# Helm chart

The chart lives in [deploy/helm/trove](../../deploy/helm/trove). This page covers its layout, what
it renders, how its values reach the pods, how it is validated and tested, and how to extend it.
The requirements are in [ha-and-helm.md](../spec/ha-and-helm.md) (R-HA-002, R-HA-011, R-HA-019,
R-HA-027 to R-HA-037, R-HA-056, R-HA-061); the dev profile that installs it is described in
[local-profile.md](local-profile.md).

## Scope: a skeleton

The chart is the M0 skeleton (work package WP0.7). Everything it renders is real and tested. It
does not render what later phases add, and `values.schema.json` refuses their values
(`additionalProperties: false`), so a value of a later phase fails before anything renders instead
of being silently ignored.

| In the chart | Not in the chart yet |
|---|---|
| `web` and `worker` Deployments, always both (R-HA-002) | autoscaling (HPA), `web.pools`, `worker.pools` (R-HA-062) |
| client Service `<fullname>` and ops Service `<fullname>-ops`, `sessionAffinity: None` (R-HA-037) | Gateway API HTTPRoute (R-HA-038), `contentOrigin` |
| PodDisruptionBudget per Deployment, topology spread with `nodeTaintsPolicy: Honor` (R-HA-032) | NetworkPolicy (R-HA-042), ServiceMonitor, PrometheusRule |
| migration Job as `pre-install,pre-upgrade` hook, with hook ConfigMap and ServiceAccount (R-HA-019) | backup CronJob, restore Job, post-restore Job (R-HA-060) |
| configuration file `trove.yaml`, Secret wiring, downward-API variables (R-HA-030) | `secrets.oidc`, `secrets.breakGlass`, `secrets.backupTarget`, `trustedCA`, `bootstrap` |
| restricted-PSS security contexts, probes, spool and `/tmp` volumes (R-HA-033 to R-HA-035, R-HA-061) | `initContainers`, `sidecars`, `lifecycle`, Service types beyond the three in the schema |
| Ingress, path `/` only (R-HA-037) | profiles `small`, `medium`, `large` (R-HA-050), the complete values reference (R-HA-031) |
| fail-fast validation for the rows listed [below](#validation) | the validation rows of the features in the right column |

Gaps in what is there:

- **The `config` subtree is free-form.** R-HA-029 wants it validated against the schema the binary
  emits (`internal/config/schema.json`, not generated yet). The schema types only the keys the
  chart reads itself; a typo elsewhere in `config` fails at runtime (strict validation in the
  binary), not at `helm install`. When the binary emits its schema, `values.schema.json` takes it
  as the `config` property.
- **The container is started with `args: [serve, --role, <role>]` and no `command`.** It relies on
  the image's `ENTRYPOINT` being the trove binary.
- **No kubeconform run** over the rendered output yet; the tool is not pinned.

## Layout

```text
deploy/helm/trove/
├── Chart.yaml              apiVersion v2; version = appVersion = 0.0.0-dev (the release rewrites both)
├── values.yaml             defaults; every value commented "default (proposed)" or "example"
├── values.schema.json      draft-07, additionalProperties false for everything the chart owns
├── ci/                     value sets the tests render (not packaged, see .helmignore)
│   ├── required-only.yaml      only the values R-HA-031 names as required
│   ├── ha-ingress.yaml         every optional Secret, Ingress with TLS, ephemeral spool
│   ├── single-node.yaml        no enforcement, one replica per role, standalone Valkey
│   └── migrations-disabled.yaml  migrations.mode disabled, minAvailable budget, image digest
└── templates/
    ├── _helpers.tpl        names, labels, image, byte and duration parsing, spool budget, config
    ├── _secrets.tpl        the Secret mounts of a pod, as one list used for volumes and mounts
    ├── _pod.tpl            probes, topology spread, downward-API variables
    ├── _validate.tpl       fail-fast validation (R-HA-029)
    ├── validate.yaml       renders nothing; includes the validation
    ├── deployments.yaml    <fullname>-web and <fullname>-worker
    ├── services.yaml       <fullname> and <fullname>-ops
    ├── pdb.yaml            one PodDisruptionBudget per Deployment
    ├── configmap.yaml      <fullname>-config (trove.yaml)
    ├── serviceaccount.yaml
    ├── migrate.yaml        hook ConfigMap, hook ServiceAccount and the migration Job
    ├── ingress.yaml        only with ingress.enabled
    └── extra-objects.yaml  extraObjects, rendered with tpl
```

| File | Responsibility |
|---|---|
| `_helpers.tpl` | `trove.fullname` (release name, or `<release>-trove`, cut at 50 characters so derived names stay DNS labels), `trove.labels` / `trove.selectorLabels`, `trove.image` (digest wins over tag), `trove.bytes` and `trove.seconds` (parse sizes and durations; they fail on anything else, so a typo never becomes a wrong budget), `trove.spoolBudget`, `trove.config` |
| `_secrets.tpl` | `trove.secretMounts` builds one list per scope (`runtime` for `web` and `worker`, `migrate` for the Job); `trove.secretVolumes` and `trove.secretVolumeMounts` render it |
| `_validate.tpl` | every `fail` of R-HA-029, each message naming the offending value |
| `deployments.yaml` | the two Deployments from one loop over `web` and `worker`, so a role cannot drift from the other |
| `migrate.yaml` | the Job and its hook resources; renders nothing with `migrations.mode: disabled` |

## What the chart renders

`<fullname>` is the release name, or `<release>-trove` when the release name does not contain
`trove`; `fullnameOverride` wins.

| Kind | Name | Notes |
|---|---|---|
| Deployment | `<fullname>-web` | `trove serve --role web`; ports `http` 8080 and `ops` 8081 |
| Deployment | `<fullname>-worker` | `trove serve --role worker`; port `ops` 8081 only |
| Service | `<fullname>` | port `http` 8080, selects `web` pods only, `sessionAffinity: None` |
| Service | `<fullname>-ops` | port `ops` 8081, selects every pod labelled `trove/serve: "true"` (`web` and `worker`) |
| ConfigMap | `<fullname>-config` | `trove.yaml`, mounted at `/etc/trove/trove.yaml` |
| ServiceAccount | `<fullname>` | `automountServiceAccountToken: false` |
| PodDisruptionBudget | `<fullname>-web`, `<fullname>-worker` | `maxUnavailable: 1`, `unhealthyPodEvictionPolicy: AlwaysAllow` |
| ConfigMap, ServiceAccount (hook, weight -10) | `<fullname>-migrate` | same `trove.yaml` as `<fullname>-config` |
| Job (hook, weight 0) | `<fullname>-migrate` | `trove migrate up`; `backoffLimit` 2, deadline 1800 s, TTL 86400 s |
| Ingress | `<fullname>` | `ingress.enabled`; one rule per host, path `/`, `Prefix`, backend `<fullname>:http` |
| anything | from `extraObjects` | |

The chart renders no Secret, no Role or RoleBinding, no PersistentVolumeClaim, no StatefulSet and
no CRD (R-HA-028, R-HA-033, R-HA-061); the tests check each.

Labels on every object: `app.kubernetes.io/name`, `instance`, `version`, `managed-by`,
`component` (`web`, `worker`, `migrate`, `ops`, `config`, `serviceaccount`) and `helm.sh/chart`.
`web` and `worker` pods also carry `trove/pool: ""` (selector label, empty for the main
Deployments so that a pool added later never overlaps their selectors; selector labels are
immutable) and `trove/serve: "true"` (what the ops Service selects).

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
| `TROVE_RUNTIME_MEMORY_LIMIT_BYTES` | `resourceFieldRef limits.memory` | `web`, `worker` |
| `TROVE_RUNTIME_TERMINATION_GRACE_PERIOD` | `<terminationGracePeriodSeconds>s` | `web`, `worker` |
| `TROVE_STORAGE_SPOOL_MAX_BYTES` | spool size minus `spool.headroom`, in bytes; not set when `config.storage.spool.max_bytes` is set | `web`, `worker` |
| `GOMAXPROCS` | `resourceFieldRef requests.cpu`, only when the role has no CPU limit | `web`, `worker` |
| `TMPDIR` | `/tmp` | every trove pod |
| `TROVE_DATABASE_PREFLIGHT_EXPECTED_WEB_REPLICAS`, `..._WORKER_REPLICAS` | the replica counts | migration Job |

`extraEnv` and `extraEnvFrom` append to this list. The binary's strict validation accepts the
`TROVE_` names above as schema keys or as reserved variables (R-OPS-028).

### Configuration file

`trove.config` (in `_helpers.tpl`) renders `config` merged with the keys the chart manages. The
chart-managed keys win, and setting one inside `config` fails the render naming it
(`config.storage.spool.dir`, `config.database.password_file`, `config.secrets.active_key_id`, …).

### Secrets

Every Secret is pre-existing; the chart never creates one. Each is mounted read-only with `items`
(only the named keys), `defaultMode: 0440`, no `subPath`, so a rotation reaches the files, and
never as an environment variable. A mount exists only in the pods that read it.

| Value | Keys (default names) | Mount path | Config key set by the chart | Pods |
|---|---|---|---|---|
| `secrets.database` | `username`, `password` | `/var/run/secrets/trove/database/` | `database.username_file`, `database.password_file` | all (the Job uses `secrets.databaseMigration` when set) |
| `secrets.databaseCA` | `ca.crt` | `/var/run/secrets/trove/database-ca/` | `database.ssl_root_cert_file` | all |
| `secrets.databaseClientCert` | `tls.crt`, `tls.key` | `/var/run/secrets/trove/database-client/` | `database.ssl_cert_file`, `database.ssl_key_file` | all |
| `secrets.encryption` | the names in `secrets.encryption.keys` | `/var/run/secrets/trove/encryption/` | `secrets.encryption_key_files` (ordered), `secrets.active_key_id` (first entry without `.key`) | all, the same Secret (R-HA-027) |
| `secrets.s3` (`source: static`) | `access-key-id`, `secret-access-key` | `/var/run/secrets/trove/s3/` | `storage.s3.credentials.access_key_id_file`, `..._secret_access_key_file` | `web`, `worker` |
| `secrets.valkey` | `username` (omit with `usernameKey: ""`), `password` | `/var/run/secrets/trove/valkey/` | `valkey.username_file`, `valkey.password_file` | `web`, `worker` |
| `secrets.valkeySentinel` | `username` (default: none), `password` | `/var/run/secrets/trove/valkey-sentinel/` | `valkey.sentinel.username_file`, `valkey.sentinel.password_file` | `web`, `worker` |
| `secrets.valkeyCA` | `ca.crt` | `/var/run/secrets/trove/valkey-ca/` | `valkey.tls.ca_file` | `web`, `worker` |
| `secrets.valkeyClientCert` | `tls.crt`, `tls.key` | `/var/run/secrets/trove/valkey-client/` | `valkey.tls.cert_file`, `valkey.tls.key_file` | `web`, `worker` |
| `secrets.metricsToken` (`config.metrics.auth: static`) | `token` | `/var/run/secrets/trove/metrics/` | `metrics.bearer_token_file` | `web`, `worker` |

The file *names* inside a mount are fixed (`username`, `password`, `ca.crt`, …); the Secret's key
names are the values (`usernameKey`, `passwordKey`, …). The encryption keys are the exception:
file name equals Secret key, because the file name without `.key` is the key ID (R-ARCH-021).

## Validation

Two layers. `values.schema.json` checks types, enums and unknown keys on install, upgrade, lint
and template. `_validate.tpl` checks the rest and fails with a message that names the value. The
tests in `chart_test.go` assert both, row by row.

<details>
<summary>R-HA-029 rows: implemented and not yet</summary>

| Row | State |
|---|---|
| `config.server.base_url`, `config.database.host`, `secrets.database.existingSecret`, `config.storage.s3.bucket` empty | implemented |
| `credentials.source: static` without `secrets.s3.existingSecret` | implemented |
| `secrets.encryption.existingSecret` or `.keys` empty (prints the creation command of R-HA-027) | implemented |
| `highAvailability.enforce` with fewer than 2 replicas, a disabled PDB, a host spread that is not `DoNotSchedule` or lacks `nodeTaintsPolicy: Honor` | implemented, per role and for custom constraints |
| `worker.replicaCount` < 1 (names `worker.replicaCount`) | implemented |
| `config.storage.upload.max_size` larger than the `web` spool budget | implemented |
| `terminationGracePeriodSeconds` below `config.health.shutdown_drain_delay` + 15 s | implemented, per role |
| Sentinel mode without addresses or `primary_name`; fewer than 3 addresses under `enforce`; standalone under `enforce` | implemented |
| `secrets.valkey.existingSecret` empty | implemented |
| `config.valkey.tls.mode: disable` without `allow_insecure` | implemented |
| `podDisruptionBudget` with both `minAvailable` and `maxUnavailable` | implemented |
| a chart-managed key set inside `config` | implemented (secret file paths, `storage.spool.dir`) |
| `config.metrics.auth: static` without `secrets.metricsToken` | implemented |
| `spool.headroom` not smaller than the spool; sizes that are not quantities | implemented |
| pooler (`session_host`, control endpoint), backup, restore, pools, gateway, ServiceMonitor, `contentOrigin` | with those features |
| a `ReadWriteMany` spool, `--set ingress.path=…`, any unknown key | schema |

</details>

## Value sets and tests

`ci/*.yaml` are the value sets every requirement is asserted against. The dev profile's
[values-dev.yaml](../../deploy/dev/values-dev.yaml) is rendered alongside them (E0.7). Adding a
file to `ci/` adds it to every per-set assertion with no code change.

| Command | What it does |
|---|---|
| `make chart-lint` → [test/chart/lint.sh](../../test/chart/lint.sh) | `helm lint --strict` and `helm template` for every `ci/` set, `values-dev.yaml`, and `values-dev.yaml` with `values-pooler.yaml` |
| `make chart-test` → [test/chart/test.sh](../../test/chart/test.sh) | the Go package [test/chart](../../test/chart/chart_test.go) with build tag `chart`; writes `.out/test-chart.json` for the coverage generator |
| `go test -tags chart ./test/chart/...` | the same, without the wrapper |

Both need only `helm` at the version in `tools/versions.env` (the test binary and `lint.sh` fail
with the install command when it differs) and no cluster. The tests run `helm template` and parse
the output; they are named after the requirement they verify (`R-HA-002 …`, `R-HA-061 …`), which
is where the coverage generator reads the IDs from.

| Test (subtest prefix) | Asserts |
|---|---|
| `R-HA-028` | lint passes for every set; no Secret, RBAC, CRD or subchart; names follow the fullname rule; standard labels |
| `R-HA-002` | exactly two Deployments with `serve --role web` / `serve --role worker`, never `all`; `worker.replicaCount=0` fails naming the key; the client Service selects only `web`; the pool label is in the selectors |
| `R-HA-061` | no `ReadWriteMany`, `ReadOnlyMany`, PVC, StatefulSet, `volumeClaimTemplates`, hostPath; a `ReadWriteMany` spool fails schema validation; only the spool and `/tmp` are writable |
| `R-HA-029` | one subtest per implemented row fails and names the value; the schema refuses unknown keys and wrong types |
| `R-HA-029` and `R-HA-027` | without the encryption Secret the error prints the `kubectl create secret` command |
| `R-HA-027` | all pods mount the same key Secret; ordered ring, first is active; a key-list change rolls the pods |
| `R-HA-030` | file mount, `checksum/config`; Secrets read-only with items, mode 0440, no `subPath`, never in the environment; per-pod scoping; the config paths match the mounts; the environment set; service links off |
| `R-HA-033` | every pod and container of every set satisfies the restricted Pod Security Standard; UID 65532 |
| `R-HA-034`, `R-ARCH-024` | probes as the table of R-HA-034; ports; no `preStop` |
| `R-HA-011` | grace period and its environment variable |
| `R-HA-035`, `R-HA-036` | spool `sizeLimit` and budget; `GOMAXPROCS` rule; default resources |
| `R-HA-032` | PDB, spread, tolerations, default replica counts and rolling update |
| `R-HA-037`, `R-ARCH-032` | `sessionAffinity: None` everywhere, no affinity annotations, Ingress path `/` and `Prefix`, `ingress.path` fails naming `path` |
| `R-HA-019` | hook annotations and weights, `podFailurePolicy`, same `trove.yaml`, nothing from Valkey or S3; disabled renders no Job |
| `R-HA-056` | Valkey wiring reaches `web` and `worker` only |

Not covered by these tests (needs a cluster): that the pods start in a restricted namespace, that
`kubectl exec … touch /x` fails, that the Job finishes before the Deployments roll. The dev
profile covers them once the trove image serves ([local-profile.md](local-profile.md)).

## Extension checklists

**Add a value.**

1. Add it to [values.yaml](../../deploy/helm/trove/values.yaml) with `# default (proposed)` or
   `# example`.
2. Add it to [values.schema.json](../../deploy/helm/trove/values.schema.json) with its type and
   enum. The schema is `additionalProperties: false`; a value missing there fails every render.
3. Use it in the template, and add a validation row to `_validate.tpl` if a wrong value must fail.
4. Add a subtest to `chart_test.go` named with the requirement ID.
5. Update the tables on this page.

**Add a Secret.** Add an entry in `trove.secretMounts` (`_secrets.tpl`) with its scope, the config
key it sets in `trove.config`, the value in `values.yaml` and the schema, and the managed-key
check in `_validate.tpl`. Extend `R-HA-030 only the pods that need a Secret mount it`.

**Add an object.** New template file, label it with `trove.labels`, take its name from
`trove.fullname`, and extend `R-HA-028 names follow the fullname rule`. Check `R-HA-061` still
passes: no PVC, no StatefulSet.

**Add a role or pool.** Both roles come from one loop in `deployments.yaml`, `pdb.yaml` and the
validation; extend the loops, not a copy. Selector labels are immutable: a new selector label
needs a migration plan for the existing Deployments.
