#!/usr/bin/env bash
# Deletes the kind cluster "trove" and the local state (.dev/). Other kind clusters are not
# touched.
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

if command -v kind >/dev/null 2>&1 && kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  log "deleting the kind cluster '$CLUSTER'"
  kind delete cluster --name "$CLUSTER"
else
  log "no kind cluster '$CLUSTER'"
fi
if [ -d "$STATE_DIR" ]; then
  log "removing $STATE_DIR (dev CA, certificates, logs)"
  rm -rf "$STATE_DIR"
fi
