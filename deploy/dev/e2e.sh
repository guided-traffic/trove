#!/usr/bin/env bash
# End-to-end tests of the dev cluster (WP0.11): E0.3, E0.4, E0.6, E0.7, E0.11, E0.12, E0.13, E0.14.
# The trove release must be installed (make dev-up). The suite is Go (test/e2e, build tag e2e); this
# script prepares what it needs and runs it:
#
#   1. builds the driver (test/e2e/cmd/driver) and loads its image into the cluster,
#   2. starts the driver pod next to the release (same ConfigMap, Secrets and security contexts as a
#      worker pod; it only writes changes and enqueues jobs, it claims nothing),
#   3. checks that the pinned k6 image is present,
#   4. runs `go test -tags e2e`, writes the machine-readable report to .out/test-e2e.json and the
#      artifacts (k6 logs and summaries, pod watch, events, driver output) to .out/e2e/.
#
#   e2e.sh              every test except the profile switch of E0.11 (the default profile)
#   e2e.sh push         the set CI runs on every change to main: E0.3, E0.4, E0.6, E0.7, E0.12, E0.14 and then E0.11
#   e2e.sh full         everything, in the order of the milestone, ending with E0.11
#   e2e.sh failover     E0.4, E0.6 and the E0.13 failover set (CI runs it nightly)
#   e2e.sh drain        E0.14 only (this is `make dev-drain`)
#   e2e.sh <regex>      any other argument is a `go test -run` expression
#
# Environment: TROVE_DEV_HTTP_PORT (the port the cluster was created with; read back from the node),
# TROVE_E2E_OUT (artifact directory, default .out/e2e), TROVE_E2E_TIMEOUT (go test timeout, default 90m),
# TROVE_E2E_DRIVER_BIN (a prebuilt linux driver binary instead of building one),
# TROVE_E2E_SCATTER_RETURN=0 (leave the cluster in the scatter profile after E0.11).
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_cluster
for tool in docker jq go helm kubectl; do command -v "$tool" >/dev/null 2>&1 || die "$tool is required"; done

set_name="${1:-default}"
case "$set_name" in
  default) run='^Test(E03|E04|E06|E07|E12|E13|E14)' ;;
  push) run='^Test(E03|E04|E06|E07|E12|E14|E99)' ;;
  full) run='^Test(E03|E04|E06|E07|E12|E13|E14|E99)' ;;
  failover) run='^Test(E04|E06|E13)' ;;
  drain) run='^TestE14' ;;
  *) run="$set_name" ;;
esac

EDGE_PORT="$(docker port "${CLUSTER}-control-plane" 30080/tcp 2>/dev/null | head -1 | sed 's/.*://' || true)"
EDGE_PORT="${EDGE_PORT:-$DEV_HTTP_PORT}"
OUT="${TROVE_E2E_OUT:-$REPO_ROOT/.out/e2e}"
mkdir -p "$OUT" "$REPO_ROOT/.out"

helm --kube-context "$CTX" -n "$NS" status trove >/dev/null 2>&1 || die "the trove release is not installed; run make dev-up"

# ── k6 ───────────────────────────────────────────────────────────────────────
[ "${K6_IMAGE#*:}" != "$K6_IMAGE" ] || die "K6_IMAGE is not set in tools/versions.env"
tag="${K6_IMAGE#*:}"; tag="${tag%%@*}"
[ "v$tag" = "$K6_VERSION" ] || die "K6_IMAGE tag $tag does not match K6_VERSION $K6_VERSION in tools/versions.env"
docker image inspect "$K6_IMAGE" >/dev/null 2>&1 || { log "pulling $K6_IMAGE"; docker pull "$K6_IMAGE" >/dev/null; }

# ── the driver ───────────────────────────────────────────────────────────────
ARCH="$(arch)"
DRIVER_IMAGE="trove-e2e-driver:dev"
BUILD="$REPO_ROOT/.out/e2e-driver"
rm -rf "$BUILD"; mkdir -p "$BUILD"
if [ -n "${TROVE_E2E_DRIVER_BIN:-}" ]; then
  log "using the prebuilt driver $TROVE_E2E_DRIVER_BIN"
  cp "$TROVE_E2E_DRIVER_BIN" "$BUILD/driver-linux-$ARCH"
else
  log "building the driver for linux/$ARCH"
  (cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -o "$BUILD/driver-linux-$ARCH" ./test/e2e/cmd/driver)
fi
docker buildx build --load --platform "linux/$ARCH" --build-arg RUNTIME_BASE_IMAGE="$RUNTIME_BASE_IMAGE" \
  -f "$REPO_ROOT/test/e2e/Dockerfile" -t "$DRIVER_IMAGE" "$BUILD" >"$OUT/driver-image.log" 2>&1 || { tail -20 "$OUT/driver-image.log" >&2; die "driver image build failed"; }
