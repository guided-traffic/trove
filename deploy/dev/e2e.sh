#!/usr/bin/env bash
# End-to-end tests of the dev cluster (WP0.11, WP1.x): E0.3, E0.4, E0.6, E0.7, E0.11 to E0.16 and, for the access core,
# E1.1, E1.8, E1.11 (API part), E1.13 and E1.14.
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
#   e2e.sh push         the set CI runs on every change: E0.3, E0.4, E0.6, E0.7, E0.12, E0.14 (with the upload and download
#                       loop of E1.13), E0.15, E0.16, the exposure of E1.14, then E0.11
#   e2e.sh full         everything, in the order of the milestone, ending with E0.11
#   e2e.sh failover     the E0.13 failover set and the limits of E1.8 (CI: nightly and on dispatch, HA and Performance)
#   e2e.sh access-long  the sign-in of E1.1, the replica scatter of E1.11, the 500 MB transfers of E1.13 (CI, every change)
#   e2e.sh access       the M1 tests alone: E1.1, E1.8, E1.11, E1.13, E1.14 (they switch to the scatter profile and back)
#   e2e.sh drain        E0.14 only (this is `make dev-drain`)
#   e2e.sh clients      the smoke test of the npm client pods (R-NPM-045: every gate row runs --version, the base URL
#                       reaches the cluster); builds the clients image and starts the pods, which stay up
#   e2e.sh clients-down removes the client pods
#   e2e.sh npm-ha       drain variant A and the failovers of R-HA-052 under the npm load (E2.11)
#   e2e.sh npm-ha-crash the pod crash of R-HA-052 under the npm load, three force-deletes (E2.11)
#   e2e.sh npm-ha-soak  the pod crash with ten force-deletes over 20 minutes, then the npm load scripts of R-ARCH-027
#                       (perf/k6/npm.js, reported); the nightly Performance workflow runs it. npm-ha-closing runs npm-ha,
#                       npm-ha-soak and the Valkey outage in one go
#   e2e.sh nuget-clients  the smoke test of the NuGet client pods (R-NUGET-027: dotnet and nuget.exe run, the base URL reaches the
#                       cluster with a verified certificate); builds the clients image and starts the pods, which stay up
#   e2e.sh nuget-clients-down  removes the NuGet client pods
#   e2e.sh nuget        the NuGet acceptance tests (every TestNuGet... except TestNuGetClosing... and TestNuGetHA...), the counting
#                       NuGet test upstream and three client pods; CI: job e2e-nuget
#   e2e.sh nuget-closing  TestNuGetClosing...: the additions of the run that closes the phase (CI: job e2e-nuget, closing=true)
#   e2e.sh nuget-ha     TestNuGetHA...: drain and failover under the NuGet load (CI: job e2e-nuget in the nightly HA and
#                       Performance workflow). A set may name no test (nuget-ha today): then it passes with a notice (Q-ARCH-276)
#   e2e.sh <regex>      any other argument is a `go test -run` expression
#
# Environment: TROVE_DEV_HTTP_PORT (the port the cluster was created with; read back from the node),
# TROVE_E2E_OUT (artifact directory, default .out/e2e), TROVE_E2E_TIMEOUT (go test timeout, default 90m),
# TROVE_E2E_DRIVER_BIN (a prebuilt linux driver binary instead of building one),
# TROVE_E2E_SCATTER_RETURN=0 (leave the cluster in the scatter profile after E0.11 and the M1 tests).
# The npm client pods (set clients): TROVE_E2E_NPM_CLIENTS_PODS (how many, default 1), TROVE_E2E_NPM_CLIENTS_TARGET
# (host:port the forwarder in each pod relays to, default the in-cluster HAProxy Ingress HTTPS port),
# TROVE_E2E_NPM_CLIENTS_IMAGE_BUILD=0 (use the image already built locally instead of rebuilding it).
# The NuGet client pods (sets nuget*): TROVE_E2E_NUGET_CLIENTS_PODS, TROVE_E2E_NUGET_CLIENTS_TARGET and
# TROVE_E2E_NUGET_CLIENTS_IMAGE_BUILD=0 as for npm; TROVE_E2E_NUGETSTUB=1 deploys the counting NuGet test upstream.
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
for tool in docker jq go helm kubectl; do command -v "$tool" >/dev/null 2>&1 || die "$tool is required"; done

