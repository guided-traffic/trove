#!/usr/bin/env bash
# PostgreSQL at 0 instances for a time (R-HA-052 "PostgreSQL outage"): hibernates the CloudNativePG cluster trove-db
# (annotation cnpg.io/hibernation "on": the operator deletes every instance pod and keeps the volumes), waits until no
# instance pod is left, keeps the cluster down for the rest of the time, resumes it ("off") and waits until it is healthy
# again. The trove replicas must keep /healthz at 200, turn /readyz to 503 and back, and not restart. pg-stop.sh is the
# shorter outage that keeps the pods and stops PostgreSQL in them.
#
#   pg-hibernate.sh 2m    accepts seconds (120, 120s) or minutes (2m); default 2m; the time counts from the moment the last pod is gone
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_cluster

arg="${1:-2m}"
case "$arg" in
  *m) secs=$(( ${arg%m} * 60 )) ;;
  *s) secs="${arg%s}" ;;
  *) secs="$arg" ;;
esac
case "$secs" in ''|*[!0-9]*) die "duration '$arg' is not seconds (120, 120s) or minutes (2m)" ;; esac

# Whatever happens, the cluster is resumed again.
resume() { kn annotate cluster trove-db --overwrite cnpg.io/hibernation=off >/dev/null 2>&1 || true; }
trap resume EXIT

instances() { kn get pods -l cnpg.io/cluster=trove-db,cnpg.io/podRole=instance -o name 2>/dev/null | wc -l | tr -d ' '; }

log "hibernating trove-db"
kn annotate cluster trove-db --overwrite cnpg.io/hibernation=on >/dev/null
start="$(date +%s)"
until [ "$(instances)" = "0" ]; do
  [ $(( $(date +%s) - start )) -lt 180 ] || die "the instance pods of trove-db are still there 180 s after the hibernation was asked for"
  sleep 1
done
down="$(date +%s)"
log "trove-db has 0 instances after $(( down - start )) s"
sleep "$secs"
log "resuming trove-db after $(( $(date +%s) - down )) s at 0 instances"
resume
trap - EXIT
took="$(pg_wait_healthy 600)" || exit 1
log "trove-db healthy again $took s after the resume (0 instances for $(( $(date +%s) - down - took )) s)"
