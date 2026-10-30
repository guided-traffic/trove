#!/usr/bin/env bash
# helm lint and helm template for every value set of the chart: deploy/helm/trove/ci/*.yaml and
# the dev profile's values (deploy/dev/values-dev.yaml, also with values-pooler.yaml layered on
# it). Needs only helm at the version pinned in tools/versions.env, no cluster.
#
# The render assertions (what the output must and must not contain) are in chart_test.go and run
# with `make chart-test`.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CHART="$ROOT/deploy/helm/trove"
# shellcheck source=/dev/null
. "$ROOT/tools/versions.env"

got="$(helm version --template '{{.Version}}' 2>/dev/null || true)"
if [ "$got" != "$HELM_VERSION" ]; then
  echo "error: helm ${got:-<none>} is installed, tools/versions.env pins $HELM_VERSION." >&2
  echo "  install it with: curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-4 | DESIRED_VERSION=$HELM_VERSION bash" >&2
  exit 1
fi

sets=("$CHART"/ci/*.yaml)
failed=0
run() {
  local label="$1"; shift
  if out="$(helm lint --strict "$CHART" "$@" 2>&1 && helm template trove "$CHART" "$@" 2>&1 >/dev/null)"; then
    echo "ok    $label"
  else
    echo "FAIL  $label" >&2
    printf '%s\n' "$out" >&2
    failed=1
  fi
}

for f in "${sets[@]}"; do
  run "ci/$(basename "$f")" -f "$f"
done
run "deploy/dev/values-dev.yaml" -f "$ROOT/deploy/dev/values-dev.yaml"
run "deploy/dev/values-dev.yaml + values-pooler.yaml" -f "$ROOT/deploy/dev/values-dev.yaml" -f "$ROOT/deploy/dev/values-pooler.yaml"

# Offline rendering has no cluster capabilities; the kubeconform run of R-HA-028 joins with the
# CI workflow (the tool is not pinned yet).
exit "$failed"