set_name="${1:-default}"
case "$set_name" in
  default) run='^Test(E03|E04|E06|E07|E12|E13|E14|E15|E16)' ;;
  push) run='^Test(E03|E04|E06|E07|E12|E14|E15|E16|E1_14|E99)' ;;
  full) run='^Test(E03|E04|E06|E07|E12|E13|E14|E15|E16|E1_|E99)' ;;
  failover) run='^Test(E13|E1_08)' ;;
  access-long) run='^TestE1_(01|11|13)' ;;
  access) run='^TestE1_' ;;
  drain) run='^TestE14' ;;
  # >>> M2 npm-clients
  clients) run='^TestNPMClients$'; NPM_CLIENTS=1 ;;
  clients-down) NPM_CLIENTS=down ;;
  # The npm acceptance matrix and the criteria E2.1 to E2.12 (test/e2e/npm_*_test.go): the counting test upstream,
  # three client pods (one per web replica), the scatter profile. CI runs it as the job e2e-npm on every change.
  npm) run='^TestNPM(Clients|Matrix|Replicas|Collapse|Upstream|Endpoint|Project)'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 ;;
  # The additions of the run that closes the phase: the matrix again with Valkey stopped, the read-only subset against
  # the public registry. Started by the workflow only when it is dispatched with closing=true (Q-NPM-46).
  npm-closing) run='^TestNPMClosing'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 TROVE_E2E_NPM_CLOSING=1 ;;
  # <<< M2 npm-clients
  # >>> M3 nuget-clients
  # The NuGet client pods (R-NUGET-027, docs/developer/e2e.md#the-nuget-clients): the smoke test of the clients alone, and
  # the NuGet acceptance tests (test/e2e/nuget_*_test.go, every test named TestNuGet...) with the counting test upstream and
  # three client pods. The closing and nightly tests (TestNuGetClosing..., TestNuGetHA...) are sets of their own and are
  # skipped by `nuget` (Q-NUGET-34). CI runs `nuget` as the job e2e-nuget on every change.
  nuget-clients) run='^TestNuGetClients$'; NUGET_CLIENTS=1 ;;
  nuget-clients-down) NUGET_CLIENTS=down ;;
  nuget) run='^TestNuGet'; skip='^TestNuGet(Closing|HA)'; NUGET_CLIENTS=1; NUGET_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NUGETSTUB=1 ;;
  # empty_ok: the set may name no test yet (Q-ARCH-276); see the check after the case statement.
  nuget-closing) run='^TestNuGetClosing'; empty_ok=1; NUGET_CLIENTS=1; NUGET_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NUGETSTUB=1 TROVE_E2E_NUGET_CLOSING=1 ;;
  nuget-ha) run='^TestNuGetHA'; empty_ok=1; NUGET_CLIENTS=1; NUGET_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NUGETSTUB=1 ;;
  # <<< M3 nuget-clients
  # Drain and failover under npm load (E2.11, test/e2e/npm_ha_test.go): drain variant A of R-HA-059, the PostgreSQL
  # switchover and the Valkey failover of R-HA-052 under eight npm ci loops, a publish loop and k6 (CI: e2e-npm leg
  # npm-ha of the nightly HA and Performance workflow); the pod crash in a set of its own (npm-ha-crash, three force-deletes,
  # run by hand). The soak
  # (nightly) runs the pod crash with ten force-deletes, then perf/k6/npm.js against one replica (TestNPMPerf, reported,
  # not gated). npm-ha-closing runs all of it and adds the 5-minute Valkey outage of the full suite (closing run).
  npm-ha) run='^TestNPMHA$'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 ;;
  npm-ha-crash) run='^TestNPMHACrash$'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 ;;
  npm-ha-soak) run='^TestNPM(HACrash|Perf)$'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 TROVE_E2E_NPM_CRASH_SOAK=1 ;;
  npm-ha-closing) run='^TestNPM(HA|HACrash|HAClosing|Perf)$'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 TROVE_E2E_NPM_CLOSING=1 TROVE_E2E_NPM_CRASH_SOAK=1 ;;
  *) run="$set_name" ;;
