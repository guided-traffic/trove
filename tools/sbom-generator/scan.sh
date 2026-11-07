# shellcheck shell=sh
# The scanner protocol of BuildKit, as docker/buildkit-syft-scanner implements it: BUILDKIT_SCAN_SOURCE is the root
# file system of the build result, BUILDKIT_SCAN_SOURCE_EXTRAS a directory of further ones (optional), and
# BUILDKIT_SCAN_DESTINATION the directory that receives one <name>.spdx.json per scanned file system. BuildKit reads each
# file as an in-toto statement with the predicate type https://spdx.dev/Document, so the SPDX document syft writes is
# wrapped in one here. Run by the busybox shell of this image, which has no applets on its PATH: every tool is
# called as busybox <applet>.
set -eu

bb=/bin/busybox
: "${BUILDKIT_SCAN_SOURCE:?BUILDKIT_SCAN_SOURCE is not set}"
: "${BUILDKIT_SCAN_DESTINATION:?BUILDKIT_SCAN_DESTINATION is not set}"

# syft needs a temporary directory, which the empty root file system of this image does not have.
$bb mkdir -p /tmp

scan() {
  name="${1##*/}"
  doc="/tmp/$name.spdx.json"
  /syft scan "dir:$1" --source-name "$name" -o "spdx-json=$doc"
  {
    printf '%s' '{"_type":"https://in-toto.io/Statement/v0.1","predicateType":"https://spdx.dev/Document","predicate":'
    $bb cat "$doc"
    printf '%s\n' '}'
  } > "$BUILDKIT_SCAN_DESTINATION/$name.spdx.json"
  $bb rm -f "$doc"
}

scan "$BUILDKIT_SCAN_SOURCE"

if [ -n "${BUILDKIT_SCAN_SOURCE_EXTRAS:-}" ] && [ -d "$BUILDKIT_SCAN_SOURCE_EXTRAS" ]; then
  for extra in "$BUILDKIT_SCAN_SOURCE_EXTRAS"/*; do
    [ -d "$extra" ] || continue
    scan "$extra"
  done
fi
