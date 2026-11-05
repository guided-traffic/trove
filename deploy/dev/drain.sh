#!/usr/bin/env bash
# Node drain, variant A of R-HA-059 (R-HA-051 dev-drain, E0.14).
#
#   drain.sh               the whole test: the k6 load of E0.13, the driver's change and job traffic and
#                          probe jobs that persist a cursor every second run while a node with an api, a packages
#                          and a worker pod is drained; it fails on a failed request, a job that did not resume
#                          from its cursor with its attempt unchanged, a SIGKILL of a trove container or
#                          a trove pod Pending for more than 60 s. It is `e2e.sh drain`.
#   drain.sh <node>        the drain alone (this is what the test calls): `kubectl drain
#                          --ignore-daemonsets --delete-emptydir-data`, wait for the rollouts, uncordon.
#   drain.sh --pick        print the node the test would drain.
#
# The test picks a worker node that hosts an api, a packages and a worker pod and not the CloudNativePG primary,
# whose PodDisruptionBudget (minAvailable 1) refuses its eviction; when only the primary's node
# hosts both, `failover.sh` moves the primary first (variant B).
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_cluster

sel='app.kubernetes.io/instance=trove,trove/serve=true'

nodes_of() { kn get pods -l "$sel,app.kubernetes.io/component=$1" -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' | sort -u; }

pick_node() {
  local dbprimary
  dbprimary="$(kn get pod "$(kn get cluster trove-db -o jsonpath='{.status.currentPrimary}')" -o jsonpath='{.spec.nodeName}')"
  comm -12 <(comm -12 <(nodes_of api) <(nodes_of packages)) <(nodes_of worker) | grep -vx "$dbprimary" | head -1 || true
}

case "${1:-}" in
  "")
    exec "$DEV_DIR/e2e.sh" drain ;;
  --pick)
    node="$(pick_node)"
    [ -n "$node" ] || die "no node hosts an api, a packages and a worker pod apart from the PostgreSQL primary's"
    echo "$node"
    exit 0 ;;
  -*) die "unknown option $1" ;;
esac

node="$1"
log "draining $node"
kn get pods -l "$sel" -o wide --field-selector "spec.nodeName=$node"
# A CloudNativePG standby on this node stays Pending until the uncordon: its volume is local to
# the node. The synchronous standby is preferred, not required, so writes continue.
k drain "$node" --ignore-daemonsets --delete-emptydir-data --timeout=5m
# every Deployment of the roles, the worker pools of the profile in use included (vuln in the default profile, mirror in the `mirror` one)
for d in $(kn get deployments -l "app.kubernetes.io/instance=trove,app.kubernetes.io/component in (api,packages,worker)" -o name); do kn rollout status "$d" --timeout=5m; done
log "pods after the drain"
kn get pods -l "$sel" -o wide
k uncordon "$node"
