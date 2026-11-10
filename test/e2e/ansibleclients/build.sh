#!/usr/bin/env bash
# Builds the two client images of the Ansible e2e set (R-ANS-032, Q-ANS-23, Q-ANS-49) into the local Docker: the static forwarder
# (test/e2e/clients/fwd, the one of the npm clients) for linux/<arch>, then the Dockerfile for each pinned ansible-core release on its
# pinned Python image. `make ansible-clients-image` and deploy/dev/e2e.sh both call it.
#
#   build.sh [tag-prefix]    default trove-e2e-ansibleclients-: the images are <prefix>core217:dev and <prefix>core221:dev
#
# Environment: TROVE_E2E_CLIENTS_ARCH (amd64|arm64, default the host's).
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../deploy/dev" && pwd)/lib.sh"

prefix="${1:-trove-e2e-ansibleclients-}"
arch="${TROVE_E2E_CLIENTS_ARCH:-$(arch)}"
for v in E2E_ANSIBLECLIENTS_CORE217_IMAGE E2E_ANSIBLECLIENTS_CORE217_VERSION E2E_ANSIBLECLIENTS_CORE221_IMAGE E2E_ANSIBLECLIENTS_CORE221_VERSION; do
  [ -n "${!v:-}" ] || die "$v is not set in tools/versions.env"
done
command -v docker >/dev/null 2>&1 || die "docker is required"

build="$(mktemp -d)"
trap 'rm -r "${build:?}"' EXIT
log "building the forwarder for linux/$arch"
(cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -o "$build/fwd-linux-$arch" ./test/e2e/clients/fwd)
dir="$REPO_ROOT/test/e2e/ansibleclients"
build_one() {
  local name="$1" series="$2" base="$3" version="$4"
  log "building ${prefix}${name}:dev: ansible-core $version on $base"
  docker buildx build --load --platform "linux/$arch" --build-arg BASE_IMAGE="$base" --build-arg ANSIBLE_CORE_VERSION="$version" \
    --build-arg ANSIBLE_SERIES="$series" --build-context "fwd=$build" -f "$dir/Dockerfile" -t "${prefix}${name}:dev" "$dir"
}
build_one core217 2.17 "$E2E_ANSIBLECLIENTS_CORE217_IMAGE" "$E2E_ANSIBLECLIENTS_CORE217_VERSION"
build_one core221 2.21 "$E2E_ANSIBLECLIENTS_CORE221_IMAGE" "$E2E_ANSIBLECLIENTS_CORE221_VERSION"
