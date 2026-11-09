#!/usr/bin/env bash
# One syft for the release image (Q-HA-166): the SPDX file of the release and the SBOM attestation in the image index
# both come from SYFT_VERSION of tools/versions.env. `make versions-check` runs this; it exits 1 and names each place that
# could bring a second version in.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cf="$root/tools/sbom-generator/Containerfile"
wf="$root/.github/workflows/build.yml"
fail=0
bad() { printf 'syft: %s\n' "$*"; fail=1; }

# SYFT_IMAGE is SYFT_VERSION by digest.
pins="$root/tools/versions.env"
version="$(sed -n 's/^SYFT_VERSION=//p' "$pins")"
image="$(sed -n 's/^SYFT_IMAGE=//p' "$pins")"
if [[ "$image" =~ ^docker\.io/anchore/syft:([^@]+)@sha256:[0-9a-f]{64}$ ]]; then
  [ "${BASH_REMATCH[1]}" = "$version" ] || bad "SYFT_IMAGE has the tag ${BASH_REMATCH[1]}, SYFT_VERSION is $version"
else
  bad "SYFT_IMAGE must be docker.io/anchore/syft:<SYFT_VERSION>@sha256:<digest>, not '$image'"
fi

# The generator takes the image as a build argument without a default; no other file names a syft image.
grep -qE '^ARG SYFT_IMAGE$' "$cf" || bad "$cf must declare 'ARG SYFT_IMAGE' without a default"
grep -qE '^FROM \$\{SYFT_IMAGE\} AS syft$' "$cf" || bad "$cf must take syft from 'FROM \${SYFT_IMAGE}'"
if grep -rnE '^[^#]*anchore/syft' "$root/Containerfile" "$root/Makefile" "$root/.github" "$root/deploy" "$root/test" "$root/tools" \
    --include='*' --exclude=versions.env --exclude=check-pins.sh --exclude-dir=node_modules; then
  bad "the files above name a syft image of their own: take SYFT_IMAGE from tools/versions.env"
fi

# The SPDX file runs the syft image of the pin (SYFT_IMAGE, Q-HA-204) on the registry tag of the release, and the attestation comes from the
# generator, not from BuildKit's own scanner. No step downloads syft from GitHub (F-120).
grep -qE '^\s+docker run --rm "\$\{SYFT_IMAGE\}" "registry:guidedtraffic/trove:\$\{VERSION\}" -o spdx-json > ' "$wf" || bad "$wf: the SPDX step must run the image SYFT_IMAGE on the release tag"
grep -qE '^\s+sbom: generator=\$\{\{ steps\.[a-z_]+\.outputs\.[a-z_]+ \}\}$' "$wf" || bad "$wf: the build step must set sbom to the generator of tools/sbom-generator"
if grep -nE 'buildkit-syft-scanner|syft-version:|anchore/sbom-action|anchore/syft/releases|^\s+sbom: (true|false)$' "$wf"; then
  bad "$wf: BuildKit's bundled scanner, a syft download or a literal syft version is in use"
fi

# A syft on PATH must be the pinned one.
if command -v syft >/dev/null 2>&1; then
  want="${SYFT_VERSION:-$(sed -n 's/^SYFT_VERSION=//p' "$root/tools/versions.env")}"
  have="v$(syft version | awk '$1=="Version:"{print $2}')"
  [ "$have" = "$want" ] || bad "installed $have, pin is $want"
fi

exit "$fail"
