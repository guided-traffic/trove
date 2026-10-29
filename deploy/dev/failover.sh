#!/usr/bin/env bash
# PostgreSQL switchover (R-HA-051 dev-failover): promotes the standby of the CloudNativePG
# cluster trove-db and waits until the old primary has rejoined as standby. This is what
# `kubectl cnpg promote trove-db <standby>` does, without needing the plugin: it sets
# status.targetPrimary, which the operator acts on.
#
#   failover.sh            promote the standby
#   failover.sh <instance> promote that instance (default: the first standby)
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_cluster

cluster=trove-db
current="$(kn get cluster "$cluster" -o jsonpath='{.status.currentPrimary}')"
target="${1:-}"
if [ -z "$target" ]; then
  target="$(kn get pods -l "cnpg.io/cluster=$cluster,cnpg.io/instanceRole=replica" -o jsonpath='{.items[0].metadata.name}')"
fi
[ -n "$target" ] || die "no standby found for $cluster"
[ "$target" != "$current" ] || die "$target is already the primary"

log "switchover $cluster: $current -> $target"
kn patch cluster "$cluster" --subresource=status --type=merge \
  -p "{\"status\":{\"targetPrimary\":\"$target\",\"targetPrimaryTimestamp\":\"$(date -u +%Y-%m-%dT%H:%M:%S.000000Z)\"}}" >/dev/null

deadline=$(( $(date +%s) + 180 ))
while :; do
  now="$(kn get cluster "$cluster" -o jsonpath='{.status.currentPrimary}')"
  ready="$(kn get cluster "$cluster" -o jsonpath='{.status.readyInstances}')"
  phase="$(kn get cluster "$cluster" -o jsonpath='{.status.phase}')"
  if [ "$now" = "$target" ] && [ "$ready" = "2" ] && [ "$phase" = "Cluster in healthy state" ]; then
    break
  fi
  [ "$(date +%s)" -lt "$deadline" ] || die "switchover did not finish in 3 minutes (primary $now, ready $ready, phase '$phase')"
  sleep 1
done
log "primary is now $target; $current is the standby"
