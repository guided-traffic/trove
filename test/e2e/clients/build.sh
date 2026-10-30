#!/usr/bin/env bash
# Builds the clients image of the npm acceptance matrix (R-NPM-045) into the local Docker:
# the static forwarder (test/e2e/clients/fwd) for linux/<arch>, then test/e2e/clients/Dockerfile on the
# pinned Node image. `make npm-clients-image` and deploy/dev/e2e.sh both call it.
#
#   build.sh [image]    default image trove-e2e-clients:dev
#
# Environment: TROVE_E2E_CLIENTS_ARCH (amd64|arm64, default the host's).
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../deploy/dev" && pwd)/lib.sh"

image="${1:-trove-e2e-clients:dev}"
arch="${TROVE_E2E_CLIENTS_ARCH:-$(arch)}"
[ -n "${E2E_CLIENTS_NODE_IMAGE:-}" ] || die "E2E_CLIENTS_NODE_IMAGE is not set in tools/versions.env"
command -v docker >/dev/null 2>&1 || die "docker is required"

build="$(mktemp -d)"
trap 'rm -rf "$build"' EXIT
log "building the forwarder for linux/$arch"
(cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -o "$build/fwd-linux-$arch" ./test/e2e/clients/fwd)
log "building $image on $E2E_CLIENTS_NODE_IMAGE"
docker buildx build --load --platform "linux/$arch" \
  --build-arg NODE_IMAGE="$E2E_CLIENTS_NODE_IMAGE" --build-context "fwd=$build" \
  -f "$REPO_ROOT/test/e2e/clients/Dockerfile" -t "$image" "$REPO_ROOT/test/e2e/clients"
