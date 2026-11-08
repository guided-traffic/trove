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
#   drain.sh --also valkey|postgres|pooler [--also ...] <node>
#                          the drain of variants B, C and D of R-HA-059 (Q-HA-170): the release's pods and, in a run of its own started
#                          together with it, the pods of each named dependency on the node: valkey the Valkey data pods and the
#                          Sentinels, postgres the CloudNativePG instances of trove-db, pooler the pods of both poolers. Nothing else on
#                          the node is evicted. After the uncordon the script waits until the evicted dependency is whole again: two
#                          Valkey data pods and three Sentinels ready, two ready instances, both pooler Deployments rolled out.
#
# The test picks a worker node that hosts an api, a packages and a worker pod and not the CloudNativePG primary:
# CloudNativePG switches the primary over as soon as its node is cordoned (Test and Release run 42406831768). When only
# the primary's node hosts all three, `failover.sh` moves the primary first (variant B). The drain evicts only the pods
# of the trove release (--pod-selector): PostgreSQL, Valkey and the object store on the node keep running, as the
# acceptance test of R-HA-059 has them on other nodes. The three workers cannot always offer such a node, and evicting
# a PostgreSQL instance is not trove's drain: the synchronous standby of the profile (dataDurability preferred) that
# stays on the node after a switchover holds every commit until CloudNativePG drops it, about 6 s in run 42402849059,
# long enough for uploads queued on one row lock to meet the lock timeout.
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

# the selectors of the dependencies of --also (the labels the operators put on their pods; kubectl drain --pod-selector matches
# pods of every namespace, so the restore installation of trove-restore must not exist while a variant drains)
also_selector() {
  case "$1" in
    valkey) echo 'app.kubernetes.io/instance=trove-valkey' ;;
    postgres) echo 'cnpg.io/cluster=trove-db,cnpg.io/podRole=instance' ;;
    pooler) echo 'cnpg.io/poolerName' ;;
  esac
}
also=()
while [ "${1:-}" = "--also" ]; do
  [ -n "${2:-}" ] && [ -n "$(also_selector "$2")" ] || die "--also takes valkey, postgres or pooler, not '${2:-}'"
  also+=("$2"); shift 2
done
if [ "${#also[@]}" != 0 ]; then
  [ "$#" = 1 ] && [ -n "$1" ] && [ "${1#-}" = "$1" ] || die "usage: drain.sh [--also valkey|postgres|pooler]... <node>"
fi

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
# a failure, a signal or the caller's timeout never leaves the node cordoned or a drain running
drain_cleanup() {
  local p
  for p in $(jobs -p); do pkill -TERM -P "$p" 2>/dev/null || true; kill "$p" 2>/dev/null || true; done
  k uncordon "$node" >/dev/null 2>&1 || true
}
trap drain_cleanup EXIT
log "draining $node"
kn get pods -l "$sel" -o wide --field-selector "spec.nodeName=$node"
# only the release's pods and the dependencies --also names: the others on the node stay (see the header). The runs start together;
# each cordons the node, evicts its pods and retries what a PodDisruptionBudget refuses, as kubectl drain does.
drain_one() {
  local name="$1" selector="$2" start; start="$(date +%s)"
  log "drain of $name starts (--pod-selector $selector)"
  if k drain "$node" --pod-selector "$selector" --ignore-daemonsets --delete-emptydir-data --timeout=5m; then
    log "drain of $name ended after $(( $(date +%s) - start )) s"
  else
    log "drain of $name failed after $(( $(date +%s) - start )) s"
    return 1
  fi
}
pids=(); names=()
drain_one trove 'app.kubernetes.io/name=trove,app.kubernetes.io/instance=trove' & pids+=("$!"); names+=(trove)
for dep in ${also[@]+"${also[@]}"}; do
  drain_one "$dep" "$(also_selector "$dep")" & pids+=("$!"); names+=("$dep")
done
failed=0
for i in "${!pids[@]}"; do
  wait "${pids[$i]}" || { log "the drain of ${names[$i]} failed"; failed=1; }
done
[ "$failed" = 0 ] || die "a drain of $node failed"
# every Deployment of the roles, the worker pools of the profile in use included (vuln in the default profile, mirror in the `mirror` one)
for d in $(kn get deployments -l "app.kubernetes.io/instance=trove,app.kubernetes.io/component in (api,packages,worker)" -o name); do kn rollout status "$d" --timeout=5m; done
log "pods after the drain"
kn get pods -l "$sel" -o wide
k uncordon "$node"
# the evicted dependency comes back (its volume, where it has one, is bound to the node, so it could not before the uncordon)
for dep in ${also[@]+"${also[@]}"}; do
  case "$dep" in
    valkey)
      sts_wait_ready trove-valkey 2 300
      sts_wait_ready trove-valkey-sentinel 3 300 ;;
    postgres)
      deadline=$(( $(date +%s) + 600 ))
      until [ "$(kn get cluster trove-db -o jsonpath='{.status.readyInstances}')" = 2 ]; do
        [ "$(date +%s)" -lt "$deadline" ] || die "trove-db has no two ready instances 10 minutes after the uncordon"
        sleep 2
      done
      kn wait --for=condition=Ready cluster/trove-db --timeout=10m ;;
    pooler)
      for d in trove-db-pooler-rw trove-db-pooler-control; do kn rollout status "deployment/$d" --timeout=5m; done ;;
  esac
  log "$dep is whole again"
done
