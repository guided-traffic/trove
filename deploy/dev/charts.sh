#!/usr/bin/env bash
# Fetches the archives of the dev profile's charts that are GitHub release assets (haproxy-ingress, cloudnative-pg) into
# .dev/charts, checked against their digests in tools/versions.env. The fetch takes the job's token when GITHUB_TOKEN is set
# (Q-ARCH-356), so CI runs it as its own step before make dev-up, and dev-up reads the archives from .dev/charts without a
# token. A missing archive is still fetched by dev-up itself (unauthenticated, locally).
set -euo pipefail

# shellcheck source=deploy/dev/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

log "dev charts: fetching the haproxy-ingress and cloudnative-pg archives into $STATE_DIR/charts"
haproxy_chart_tgz >/dev/null
cnpg_chart_tgz >/dev/null
log "dev charts: both archives are in place and match their digests"
