#!/usr/bin/env bash
# Fetches the archives of the dev profile's charts that are GitHub release assets (haproxy-ingress, cloudnative-pg) into
# .dev/charts, checked against their digests in tools/versions.env. The fetch takes the job's token when GITHUB_TOKEN is set
# (Q-ARCH-356), so CI runs it as its own step before make dev-up, and dev-up reads the archives from .dev/charts without a
# token. A missing archive is still fetched by dev-up itself (unauthenticated, locally).
#
# One budget covers both fetches (TROVE_DOWNLOAD_BUDGET, 900 s by default): each fetch gets the time left of it, so the two
# together stay within the budget, and the named error comes before the step's timeout.
set -euo pipefail

# shellcheck source=deploy/dev/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

budget="${TROVE_DOWNLOAD_BUDGET:-900}"
deadline=$(( SECONDS + budget ))

# fetch NAME FUNCTION: runs the chart fetch with the time left of the shared budget.
fetch() {
  local left=$(( deadline - SECONDS ))
  if [ "$left" -le 0 ]; then
    log "dev charts: the budget of $budget s is spent before the $1 archive"
    exit 1
  fi
  export TROVE_DOWNLOAD_BUDGET="$left"
  "$2" >/dev/null
}

log "dev charts: fetching the haproxy-ingress and cloudnative-pg archives and the Barman Cloud plugin manifest into $STATE_DIR/charts ($budget s in all)"
fetch haproxy-ingress haproxy_chart_tgz
fetch cloudnative-pg cnpg_chart_tgz
fetch barman-cloud-plugin barman_plugin_manifest
log "dev charts: the archives and the manifest are in place and match their digests"
