#!/usr/bin/env bash
# Regenerates internal/formats/nuget/version/testdata/vectors.json with the pinned .NET SDK
# image and the pinned NuGet.Versioning version (tools/versions.env keys NUGETVECTORS_SDK_IMAGE
# and NUGET_VERSIONING_VERSION). DOTNET=local runs a local `dotnet` instead of the container.
# Usage: generate.sh [out-file]
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
out="${1:-$root/internal/formats/nuget/version/testdata/vectors.json}"

# Values already in the environment win, so a pin can be tried before it is committed.
env_sdk="${NUGETVECTORS_SDK_IMAGE:-}"
env_ver="${NUGET_VERSIONING_VERSION:-}"
if [ -f "$root/tools/versions.env" ]; then
  set -a
  # shellcheck disable=SC1091
  . "$root/tools/versions.env"
  set +a
fi
NUGETVECTORS_SDK_IMAGE="${env_sdk:-${NUGETVECTORS_SDK_IMAGE:-}}"
NUGET_VERSIONING_VERSION="${env_ver:-${NUGET_VERSIONING_VERSION:-}}"
: "${NUGETVECTORS_SDK_IMAGE:?pin NUGETVECTORS_SDK_IMAGE in tools/versions.env}"
: "${NUGET_VERSIONING_VERSION:?pin NUGET_VERSIONING_VERSION in tools/versions.env}"

mkdir -p "$(dirname "$out")"
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

if [ "${DOTNET:-container}" = "local" ]; then
  dotnet run --project "$root/tools/nugetvectors" -c Release -v q \
    -p:NuGetVersioningVersion="$NUGET_VERSIONING_VERSION" >"$tmp"
else
  # The source is mounted read-only and copied inside, so no obj/bin lands in the checkout.
  docker run --rm -v "$root/tools/nugetvectors:/src:ro" \
    -e NUGET_VERSIONING_VERSION="$NUGET_VERSIONING_VERSION" \
    -e DOTNET_CLI_TELEMETRY_OPTOUT=1 -e DOTNET_NOLOGO=1 \
    "$NUGETVECTORS_SDK_IMAGE" sh -c \
    'cp -r /src /work && cd /work && dotnet build -c Release -v q -nologo -o /out -p:NuGetVersioningVersion="$NUGET_VERSIONING_VERSION" >&2 && dotnet /out/nugetvectors.dll' >"$tmp"
fi
chmod 644 "$tmp"
mv "$tmp" "$out"
trap - EXIT
echo "wrote $out" >&2