esac

# TROVE_E2E_RUN replaces the expression of any set (a single subtest while developing: TROVE_E2E_RUN='^TestNPMMatrix/rows/R-NPM-045_E2.1_npm11').
run="${TROVE_E2E_RUN:-${run:-}}"
# A set may skip tests (go test -skip); TROVE_E2E_RUN names the tests itself, so it drops the skip.
skip="${skip:-}"
[ -z "${TROVE_E2E_RUN:-}" ] || skip=

# A set declared empty_ok (Q-ARCH-276) passes with a notice while its expression names no test in test/e2e. The names are
# read from the sources (func Test... in *_test.go): the check needs neither a cluster nor a compile, because TestMain of
# the package needs both. Once a test of the name exists, the set runs it and the rule below applies (a run that ran
# nothing fails).
if [ "${empty_ok:-}" = 1 ] && [ -z "${TROVE_E2E_RUN:-}" ]; then
  named="$(grep -h -o '^func Test[A-Za-z0-9_]*' "$REPO_ROOT"/test/e2e/*_test.go | sed 's/^func //' | grep -c -E "$run" || true)"
  if [ "$named" = 0 ]; then
    log "e2e set '$set_name' names no test yet ('$run' matches nothing in test/e2e): nothing to run (Q-ARCH-276)"
    exit 0
  fi
fi
require_cluster

EDGE_PORT="$(docker port "${CLUSTER}-control-plane" 30443/tcp 2>/dev/null | head -1 | sed 's/.*://' || true)"
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

# >>> M2 npm-stub
# ── the counting npm test upstream ───────────────────────────────────────────
# TROVE_E2E_NPMSTUB=1 builds the image of test/npmstub (make npm-stub-image), loads it into kind and (re)starts
# the stub of deploy/dev/npmstub.yaml: Service npmstub.<ns>.svc:443, TLS from the dev CA, control token in Secret
# trove-npmstub. The tests that need it read TROVE_E2E_NPMSTUB_HOST. Nothing here runs without the switch.
if [ "${TROVE_E2E_NPMSTUB:-0}" = "1" ]; then
  log "building the npm test upstream image for linux/$ARCH"
  (cd "$REPO_ROOT" && make --no-print-directory npm-stub-image IMAGE_ARCH="$ARCH") >"$OUT/npmstub-image.log" 2>&1 || { tail -20 "$OUT/npmstub-image.log" >&2; die "npm test upstream image build failed"; }
  kind load docker-image trove-e2e-npmstub:dev --name "$CLUSTER" >"$OUT/npmstub-load.log" 2>&1 || { tail -20 "$OUT/npmstub-load.log" >&2; die "kind load of the npm test upstream failed"; }
  log "starting the npm test upstream"
  render "$DEV_DIR/npmstub.yaml" | kn apply -f - >/dev/null
  kn wait --for=condition=Ready certificate/trove-npmstub-tls --timeout=3m >/dev/null
  kn rollout restart deployment/trove-npmstub >/dev/null   # same tag, new image content
  kn rollout status deployment/trove-npmstub --timeout=3m >/dev/null || { kn describe pod -l app.kubernetes.io/name=trove-npmstub >&2; die "the npm test upstream did not become ready"; }
  export TROVE_E2E_NPMSTUB_HOST="npmstub.$NS.svc"
fi
# <<< M2 npm-stub

