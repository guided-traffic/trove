#!/usr/bin/env bash
# Ends the outage of valkey-stop.sh: scales the two StatefulSets back, starts the operator and waits until a
# Sentinel reports a primary again. Safe to run when Valkey is up.
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_cluster

OPNS=valkey-operator-system
op() { kubectl --context "$CTX" -n "$OPNS" "$@"; }

log "scaling back (2 Valkey pods, 3 Sentinels) and starting the operator"
kn scale statefulset/trove-valkey --replicas=2 >/dev/null
kn scale statefulset/trove-valkey-sentinel --replicas=3 >/dev/null
op scale deployment/valkey-operator --replicas=1 >/dev/null
sts_wait_ready trove-valkey 2 300
sts_wait_ready trove-valkey-sentinel 3 300
op rollout status deployment/valkey-operator --timeout=2m >/dev/null
deadline=$(( $(date +%s) + 60 ))
until primary="$(valkey_primary 2>/dev/null)"; do
  [ "$(date +%s)" -lt "$deadline" ] || die "no Sentinel reports a primary 60 s after the return"
  sleep 1
done
log "Valkey is back, primary: $primary"
