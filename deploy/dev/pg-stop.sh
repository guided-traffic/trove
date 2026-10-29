#!/usr/bin/env bash
# PostgreSQL stopped for a time (R-ARCH-024, E0.13): fences every instance of the CloudNativePG
# cluster trove-db (annotation cnpg.io/fencedInstances '["*"]'; the operator stops PostgreSQL in the
# pods and keeps the pods and their volumes), waits, removes the fence and waits until the cluster is
# healthy again. The trove replicas must keep /healthz at 200, turn /readyz to 503 and back, and
# not restart.
#
#   pg-stop.sh 30s     accepts seconds (30, 30s) or minutes (1m); default 30s
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_cluster

arg="${1:-30s}"
case "$arg" in
  *m) secs=$(( ${arg%m} * 60 )) ;;
  *s) secs="${arg%s}" ;;
  *) secs="$arg" ;;
esac
case "$secs" in ''|*[!0-9]*) die "duration '$arg' is not seconds (30, 30s) or minutes (1m)" ;; esac

# Whatever happens, the fence is removed again.
unfence() { kn annotate cluster trove-db cnpg.io/fencedInstances- >/dev/null 2>&1 || true; }
trap unfence EXIT

log "fencing every instance of trove-db for ${secs}s"
kn annotate cluster trove-db --overwrite 'cnpg.io/fencedInstances=["*"]' >/dev/null
# The fence takes effect when PostgreSQL has stopped in the primary's pod.
primary="$(kn get cluster trove-db -o jsonpath='{.status.currentPrimary}')"
start="$(date +%s)"
until ! kn exec "$primary" -c postgres -- pg_isready -q -h /controller/run >/dev/null 2>&1; do
  [ $(( $(date +%s) - start )) -lt 60 ] || die "PostgreSQL in $primary did not stop within 60 s"
  sleep 1
done
log "PostgreSQL stopped after $(( $(date +%s) - start )) s"
rest=$(( secs - ($(date +%s) - start) ))
[ "$rest" -le 0 ] || sleep "$rest"
log "removing the fence"
unfence
trap - EXIT
took="$(pg_wait_healthy 300)" || exit 1
log "trove-db healthy again $took s after the fence was removed (stopped for $(( $(date +%s) - start - took )) s)"
