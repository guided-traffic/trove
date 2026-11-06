#!/usr/bin/env bash
# Object-store outage (R-HA-051 dev-s3-outage): the S3 store of the local profile is gone for a given time, then
# back, and the script waits until its pod is ready again.
#
#   s3-outage.sh 60s     accepts seconds (60, 60s) or minutes (2m); default 60s
#
# s3-stop.sh and s3-start.sh do the work (the objects survive the outage, see there).
set -euo pipefail
DEV="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=deploy/dev/lib.sh
. "$DEV/lib.sh"
require_cluster

arg="${1:-60s}"
case "$arg" in
  *m) secs=$(( ${arg%m} * 60 )) ;;
  *s) secs="${arg%s}" ;;
  *) secs="$arg" ;;
esac
case "$secs" in ''|*[!0-9]*) die "duration '$arg' is not seconds (60, 60s) or minutes (2m)" ;; esac

# Whatever happens, the store is started again.
trap '"$DEV/s3-start.sh"' EXIT
"$DEV/s3-stop.sh"
log "The object store stays down for ${secs}s"
sleep "$secs"
"$DEV/s3-start.sh"
trap - EXIT
