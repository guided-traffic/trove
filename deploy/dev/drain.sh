#!/usr/bin/env bash
# Node drain, variant A of R-HA-059 (R-HA-051 dev-drain, E0.14).
#
#   drain.sh               the whole test: the k6 load of E0.13, the driver's change and job traffic and
#                          probe jobs that persist a cursor every second run while a node with an api, a packages
#                          and a worker pod is drained; it fails on a failed request, a job that did not resume
#                          from its cursor with its attempt unchanged, a SIGKILL of a trove container or
#                          a trove pod Pending for more than 60 s. It is `e2e.sh drain`.
#   drain.sh <node>        the drain alone (this is what the test calls): `kubectl drain
#                          --ignore-daemonsets --delete-emptydir-data` of trove's pods, wait for the rollouts, uncordon.
#   drain.sh --pick        print the node the test would drain.
#
# The test picks a worker node that hosts an api, a packages and a worker pod. The drain evicts only the pods of the
# trove release (--pod-selector): PostgreSQL, Valkey and the object store on the node keep running, as the acceptance
# test of R-HA-059 has them on other nodes. The three workers cannot always offer such a node, and evicting a
# PostgreSQL instance is not trove's drain: the synchronous standby of the profile (dataDurability preferred) holds
# every commit until CloudNativePG drops it, about 6 s in Test and Release run 42402849059, long enough for uploads
# queued on one row lock to meet the lock timeout.
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_cluster

sel='app.kubernetes.io/instance=trove,trove/serve=true'

nodes_of() { kn get pods -l "$sel,app.kubernetes.io/component=$1" -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' | sort -u; }

pick_node() {
  comm -12 <(comm -12 <(nodes_of api) <(nodes_of packages)) <(nodes_of worker) | head -1 || true
}

case "${1:-}" in
  "")
    exec "$DEV_DIR/e2e.sh" drain ;;
  --pick)
    node="$(pick_node)"
    [ -n "$node" ] || die "no node hosts an api, a packages and a worker pod"
    echo "$node"
    exit 0 ;;
  -*) die "unknown option $1" ;;
esac

node="$1"
log "draining $node"
kn get pods -l "$sel" -o wide --field-selector "spec.nodeName=$node"
# only the release's pods: the dependencies on the node stay (see the header)
k drain "$node" --pod-selector 'app.kubernetes.io/name=trove,app.kubernetes.io/instance=trove' --ignore-daemonsets --delete-emptydir-data --timeout=5m
# every Deployment of the roles, the worker pools of the profile in use included (vuln in the default profile, mirror in the `mirror` one)
for d in $(kn get deployments -l "app.kubernetes.io/instance=trove,app.kubernetes.io/component in (api,packages,worker)" -o name); do kn rollout status "$d" --timeout=5m; done
log "pods after the drain"
kn get pods -l "$sel" -o wide
k uncordon "$node"
