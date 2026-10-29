#!/usr/bin/env bash
# Valkey outage (R-HA-051 dev-valkey-outage): Valkey and the Sentinels are gone for a given time, then
# back, and the script waits until a primary is known again.
#
#   valkey-outage.sh 60s     accepts seconds (60, 60s) or minutes (2m); default 60s
#
# The Valkey operator owns the StatefulSets and restores their replica count within seconds, so scaling
# them down alone is not an outage. The script therefore stops the operator first (its Deployment to 0:
# the operator is part of the outage, as when the whole namespace of a node pool is lost), scales the two
# StatefulSets to 0, waits, scales them back and starts the operator again. Nothing fights the operator:
# while it is off it has no opinion. The data set is gone afterwards (emptyDir), which costs cache warmth
# only (R-HA-056, INV-3).
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_cluster

arg="${1:-60s}"
case "$arg" in
  *m) secs=$(( ${arg%m} * 60 )) ;;
  *s) secs="${arg%s}" ;;
  *) secs="$arg" ;;
esac
case "$secs" in ''|*[!0-9]*) die "duration '$arg' is not seconds (60, 60s) or minutes (2m)" ;; esac

OPNS=valkey-operator-system
op() { kubectl --context "$CTX" -n "$OPNS" "$@"; }
# Whatever happens, the operator is started again.
restore() { op scale deployment/valkey-operator --replicas=1 >/dev/null 2>&1 || true; }
trap restore EXIT

log "stopping the Valkey operator, then scaling Valkey and the Sentinels to 0 for ${secs}s"
op scale deployment/valkey-operator --replicas=0 >/dev/null
op wait --for=delete pod -l app.kubernetes.io/component=operator --timeout=60s >/dev/null 2>&1 || true
kn scale statefulset/trove-valkey statefulset/trove-valkey-sentinel --replicas=0 >/dev/null
sleep "$secs"
log "scaling back (2 Valkey pods, 3 Sentinels) and starting the operator"
kn scale statefulset/trove-valkey --replicas=2 >/dev/null
kn scale statefulset/trove-valkey-sentinel --replicas=3 >/dev/null
restore
trap - EXIT
sts_wait_ready trove-valkey 2 300
sts_wait_ready trove-valkey-sentinel 3 300
op rollout status deployment/valkey-operator --timeout=2m >/dev/null
deadline=$(( $(date +%s) + 60 ))
until primary="$(valkey_primary 2>/dev/null)"; do
  [ "$(date +%s)" -lt "$deadline" ] || die "no Sentinel reports a primary 60 s after the return"
  sleep 1
done
log "Valkey is back, primary: $primary"
