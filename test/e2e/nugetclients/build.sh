#!/usr/bin/env bash
# Builds the NuGet clients image of the acceptance suite (R-NUGET-027) into the local Docker: the static forwarder
# (test/e2e/clients/fwd, the one of the npm clients) for linux/<arch>, then test/e2e/nugetclients/Dockerfile on the
# pinned .NET SDK image with the pinned Mono and nuget.exe. `make nuget-clients-image` and deploy/dev/e2e.sh both
# call it.
#
#   build.sh [image]    default image trove-e2e-nugetclients:dev
#
# Environment: TROVE_E2E_CLIENTS_ARCH (amd64|arm64, default the host's).
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../deploy/dev" && pwd)/lib.sh"

image="${1:-trove-e2e-nugetclients:dev}"
arch="${TROVE_E2E_CLIENTS_ARCH:-$(arch)}"
for v in E2E_NUGETCLIENTS_SDK_IMAGE E2E_NUGETCLIENTS_SDK8_IMAGE E2E_NUGETCLIENTS_SDK9_IMAGE E2E_NUGETCLIENTS_MONO_VERSION E2E_NUGETCLIENTS_JQ_VERSION E2E_NUGETCLIENTS_NUGET_EXE_VERSION E2E_NUGETCLIENTS_NUGET_EXE_SHA256; do
  [ -n "${!v:-}" ] || die "$v is not set in tools/versions.env"
done
command -v docker >/dev/null 2>&1 || die "docker is required"

build="$(mktemp -d)"
trap 'rm -rf "$build"' EXIT
log "building the forwarder for linux/$arch"
(cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -o "$build/fwd-linux-$arch" ./test/e2e/clients/fwd)
log "building $image on $E2E_NUGETCLIENTS_SDK_IMAGE"
docker buildx build --load --platform "linux/$arch" \
  --build-arg SDK_IMAGE="$E2E_NUGETCLIENTS_SDK_IMAGE" --build-arg SDK8_IMAGE="$E2E_NUGETCLIENTS_SDK8_IMAGE" --build-arg SDK9_IMAGE="$E2E_NUGETCLIENTS_SDK9_IMAGE" \
  --build-arg MONO_VERSION="$E2E_NUGETCLIENTS_MONO_VERSION" --build-arg JQ_VERSION="$E2E_NUGETCLIENTS_JQ_VERSION" \
  --build-arg NUGET_EXE_VERSION="$E2E_NUGETCLIENTS_NUGET_EXE_VERSION" --build-arg NUGET_EXE_SHA256="$E2E_NUGETCLIENTS_NUGET_EXE_SHA256" \
  --build-context "fwd=$build" \
  -f "$REPO_ROOT/test/e2e/nugetclients/Dockerfile" -t "$image" "$REPO_ROOT/test/e2e/nugetclients"
