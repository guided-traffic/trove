#!/usr/bin/env bash
# Node drain, variant A of R-HA-059 (R-HA-051 dev-drain, E0.14).
#
#   drain.sh               the whole test: the k6 load of E0.13, the driver's change and job traffic and
#                          probe jobs that persist a cursor every second run while a node with a web and a
#                          worker pod is drained; it fails on a failed request, a job that did not resume
#                          from its cursor with its attempt unchanged, a SIGKILL of a trove container or
#                          a trove pod Pending for more than 60 s. It is `e2e.sh drain`.
#   drain.sh <node>        the drain alone (this is what the test calls): `kubectl drain
#                          --ignore-daemonsets --delete-emptydir-data`, wait for the rollouts, uncordon.
#   drain.sh --pick        print the node the test would drain.
#
# The test picks a worker node that hosts a web and a worker pod and not the CloudNativePG primary,
# whose PodDisruptionBudget (minAvailable 1) refuses its eviction; when only the primary's node
# hosts both, `failover.sh` moves the primary first (variant B).
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_cluster

sel='app.kubernetes.io/instance=trove,trove/serve=true'

pick_node() {
  local webs workers dbprimary
  webs="$(kn get pods -l "$sel,app.kubernetes.io/component=web" -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' | sort -u)"
  workers="$(kn get pods -l "$sel,app.kubernetes.io/component=worker" -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' | sort -u)"
  dbprimary="$(kn get pod "$(kn get cluster trove-db -o jsonpath='{.status.currentPrimary}')" -o jsonpath='{.spec.nodeName}')"
  comm -12 <(printf '%s\n' "$webs") <(printf '%s\n' "$workers") | grep -vx "$dbprimary" | head -1 || true
}

case "${1:-}" in
  "")
    exec "$DEV_DIR/e2e.sh" drain ;;
  --pick)
    node="$(pick_node)"
    [ -n "$node" ] || die "no node hosts a web and a worker pod apart from the PostgreSQL primary's"
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
kn rollout status deployment/trove-web --timeout=5m
kn rollout status deployment/trove-worker --timeout=5m
log "pods after the drain"
kn get pods -l "$sel" -o wide
k uncordon "$node"
