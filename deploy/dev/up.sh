#!/usr/bin/env bash
# Brings up the local development profile (R-HA-051, E0.3) on a kind cluster named "trove":
# one control-plane and three worker nodes in three zones, HAProxy Ingress as the edge, cert-manager with a
# dev CA that issues every certificate, the internal-secrets-operator that generates every Secret,
# CloudNativePG with a two-instance PostgreSQL cluster, the Valkey operator with a replica and three
# Sentinels over TLS, SeaweedFS with its buckets over HTTPS, and the trove chart with the dev values.
# TLS on every connection: client -> edge, edge -> trove, trove -> PostgreSQL, Valkey, Sentinel, S3.
#
# Idempotent: a second run reuses the cluster, the Secrets and the certificates and re-applies
# the rest. Independent installs run in parallel; their logs are in .dev/logs/.
#
# Environment:
#   TROVE_SKIP_RELEASE=1  install the dependency stack only, not the trove release. Use it while
#                         the trove image does not serve yet, or to work on the dependencies.
#   TROVE_PROFILE=scatter the `scatter` e2e profile (E0.11): three api and three packages replicas and an edge that
#                         balances round robin per request with its keep-alive to the pods off
#                         (values-scatter.yaml: HAProxy Ingress annotations on trove's Ingress).
#                         Run up.sh again without it to switch back.
#   TROVE_PROFILE=vuln    the `vuln` e2e profile (R-VULN-029): the scatter profile with the scanning reading the test advisory
#                         source and the hosts of the test upstreams listed as public registries (values-vuln.yaml).
#                         Run up.sh again without it to switch back.
#   TROVE_PROFILE=mirror  the `mirror` e2e profile (R-REPO-059): the scatter profile (three api and three packages replicas) with the
#                         passes of a mirror in a worker pool of two processes that claim the queue `mirror` alone (values-mirror.yaml).
#                         Run up.sh again without it to switch back.
#   TROVE_PROFILE=bastion the `bastion` e2e profile (R-BAS-049): the main installation trusts the pod range as a proxy
#                         and lists the base URLs of the e2e Bastions (values-bastion.yaml, deploy/dev/bastion.sh).
#                         Run up.sh again without it to switch back.
#   TROVE_PROFILE=backup  the `backup` e2e profile (R-BAK-029): garbage collection with a grace period of 0s, so that the consistency
#                         scenario of the backup suite collects what it deletes at once (values-backup.yaml).
#                         Run up.sh again without it to switch back.
#   TROVE_POOLER=1        also install the transaction-mode poolers and point the chart at them
#                         (pooler.yaml, values-pooler.yaml, R-HA-058). The profile carries what the drain of a pooler instance
#                         needs (R-HA-059 rule 5: PodDisruptionBudgets, node spread, a grace period of 120 s, a pooled connection
#                         lifetime of 60 s + 15 s) and the role defaults that preflight reads on a pooled connection, which this
#                         script sets with ALTER ROLE trove IN DATABASE trove SET ... (statement_timeout 30s, lock_timeout 5s,
#                         idle_in_transaction_session_timeout 60s, search_path trove: the configuration's defaults).
#   TROVE_IMAGE=<ref>     the trove image to load into the cluster (default guidedtraffic/trove:dev,
#                         built by `make image`). It must be present in the local Docker.
#   TROVE_DEV_HTTP_PORT=n the host port of the edge, default 8080 (https://localhost:8080). Set it when
#                         8080 is taken; the chart's base_url follows. Fixed when the cluster is created.
#   TROVE_RELEASE_VERSION=<x.y.z> and TROVE_RELEASE_DIGEST=<sha256:...>, set together: the published mode of the post-release
#                         check (E5.8, deploy/dev/release-smoke.sh). The release is installed as an installer gets it: the chart
#                         package of that version from https://guided-traffic.github.io/trove/ and the image by that digest from
#                         docker.io/guidedtraffic/trove, which the nodes pull. Nothing is built or loaded from the working
#                         tree, and the chart of the working tree is not used. The e2e suite runs up.sh again for its profile
#                         switches and inherits both variables, so a switch keeps the published release. They exclude
#                         TROVE_IMAGE and TROVE_SKIP_RELEASE=1. The pull secret `dockerhub-pull` is created from
#                         ~/.docker/config.json when that file holds a Docker Hub login (the nodes pull anonymously otherwise).
#   TROVE_HELM_EXTRA=...  extra arguments for the helm upgrade of the trove release.
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

