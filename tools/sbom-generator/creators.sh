#!/usr/bin/env bash
# Asserts that the SBOM attestations of an image name the pinned syft as their creator (Q-HA-166):
#
#   creators.sh image <ref>      an image in a registry (docker buildx imagetools inspect): linux/amd64 and linux/arm64
#   creators.sh oci <file.tar>   an image written with --output type=oci: two SPDX statements
#
# The creator string is "Tool: syft-<SYFT_VERSION without the v>". Exit 1 names what differs.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
set -a
# shellcheck source=tools/versions.env
. "$here/../versions.env"
set +a
want="Tool: syft-${SYFT_VERSION#v}"
command -v jq >/dev/null || { echo "error: jq is required" >&2; exit 1; }

case "${1:-}" in
  image)
    ref="${2:?usage: $0 image <ref>}"
    sbom="$(docker buildx imagetools inspect "$ref" --format '{{json .SBOM}}')"
    for p in linux/amd64 linux/arm64; do
      echo "$sbom" | jq -e --arg p "$p" --arg w "$want" '(.[$p].SPDX.creationInfo.creators // []) | index($w) != null' >/dev/null \
        || { echo "error: the SBOM attestation of $ref for $p does not name '$want': $(echo "$sbom" | jq -c --arg p "$p" '.[$p].SPDX.creationInfo.creators')" >&2; exit 1; }
    done
    echo "SBOM attestations of $ref name '$want' for linux/amd64 and linux/arm64"
    ;;
  oci)
    tar_file="${2:?usage: $0 oci <file.tar>}"
    dir="$(mktemp -d)"
    trap 'rm -rf "$dir"' EXIT
    tar -xf "$tar_file" -C "$dir"
    n=0
    for blob in "$dir"/blobs/sha256/*; do
      [ "$(wc -c < "$blob")" -lt 20000000 ] || continue
      jq -e '.predicateType == "https://spdx.dev/Document"' "$blob" >/dev/null 2>&1 || continue
      jq -e --arg w "$want" '(.predicate.creationInfo.creators // []) | index($w) != null' "$blob" >/dev/null \
        || { echo "error: an SBOM attestation does not name '$want': $(jq -c '.predicate.creationInfo.creators' "$blob")" >&2; exit 1; }
      n=$((n + 1))
    done
    [ "$n" = 2 ] || { echo "error: found $n SPDX attestations in $tar_file, want 2 (one per platform)" >&2; exit 1; }
    echo "both SBOM attestations in $tar_file name '$want'"
    ;;
  *)
    echo "usage: $0 image <ref> | oci <file.tar>" >&2
    exit 2
    ;;
esac
