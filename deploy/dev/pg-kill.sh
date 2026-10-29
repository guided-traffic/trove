#!/usr/bin/env bash
# PostgreSQL primary loss (R-HA-051, R-HA-052, E0.13): force-deletes the pod of the current primary
# of the CloudNativePG cluster trove-db without a grace period, then waits until the operator has
# promoted the standby, recreated the lost instance as a standby and reports a healthy cluster.
# The replicas retry for database.retry.max_elapsed and answer 503 while they cannot reach
# a primary (R-HA-015); nothing here restarts them.
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_cluster

before="$(kn get cluster trove-db -o jsonpath='{.status.currentPrimary}')"
[ -n "$before" ] || die "trove-db has no primary"
log "force-deleting the primary pod $before"
start="$(date +%s)"
kn delete pod "$before" --grace-period=0 --force --wait=false >/dev/null 2>&1
pg_wait_healthy 300 >/dev/null || exit 1
after="$(kn get cluster trove-db -o jsonpath='{.status.currentPrimary}')"
log "healthy again after $(( $(date +%s) - start )) s; primary $before -> $after"
