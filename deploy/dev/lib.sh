#!/usr/bin/env bash
# Shared by the scripts in deploy/dev/. Source it; it sets strict mode, reads the pins and
# defines the cluster handles. Nothing here talks to a cluster by itself.

set -euo pipefail

DEV_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$DEV_DIR/../.." && pwd)"
export REPO_ROOT DEV_DIR

CLUSTER="${TROVE_DEV_CLUSTER:-trove}"
CTX="kind-${CLUSTER}"
NS="${TROVE_DEV_NAMESPACE:-trove}"
STATE_DIR="$REPO_ROOT/.dev"
# The host port of the dev edge (HAProxy Ingress, HTTPS). base_url of values-dev.yaml names 8080.
DEV_HTTP_PORT="${TROVE_DEV_HTTP_PORT:-8080}"

# One version per tool: every pin comes from tools/versions.env.
set -a
# shellcheck source=/dev/null
. "$REPO_ROOT/tools/versions.env"
set +a

log() { printf '%s %s\n' "$(date +%H:%M:%S)" "$*" >&2; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

# Every call names the context, so a kubeconfig that currently points at another cluster is
# never touched by accident.
k() { kubectl --context "$CTX" "$@"; }
kn() { kubectl --context "$CTX" -n "$NS" "$@"; }

os() { uname -s | tr '[:upper:]' '[:lower:]'; }
arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    arm64|aarch64) echo arm64 ;;
    *) uname -m ;;
  esac
}

# check_version <tool> <pinned> <running> <install command>
check_version() {
  local tool="$1" want="$2" got="$3" install="$4"
  if [ "$got" != "$want" ]; then
    printf 'error: %s %s is installed, tools/versions.env pins %s.\n  install it with:\n    %s\n' \
      "$tool" "${got:-<none>}" "$want" "$install" >&2
    return 1
  fi
}

# check_tools verifies kind, kubectl and helm against the pins and that Docker answers.
check_tools() {
  local failed=0 got

  command -v docker >/dev/null 2>&1 || { echo "error: docker is not installed (https://docs.docker.com/get-docker/)" >&2; failed=1; }
  if [ "$failed" = 0 ] && ! docker info >/dev/null 2>&1; then
    echo "error: the Docker daemon does not answer; start Docker and retry" >&2
    failed=1
  fi

  got="$(kind version 2>/dev/null | awk '{print $2}' || true)"
  check_version kind "$KIND_VERSION" "$got" \
    "go install sigs.k8s.io/kind@${KIND_VERSION}   # or: curl -Lo ./kind https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-$(os)-$(arch) && chmod +x ./kind && sudo mv ./kind /usr/local/bin/kind" || failed=1

  got="$(kubectl version --client -o yaml 2>/dev/null | awk '/gitVersion:/{print $2}' || true)"
  check_version kubectl "$KUBECTL_VERSION" "$got" \
    "curl -fsSLo kubectl https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/$(os)/$(arch)/kubectl && chmod +x kubectl && sudo mv kubectl /usr/local/bin/kubectl" || failed=1

  got="$(helm version --template '{{.Version}}' 2>/dev/null || true)"
  check_version helm "$HELM_VERSION" "$got" \
    "curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | DESIRED_VERSION=${HELM_VERSION} bash" || failed=1

  # The Valkey operator and the secret generator are published for linux/amd64 only (tools/versions.env).
  # On an arm64 host the Docker VM must be able to run amd64 binaries (qemu-user or Rosetta).
  if [ "$failed" = 0 ] && [ "$(arch)" = "arm64" ] \
     && ! docker run --rm -v /proc/sys/fs/binfmt_misc:/binfmt:ro "$INTEGRATION_TOOLS_IMAGE" sh -c 'ls /binfmt | grep -q -E "qemu-x86_64|rosetta"' >/dev/null 2>&1; then
    printf 'error: this host is arm64 and the Docker VM cannot run linux/amd64 binaries, which the Valkey operator and\n  the internal-secrets-operator images need (they are published for amd64 only). Register the emulator once:\n    docker run --privileged --rm %s --install amd64\n  (the registration lasts until the Docker VM restarts)\n' "$DEV_BINFMT_IMAGE" >&2
    failed=1
  fi

  [ "$failed" = 0 ] || exit 1
}

# require_cluster fails with a hint when the kind cluster is not there.
require_cluster() {
  kind get clusters 2>/dev/null | grep -qx "$CLUSTER" \
    || die "the kind cluster '$CLUSTER' does not exist; run deploy/dev/up.sh (make dev-up)"
}