SKIP_RELEASE="${TROVE_SKIP_RELEASE:-0}"
POOLER="${TROVE_POOLER:-0}"
PROFILE="${TROVE_PROFILE:-default}"
case "$PROFILE" in default|scatter|bastion|vuln|mirror|backup) ;; *) die "TROVE_PROFILE must be default, scatter, bastion, vuln, mirror or backup, not '$PROFILE'" ;; esac
IMAGE="${TROVE_IMAGE:-guidedtraffic/trove:dev}"
# The published mode: both variables or none. A mixed setting would install a local build while the caller expects the release.
RELEASE_VERSION="${TROVE_RELEASE_VERSION:-}"
RELEASE_DIGEST="${TROVE_RELEASE_DIGEST:-}"
PUBLISHED=0
if [ -n "$RELEASE_VERSION$RELEASE_DIGEST" ]; then
  [ -n "$RELEASE_VERSION" ] && [ -n "$RELEASE_DIGEST" ] || die "TROVE_RELEASE_VERSION and TROVE_RELEASE_DIGEST must be set together"
  [ "$SKIP_RELEASE" != "1" ] || die "TROVE_RELEASE_VERSION and TROVE_SKIP_RELEASE=1 exclude each other"
  [ -z "${TROVE_IMAGE:-}" ] || die "TROVE_RELEASE_VERSION and TROVE_IMAGE exclude each other: the published mode installs the published image"
  [[ "$RELEASE_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "TROVE_RELEASE_VERSION must look like 0.25.0, not '$RELEASE_VERSION'"
  [[ "$RELEASE_DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]] || die "TROVE_RELEASE_DIGEST must be sha256:<64 hex digits>, not '$RELEASE_DIGEST'"
  PUBLISHED=1
fi
LOGS="$STATE_DIR/logs"
START="$(date +%s)"

check_tools
mkdir -p "$LOGS"

# ── 1. the kind cluster ──────────────────────────────────────────────────────
if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  log "kind cluster '$CLUSTER' exists, reusing it"
else
  if (exec 3<>"/dev/tcp/127.0.0.1/$DEV_HTTP_PORT") 2>/dev/null; then
    die "port $DEV_HTTP_PORT on localhost is in use; free it or set TROVE_DEV_HTTP_PORT=<other port> (base_url follows)"
  fi
  log "creating the kind cluster '$CLUSTER' (1 control-plane, 3 workers in 3 zones, edge on localhost:$DEV_HTTP_PORT)"
  sed "s|\${DEV_HTTP_PORT}|$DEV_HTTP_PORT|g" "$DEV_DIR/kind.yaml" \
    | kind create cluster --name "$CLUSTER" --config - --image "$DEV_KIND_NODE_IMAGE" --wait 180s \
    > "$LOGS/kind.log" 2>&1 || { tail -30 "$LOGS/kind.log" >&2; die "kind create cluster failed (log: $LOGS/kind.log)"; }
fi
# The host port the cluster was created with (kind.yaml), read back from the control-plane node.
EDGE_PORT="$(docker port "${CLUSTER}-control-plane" 30443/tcp 2>/dev/null | head -1 | sed 's/.*://' || true)"
EDGE_PORT="${EDGE_PORT:-$DEV_HTTP_PORT}"
k get nodes >/dev/null || die "the context $CTX does not answer"
# Pods scheduled while a worker is still NotReady would all land on the nodes that are up, and
# the host spreading of the dependencies would be decided by luck.
k wait --for=condition=Ready nodes --all --timeout=180s >/dev/null || die "the kind nodes did not become Ready"

# ── 2. namespace and Secrets ─────────────────────────────────────────────────
log "namespace $NS enforces the restricted Pod Security Standard"
k create namespace "$NS" --dry-run=client -o yaml | k apply -f - >/dev/null
k label namespace "$NS" --overwrite \
  pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/audit=restricted \
  pod-security.kubernetes.io/warn=restricted >/dev/null

# ── 3. the dependencies, in parallel ─────────────────────────────────────────
# A task that others depend on writes $LOGS/<name>.ok when it is done (or .fail), and a dependent task
# waits for the marker. So the Valkey and S3 installs do not wait for PostgreSQL, the longest one.
rm -f "$LOGS"/*.ok "$LOGS"/*.fail
declare -a PIDS=() NAMES=()
# spawn <name> <function>: runs the function in the background with its own log.
spawn() {
  local name="$1" fn="$2"
  ( set -euo pipefail; if "$fn"; then touch "$LOGS/$name.ok"; else touch "$LOGS/$name.fail"; exit 1; fi ) > "$LOGS/$name.log" 2>&1 &
  PIDS+=("$!"); NAMES+=("$name")
}
# await <name>...: waits for the markers of the tasks this one depends on.
await() {
  local n
  for n in "$@"; do
    until [ -e "$LOGS/$n.ok" ]; do
      [ ! -e "$LOGS/$n.fail" ] || { echo "the task $n failed"; return 1; }
      sleep 1
    done
  done
}
# digest_of <image-ref> prints sha256:... of a reference pinned by digest.
digest_of() { printf '%s\n' "${1##*@}"; }

task_haproxy() {
  # The chart's image values take a tag; "tag@digest" pins it by digest.
  local ref="${DEV_HAPROXY_INGRESS_IMAGE#quay.io/}"           # jcmoraisjr/haproxy-ingress:v0.16.3@sha256:...
  local repo="${ref%%:*}" tag="${ref#*:}"
  helm --kube-context "$CTX" upgrade --install haproxy-ingress \
    "$(haproxy_chart_tgz)" -n ingress-controller --create-namespace -f "$DEV_DIR/haproxy-values.yaml" \
    --set controller.image.registry=quay.io --set controller.image.repository="$repo" --set controller.image.tag="$tag" \
    --wait --timeout 5m
}

task_pki() {
  helm --kube-context "$CTX" upgrade --install cert-manager \
    "$(chart_tgz https://charts.jetstack.io cert-manager "$DEV_CERT_MANAGER_CHART_VERSION")" -n cert-manager --create-namespace --set crds.enabled=true \
    --set image.digest="$(digest_of "$DEV_CERT_MANAGER_CONTROLLER_IMAGE")" \
    --set webhook.image.digest="$(digest_of "$DEV_CERT_MANAGER_WEBHOOK_IMAGE")" \
    --set cainjector.image.digest="$(digest_of "$DEV_CERT_MANAGER_CAINJECTOR_IMAGE")" \
    --set startupapicheck.image.digest="$(digest_of "$DEV_CERT_MANAGER_STARTUPAPICHECK_IMAGE")" \
    --wait --timeout 5m
  render "$DEV_DIR/pki.yaml" | kn apply -f -
  kn wait --for=condition=Ready certificate/trove-dev-ca certificate/trove-tls certificate/trove-edge-tls certificate/trove-s3-tls --timeout=3m
}

task_secrets() {
  helm --kube-context "$CTX" upgrade --install internal-secrets-operator \
    "$(chart_tgz https://guided-traffic.github.io/internal-secrets-operator internal-secrets-operator "$DEV_ISO_CHART_VERSION")" \
    -n internal-secrets-operator-system --create-namespace \
    --set image.tag="${DEV_ISO_IMAGE#*:}" --wait --timeout 5m
  kn apply -f "$DEV_DIR/secrets.yaml"
  wait_secret_fields 120 trove-valkey-auth:password trove-s3:secret-access-key trove-encryption-keys:dev.key trove-break-glass:password
}

task_vko() {
  helm --kube-context "$CTX" upgrade --install valkey-operator \
    "$(chart_tgz https://guided-traffic.github.io/valkey-operator/ valkey-operator "$DEV_VALKEY_OPERATOR_CHART_VERSION")" \
    -n valkey-operator-system --create-namespace --set image.digest="$(digest_of "$DEV_VALKEY_OPERATOR_IMAGE")" \
    --wait --timeout 5m
}

task_postgres() {
  local image="${DEV_CNPG_OPERATOR_IMAGE#*:}"   # 1.30.1@sha256:...
  helm --kube-context "$CTX" upgrade --install cnpg \
    "$(cnpg_chart_tgz)" -n cnpg-system --create-namespace \
    --set image.repository="${DEV_CNPG_OPERATOR_IMAGE%%:*}" --set image.tag="$image" --wait --timeout 5m
  # helm --wait sees the operator pod ready before its admission webhook answers: the first apply
  # of the Cluster then fails with "failed calling webhook mcluster.cnpg.io ... connection refused".
  # Wait until the webhook Service has a ready endpoint, then apply, retrying while the webhook
  # still refuses connections.
  local deadline=$(( $(date +%s) + 300 ))
  until [ -n "$(kubectl --context "$CTX" -n cnpg-system get endpointslices -l kubernetes.io/service-name=cnpg-webhook-service \
      -o jsonpath='{range .items[*].endpoints[?(@.conditions.ready==true)]}{.addresses[0]}{end}' 2>/dev/null)" ]; do
    [ "$(date +%s)" -lt "$deadline" ] || die "the CloudNativePG webhook has no ready endpoint after 300s"
    sleep 2
  done
  until render "$DEV_DIR/postgres.yaml" | kn apply -f - 2>"$LOGS/postgres-apply.err"; do
    grep -q 'failed calling webhook' "$LOGS/postgres-apply.err" || { cat "$LOGS/postgres-apply.err" >&2; return 1; }
    [ "$(date +%s)" -lt "$deadline" ] || { cat "$LOGS/postgres-apply.err" >&2; die "the CloudNativePG webhook still refuses the Cluster after 300s"; }
    sleep 3
  done
  # The cluster object exists before its status; wait for the condition.
  kn wait --for=condition=Ready cluster/trove-db --timeout=10m
  local ready
  ready="$(kn get cluster trove-db -o jsonpath='{.status.readyInstances}')"
  [ "$ready" = "2" ] || { echo "trove-db has $ready ready instances, want 2"; exit 1; }
  if [ "$POOLER" = "1" ]; then
    # A transaction-mode pooler cannot pass these four settings as startup parameters (R-HA-058); preflight refuses to start
    # without them as role defaults. The values are the defaults of database.session (internal/config/database.go). Idempotent.
    local primary
    primary="$(kn get cluster trove-db -o jsonpath='{.status.currentPrimary}')"
    kn exec "$primary" -c postgres -- psql -U postgres -d trove -v ON_ERROR_STOP=1 -c "
      ALTER ROLE trove IN DATABASE trove SET statement_timeout = '30s';
      ALTER ROLE trove IN DATABASE trove SET lock_timeout = '5s';
      ALTER ROLE trove IN DATABASE trove SET idle_in_transaction_session_timeout = '60s';
      ALTER ROLE trove IN DATABASE trove SET search_path = trove;"
    kn apply -f "$DEV_DIR/pooler.yaml"
    # The operator creates the Deployments a moment after the Pooler objects.
    for d in trove-db-pooler-rw trove-db-pooler-control; do
      until kn get deployment "$d" >/dev/null 2>&1; do sleep 1; done
      kn rollout status "deployment/$d" --timeout=5m
    done
  fi
}

task_valkey() {
  await pki secrets vko
  render "$DEV_DIR/valkey.yaml" | kn apply -f -
  # The operator creates the StatefulSets a moment after the resource.
  until kn get statefulset trove-valkey trove-valkey-sentinel >/dev/null 2>&1; do sleep 1; done
  sts_wait_ready trove-valkey 2 300
  sts_wait_ready trove-valkey-sentinel 3 300
}

task_s3() {
  await pki secrets
  kn delete job trove-s3-buckets --ignore-not-found >/dev/null
  render "$DEV_DIR/s3.yaml" | kn apply -f -
  kn rollout status deployment/trove-s3 --timeout=5m
  kn wait --for=condition=Complete job/trove-s3-buckets --timeout=5m
  kn logs job/trove-s3-buckets
}

# The test IdP (E1.1, Q-AUTH-35): Keycloak with the realm of deploy/dev/keycloak.yaml, HTTPS with a certificate of the dev CA.
task_keycloak() {
  await pki secrets
  render "$DEV_DIR/keycloak.yaml" | kn apply -f -
  kn wait --for=condition=Ready certificate/trove-keycloak-tls --timeout=3m
  wait_secret_fields 120 trove-keycloak:admin-password trove-keycloak:client-secret trove-keycloak:alice-password trove-keycloak:bob-password
  kn rollout status deployment/trove-keycloak --timeout=10m
}

log "installing HAProxy Ingress, cert-manager + PKI, the secret generator, CloudNativePG + PostgreSQL, the Valkey operator + Valkey, SeaweedFS and the Keycloak test IdP in parallel"
spawn haproxy task_haproxy
spawn pki task_pki
spawn secrets task_secrets
spawn vko task_vko
spawn postgres task_postgres
spawn valkey task_valkey
spawn s3 task_s3
spawn keycloak task_keycloak

failed=0
for i in "${!PIDS[@]}"; do
  if wait "${PIDS[$i]}"; then
    log "${NAMES[$i]} ready ($(elapsed "$START") since start)"
  else
    log "${NAMES[$i]} FAILED, last lines of $LOGS/${NAMES[$i]}.log:"
    tail -25 "$LOGS/${NAMES[$i]}.log" >&2
    failed=1
  fi
done
[ "$failed" = 0 ] || die "a dependency did not come up"

# The dev CA, for a client on the host (curl --cacert, a browser's trust store, the e2e suite).
mkdir -p "$STATE_DIR/pki"
secret_field trove-dev-ca ca.crt | base64 -d > "$STATE_DIR/pki/ca.crt"
printf '*\n' > "$STATE_DIR/.gitignore"

# ── 4. verify the dependency stack ───────────────────────────────────────────
primary="$(valkey_primary)" || die "no Sentinel reports a primary for 'trove-valkey'"
log "Valkey primary (asked through TLS at the Sentinels): $primary"
log "PostgreSQL primary: $(kn get cluster trove-db -o jsonpath='{.status.currentPrimary}')"

# ── 5. the trove release ─────────────────────────────────────────────────────
if [ "$SKIP_RELEASE" = "1" ]; then
  log "TROVE_SKIP_RELEASE=1: the trove release is not installed"
else
  if [ "$PUBLISHED" = "1" ]; then
    # The chart package is the file that release-smoke.sh verified when it ran in this checkout; it is fetched here otherwise.
    chart="$(chart_tgz https://guided-traffic.github.io/trove/ trove "$RELEASE_VERSION")" || die "the chart trove $RELEASE_VERSION cannot be pulled from the chart repository"
    pull_args=()
    if published_pull_secret; then
      pull_args=(--set-json 'global.imagePullSecrets=[{"name":"dockerhub-pull"}]')
    else
      log "no Docker Hub login in ${DOCKER_CONFIG:-$HOME/.docker}/config.json: the nodes pull the image anonymously"
    fi
    release_values "$DEV_DIR" "$PROFILE" 0
    values=("${RELEASE_VALUES[@]}" --set "image.digest=$RELEASE_DIGEST" ${pull_args[@]+"${pull_args[@]}"})
    log "installing the published release $RELEASE_VERSION by digest $RELEASE_DIGEST (helm upgrade --install trove, migration Job first)"
  else
    if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
      die "the image $IMAGE is not in the local Docker; build it with 'make image' (or set TROVE_SKIP_RELEASE=1 for the dependencies only)"
    fi
    log "loading $IMAGE into the kind cluster"
    kind load docker-image "$IMAGE" --name "$CLUSTER" > "$LOGS/image-load.log" 2>&1 \
      || { tail -20 "$LOGS/image-load.log" >&2; die "kind load docker-image failed"; }
    chart="$REPO_ROOT/deploy/helm/trove"
    release_values "$DEV_DIR" "$PROFILE" 1
    values=("${RELEASE_VALUES[@]}")
    log "installing the trove release (helm upgrade --install trove, migration Job first)"
  fi
  # shellcheck disable=SC2086
  helm --kube-context "$CTX" upgrade --install trove "$chart" -n "$NS" \
    "${values[@]}" --wait --timeout 10m ${TROVE_HELM_EXTRA:-}
  kn get deploy,pods -l app.kubernetes.io/instance=trove -o wide
  log "check: curl -s --cacert $STATE_DIR/pki/ca.crt https://localhost:$EDGE_PORT/api/v1/system/ping"
fi

log "dev profile is up in $(elapsed "$START")"
cat >&2 <<MSG

  kubectl:        kubectl --context $CTX -n $NS get pods -o wide
  trove (edge):   https://localhost:$EDGE_PORT   (HAProxy Ingress, class haproxy; trust $STATE_DIR/pki/ca.crt)
  PostgreSQL:     kubectl --context $CTX -n $NS get cluster trove-db
  Valkey:         deploy/dev/valkey-failover.sh | valkey-kill.sh | valkey-outage.sh 60s
  test IdP:       https://trove-keycloak.$NS.svc:8443/realms/trove (users alice, bob; docs/developer/local-profile.md)
  tear down:      deploy/dev/down.sh
MSG
