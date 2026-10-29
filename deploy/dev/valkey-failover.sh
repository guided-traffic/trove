#!/usr/bin/env bash
# Valkey planned failover (R-HA-051 dev-valkey-failover): SENTINEL FAILOVER trove on one
# Sentinel, then waits until the Sentinels report another primary and the old primary is
# the replica.
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_cluster

before="$(valkey_primary)" || die "no Sentinel reports a primary"
log "primary before: $before"
sentinel_cli trove-valkey-sentinel-0 sentinel failover trove
deadline=$(( $(date +%s) + 60 ))
while :; do
  after="$(valkey_primary || true)"
  if [ -n "$after" ] && [ "$after" != "$before" ]; then
    break
  fi
  [ "$(date +%s)" -lt "$deadline" ] || die "the primary did not move within 60 s (still ${after:-unknown})"
  sleep 1
done
pod="${after%%.*}"
until [ "$(valkey_cli "$pod" role 2>/dev/null | head -1)" = "master" ]; do
  [ "$(date +%s)" -lt "$deadline" ] || die "$pod does not report role master"
  sleep 1
done
log "primary after:  $after"
