#!/usr/bin/env bash
# Scales the Valkey Sentinels to a number (R-HA-052 "Sentinel quorum loss"): the Valkey pods stay up and the primary stays the
# primary, so only the Sentinels' majority is lost. The Valkey operator restores the replica count of its StatefulSets within
# seconds, so the script stops the operator first (the operator is part of the fault, as in valkey-stop.sh) and waits until
# exactly that many Sentinel pods are left. valkey-start.sh ends the fault: it scales the Sentinels back to 3 and starts the operator.
#
#   sentinel-scale.sh 1     two of three Sentinels gone (the quorum of 2 is lost for every failover, the majority of 3 is not there)
#   sentinel-scale.sh 0     all of them gone
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_cluster

n="${1:-}"
case "$n" in 0|1|2|3) ;; *) die "usage: sentinel-scale.sh <0|1|2|3>" ;; esac
OPNS=valkey-operator-system

log "stopping the Valkey operator, then scaling the Sentinels to $n"
kubectl --context "$CTX" -n "$OPNS" scale deployment/valkey-operator --replicas=0 >/dev/null
kubectl --context "$CTX" -n "$OPNS" wait --for=delete pod -l app.kubernetes.io/component=operator --timeout=60s >/dev/null 2>&1 || true
kn scale statefulset/trove-valkey-sentinel --replicas="$n" >/dev/null
deadline=$(( $(date +%s) + 120 ))
while :; do
  left="$(kn get pods -o name 2>/dev/null | grep -c '^pod/trove-valkey-sentinel-[0-9]' || true)"
  [ "$left" = "$n" ] && break
  [ "$(date +%s)" -lt "$deadline" ] || die "$left Sentinel pods are left 120 s after the scale to $n"
  sleep 1
done
log "$n Sentinel pod(s) left"
