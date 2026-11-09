#!/usr/bin/env bash
# helm lint and helm template for every value set of the chart trove (deploy/helm/trove/ci/*.yaml and
# the dev profile's values: deploy/dev/values-dev.yaml, also with values-pooler.yaml layered on
# it, and the sizing profiles of deploy/helm/trove/profiles on test/chart/testdata/operator-minimum.yaml) and of the chart trove-bastion (deploy/helm/trove-bastion/ci/*.yaml). Needs only helm at the
# version pinned in tools/versions.env, no cluster.
#
# The render assertions (what the output must and must not contain) are in chart_test.go and run
# with `make chart-test`.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CHART="$ROOT/deploy/helm/trove"
BASTION_CHART="$ROOT/deploy/helm/trove-bastion"
# shellcheck source=/dev/null
. "$ROOT/tools/versions.env"

got="$(helm version --template '{{.Version}}' 2>/dev/null || true)"
if [ "$got" != "$HELM_VERSION" ]; then
  echo "error: helm ${got:-<none>} is installed, tools/versions.env pins $HELM_VERSION." >&2
  echo "  install it with: curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-4 | DESIRED_VERSION=$HELM_VERSION bash" >&2
  exit 1
fi

sets=("$CHART"/ci/*.yaml "$CHART"/examples/*.yaml)
failed=0
# helm lint takes no API versions, so a value set that enables the Gateway API (R-HA-038) is linted with the
# HTTPRoute off and rendered with the Gateway API declared.
LINT_EXTRA=()
TEMPLATE_EXTRA=()
run_chart() {
  local chart="$1" release="$2" label="$3"; shift 3
  if out="$(helm lint --strict "$chart" "$@" ${LINT_EXTRA[@]+"${LINT_EXTRA[@]}"} 2>&1 && helm template "$release" "$chart" "$@" ${TEMPLATE_EXTRA[@]+"${TEMPLATE_EXTRA[@]}"} 2>&1 >/dev/null)"; then
    echo "ok    $label"
  else
    echo "FAIL  $label" >&2
    printf '%s\n' "$out" >&2
    failed=1
  fi
}
run() {
  local label="$1"; shift
  run_chart "$CHART" trove "$label" "$@"
}

for f in "${sets[@]}"; do
  if grep -q '^gateway:' "$f"; then
    LINT_EXTRA=(--set gateway.enabled=false)
    TEMPLATE_EXTRA=(--api-versions gateway.networking.k8s.io/v1/HTTPRoute)
  fi
  run "${f#"$CHART"/}" -f "$f"
  LINT_EXTRA=()
  TEMPLATE_EXTRA=()
done
# The sizing profiles are layered before the operator's file (R-HA-050): lint each on the minimum an operator gives.
for f in "$CHART"/profiles/*.yaml; do
  run "profiles/$(basename "$f")" -f "$f" -f "$ROOT/test/chart/testdata/operator-minimum.yaml"
done
run "deploy/dev/values-dev.yaml" -f "$ROOT/deploy/dev/values-dev.yaml"
run "deploy/dev/values-dev.yaml + values-pooler.yaml" -f "$ROOT/deploy/dev/values-dev.yaml" -f "$ROOT/deploy/dev/values-pooler.yaml"

for f in "$BASTION_CHART"/ci/*.yaml; do
  run_chart "$BASTION_CHART" trove-bastion "trove-bastion ci/$(basename "$f")" -f "$f"
done

# Offline rendering has no cluster capabilities; the kubeconform run of R-HA-028 joins with the
# CI workflow (the tool is not pinned yet).
exit "$failed"
