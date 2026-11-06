#!/usr/bin/env bash
# Ends the outage of s3-stop.sh: scales the object store back to 1, waits until its pod is ready and then until it
# stores and returns an object. The pod's readiness is a TCP check on the S3 port, which the gateway passes before the
# volume server has registered with the master; the check here writes, reads and deletes a file through the filer
# (curl in the store's own container, on the loopback), which needs the same volume the S3 gateway writes to. Safe to run
# when the store is up.
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_cluster

log "scaling the object store (trove-s3) to 1"
kn scale deployment/trove-s3 --replicas=1 >/dev/null
kn rollout status deployment/trove-s3 --timeout=5m >/dev/null || die "the object store did not become ready"
deadline=$(( $(date +%s) + 120 ))
# the quoted command runs in the store's container: its $( ) is the container shell's
# shellcheck disable=SC2016
until kn exec deployment/trove-s3 -c seaweedfs -- sh -c \
  'curl -sf -X PUT --data-binary ready http://127.0.0.1:8888/probe/s3-start >/dev/null && [ "$(curl -sf http://127.0.0.1:8888/probe/s3-start)" = ready ] && curl -sf -X DELETE http://127.0.0.1:8888/probe/s3-start >/dev/null' \
  >/dev/null 2>&1; do
  [ "$(date +%s)" -lt "$deadline" ] || die "the object store is ready but stores nothing 120 s later"
  sleep 1
done
log "the object store is back"
