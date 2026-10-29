#!/usr/bin/env bash
# Node drain (R-HA-051 dev-drain, variant A of R-HA-059): picks the worker node that hosts both a
# web and a worker pod of the trove release and no CloudNativePG primary, drains it with
# `kubectl drain --ignore-daemonsets --delete-emptydir-data`, reports where the evicted pods
# went and uncordons the node again.
#
# This is the drain only. WP0.11 adds the parts that make it a test: the k6 load of E0.13
# (100 requests/s against /api/v1/system/ping and /api/v1/system/replicas), the test job that
# persists its cursor, the check that no trove container received SIGKILL and that no pod
# stays Pending for more than 60 s. Until then this script proves the PodDisruptionBudgets and
# the node-taint-aware spreading, not zero failed requests.
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_cluster

sel='app.kubernetes.io/instance=trove,trove/serve=true'
node="${1:-}"
if [ -z "$node" ]; then
  # A node that hosts a web and a worker pod.
  webs="$(kn get pods -l "$sel,app.kubernetes.io/component=web" -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' | sort -u)"
  workers="$(kn get pods -l "$sel,app.kubernetes.io/component=worker" -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' | sort -u)"
  # The primary's own PodDisruptionBudget (minAvailable 1) refuses its eviction; moving the
  # PostgreSQL primary is `failover.sh` first (R-HA-059 variant B).
  dbprimary="$(kn get pods -l 'cnpg.io/cluster=trove-db,cnpg.io/instanceRole=primary' -o jsonpath='{.items[0].spec.nodeName}')"
  node="$(comm -12 <(printf '%s\n' "$webs") <(printf '%s\n' "$workers") | grep -vx "$dbprimary" | head -1 || true)"
  [ -n "$node" ] || node="$(printf '%s\n' "$webs" | grep -vx "$dbprimary" | head -1 || true)"
fi
[ -n "$node" ] || die "no trove pods found; install the release first (unset TROVE_SKIP_RELEASE)"

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
