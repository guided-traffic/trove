#!/usr/bin/env bash
# The check of the SBOM generator before a release (Q-HA-166; Test and Release, job sbom-generator): builds the generator
# at the pins, builds a two-platform test image with it as the SBOM generator, writes the image to a file (nothing is
# pushed to a public registry) and asserts that both attestations name the pinned syft. A syft bump that breaks the
# generator fails here, in a pull request or push run, and never in a release.
#
# Needs Docker with buildx and jq. Uses a docker network, a registry container and a buildx builder of its own, all
# removed at the end.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
set -a
# shellcheck source=tools/versions.env
. "$here/../versions.env"
set +a

export TROVE_SBOM_RUN="check-${GITHUB_RUN_ID:-$$}-${GITHUB_RUN_ATTEMPT:-0}"
builder="trove-sbom-builder-${TROVE_SBOM_RUN}"
tmp="$(mktemp -d)"
cleanup() {
  docker buildx rm -f "$builder" >/dev/null 2>&1 || true
  "$here/registry.sh" down || true
  rm -rf "$tmp"
}
trap cleanup EXIT

net="$("$here/registry.sh" network)"
"$here/registry.sh" buildkitd-config > "$tmp/buildkitd.toml"
docker buildx create --name "$builder" --driver docker-container --driver-opt "network=$net" \
  --buildkitd-config "$tmp/buildkitd.toml" --bootstrap >/dev/null
export BUILDX_BUILDER="$builder"

ref="$("$here/registry.sh" up)"
echo "generator: $ref"

# A file and a base image, no RUN: the image needs no emulation to build.
mkdir "$tmp/app"
printf 'FROM %s\nCOPY hello /hello\n' "$SBOM_GENERATOR_SHELL_IMAGE" > "$tmp/app/Containerfile"
echo hello > "$tmp/app/hello"
docker buildx build --platform linux/amd64,linux/arm64 --provenance=true --sbom="generator=$ref" \
  -f "$tmp/app/Containerfile" --output "type=oci,dest=$tmp/app.tar" "$tmp/app" >&2
"$here/creators.sh" oci "$tmp/app.tar"
