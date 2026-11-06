#!/usr/bin/env bash
# Stops the backing services of the UI development profile. The data volumes are kept unless --purge is
# given; --purge also removes the generated state in .dev/ui-dev (certificates, secrets, key ring).
# docs/developer/web-ui.md#local-ui-development
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"

# compose.yaml requires the variables up.sh sets; the values do not matter for down.
export POSTGRES_IMAGE=unused VALKEY_IMAGE=unused SEAWEEDFS_IMAGE=unused CERT_DIR="$root/.dev/ui-dev/certs"

if [ "${1:-}" = "--purge" ]; then
  docker compose -f deploy/ui-dev/compose.yaml down -v --remove-orphans
  rm -rf "$root/.dev/ui-dev"
  echo "UI development services removed with their data and .dev/ui-dev"
else
  docker compose -f deploy/ui-dev/compose.yaml down --remove-orphans
  echo "UI development services stopped; the data is kept (deploy/ui-dev/down.sh --purge removes it)"
fi