# >>> M3 nuget-stub
# ── the counting NuGet test upstream ─────────────────────────────────────────
# TROVE_E2E_NUGETSTUB=1 builds the image of test/nugetstub (make nuget-stub-image), loads it into kind and (re)starts
# the stub of deploy/dev/nugetstub.yaml: Services nugetstub.<ns>.svc (index, registrations) and nugetstub-content.<ns>.svc
# (flat container, search) on 443 in front of one pod, TLS from the dev CA, control token in Secret trove-nugetstub. The
# tests read TROVE_E2E_NUGETSTUB_HOST (the API host; the upstream URL of a remote repository is
# https://$TROVE_E2E_NUGETSTUB_HOST/v3/index.json) and TROVE_E2E_NUGETSTUB_CONTENT_HOST (the second host, which the
# repository names in its extraHosts, R-NUGET-014). trove trusts the dev CA for upstreams through
# proxy.outbound.tls.ca_files (values-dev.yaml). Nothing here runs without the switch.
if [ "${TROVE_E2E_NUGETSTUB:-0}" = "1" ]; then
  log "building the NuGet test upstream image for linux/$ARCH"
  (cd "$REPO_ROOT" && make --no-print-directory nuget-stub-image IMAGE_ARCH="$ARCH") >"$OUT/nugetstub-image.log" 2>&1 || { tail -20 "$OUT/nugetstub-image.log" >&2; die "NuGet test upstream image build failed"; }
  kind load docker-image trove-e2e-nugetstub:dev --name "$CLUSTER" >"$OUT/nugetstub-load.log" 2>&1 || { tail -20 "$OUT/nugetstub-load.log" >&2; die "kind load of the NuGet test upstream failed"; }
  log "starting the NuGet test upstream"
  render "$DEV_DIR/nugetstub.yaml" | kn apply -f - >/dev/null
  kn wait --for=condition=Ready certificate/trove-nugetstub-tls --timeout=3m >/dev/null
  kn rollout restart deployment/trove-nugetstub >/dev/null   # same tag, new image content
  kn rollout status deployment/trove-nugetstub --timeout=3m >/dev/null || { kn describe pod -l app.kubernetes.io/name=trove-nugetstub >&2; die "the NuGet test upstream did not become ready"; }
  export TROVE_E2E_NUGETSTUB_HOST="nugetstub.$NS.svc" TROVE_E2E_NUGETSTUB_CONTENT_HOST="nugetstub-content.$NS.svc"
fi
# <<< M3 nuget-stub

# >>> M4 oidc-stub
# ── the GitLab-shaped OIDC issuer double ─────────────────────────────────────
# TROVE_E2E_OIDCSTUB=1 builds the image of test/oidcstub (make oidc-stub-image), loads it into kind and (re)starts the
# double of deploy/dev/oidcstub.yaml: Service oidcstub.<ns>.svc, port 443 for the issuer endpoints and 8444 for the
# control API (mint, rotate, faults, counters), TLS from the dev CA, control token in Secret trove-oidcstub. The tests
# read TROVE_E2E_OIDCSTUB_HOST (the issuer is https://$TROVE_E2E_OIDCSTUB_HOST) and TROVE_E2E_OIDCSTUB_CONTROL_URL.
# trove trusts the dev CA for outbound requests through proxy.outbound.tls.ca_files (values-dev.yaml). Nothing here
# runs without the switch.
if [ "${TROVE_E2E_OIDCSTUB:-0}" = "1" ]; then
  log "building the OIDC issuer double image for linux/$ARCH"
  (cd "$REPO_ROOT" && make --no-print-directory oidc-stub-image IMAGE_ARCH="$ARCH") >"$OUT/oidcstub-image.log" 2>&1 || { tail -20 "$OUT/oidcstub-image.log" >&2; die "OIDC issuer double image build failed"; }
  kind load docker-image trove-e2e-oidcstub:dev --name "$CLUSTER" >"$OUT/oidcstub-load.log" 2>&1 || { tail -20 "$OUT/oidcstub-load.log" >&2; die "kind load of the OIDC issuer double failed"; }
  log "starting the OIDC issuer double"
  render "$DEV_DIR/oidcstub.yaml" | kn apply -f - >/dev/null
  kn wait --for=condition=Ready certificate/trove-oidcstub-tls --timeout=3m >/dev/null
  kn rollout restart deployment/trove-oidcstub >/dev/null   # same tag, new image content
  kn rollout status deployment/trove-oidcstub --timeout=3m >/dev/null || { kn describe pod -l app.kubernetes.io/name=trove-oidcstub >&2; die "the OIDC issuer double did not become ready"; }
  export TROVE_E2E_OIDCSTUB_HOST="oidcstub.$NS.svc" TROVE_E2E_OIDCSTUB_CONTROL_URL="https://oidcstub.$NS.svc:8444"
