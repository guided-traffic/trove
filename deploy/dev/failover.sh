#!/usr/bin/env bash
# PostgreSQL switchover (R-HA-051 dev-failover): promotes a standby of the CloudNativePG cluster trove-db and waits
# until the old primary has rejoined as standby and the read-write service routes to the new primary. This is what
# `kubectl cnpg promote trove-db <standby>` does, without needing the plugin: it sets status.targetPrimary, which the
# instance managers act on and the operator follows (the status, and the pod label that trove-db-rw selects).
#
# Before it asks, it waits for what the switchover needs and names the precondition that is not met (F-65, Q-HA-126):
#   1. the operator runs: a ready pod, not being deleted, holds the operator's leader lease;
#   2. the cluster is settled: no switchover or failover in flight (status.targetPrimary = status.currentPrimary), every
#      instance ready, the phase "Cluster in healthy state". A cordon of the primary's node makes the operator switch the
#      primary away by itself, and a request that races that switchover names the old primary as its target;
#   3. the target is a ready standby on a schedulable node: the operator switches a primary off a cordoned node again.
# The request is a JSON patch that tests the primary it read, so it is refused instead of overriding a switchover that
# started in between.
#
#   failover.sh            promote a ready standby on a schedulable node
#   failover.sh <instance> promote that instance
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_cluster

cluster=trove-db
operator_ns=cnpg-system
operator_lease=db9c8771.cnpg.io # the leader-election ID of the CloudNativePG operator
settle_seconds=180

# cluster_status prints currentPrimary|targetPrimary|readyInstances|instances|phase (empty fields when the read fails).
cluster_status() {
  kn get cluster "$cluster" \
    -o jsonpath='{.status.currentPrimary}|{.status.targetPrimary}|{.status.readyInstances}|{.spec.instances}|{.status.phase}' 2>/dev/null || true
}

# operator_state prints why the operator cannot follow a switchover, or nothing when it can.
operator_state() {
  local holder pod state
  holder="$(k -n "$operator_ns" get lease "$operator_lease" -o jsonpath='{.spec.holderIdentity}' 2>/dev/null || true)"
  pod="${holder%%_*}"
  if [ -z "$pod" ]; then
    echo "no pod holds the lease $operator_ns/$operator_lease"
    return
  fi
  state="$(k -n "$operator_ns" get pod "$pod" \
    -o jsonpath='{.metadata.deletionTimestamp}|{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo gone)"
  case "$state" in
    "|True") ;;
    gone) echo "the lease holder $pod does not exist" ;;
    "|"*) echo "the lease holder $pod is not ready" ;;
    *) echo "the lease holder $pod is being deleted" ;;
  esac
}

# cluster_state prints why the cluster is not settled, or nothing when it is.
cluster_state() {
  local cur tgt ready want phase
  IFS='|' read -r cur tgt ready want phase <<<"$(cluster_status)"
  if [ -z "$cur" ]; then
    echo "the cluster has no current primary (or could not be read)"
  elif [ "$cur" != "$tgt" ]; then
    echo "a switchover is in flight (current primary $cur, target $tgt, phase '$phase')"
  elif [ "$ready" != "$want" ]; then
    echo "${ready:-0} of $want instances are ready (phase '$phase')"
  elif [ "$phase" != "Cluster in healthy state" ]; then
    echo "the phase is '$phase'"
  fi
}

# wait_until <what> <state function>: waits until the function prints nothing; dies with what it printed last after the bound.
wait_until() {
  local what="$1" fn="$2" state deadline
  deadline=$(( $(date +%s) + settle_seconds ))
  while :; do
    state="$("$fn")"
    [ -n "$state" ] || return 0
    [ "$(date +%s)" -lt "$deadline" ] || die "precondition not met within ${settle_seconds}s: $what: $state"
    sleep 1
  done
}

# standby_problem <instance> prints why the instance cannot be the target, or nothing when it can.
standby_problem() {
  local inst="$1" info role ready deleting node unschedulable
  info="$(kn get pod "$inst" \
    -o jsonpath='{.metadata.labels.cnpg\.io/instanceRole}|{.status.conditions[?(@.type=="Ready")].status}|{.metadata.deletionTimestamp}|{.spec.nodeName}' 2>/dev/null || true)"
  if [ -z "$info" ]; then
    echo "$inst does not exist"
    return
  fi
  IFS='|' read -r role ready deleting node <<<"$info"
  unschedulable="$(k get node "$node" -o jsonpath='{.spec.unschedulable}' 2>/dev/null || true)"
  if [ "$role" != "replica" ]; then
    echo "$inst is not a standby (role '$role')"
  elif [ "$ready" != "True" ] || [ -n "$deleting" ]; then
    echo "$inst is not ready"
  elif [ "$unschedulable" = "true" ]; then
    echo "$inst runs on the cordoned node $node, off which the operator switches a primary again"
  fi
}

wait_until "the CloudNativePG operator runs" operator_state
wait_until "the cluster $cluster is settled" cluster_state

current="$(kn get cluster "$cluster" -o jsonpath='{.status.currentPrimary}')"
target="${1:-}"
if [ -z "$target" ]; then
  reasons=""
  for inst in $(kn get pods -l "cnpg.io/cluster=$cluster,cnpg.io/instanceRole=replica" -o jsonpath='{.items[*].metadata.name}'); do
    problem="$(standby_problem "$inst")"
    if [ -z "$problem" ]; then
      target="$inst"
      break
    fi
    reasons="${reasons:+$reasons; }$problem"
  done
  [ -n "$target" ] || die "no standby of $cluster can take over: ${reasons:-the cluster has no standby}"
else
  [ "$target" != "$current" ] || die "$target is already the primary"
  problem="$(standby_problem "$target")"
  [ -z "$problem" ] || die "$problem"
fi

log "switchover $cluster: $current -> $target"
patch="[
  {\"op\":\"test\",\"path\":\"/status/currentPrimary\",\"value\":\"$current\"},
  {\"op\":\"test\",\"path\":\"/status/targetPrimary\",\"value\":\"$current\"},
  {\"op\":\"add\",\"path\":\"/status/targetPrimary\",\"value\":\"$target\"},
  {\"op\":\"add\",\"path\":\"/status/targetPrimaryTimestamp\",\"value\":\"$(date -u +%Y-%m-%dT%H:%M:%S.000000Z)\"}
]"
if ! out="$(kn patch cluster "$cluster" --subresource=status --type=json -p "$patch" 2>&1)"; then
  die "the switchover request was refused (the primary read was $current; a test operation fails when it changed since): $out"
fi

deadline=$(( $(date +%s) + 180 ))
while :; do
  IFS='|' read -r now tgt ready want phase <<<"$(cluster_status)"
  role="$(kn get pod "$target" -o jsonpath='{.metadata.labels.cnpg\.io/instanceRole}' 2>/dev/null || true)"
  if [ "$now" = "$target" ] && [ "$tgt" = "$target" ] && [ "$ready" = "$want" ] &&
    [ "$phase" = "Cluster in healthy state" ] && [ "$role" = "primary" ]; then
    break
  fi
  [ "$(date +%s)" -lt "$deadline" ] ||
    die "switchover did not finish in 3 minutes (primary $now, target $tgt, ready ${ready:-0} of $want, phase '$phase', $target labelled '$role')"
  sleep 1
done
log "primary is now $target; $current is the standby"
