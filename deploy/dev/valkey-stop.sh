#!/usr/bin/env bash
# Stops Valkey and the Sentinels until valkey-start.sh runs (the e2e tests of the "Valkey stopped" criteria, E1.8,
# E1.11 and E1.14, hold the outage for as long as they need and start it again in their cleanup).
#
# The Valkey operator owns the StatefulSets and restores their replica count within seconds, so scaling them down
# alone is not an outage. The script therefore stops the operator first (its Deployment to 0: the operator is part
# of the outage), then scales the two StatefulSets to 0 and waits until their pods are gone. The data set is gone
# afterwards (emptyDir), which costs cache warmth only (R-HA-056, INV-3).
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_cluster

OPNS=valkey-operator-system
op() { kubectl --context "$CTX" -n "$OPNS" "$@"; }

log "stopping the Valkey operator, then scaling Valkey and the Sentinels to 0"
op scale deployment/valkey-operator --replicas=0 >/dev/null
op wait --for=delete pod -l app.kubernetes.io/component=operator --timeout=60s >/dev/null 2>&1 || true
kn scale statefulset/trove-valkey statefulset/trove-valkey-sentinel --replicas=0 >/dev/null
kn wait --for=delete pod -l app.kubernetes.io/instance=trove-valkey,app.kubernetes.io/managed-by=vko.gtrfc.com --timeout=120s >/dev/null 2>&1 || true
log "Valkey is stopped"