fi
# <<< M4 oidc-stub

# ── the tests ────────────────────────────────────────────────────────────────
# The whole path is TLS (R-ARCH-035): the edge serves the dev CA's certificate for localhost, and the
# pods' :8081 is HTTPS too. The scheme and the CA stay parameters: TROVE_DEV_SCHEME (https|http),
# TROVE_DEV_OPS_SCHEME (the scheme of the pods' :8081, default https), TROVE_DEV_CA_FILE (a PEM bundle that
# signs the edge's certificate, default .dev/pki/ca.crt written by up.sh), TROVE_E2E_K6_TARGET (default
# https://localhost:30443, the edge's HTTPS NodePort; the name must be one the certificate carries) and
# TROVE_E2E_K6_HOSTS_ADDR (host:port that name resolves to inside the k6 container, default the control-plane
# node's address on the Docker network `kind`).
SCHEME="${TROVE_DEV_SCHEME:-https}"
OPS_SCHEME="${TROVE_DEV_OPS_SCHEME:-https}"
CA_FILE="${TROVE_DEV_CA_FILE:-}"
if [ "$SCHEME" = "https" ] || [ "$OPS_SCHEME" = "https" ]; then
  CA_FILE="${CA_FILE:-$STATE_DIR/pki/ca.crt}"
  [ -s "$CA_FILE" ] || die "the dev CA $CA_FILE is missing; run make dev-up"
fi
if [ "$SCHEME" = "https" ]; then
  node_ip="$(docker inspect -f '{{.NetworkSettings.Networks.kind.IPAddress}}' "${CLUSTER}-control-plane")"
  K6_TARGET="${TROVE_E2E_K6_TARGET:-https://localhost:30443}"
  K6_HOSTS_ADDR="${TROVE_E2E_K6_HOSTS_ADDR:-$node_ip:30443}"
else
  K6_TARGET="${TROVE_E2E_K6_TARGET:-http://${CLUSTER}-control-plane:30080}"
  K6_HOSTS_ADDR="${TROVE_E2E_K6_HOSTS_ADDR:-}"
fi
export TROVE_E2E_REPO="$REPO_ROOT" TROVE_E2E_CONTEXT="$CTX" TROVE_E2E_NAMESPACE="$NS" \
  TROVE_E2E_EDGE_URL="$SCHEME://localhost:$EDGE_PORT" TROVE_E2E_K6_TARGET="$K6_TARGET" \
  TROVE_E2E_K6_IMAGE="$K6_IMAGE" TROVE_E2E_OUT="$OUT" \
  TROVE_E2E_OPS_SCHEME="$OPS_SCHEME" TROVE_E2E_CA_FILE="$CA_FILE" TROVE_E2E_K6_HOSTS_ADDR="$K6_HOSTS_ADDR" \
  TROVE_E2E_OPENSSL_IMAGE="$OPENSSL_IMAGE" \
  TROVE_E2E_DEV_DIR="$DEV_DIR" TROVE_E2E_SCATTER_RETURN="${TROVE_E2E_SCATTER_RETURN:-1}" TROVE_DEV_HTTP_PORT="$EDGE_PORT"

# >>> M2 npm-clients
# The client pods of the npm acceptance matrix (R-NPM-045, docs/developer/e2e.md#the-npm-client-matrix). Each pod
# runs the forwarder fwd as its only process: it listens on 127.0.0.1:<edge port> inside the pod and relays to
# the in-cluster edge, so the base URL of the setup snippets (https://localhost:<port>) works unchanged and the
# certificate is verified for `localhost`. The dev CA is mounted from a ConfigMap, never baked into the image.
# The clients run through `kubectl exec <pod> -- <row> ...` (npm10 ... yarn1, see test/e2e/clients/bin).
NPM_CLIENTS_IMAGE="trove-e2e-clients:dev"
NPM_CLIENTS_LABEL="app.kubernetes.io/component=e2e-npm-client"
NPM_CLIENTS_PREFIX="trove-e2e-npm-"
NPM_CLIENTS_COUNT="${TROVE_E2E_NPM_CLIENTS_PODS:-${NPM_CLIENTS_DEFAULT_PODS:-1}}"

