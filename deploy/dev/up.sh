#!/usr/bin/env bash
# Brings up the local development profile (R-HA-051, E0.3) on a kind cluster named "trove":
# one control-plane and three worker nodes in three zones, the Traefik Ingress controller,
# CloudNativePG with a two-instance PostgreSQL cluster, Valkey with a replica and three
# Sentinels over TLS, SeaweedFS with its buckets, and the trove chart with the dev values.
#
# Idempotent: a second run reuses the cluster, the Secrets and the certificates and re-applies
# the rest. Independent installs run in parallel; their logs are in .dev/logs/.
#
# Environment:
#   TROVE_SKIP_RELEASE=1  install the dependency stack only, not the trove release. Use it while
#                         the trove image does not serve yet, or to work on the dependencies.
#   TROVE_PROFILE=scatter the `scatter` e2e profile (E0.11): three web replicas and an edge that
#                         balances round robin per request with its keep-alive to the pods off
#                         (values-scatter.yaml, Traefik --serversTransport.maxIdleConnsPerHost=-1).
#                         Run up.sh again without it to switch back.
#   TROVE_POOLER=1        also install the transaction-mode poolers and point the chart at them
#                         (pooler.yaml, values-pooler.yaml, R-HA-058).
#   TROVE_IMAGE=<ref>     the trove image to load into the cluster (default ghcr.io/guided-traffic/trove:dev,
#                         built by `make image`). It must be present in the local Docker.
#   TROVE_DEV_HTTP_PORT=n the host port of the edge, default 8080 (http://localhost:8080). Set it when
#                         8080 is taken; the chart's base_url follows. Fixed when the cluster is created.
#   TROVE_HELM_EXTRA=...  extra arguments for the helm upgrade of the trove release.
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

SKIP_RELEASE="${TROVE_SKIP_RELEASE:-0}"
POOLER="${TROVE_POOLER:-0}"
PROFILE="${TROVE_PROFILE:-default}"
case "$PROFILE" in default|scatter) ;; *) die "TROVE_PROFILE must be default or scatter, not '$PROFILE'" ;; esac
IMAGE="${TROVE_IMAGE:-ghcr.io/guided-traffic/trove:dev}"
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
EDGE_PORT="$(docker port "${CLUSTER}-control-plane" 30080/tcp 2>/dev/null | head -1 | sed 's/.*://' || true)"
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

# ensure_secret <name> <kubectl create secret args...>: creates the Secret when it is missing,
# so a second run keeps every credential.
ensure_secret() {
  local name="$1"; shift
  if kn get secret "$name" >/dev/null 2>&1; then
    return 0
  fi
  kn create secret "$@" >/dev/null
  log "created Secret $name"
}

"$DEV_DIR/valkey-certs.sh"
# randhex <bytes> prints that many random bytes as hex (no host openssl involved).
randhex() { head -c "$1" /dev/urandom | od -An -tx1 | tr -d " \n"; }
ensure_secret trove-valkey-ca generic trove-valkey-ca --from-file=ca.crt="$STATE_DIR/valkey/ca.crt"
ensure_secret trove-valkey-tls tls trove-valkey-tls --cert="$STATE_DIR/valkey/tls.crt" --key="$STATE_DIR/valkey/tls.key"
ensure_secret trove-valkey generic trove-valkey --from-literal=username=trove --from-literal=password="$(randhex 24)"
ensure_secret trove-valkey-sentinel generic trove-valkey-sentinel --from-literal=password="$(randhex 24)"
ensure_secret trove-valkey-admin generic trove-valkey-admin --from-literal=password="$(randhex 24)"
ensure_secret trove-s3 generic trove-s3 --from-literal=access-key-id=trove-dev --from-literal=secret-access-key="$(randhex 24)"
# R-HA-027: the operator creates the encryption key; the file name without .key is the key ID.
ensure_secret trove-encryption-keys generic trove-encryption-keys --from-literal=dev.key="$(head -c 32 /dev/urandom | base64)"

# ── 3. the dependencies, in parallel ─────────────────────────────────────────
declare -a PIDS=() NAMES=()
# spawn <name> <function>: runs the function in the background with its own log.
spawn() {
  local name="$1" fn="$2"
  ( set -euo pipefail; "$fn" ) > "$LOGS/$name.log" 2>&1 &
  PIDS+=("$!"); NAMES+=("$name")
}

task_traefik() {
  local image="${DEV_TRAEFIK_IMAGE#docker.io/}"      # traefik:v3.7.16@sha256:...
  local repo="${image%%:*}" digest="${image##*@}" tag="${image#*:}"; tag="${tag%%@*}"
  local scatter_args=()
  # The scatter profile: no idle connection to a backend is kept, so no client sticks to a pod.
  [ "$PROFILE" = "scatter" ] && scatter_args=(--set-json 'additionalArguments=["--serversTransport.maxIdleConnsPerHost=-1"]')
  helm --kube-context "$CTX" upgrade --install traefik traefik --repo https://traefik.github.io/charts \
    --version "$DEV_TRAEFIK_CHART_VERSION" -n traefik --create-namespace -f "$DEV_DIR/traefik-values.yaml" \
    --set image.registry=docker.io --set image.repository="$repo" --set image.digest="$digest" \
    --set versionOverride="$tag" ${scatter_args[@]+"${scatter_args[@]}"} --wait --timeout 5m
}

