# Local development profile

The profile brings up an HA-shaped installation on a laptop with one command: a kind cluster with
one control-plane and three worker nodes in three zones, PostgreSQL with a standby, Valkey with a
replica and three Sentinels over TLS, an S3 store, an Ingress controller and the trove chart with
two `web` and two `worker` replicas. It is the target of the failover and drain tests (E0.13,
E0.14) and the base of the end-to-end tests. The requirement is
[R-HA-051](../spec/ha-and-helm.md); the chart it installs is described in
[helm-chart.md](helm-chart.md). Everything lives in [deploy/dev](../../deploy/dev).

## What runs

| Component | Where | What | Pinned by (`tools/versions.env`) |
|---|---|---|---|
| kind cluster `trove` | Docker | `trove-control-plane` and `trove-worker`, `trove-worker2`, `trove-worker3` in zones `trove-zone-a`, `-b`, `-c`; pod CIDR `10.244.0.0/16` | `KIND_VERSION`, `DEV_KIND_NODE_IMAGE` |
| Traefik | namespace `traefik` | Ingress controller, class `traefik` (default), two replicas pinned to the control-plane node (never drained by the tests, and the source of every proxied request is that node's pod range, which `config.server.trusted_proxies` names); NodePort 30080, mapped to `localhost:8080` (`TROVE_DEV_HTTP_PORT`) | `DEV_TRAEFIK_CHART_VERSION`, `DEV_TRAEFIK_IMAGE` |
| CloudNativePG operator | namespace `cnpg-system` | operator only | `CNPG_CHART_VERSION`, `DEV_CNPG_OPERATOR_IMAGE` |
| PostgreSQL 16 | namespace `trove` | CloudNativePG `Cluster` `trove-db`: 2 instances (primary and standby), synchronous replication with `dataDurability: preferred`, database and owner `trove`, TLS | `DEV_CNPG_POSTGRES_IMAGE` |
| Valkey 9.1 | namespace `trove` | StatefulSet `trove-valkey` (2 pods) and StatefulSet `trove-valkey-sentinel` (3 pods), TLS only, ACL user `trove` | `DEV_VALKEY_IMAGE` |
| SeaweedFS | namespace `trove` | Deployment `trove-s3` (one `weed server`), Service `trove-s3:9000`, buckets `trove-blobs` (versioning on), `trove-pg-archive`, `trove-backups` | `DEV_SEAWEEDFS_IMAGE`, `DEV_AWSCLI_IMAGE` |
| trove | namespace `trove` | Helm release `trove` with [values-dev.yaml](../../deploy/dev/values-dev.yaml): 2 `web`, 2 `worker` (grace period 60 s, so a drain test does not wait 300 s), migration Job, the chart's NetworkPolicies; `up.sh` adds the Docker `kind` network ranges as `networkPolicy.probeCIDRs` | image `ghcr.io/guided-traffic/trove:dev` |

The namespace `trove` enforces the restricted Pod Security Standard (`enforce`, `audit` and `warn`),
and every dependency manifest satisfies it. Images are pinned by digest; the manifests carry
`${DEV_…_IMAGE}` placeholders that `lib.sh` (`render`) substitutes from `tools/versions.env`, so
the pins exist in one place only.

## Targets

All targets call a script in [deploy/dev](../../deploy/dev). They address the cluster through the
context `kind-trove` explicitly, so other kind clusters or whatever the kubeconfig currently
selects are never touched.

| Target | Script | What it does |
|---|---|---|
| `make dev-up` | `up.sh` | creates the cluster and installs everything; idempotent (a second run takes about 20 s) |
| `make dev-down` | `down.sh` | deletes the kind cluster `trove` and `.dev/` |
| `make dev-failover` | `failover.sh [instance]` | PostgreSQL switchover to the standby, waits until the old primary is a standby again |
| `make dev-valkey-failover` | `valkey-failover.sh` | `SENTINEL FAILOVER trove`, waits until another pod is primary |
| `make dev-valkey-kill` | `valkey-kill.sh` | force-deletes the Valkey primary pod, waits for a primary and a connected replica |
| `make dev-valkey-outage DURATION=60s` | `valkey-outage.sh 60s` | scales Valkey and the Sentinels to 0 for the duration, then back |
| `make dev-pg-kill` | `pg-kill.sh` | force-deletes the PostgreSQL primary pod, waits until the operator reports a healthy cluster |
| `make dev-pg-stop DURATION=30s` | `pg-stop.sh 30s` | fences every instance of `trove-db` (`cnpg.io/fencedInstances`, PostgreSQL stops, pods and volumes stay), waits, removes the fence, waits for a healthy cluster |
| `make dev-drain` | `drain.sh` | the E0.14 test: drains a node with a web and a worker pod under the k6 load and cursor-persisting jobs; `drain.sh <node>` is the drain alone, `drain.sh --pick` prints the node |
| `make e2e SET=push\|full\|drain\|<regexp>` | `e2e.sh` | the end-to-end suite, see [e2e.md](e2e.md) |
| `make chart-lint`, `make chart-test` | `test/chart/lint.sh`, `test/chart/test.sh` | see [helm-chart.md](helm-chart.md#value-sets-and-tests) |

The failover scripts show that the failover happens and how long it takes. What they cost the clients
is measured by [the end-to-end suite](e2e.md), which runs them under load.

### Environment

| Variable | Default | Effect |
|---|---|---|
| `TROVE_SKIP_RELEASE=1` | unset | `up.sh` installs the dependency stack only, not the trove release. Use it while the image does not serve yet, or to work on the dependencies |
| `TROVE_IMAGE` | `ghcr.io/guided-traffic/trove:dev` | the image `up.sh` loads into the cluster with `kind load docker-image`; build it with `make image IMAGE=ghcr.io/guided-traffic/trove:dev` first. `VERSION` defaults to `dev` (or the number of a `v0.*` tag on `HEAD`), the one non-release version the binary accepts ([e2e.md](e2e.md#findings-of-the-suite)) |
| `TROVE_DEV_HTTP_PORT` | `8080` | host port of the edge. Set it when 8080 is taken (`up.sh` checks and says so); `config.server.base_url` follows. Fixed when the cluster is created |
| `TROVE_PROFILE=scatter` | `default` | the `scatter` e2e profile, see below |
| `TROVE_POOLER=1` | unset | also installs the two transaction-mode poolers ([pooler.yaml](../../deploy/dev/pooler.yaml)) and layers [values-pooler.yaml](../../deploy/dev/values-pooler.yaml). The manifests are applied and the poolers start; the pooler profile itself is verified in M5 |
| `TROVE_HELM_EXTRA` | unset | extra arguments for the `helm upgrade` of the trove release |
| `TROVE_DEV_CLUSTER`, `TROVE_DEV_NAMESPACE` | `trove`, `trove` | cluster and namespace names. The dev CA's certificate names the namespace, so change both only together with a fresh `.dev/` |

### The `scatter` profile

`TROVE_PROFILE=scatter deploy/dev/up.sh` (also on a running cluster) switches to the profile of
R-ARCH-032 and E0.11: three `web` replicas ([values-scatter.yaml](../../deploy/dev/values-scatter.yaml)),
and Traefik started with `--serversTransport.maxIdleConnsPerHost=-1`, so it keeps no idle
connection to a pod and balances every request round robin. Run `up.sh` without the variable to
switch back. [TestE99Scatter](../../test/e2e/e99_scatter_test.go) (E0.11) switches to the profile, runs the M0 checks, shows sixty sequential requests reaching the three pods twenty times each and runs the 100 requests/s load; `e2e.sh` returns the cluster to the default profile afterwards.

## How `up.sh` works

1. Checks the tools against the pins and Docker; on a mismatch it prints the exact install command
   (see [Tools](#tools)).
2. `kind create cluster` with [kind.yaml](../../deploy/dev/kind.yaml) and the pinned node image, then waits for
   every node to be Ready. (Pods scheduled while a worker is still NotReady would all land on the
   nodes that are up, and the host spreading of the dependencies would be decided by luck.)
3. Creates the namespace with the restricted labels and the Secrets that do not exist yet
   (below). A second run keeps every credential.
4. In parallel, with logs in `.dev/logs/<name>.log`: Traefik; the CloudNativePG operator, then
   `postgres.yaml`, then waits for two ready instances; `valkey.yaml` and both StatefulSets;
   `s3.yaml`, the Deployment and the bucket Job.
5. Asks the Sentinels over TLS for the primary and prints it, and prints the PostgreSQL primary.
6. Unless `TROVE_SKIP_RELEASE=1`: `kind load docker-image`, then
   `helm upgrade --install trove deploy/helm/trove -n trove -f deploy/dev/values-dev.yaml --wait`.
   The migration Job runs first as a hook.

Measured on an Apple-silicon laptop with Docker, cold (no cluster, `.dev/` removed, images pulled by
the nodes): 2 min 1 s for the dependency stack (E0.3 budget: 15 min). A repeat run: about 20 s.
The release step added about 10 s (measured with the stand-in image described under [verified](#what-is-verified-and-what-is-not)).

### Secrets

Created by `up.sh` when missing, in namespace `trove`:

| Secret | Keys | Used by |
|---|---|---|
| `trove-db-app`, `trove-db-ca` | created by CloudNativePG: `username`, `password`; `ca.crt` | chart: `secrets.database`, `secrets.databaseCA` |
| `trove-valkey-ca`, `trove-valkey-tls` | `ca.crt`; `tls.crt`, `tls.key` (from `.dev/valkey/`, made by [valkey-certs.sh](../../deploy/dev/valkey-certs.sh)) | Valkey, Sentinels; chart: `secrets.valkeyCA` |
| `trove-valkey` | `username` (`trove`), `password` | the ACL user trove connects as; chart: `secrets.valkey` |
| `trove-valkey-sentinel` | `password` | clients of the Sentinels; chart: `secrets.valkeySentinel` |
| `trove-valkey-admin` | `password` | the `default` user: replication and Sentinel to Valkey. trove never gets it |
| `trove-s3` | `access-key-id`, `secret-access-key` | SeaweedFS and the bucket Job; chart: `secrets.s3` |
| `trove-encryption-keys` | `dev.key` (32 random bytes, base64) | chart: `secrets.encryption`; the file name without `.key`, `dev`, is the key ID |

The dev CA and the certificate (825 days, EC P-256, SANs for the Valkey and Sentinel pod names and
`localhost`) are in `.dev/valkey/`. `.dev/` carries its own `.gitignore`, so nothing in it is
committed. `valkey-certs.sh --force` recreates them; delete the two `trove-valkey-*` Secrets and
re-run `up.sh` to roll them into the cluster.

## Valkey and Sentinel

[valkey.yaml](../../deploy/dev/valkey.yaml) implements Q-HA-15 option (d). Facts worth knowing:

- TLS only: `port 0`, `tls-port 6379` and `26379`, `tls-replication yes`, `tls-auth-clients
  optional`. A plaintext connection is reset.
- Pods announce stable host names (`replica-announce-ip`, `sentinel announce-hostnames yes`,
  `resolve-hostnames yes`), so a restarted pod keeps its identity and `get-master-addr-by-name`
  returns a name the certificate covers (`trove-valkey-0.trove-valkey.trove.svc`).
- An init container asks the running Sentinels for the primary before Valkey or a Sentinel
  starts. Without an answer (first start, or after a full outage) pod 0 is the primary. A restarted
  old primary therefore joins as replica after a failover.
- ACL: the user `trove` has the example rule of R-ARCH-031 (`~trove:*`, `&trove:*`, no scripts, no
  administration). `maxmemory 256mb`, `allkeys-lru`, no persistence, `emptyDir` only.
- Sentinels: quorum 2, `down-after-milliseconds 5000`, `failover-timeout 60000`. After a failover
  the Sentinels refuse another automatic one for twice the failover timeout (120 s); a
  `SENTINEL FAILOVER` does not wait for that.
- A NetworkPolicy admits only pods labelled `app.kubernetes.io/name=trove` and `trove/serve=true`
  (the chart's `web` and `worker` pods) and the Valkey pods themselves. kind's CNI enforces it:
  an unlabelled pod's connection hangs, a labelled one gets `NOAUTH` (verified).
- `valkey-kill.sh` reports two outcomes, both correct: the primary *moved* (the pod stayed away
  longer than 5 s) or *restarted in place* (the StatefulSet recreated it within 5 s, before any
  Sentinel declared it down; its data is gone and the replica resynchronised to nothing, which
  costs cache warmth only, R-HA-056).
- Both StatefulSets are spread over the nodes (`DoNotSchedule`, `nodeTaintsPolicy: Honor`) and
  have a PodDisruptionBudget with `maxUnavailable: 1`.

## Reaching things

```shell
kubectl --context kind-trove -n trove get pods -o wide          # everything
curl -s http://localhost:8080/api/v1/system/ping                # through Traefik (once trove serves)
kubectl --context kind-trove -n trove get cluster trove-db      # PostgreSQL, primary and standby
```

Sentinel and Valkey from inside their pods (the password is read in the pod, never typed):

```shell
kubectl --context kind-trove -n trove exec trove-valkey-sentinel-0 -c sentinel -- sh -c \
  'valkey-cli --tls --cacert /tls/ca.crt -p 26379 --no-auth-warning -a "$(cat /secrets/sentinel/password)" \
   sentinel get-master-addr-by-name trove'
kubectl --context kind-trove -n trove exec trove-valkey-0 -c valkey -- sh -c \
  'valkey-cli --tls --cacert /tls/ca.crt -p 6379 --no-auth-warning -a "$(cat /secrets/admin/password)" client list'
```

PostgreSQL and S3 from the host: `kubectl --context kind-trove -n trove port-forward svc/trove-db-rw 5432`
and `svc/trove-s3 9000`; the credentials are in `trove-db-app` and `trove-s3`.

## Tools

| Tool | Pin | Check |
|---|---|---|
| kind | `KIND_VERSION` | `kind version` |
| kubectl | `KUBECTL_VERSION` | `kubectl version --client` |
| Helm | `HELM_VERSION` | `helm version` |
| openssl | `OPENSSL_IMAGE` (a container image, no host install) | `docker run --rm "$OPENSSL_IMAGE" version` |
| Docker | none | the daemon must answer |

`up.sh` fails when kind, kubectl or Helm differs from its pin and print the install command.
openssl is never the host's (macOS ships LibreSSL, Ubuntu 3.x): `valkey-certs.sh` runs it from `OPENSSL_IMAGE` with `docker run --rm --user "$(id -u):$(id -g)"`, so the files belong to the caller. `test/integration/certs.sh` uses the same image. `TROVE_CERT_DIR` redirects the output of `valkey-certs.sh`. The Secret passwords come from `/dev/urandom`.
The scripts run under macOS `/bin/bash` 3.2 as well as bash 5.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `port 8080 on localhost is in use` | another process holds the port. `TROVE_DEV_HTTP_PORT=18080 make dev-up`; `base_url` follows. The port is fixed per cluster: `make dev-down` first to change it |
| `kind create cluster failed` | see `.dev/logs/kind.log`; usually Docker is out of memory or a stale `trove-*` container exists (`docker ps -a --filter name=trove-`) |
| a dependency FAILED | the last lines of its log are printed; the full log is `.dev/logs/<traefik\|postgres\|valkey\|s3>.log`. Re-run `make dev-up`, it is idempotent |
| `the image … is not in the local Docker` | build it (`make image`), or `TROVE_SKIP_RELEASE=1` for the dependencies only |
| trove pods `CrashLoopBackOff` right after install | the binary's server shell may not be there yet: `serve` is a stub until the server-shell work package lands. Check `kubectl logs` and use `TROVE_SKIP_RELEASE=1` |
| `trove-db-2` Pending after a drain | its volume is local to the drained node; it starts again after `kubectl uncordon`. `drain.sh` uncordons for you |
| drain hangs on a CloudNativePG pod | the primary's own PodDisruptionBudget (`trove-db-primary`, `minAvailable: 1`) refuses its eviction. Run `make dev-failover` first, or let `drain.sh` choose the node |
| Valkey kill: no failover although the pod is gone | a failover less than 120 s ago blocks the next automatic one (see above); the script waits up to 4 minutes |
| helm release stuck `pending-install` | `helm --kube-context kind-trove -n trove uninstall trove`; the hook Job, ConfigMap and ServiceAccount `trove-migrate` outlive an uninstall by design, delete them with `kubectl delete job,cm,sa trove-migrate` |
| a clean start | `make dev-down && make dev-up` |

## What is verified, and what is not

Verified by [the end-to-end suite](e2e.md) on this profile, with the release image: the cold `make dev-up`
(about 2 min 20 s for the dependencies plus the release step, budget 15 min), every trove pod answering
`200` on `:8081/readyz`, the replica list and ping through the Ingress, the restricted Pod Security
Standard and the chart's NetworkPolicy (enforced by kindnet: a pod of another namespace reaches `:8080`
and not `:8081`), the failing migration Job, the lease failover, the Valkey connection names and the
start-up cases, the Valkey failover, kill and outage under load, the `scatter` profile, the PostgreSQL
failovers under load and the node drain. `e2e.md` lists what failed in the product and why.

Not configured: WAL archiving to `trove-pg-archive` (point-in-time recovery is M5), the backup
CronJob (the chart skeleton has none), an OIDC provider. TLS between the edge and trove (`tls.enabled`
is `false` in [values-dev.yaml](../../deploy/dev/values-dev.yaml) until the TLS work package switches the
profile).
