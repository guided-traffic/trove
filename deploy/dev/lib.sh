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
# The host port of the dev edge (Traefik). base_url of values-dev.yaml names 8080.
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

  [ "$failed" = 0 ] || exit 1
}

# require_cluster fails with a hint when the kind cluster is not there.
require_cluster() {
  kind get clusters 2>/dev/null | grep -qx "$CLUSTER" \
    || die "the kind cluster '$CLUSTER' does not exist; run deploy/dev/up.sh (make dev-up)"
}

# render <manifest> substitutes the pinned images and the namespace (the only variables the
# manifests use) and prints the result.
render() {
  sed \
    -e "s|\${DEV_VALKEY_IMAGE}|${DEV_VALKEY_IMAGE}|g" \
    -e "s|\${DEV_SEAWEEDFS_IMAGE}|${DEV_SEAWEEDFS_IMAGE}|g" \
    -e "s|\${DEV_AWSCLI_IMAGE}|${DEV_AWSCLI_IMAGE}|g" \
    -e "s|\${DEV_CNPG_POSTGRES_IMAGE}|${DEV_CNPG_POSTGRES_IMAGE}|g" \
    "$1"
}

# sentinel_cli <pod> <command...> runs valkey-cli against the Sentinel in <pod> over TLS.
# The password is read inside the pod from the mounted Secret, never put on a command line
# of this machine.
sentinel_cli() {
  local pod="$1"; shift
  kn exec "$pod" -c sentinel -- sh -c \
    'exec valkey-cli --tls --cacert /tls/ca.crt -h 127.0.0.1 -p 26379 --no-auth-warning -a "$(cat /secrets/sentinel/password)" "$@"' sh "$@"
}

# valkey_cli <pod> <command...> runs valkey-cli as the admin user against the Valkey in <pod>.
valkey_cli() {
  local pod="$1"; shift
  kn exec "$pod" -c valkey -- sh -c \
    'exec valkey-cli --tls --cacert /tls/ca.crt -h 127.0.0.1 -p 6379 --no-auth-warning -a "$(cat /secrets/admin/password)" "$@"' sh "$@"
}

# valkey_primary prints the host name of the current primary as the first Sentinel that
# answers reports it.
valkey_primary() {
  local i out
  for i in 0 1 2; do
    out="$(sentinel_cli "trove-valkey-sentinel-$i" sentinel get-master-addr-by-name trove 2>/dev/null | head -1 || true)"
    if [ -n "$out" ] && [ "$out" != "(nil)" ]; then
      printf '%s\n' "$out"
      return 0
    fi
  done
  return 1
}

# openssl runs the pinned OPENSSL_IMAGE in the current directory, as the calling user so the
# files it writes belong to the caller (Linux). Arguments are openssl's own; paths must be
# relative to the current directory.
openssl_pinned() {
  docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:$PWD" -w "$PWD" "$OPENSSL_IMAGE" "$@"
}

# elapsed <start-epoch> prints "Xm Ys".
elapsed() {
  local s=$(( $(date +%s) - $1 ))
  printf '%dm %ds' $((s / 60)) $((s % 60))
}