# render <manifest> substitutes the pinned images, ${NS} and ${EDGE_PORT} (the only variables the
# manifests use) and prints the result.
render() {
  sed \
    -e "s|\${DEV_VALKEY_IMAGE}|${DEV_VALKEY_IMAGE}|g" \
    -e "s|\${DEV_SEAWEEDFS_IMAGE}|${DEV_SEAWEEDFS_IMAGE}|g" \
    -e "s|\${DEV_AWSCLI_IMAGE}|${DEV_AWSCLI_IMAGE}|g" \
    -e "s|\${DEV_CNPG_POSTGRES_IMAGE}|${DEV_CNPG_POSTGRES_IMAGE}|g" \
    -e "s|\${DEV_KEYCLOAK_IMAGE}|${DEV_KEYCLOAK_IMAGE}|g" \
    -e "s|\${NS}|${NS}|g" \
    -e "s|\${EDGE_PORT}|${EDGE_PORT:-$DEV_HTTP_PORT}|g" \
    "$1"
}

# sentinel_cli <pod> <command...> runs valkey-cli against the Sentinel in <pod> over TLS (port 36379, the
# Valkey operator's). The password is read inside the pod from the environment the operator sets from
# the Secret, never put on a command line of this machine.
sentinel_cli() {
  local pod="$1"; shift
  kn exec "$pod" -c sentinel -- sh -c \
    'exec valkey-cli --tls --cacert /tls/ca.crt -h 127.0.0.1 -p 36379 --no-auth-warning -a "$VALKEY_PASSWORD" "$@"' sh "$@"
}

# valkey_cli <pod> <command...> runs valkey-cli as the default user against the Valkey in <pod> (port 16379).
valkey_cli() {
  local pod="$1"; shift
  kn exec "$pod" -c valkey -- sh -c \
    'exec valkey-cli --tls --cacert /tls/ca.crt -h 127.0.0.1 -p 16379 --no-auth-warning -a "$VALKEY_PASSWORD" "$@"' sh "$@"
}

# valkey_primary prints the host name of the current primary as the first Sentinel that
# answers reports it (the FQDN the Valkey operator announces).
valkey_primary() {
  local i out
  for i in 0 1 2; do
    out="$(sentinel_cli "trove-valkey-sentinel-$i" sentinel get-master-addr-by-name trove-valkey 2>/dev/null | head -1 || true)"
    if [ -n "$out" ] && [ "$out" != "(nil)" ]; then
      printf '%s\n' "$out"
      return 0
    fi
  done
  return 1
}

# secret_field <secret> <key> prints the base64 value of a field, empty when the Secret or the field is missing.
secret_field() {
  kn get secret "$1" -o "go-template={{with index .data \"$2\"}}{{.}}{{end}}" 2>/dev/null || true
}

# wait_secret_fields <timeout-seconds> <secret:key>... waits until the internal-secrets-operator has filled
# every field.
wait_secret_fields() {
  local timeout="$1" start spec; shift
  start="$(date +%s)"
  for spec in "$@"; do
    until [ -n "$(secret_field "${spec%%:*}" "${spec#*:}")" ]; do
      [ $(( $(date +%s) - start )) -lt "$timeout" ] || { echo "Secret ${spec%%:*} has no field ${spec#*:} after ${timeout}s: is the internal-secrets-operator running?" >&2; return 1; }
      sleep 1
    done
  done
}

# sts_wait_ready <statefulset> <replicas> <timeout-seconds> waits until that many pods of the StatefulSet are
# Ready. `kubectl rollout status` does not work for the Valkey operator's StatefulSets (update strategy OnDelete).
sts_wait_ready() {
  local sts="$1" want="$2" timeout="$3" start got
  start="$(date +%s)"
  while :; do
    got="$(kn get statefulset "$sts" -o jsonpath='{.status.readyReplicas}' 2>/dev/null || true)"
    [ "${got:-0}" = "$want" ] && return 0
    [ $(( $(date +%s) - start )) -lt "$timeout" ] || { echo "statefulset $sts has ${got:-0} of $want pods ready after ${timeout}s" >&2; return 1; }
    sleep 1
  done
}

# elapsed <start-epoch> prints "Xm Ys".
elapsed() {
  local s=$(( $(date +%s) - $1 ))
  printf '%dm %ds' $((s / 60)) $((s % 60))
}

# pg_wait_healthy <timeout-seconds> waits until the CloudNativePG cluster trove-db has two ready
# instances and reports a healthy state, and prints the seconds it took.
pg_wait_healthy() {
  local timeout="$1" start ready phase
  start="$(date +%s)"
  while :; do
    ready="$(kn get cluster trove-db -o jsonpath='{.status.readyInstances}' 2>/dev/null || true)"
    phase="$(kn get cluster trove-db -o jsonpath='{.status.phase}' 2>/dev/null || true)"
    if [ "$ready" = "2" ] && [ "$phase" = "Cluster in healthy state" ]; then
      echo $(( $(date +%s) - start ))
      return 0
    fi
    [ $(( $(date +%s) - start )) -lt "$timeout" ] || { echo "trove-db is not healthy after ${timeout}s (ready '$ready', phase '$phase')" >&2; return 1; }
    sleep 1
  done
}
