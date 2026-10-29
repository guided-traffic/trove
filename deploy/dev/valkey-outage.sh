#!/usr/bin/env bash
# Valkey outage (R-HA-051 dev-valkey-outage): scales Valkey and the Sentinels to 0 for a given
# time, then back, and waits until a primary is known again.
#
#   valkey-outage.sh 60s     accepts seconds (60, 60s) or minutes (2m); default 60s
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

log "scaling Valkey and the Sentinels to 0 for ${secs}s"
kn scale statefulset/trove-valkey statefulset/trove-valkey-sentinel --replicas=0 >/dev/null
sleep "$secs"
log "scaling back (2 Valkey pods, 3 Sentinels)"
kn scale statefulset/trove-valkey --replicas=2 >/dev/null
kn scale statefulset/trove-valkey-sentinel --replicas=3 >/dev/null
kn rollout status statefulset/trove-valkey --timeout=5m
kn rollout status statefulset/trove-valkey-sentinel --timeout=5m
deadline=$(( $(date +%s) + 60 ))
until primary="$(valkey_primary 2>/dev/null)"; do
  [ "$(date +%s)" -lt "$deadline" ] || die "no Sentinel reports a primary 60 s after the return"
  sleep 1
done
log "Valkey is back, primary: $primary"
