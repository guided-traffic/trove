#!/usr/bin/env bash
# Builds the four client images of the Debian e2e set (R-DEB-035, Q-DEB-27) into the local Docker: the static forwarder
# (test/e2e/clients/fwd, the one of the npm clients) for linux/<arch>, then the Dockerfile on each pinned base image.
# `make apt-clients-image` and deploy/dev/e2e.sh both call it.
#
#   build.sh [tag-prefix]    default trove-e2e-aptclients-: the images are <prefix>debian13:dev, debian12, ubuntu2404 and ubuntu2204
#
# Environment: TROVE_E2E_CLIENTS_ARCH (amd64|arm64, default the host's).
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../deploy/dev" && pwd)/lib.sh"

prefix="${1:-trove-e2e-aptclients-}"
arch="${TROVE_E2E_CLIENTS_ARCH:-$(arch)}"
for v in E2E_APTCLIENTS_DEBIAN13_IMAGE E2E_APTCLIENTS_DEBIAN12_IMAGE E2E_APTCLIENTS_UBUNTU2404_IMAGE E2E_APTCLIENTS_UBUNTU2204_IMAGE; do
  [ -n "${!v:-}" ] || die "$v is not set in tools/versions.env"
done
command -v docker >/dev/null 2>&1 || die "docker is required"

build="$(mktemp -d)"
trap 'rm -r "${build:?}"' EXIT
log "building the forwarder for linux/$arch"
(cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -o "$build/fwd-linux-$arch" ./test/e2e/clients/fwd)
dir="$REPO_ROOT/test/e2e/aptclients"
build_one() {
  local name="$1" base="$2"
  log "building ${prefix}${name}:dev on $base"
  docker buildx build --load --platform "linux/$arch" --build-arg BASE_IMAGE="$base" \
    --build-context "fwd=$build" -f "$dir/Dockerfile" -t "${prefix}${name}:dev" "$dir"
}
build_one debian13 "$E2E_APTCLIENTS_DEBIAN13_IMAGE"
build_one debian12 "$E2E_APTCLIENTS_DEBIAN12_IMAGE"
build_one ubuntu2404 "$E2E_APTCLIENTS_UBUNTU2404_IMAGE"
build_one ubuntu2204 "$E2E_APTCLIENTS_UBUNTU2204_IMAGE"