npm_clients_down() {
  kn delete pod -l "$NPM_CLIENTS_LABEL" --ignore-not-found --wait=true >/dev/null
  kn delete configmap trove-e2e-npm-ca --ignore-not-found >/dev/null
}

# npm_clients_exec <index> <command...>: what the tests do through kubectl, for a shell session.
npm_clients_exec() {
  local i="$1"; shift
  kn exec "${NPM_CLIENTS_PREFIX}${i}" -- "$@"
}

npm_clients_up() {
  local target="${TROVE_E2E_NPM_CLIENTS_TARGET:-haproxy-ingress.ingress-controller.svc.cluster.local:443}" i
  [ -n "${CA_FILE:-}" ] && [ -s "$CA_FILE" ] || die "the client pods verify the edge against the dev CA, which is missing (TROVE_DEV_CA_FILE, TROVE_DEV_SCHEME=https; run make dev-up)"
  if [ "${TROVE_E2E_NPM_CLIENTS_IMAGE_BUILD:-1}" != 0 ]; then
    "$REPO_ROOT/test/e2e/clients/build.sh" "$NPM_CLIENTS_IMAGE" >"$OUT/npm-clients-image.log" 2>&1 || { tail -20 "$OUT/npm-clients-image.log" >&2; die "clients image build failed"; }
  fi
  kind load docker-image "$NPM_CLIENTS_IMAGE" --name "$CLUSTER" >"$OUT/npm-clients-load.log" 2>&1 || { tail -20 "$OUT/npm-clients-load.log" >&2; die "kind load failed"; }
  npm_clients_down
  kn create configmap trove-e2e-npm-ca --from-file=ca.crt="$CA_FILE" >/dev/null
  log "starting $NPM_CLIENTS_COUNT client pod(s) forwarding localhost:$EDGE_PORT to $target"
  for i in $(seq 0 $((NPM_CLIENTS_COUNT - 1))); do
    jq -n --arg name "${NPM_CLIENTS_PREFIX}${i}" --arg ns "$NS" --arg image "$NPM_CLIENTS_IMAGE" \
      --arg listen "127.0.0.1:$EDGE_PORT" --arg target "$target" '
      { apiVersion: "v1", kind: "Pod",
        metadata: { name: $name, namespace: $ns, labels: { "app.kubernetes.io/name": "trove-e2e", "app.kubernetes.io/component": "e2e-npm-client" } },
        spec: {
          terminationGracePeriodSeconds: 5,
          nodeSelector: { "node-role.kubernetes.io/control-plane": "" },
          tolerations: [{ key: "node-role.kubernetes.io/control-plane", operator: "Exists", effect: "NoSchedule" }],
          automountServiceAccountToken: false,
          # the host of the customers endpoint (values-dev.yaml) is the forwarder too: E2.12 uses its registry URLs unchanged
          hostAliases: [{ ip: "127.0.0.1", hostnames: ["packages.trove.localhost"] }],
          securityContext: { runAsNonRoot: true, runAsUser: 1000, runAsGroup: 1000, seccompProfile: { type: "RuntimeDefault" } },
          containers: [{
            name: "clients", image: $image, imagePullPolicy: "IfNotPresent",
            args: ["-listen", $listen, "-to", $target, "-target-file", "/tmp/target"],
            env: [{ name: "NODE_EXTRA_CA_CERTS", value: "/etc/trove-ca/ca.crt" }],
            securityContext: { allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: { drop: ["ALL"] } },
            volumeMounts: [
              { name: "ca", mountPath: "/etc/trove-ca", readOnly: true },
              { name: "work", mountPath: "/work" },
              { name: "tmp", mountPath: "/tmp" } ] }],
          volumes: [
            { name: "ca", configMap: { name: "trove-e2e-npm-ca" } },
            { name: "work", emptyDir: { sizeLimit: "2Gi" } },
            { name: "tmp", emptyDir: { sizeLimit: "512Mi" } } ] } }' \
      | kn apply -f - >/dev/null
  done
  kn wait --for=condition=Ready pod -l "$NPM_CLIENTS_LABEL" --timeout=120s >/dev/null || { kn describe pod -l "$NPM_CLIENTS_LABEL" >&2; die "the client pods did not become ready"; }
}