task_postgres() {
  local image="${DEV_CNPG_OPERATOR_IMAGE#*:}"   # 1.30.1@sha256:...
  helm --kube-context "$CTX" upgrade --install cnpg cloudnative-pg --repo https://cloudnative-pg.github.io/charts \
    --version "$CNPG_CHART_VERSION" -n cnpg-system --create-namespace \
    --set image.repository="${DEV_CNPG_OPERATOR_IMAGE%%:*}" --set image.tag="$image" --wait --timeout 5m
  render "$DEV_DIR/postgres.yaml" | kn apply -f -
  # The cluster object exists before its status; wait for the condition.
  kn wait --for=condition=Ready cluster/trove-db --timeout=10m
  local ready
  ready="$(kn get cluster trove-db -o jsonpath='{.status.readyInstances}')"
  [ "$ready" = "2" ] || { echo "trove-db has $ready ready instances, want 2"; exit 1; }
  if [ "$POOLER" = "1" ]; then
    kn apply -f "$DEV_DIR/pooler.yaml"
    # The operator creates the Deployments a moment after the Pooler objects.
    for d in trove-db-pooler-rw trove-db-pooler-control; do
      until kn get deployment "$d" >/dev/null 2>&1; do sleep 1; done
      kn rollout status "deployment/$d" --timeout=5m
    done
  fi
}

task_valkey() {
  render "$DEV_DIR/valkey.yaml" | kn apply -f -
  kn rollout status statefulset/trove-valkey --timeout=5m
  kn rollout status statefulset/trove-valkey-sentinel --timeout=5m
}

task_s3() {
  kn delete job trove-s3-buckets --ignore-not-found >/dev/null
  render "$DEV_DIR/s3.yaml" | kn apply -f -
  kn rollout status deployment/trove-s3 --timeout=5m
  kn wait --for=condition=Complete job/trove-s3-buckets --timeout=5m
  kn logs job/trove-s3-buckets
}

log "installing Traefik, CloudNativePG + PostgreSQL, Valkey + Sentinels and SeaweedFS in parallel"
spawn traefik task_traefik
spawn postgres task_postgres
spawn valkey task_valkey
spawn s3 task_s3

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

# ── 4. verify the dependency stack ───────────────────────────────────────────
primary="$(valkey_primary)" || die "no Sentinel reports a primary for 'trove'"
log "Valkey primary (asked through TLS at the Sentinels): $primary"
log "PostgreSQL primary: $(kn get cluster trove-db -o jsonpath='{.status.currentPrimary}')"

# ── 5. the trove release ─────────────────────────────────────────────────────
if [ "$SKIP_RELEASE" = "1" ]; then
  log "TROVE_SKIP_RELEASE=1: the trove release is not installed"
else
  if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
    die "the image $IMAGE is not in the local Docker; build it with 'make image' (or set TROVE_SKIP_RELEASE=1 for the dependencies only)"
  fi
  log "loading $IMAGE into the kind cluster"
  kind load docker-image "$IMAGE" --name "$CLUSTER" > "$LOGS/image-load.log" 2>&1 \
    || { tail -20 "$LOGS/image-load.log" >&2; die "kind load docker-image failed"; }

  # trusted_proxies of values-dev.yaml is the pod range of the control-plane node, where Traefik runs.
  cp_cidr="$(k get node "${CLUSTER}-control-plane" -o jsonpath='{.spec.podCIDR}')"
  grep -q "\"$cp_cidr\"" "$DEV_DIR/values-dev.yaml" \
    || die "the control-plane node's pod range is $cp_cidr; set config.server.trusted_proxies in deploy/dev/values-dev.yaml to it"
  # Kubelet probes come from the node addresses, which are on the kind Docker network. A CNI that
  # enforces the chart's NetworkPolicy for them needs the range in networkPolicy.probeCIDRs.
  node_cidrs="$(docker network inspect kind -f '{{range .IPAM.Config}}{{.Subnet}} {{end}}' 2>/dev/null || true)"
  probe_cidrs="["; sep=""
  for c in $node_cidrs; do probe_cidrs="$probe_cidrs$sep\"$c\""; sep=","; done
  probe_cidrs="$probe_cidrs]"
  values=(-f "$DEV_DIR/values-dev.yaml" --set-json "networkPolicy.probeCIDRs=$probe_cidrs")
  [ "$EDGE_PORT" = "8080" ] || values+=(--set "config.server.base_url=http://localhost:$EDGE_PORT")
  [ "$POOLER" = "1" ] && values+=(-f "$DEV_DIR/values-pooler.yaml")
  [ "$PROFILE" = "scatter" ] && values+=(-f "$DEV_DIR/values-scatter.yaml")
  log "installing the trove release (helm upgrade --install trove, migration Job first)"
  # shellcheck disable=SC2086
  helm --kube-context "$CTX" upgrade --install trove "$REPO_ROOT/deploy/helm/trove" -n "$NS" \
    "${values[@]}" --wait --timeout 10m ${TROVE_HELM_EXTRA:-}
  kn get deploy,pods -l app.kubernetes.io/instance=trove -o wide
  log "check: curl -s http://localhost:$EDGE_PORT/api/v1/system/ping"
fi

log "dev profile is up in $(elapsed "$START")"
cat >&2 <<MSG

  kubectl:        kubectl --context $CTX -n $NS get pods -o wide
  trove (edge):   http://localhost:$EDGE_PORT   (Traefik, Ingress class traefik)
  PostgreSQL:     kubectl --context $CTX -n $NS get cluster trove-db
  Valkey:         deploy/dev/valkey-failover.sh | valkey-kill.sh | valkey-outage.sh 60s
  tear down:      deploy/dev/down.sh
MSG
