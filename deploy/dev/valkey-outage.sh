#!/usr/bin/env bash
# Valkey outage (R-HA-051 dev-valkey-outage): Valkey and the Sentinels are gone for a given time, then
# back, and the script waits until a primary is known again.
#
#   valkey-outage.sh 60s     accepts seconds (60, 60s) or minutes (2m); default 60s
#
# valkey-stop.sh and valkey-start.sh do the work (the operator is part of the outage, see there).
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

# Whatever happens, Valkey is started again.
trap '"$DEV/valkey-start.sh"' EXIT
"$DEV/valkey-stop.sh"
log "Valkey stays down for ${secs}s"
sleep "$secs"
"$DEV/valkey-start.sh"
trap - EXIT