if [ "${NPM_CLIENTS:-}" = down ]; then npm_clients_down; log "client pods removed"; exit 0; fi
if [ "${NPM_CLIENTS:-}" = 1 ]; then
  npm_clients_up
  export TROVE_E2E_NPM_CLIENTS_POD_PREFIX="$NPM_CLIENTS_PREFIX" TROVE_E2E_NPM_CLIENTS_PODS="$NPM_CLIENTS_COUNT"
fi
# <<< M2 npm-clients

# >>> M3 nuget-clients
# The client pods of the NuGet acceptance tests (R-NUGET-027, docs/developer/e2e.md#the-nuget-clients), built like the npm
# ones: fwd is PID 1 behind entrypoint.sh, which first builds the trust of .NET and Mono from the mounted dev CA (the
# bundle at SSL_CERT_FILE, the Mono store below HOME) and then marks the pod ready. The clients run through
# `kubectl exec <pod> -- dotnet|nuget ...`; every test sets NUGET_PACKAGES and NUGET_HTTP_CACHE_PATH below /work.
NUGET_CLIENTS_IMAGE="trove-e2e-nugetclients:dev"
NUGET_CLIENTS_LABEL="app.kubernetes.io/component=e2e-nuget-client"
NUGET_CLIENTS_PREFIX="trove-e2e-nuget-"
NUGET_CLIENTS_COUNT="${TROVE_E2E_NUGET_CLIENTS_PODS:-${NUGET_CLIENTS_DEFAULT_PODS:-1}}"

nuget_clients_down() {
  kn delete pod -l "$NUGET_CLIENTS_LABEL" --ignore-not-found --wait=true >/dev/null
  kn delete configmap trove-e2e-nuget-ca --ignore-not-found >/dev/null
}

