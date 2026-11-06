#!/usr/bin/env bash
# Stops the object store of the local profile until s3-start.sh runs (the object-store outage of R-HA-052 and the
# acceptance criteria of R-HA-010: the S3 store scaled to 0). The data directory is a PersistentVolumeClaim
# (s3.yaml), so the objects survive the outage: the metadata keeps naming blobs the store holds again after the
# return, as it does for a real store that is unreachable.
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_cluster

log "scaling the object store (trove-s3) to 0"
kn scale deployment/trove-s3 --replicas=0 >/dev/null
kn wait --for=delete pod -l app.kubernetes.io/name=trove-s3 --timeout=120s >/dev/null 2>&1 || true
log "the object store is stopped"