kind load docker-image "$DRIVER_IMAGE" --name "$CLUSTER" >"$OUT/driver-load.log" 2>&1 || { tail -20 "$OUT/driver-load.log" >&2; die "kind load failed"; }

# The pod is the worker pod's template with another command: the same ConfigMap and Secrets, the same
# security contexts, no probes and no ports. It carries the labels the Valkey NetworkPolicy admits, a
# component label no Service, PodDisruptionBudget or spread constraint selects, and runs on the control-
# plane node so a drain of a worker node never evicts it.
log "starting the driver pod"
kn delete pod trove-e2e-driver --ignore-not-found --wait=true >/dev/null
kn get deployment trove-worker -o json | jq --arg image "$DRIVER_IMAGE" --arg ns "$NS" '
  .spec.template as $t
  | { apiVersion: "v1", kind: "Pod",
      metadata: { name: "trove-e2e-driver", namespace: $ns,
                  labels: ($t.metadata.labels | .["app.kubernetes.io/component"] = "e2e-driver") },
      spec: ($t.spec
        | del(.topologySpreadConstraints)
        | .terminationGracePeriodSeconds = 5
        | .nodeSelector = {"node-role.kubernetes.io/control-plane": ""}
        | .tolerations = [{"key": "node-role.kubernetes.io/control-plane", "operator": "Exists", "effect": "NoSchedule"}]
        | .containers[0] |= (.image = $image | .imagePullPolicy = "IfNotPresent" | .args = ["idle"] | del(.ports, .startupProbe, .livenessProbe, .readinessProbe))) }' \
  | kn apply -f - >/dev/null
kn wait --for=condition=Ready pod/trove-e2e-driver --timeout=120s >/dev/null || { kn describe pod trove-e2e-driver >&2; die "the driver pod did not become ready"; }

# ── the tests ────────────────────────────────────────────────────────────────
# The edge scheme and the CA are parameters, so the run after the dev profile moves to TLS needs no
# rewrite: TROVE_DEV_SCHEME (http|https), TROVE_DEV_OPS_SCHEME (the scheme of the pods' :8081, default
# the edge's), TROVE_DEV_CA_FILE (a PEM bundle that signs the edge's certificate), TROVE_E2E_K6_TARGET
# (default <scheme>://<control-plane>:30080, the edge's NodePort on the kind Docker network),
# TROVE_E2E_K6_HOSTS_ADDR (host:port the edge's name resolves to inside the k6 container).
SCHEME="${TROVE_DEV_SCHEME:-http}"
OPS_SCHEME="${TROVE_DEV_OPS_SCHEME:-$SCHEME}"
export TROVE_E2E_REPO="$REPO_ROOT" TROVE_E2E_CONTEXT="$CTX" TROVE_E2E_NAMESPACE="$NS" \
  TROVE_E2E_EDGE_URL="$SCHEME://localhost:$EDGE_PORT" TROVE_E2E_K6_TARGET="${TROVE_E2E_K6_TARGET:-$SCHEME://${CLUSTER}-control-plane:30080}" \
  TROVE_E2E_K6_IMAGE="$K6_IMAGE" TROVE_E2E_OUT="$OUT" \
  TROVE_E2E_OPS_SCHEME="$OPS_SCHEME" TROVE_E2E_CA_FILE="${TROVE_DEV_CA_FILE:-}" TROVE_E2E_K6_HOSTS_ADDR="${TROVE_E2E_K6_HOSTS_ADDR:-}" \
  TROVE_E2E_PROBE_IMAGE="$DEV_VALKEY_IMAGE" TROVE_E2E_DEV_DIR="$DEV_DIR" TROVE_E2E_SCATTER_RETURN="${TROVE_E2E_SCATTER_RETURN:-1}" TROVE_DEV_HTTP_PORT="$EDGE_PORT"

log "go test -tags e2e -run '$run' (report: .out/test-e2e.json, artifacts: $OUT)"
cd "$REPO_ROOT"
set -o pipefail
if ! go test -count=1 -tags e2e -timeout "${TROVE_E2E_TIMEOUT:-90m}" -json -run "$run" ./test/e2e \
  | tee "$REPO_ROOT/.out/test-e2e.json" \
  | jq -r --unbuffered 'select(.Action=="output") | .Output' | { grep -v -E '^(=== |PASS$)' || true; }; then
  log "e2e FAILED (report: .out/test-e2e.json, artifacts: $OUT)"
  exit 1
fi
log "e2e passed"