nuget_clients_up() {
  local target="${TROVE_E2E_NUGET_CLIENTS_TARGET:-haproxy-ingress.ingress-controller.svc.cluster.local:443}" i
  [ -n "${CA_FILE:-}" ] && [ -s "$CA_FILE" ] || die "the client pods verify the edge against the dev CA, which is missing (TROVE_DEV_CA_FILE, TROVE_DEV_SCHEME=https; run make dev-up)"
  if [ "${TROVE_E2E_NUGET_CLIENTS_IMAGE_BUILD:-1}" != 0 ]; then
    "$REPO_ROOT/test/e2e/nugetclients/build.sh" "$NUGET_CLIENTS_IMAGE" >"$OUT/nuget-clients-image.log" 2>&1 || { tail -20 "$OUT/nuget-clients-image.log" >&2; die "NuGet clients image build failed"; }
  fi
  kind load docker-image "$NUGET_CLIENTS_IMAGE" --name "$CLUSTER" >"$OUT/nuget-clients-load.log" 2>&1 || { tail -20 "$OUT/nuget-clients-load.log" >&2; die "kind load failed"; }
  nuget_clients_down
  kn create configmap trove-e2e-nuget-ca --from-file=ca.crt="$CA_FILE" >/dev/null
  log "starting $NUGET_CLIENTS_COUNT NuGet client pod(s) forwarding localhost:$EDGE_PORT to $target"
  for i in $(seq 0 $((NUGET_CLIENTS_COUNT - 1))); do
    jq -n --arg name "${NUGET_CLIENTS_PREFIX}${i}" --arg ns "$NS" --arg image "$NUGET_CLIENTS_IMAGE" \
      --arg listen "127.0.0.1:$EDGE_PORT" --arg target "$target" '
      { apiVersion: "v1", kind: "Pod",
        metadata: { name: $name, namespace: $ns, labels: { "app.kubernetes.io/name": "trove-e2e", "app.kubernetes.io/component": "e2e-nuget-client" } },
        spec: {
          terminationGracePeriodSeconds: 5,
          nodeSelector: { "node-role.kubernetes.io/control-plane": "" },
          tolerations: [{ key: "node-role.kubernetes.io/control-plane", operator: "Exists", effect: "NoSchedule" }],
          automountServiceAccountToken: false,
          # the host of the customers endpoint (values-dev.yaml) is the forwarder too: R-NUGET-034 uses its registry URLs unchanged
          hostAliases: [{ ip: "127.0.0.1", hostnames: ["packages.trove.localhost"] }],
          securityContext: { runAsNonRoot: true, runAsUser: 1000, runAsGroup: 1000, seccompProfile: { type: "RuntimeDefault" } },
          containers: [{
            name: "clients", image: $image, imagePullPolicy: "IfNotPresent",
            args: ["-listen", $listen, "-to", $target, "-target-file", "/tmp/target"],
            env: [{ name: "TROVE_CA_FILE", value: "/etc/trove-ca/ca.crt" }, { name: "SSL_CERT_FILE", value: "/tmp/trove-ca/bundle.crt" }],
            readinessProbe: { exec: { command: ["test", "-f", "/tmp/trove-ca/ready"] }, periodSeconds: 2 },
            securityContext: { allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: { drop: ["ALL"] } },
            volumeMounts: [
              { name: "ca", mountPath: "/etc/trove-ca", readOnly: true },
              { name: "work", mountPath: "/work" },
              { name: "tmp", mountPath: "/tmp" } ] }],
          volumes: [
            { name: "ca", configMap: { name: "trove-e2e-nuget-ca" } },
            { name: "work", emptyDir: { sizeLimit: "4Gi" } },
            { name: "tmp", emptyDir: { sizeLimit: "512Mi" } } ] } }' \
      | kn apply -f - >/dev/null
  done
  kn wait --for=condition=Ready pod -l "$NUGET_CLIENTS_LABEL" --timeout=180s >/dev/null || { kn describe pod -l "$NUGET_CLIENTS_LABEL" >&2; die "the NuGet client pods did not become ready"; }
}

if [ "${NUGET_CLIENTS:-}" = down ]; then nuget_clients_down; log "NuGet client pods removed"; exit 0; fi
if [ "${NUGET_CLIENTS:-}" = 1 ]; then
  nuget_clients_up
  export TROVE_E2E_NUGET_CLIENTS_POD_PREFIX="$NUGET_CLIENTS_PREFIX" TROVE_E2E_NUGET_CLIENTS_PODS="$NUGET_CLIENTS_COUNT"
fi
# <<< M3 nuget-clients

log "go test -tags e2e -run '$run' (report: .out/test-e2e.json, artifacts: $OUT)"
cd "$REPO_ROOT"
set -o pipefail
if ! go test -count=1 -tags e2e -timeout "${TROVE_E2E_TIMEOUT:-90m}" -json -run "$run" ${skip:+-skip "$skip"} ./test/e2e \
  | tee "$REPO_ROOT/.out/test-e2e.json" \
  | jq -r --unbuffered 'select(.Action=="output") | .Output' | { grep -v -E '^(=== |PASS$)' || true; }; then
  log "e2e FAILED (report: .out/test-e2e.json, artifacts: $OUT)"
  exit 1
fi
# A -run expression that matches nothing is a pass for `go test`; for this suite it is a mistake.
ran="$(jq -s '[.[] | select(.Action == "pass" and .Test != null)] | length' "$REPO_ROOT/.out/test-e2e.json")"
[ "$ran" -gt 0 ] || { log "e2e FAILED: the expression '$run' matched no test"; exit 1; }
log "e2e passed ($ran tests and subtests)"
