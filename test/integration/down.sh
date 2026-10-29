#!/usr/bin/env bash
# Stops the integration stack and removes its volumes and the generated env file.
# The certificates in .dev/integration/certs are kept.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"

set -a
# shellcheck disable=SC1091
. tools/versions.env
set +a
export VALKEY_IMAGE="$VALKEY_91_IMAGE" POSTGRES_IMAGE="$INTEGRATION_POSTGRES_IMAGE" PGBOUNCER_IMAGE="$INTEGRATION_PGBOUNCER_IMAGE" \
  SEAWEEDFS_IMAGE TOOLS_IMAGE="$INTEGRATION_TOOLS_IMAGE" CERT_DIR="$root/.dev/integration/certs"

docker compose -f test/integration/compose.yaml down -v --remove-orphans
rm -f .dev/integration/env .dev/integration/valkey-line
