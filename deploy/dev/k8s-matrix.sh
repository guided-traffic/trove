#!/usr/bin/env bash
# One leg of the platform matrix of R-HA-049: the chart against the API server of one Kubernetes version, installed with one
# major version of Helm.
#
#   k8s-matrix.sh <kind node image> <helm major>      for example: k8s-matrix.sh "$K8S_MIN_KIND_NODE_IMAGE" 3
#
# It creates a one-node kind cluster of its own (trove-k8s-<minor>-helm<major>, removed on exit), checks that the API server is
# the minor version the node image names and that the helm on PATH is the major version asked for, lints the chart, asks the
# API server to admit every set of values below (`helm install --dry-run=server`), then installs the chart for real without
# waiting (no database, no Valkey and no image are there: the pods never start), reads back the fields that R-HA-049 names as
# the reason the chart needs Kubernetes 1.31 or later, upgrades it to another profile and removes it. The pods are not
# started, so this is the install half of the acceptance; the failure suite of R-HA-052 runs on the dev profile only.
#
# Helm 3 and Helm 4 are installed by the workflow (HELM_VERSION, HELM3_VERSION in tools/versions.env); this script uses the
# helm on PATH and fails when its major version is not the one asked for.
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

IMAGE="${1:?usage: k8s-matrix.sh <kind node image> <helm major>}"
HELM_MAJOR="${2:?usage: k8s-matrix.sh <kind node image> <helm major>}"
for t in kind kubectl helm; do command -v "$t" >/dev/null 2>&1 || die "$t is required"; done

tag="${IMAGE#*:}"; tag="${tag%%@*}"                         # v1.34.11
want_minor="$(printf '%s' "$tag" | cut -d. -f2)"           # 34
[[ "$want_minor" =~ ^[0-9]+$ ]] || die "the node image '$IMAGE' has no Kubernetes version in its tag"
have_helm="$(helm version --template '{{.Version}}')"
[ "${have_helm#v}" != "$have_helm" ] && [ "$(printf '%s' "${have_helm#v}" | cut -d. -f1)" = "$HELM_MAJOR" ] \
  || die "helm $have_helm is on PATH, this leg is for Helm $HELM_MAJOR"

CLUSTER="trove-k8s-${want_minor}-helm${HELM_MAJOR}"
CTX="kind-${CLUSTER}"
RELEASE=trove
NAMESPACE=trove
CHART="$REPO_ROOT/deploy/helm/trove"
OPERATOR="$REPO_ROOT/test/chart/testdata/operator-minimum.yaml"
OUT="$REPO_ROOT/.out/k8s-matrix"
mkdir -p "$OUT"

cleanup() { kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true; }
trap cleanup EXIT

log "Kubernetes $tag, helm $have_helm"
kind create cluster --name "$CLUSTER" --image "$IMAGE" --wait 180s
kc() { kubectl --context "$CTX" "$@"; }
hm() { helm --kube-context "$CTX" "$@"; }

got_minor="$(kc version -o json | sed -n 's/.*"minor": "\([0-9]*\).*/\1/p' | tail -n 1)"
[ "$got_minor" = "$want_minor" ] || die "the API server is Kubernetes 1.$got_minor, the node image names 1.$want_minor"
kc create namespace "$NAMESPACE"

log "helm lint"
helm lint "$CHART" -f "$CHART/ci/required-only.yaml"

# Each entry is the arguments of one install: value files and --set pairs. No set needs a CRD (no Gateway API, no Prometheus
# Operator): the API server of a bare kind cluster has none.
declare -a SETS=(
  "-f $CHART/ci/required-only.yaml"
  "-f $CHART/ci/roles-pools.yaml"
  "-f $CHART/profiles/small.yaml -f $OPERATOR"
  "-f $CHART/profiles/medium.yaml -f $OPERATOR"
  "-f $CHART/profiles/large.yaml -f $OPERATOR"
  "-f $CHART/ci/required-only.yaml --set backup.enabled=true --set config.backup.target.bucket=trove-backups"
)
i=0
for args in "${SETS[@]}"; do
  i=$((i + 1))
  log "set $i: helm install --dry-run=server ${args//$CHART\//}"
  # shellcheck disable=SC2086 # the entries are word lists on purpose
  hm install "$RELEASE" "$CHART" -n "$NAMESPACE" $args --dry-run=server >"$OUT/dry-run-$CLUSTER-$i.yaml"
done

# The install is real, the pods are not: with the migration hook off Helm returns when the API server has accepted the objects.
log "helm install (medium profile, backup on), not waiting"
hm install "$RELEASE" "$CHART" -n "$NAMESPACE" -f "$CHART/profiles/medium.yaml" -f "$OPERATOR" \
  --set migrations.mode=disabled --set backup.enabled=true --set config.backup.target.bucket=trove-backups

# expect <what> <kubectl jsonpath> <wanted> <kubectl get args...>: the API server stored the field the chart rendered.
expect() {
  local what="$1" path="$2" wanted="$3"; shift 3
  local got; got="$(kc -n "$NAMESPACE" get "$@" -o "jsonpath=$path")"
  case " $got " in
    *" $wanted "*) log "ok: $what = $got" ;;
    *) die "$what is '$got' on this Kubernetes, want '$wanted'" ;;
  esac
}
expect "PodDisruptionBudget unhealthyPodEvictionPolicy" '{.spec.unhealthyPodEvictionPolicy}' AlwaysAllow pdb "$RELEASE-packages"
expect "topologySpreadConstraints nodeTaintsPolicy" '{.spec.template.spec.topologySpreadConstraints[*].nodeTaintsPolicy}' Honor deployment "$RELEASE-packages"
expect "CronJob timeZone" '{.spec.timeZone}' Etc/UTC cronjob "$RELEASE-backup"
expect "HorizontalPodAutoscaler minReplicas of the medium profile" '{.spec.minReplicas}' 4 hpa "$RELEASE-packages"
expect "autoscaling/v2 metric" '{.spec.metrics[*].type}' Resource hpa "$RELEASE-packages"

log "helm upgrade to the small profile"
hm upgrade "$RELEASE" "$CHART" -n "$NAMESPACE" -f "$CHART/profiles/small.yaml" -f "$OPERATOR" \
  --set migrations.mode=disabled --set backup.enabled=true --set config.backup.target.bucket=trove-backups
if kc -n "$NAMESPACE" get hpa "$RELEASE-packages" >/dev/null 2>&1; then die "the autoscaler of the medium profile is still there after the upgrade to small"; fi
expect "replicas of the small profile" '{.spec.replicas}' 3 deployment "$RELEASE-packages"

log "helm uninstall"
hm uninstall "$RELEASE" -n "$NAMESPACE"
left="$(kc -n "$NAMESPACE" get deployments,cronjobs,pdb,hpa -o name | wc -l | tr -d ' ')"
[ "$left" = "0" ] || die "$left objects of the release are left after helm uninstall"
log "Kubernetes $tag with Helm $have_helm: the chart installs, upgrades and uninstalls"
