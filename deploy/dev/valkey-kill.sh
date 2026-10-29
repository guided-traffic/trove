#!/usr/bin/env bash
# Valkey primary loss (R-HA-051 dev-valkey-kill): force-deletes the pod of the current primary
# (no grace period) and waits until a primary answers again and both pods are healthy.
#
# Two outcomes are both correct, and the script says which one happened:
#   moved               the pod stayed away longer than down-after-milliseconds (5 s); the
#                       Sentinels promoted the replica and the old primary rejoined as replica.
#   restarted in place  the StatefulSet recreated the pod within 5 s, before any Sentinel
#                       declared it down. It came back empty and its replica resynchronised to
#                       an empty data set, which costs cache warmth only (R-HA-056, INV-3).
# After a failover the Sentinels refuse to start another for 2 x failover-timeout (120 s);
# a kill within that window waits for it.
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_cluster

before="$(valkey_primary)" || die "no Sentinel reports a primary"
pod="${before%%.*}"
log "force-deleting the primary pod $pod"
start="$(date +%s)"
kn delete pod "$pod" --grace-period=0 --force --wait=false >/dev/null 2>&1

deadline=$(( start + 240 ))
while :; do
  after="$(valkey_primary 2>/dev/null || true)"
  if [ -n "$after" ] && [ "$(valkey_cli "${after%%.*}" role 2>/dev/null | head -1 || true)" = "master" ]; then
    # Not the pod that was just deleted and has not restarted yet.
    if [ "$after" != "$before" ] || [ "$(( $(date +%s) - start ))" -ge 2 ]; then
      break
    fi
  fi
  [ "$(date +%s)" -lt "$deadline" ] || die "no primary answered within 240 s"
  sleep 1
done
if [ "$after" = "$before" ]; then
  log "primary restarted in place: $after, $(( $(date +%s) - start )) s after the kill"
else
  log "primary moved to $after, $(( $(date +%s) - start )) s after the kill"
fi

# Both pods ready and the replica linked to the primary.
kn wait --for=condition=Ready pod/trove-valkey-0 pod/trove-valkey-1 --timeout=3m >/dev/null
primary_pod="${after%%.*}"
replica_pod="trove-valkey-0"; [ "$primary_pod" = "trove-valkey-0" ] && replica_pod="trove-valkey-1"
deadline=$(( $(date +%s) + 120 ))
until [ "$(valkey_cli "$replica_pod" role | sed -n '1p;4p' | tr '\n' ' ')" = "slave connected " ]; do
  [ "$(date +%s)" -lt "$deadline" ] || die "$replica_pod is not a connected replica: $(valkey_cli "$replica_pod" role | tr '\n' ' ')"
  sleep 1
done
log "$replica_pod is a connected replica of $primary_pod"
