#!/usr/bin/env bash
# release-values.sh <profile> [<values dir>] prints, one per line, the helm arguments up.sh installs the trove release
# with: the values files, the node ranges, the edge port and the profile's files. Without a values dir they are the files
# of deploy/dev and carry the image ID of $TROVE_IMAGE (default guidedtraffic/trove:dev); with one (the copy of an older
# release's deploy/dev files, which the upgrade tests install) the arguments name that directory's files and carry no
# image annotation. The upgrade tests (docs/developer/e2e.md#the-upgrade-tests) call it so that they upgrade with
# exactly the values of the dev profile.
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_cluster

PROFILE="${1:?usage: release-values.sh <profile> [<values dir>]}"
case "$PROFILE" in default|scatter|bastion|vuln|mirror|backup) ;; *) die "the profile must be default, scatter, bastion, vuln, mirror or backup, not '$PROFILE'" ;; esac
POOLER="${TROVE_POOLER:-0}"
IMAGE="${TROVE_IMAGE:-guidedtraffic/trove:dev}"
EDGE_PORT="$(docker port "${CLUSTER}-control-plane" 30443/tcp 2>/dev/null | head -1 | sed 's/.*://' || true)"
EDGE_PORT="${EDGE_PORT:-$DEV_HTTP_PORT}"

if [ -n "${2:-}" ]; then
  release_values "$2" "$PROFILE" 0
else
  release_values "$DEV_DIR" "$PROFILE" 1
fi
printf '%s\n' "${RELEASE_VALUES[@]}"
