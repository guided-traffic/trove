#!/usr/bin/env bash
# Writes (or checks) the reference project of the npm performance and e2e tests, perf/npm/reference:
#
#   reference.sh            regenerate package.json and package-lock.json in place
#   reference.sh --check    regenerate into a scratch directory and fail when the committed files differ
#
# package.json comes from the synthetic corpus (npmstub reference). The lockfile is resolved by the npm
# bundled in the pinned Node image (E2E_CLIENTS_NODE_IMAGE in tools/versions.env) against the counting
# test upstream (image NPM_STUB_IMAGE, `make npm-stub-image`), which runs on a throwaway Docker network
# behind a throwaway CA. `--package-lock-only --ignore-scripts` resolves without installing anything. The
# stub writes the tarball addresses of the dev profile's npm-all repository into its documents
# (-public-url), so the lockfile's `resolved` fields are what a client of the dev profile would have.
# Nothing in this script reaches the internet.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
set -a
# shellcheck source=/dev/null
. "$repo/tools/versions.env"
set +a

mode="write"
case "${1:-}" in
  "") ;;
  --check) mode="check" ;;
  *) echo "usage: reference.sh [--check]" >&2; exit 2 ;;
esac
command -v docker >/dev/null 2>&1 || { echo "reference.sh: docker is required" >&2; exit 1; }
: "${NPM_STUB_IMAGE:?reference.sh: NPM_STUB_IMAGE is not set (run it through make npm-reference)}"

# One Node: the image tag carries web/.nvmrc's version.
nvmrc="$(tr -d '[:space:]' < "$repo/web/.nvmrc")"
tag="${E2E_CLIENTS_NODE_IMAGE#*:}"; tag="${tag%%@*}"
[ "${tag%%-*}" = "$nvmrc" ] || { echo "reference.sh: E2E_CLIENTS_NODE_IMAGE has Node ${tag%%-*}, web/.nvmrc pins $nvmrc" >&2; exit 1; }

ref="$repo/perf/npm/reference"
work="$repo/.out/npm-reference"
rm -rf "$work"; mkdir -p "$work/project" "$work/ca"
chmod 777 "$work/ca"   # the stub runs as another user and writes its CA certificate here

net="trove-npm-reference-$$"
stub="$net-stub"
cleanup() { docker rm -f "$stub" >/dev/null 2>&1 || true; docker network rm "$net" >/dev/null 2>&1 || true; }
trap cleanup EXIT

(cd "$repo" && go run ./test/npmstub/cmd/npmstub reference -out "$work/project")

docker network create "$net" >/dev/null
docker run -d --name "$stub" --network "$net" --network-alias npmstub -v "$work/ca:/ca" "$NPM_STUB_IMAGE" \
  -listen :8443 -self-signed npmstub,localhost -ca-out /ca/ca.crt \
  -public-url https://localhost:8080/api/npm/npm-all >/dev/null
for _ in $(seq 1 50); do
  [ -s "$work/ca/ca.crt" ] && docker logs "$stub" 2>&1 | grep -q 'npmstub listening' && break
  sleep 0.2
done
[ -s "$work/ca/ca.crt" ] || { docker logs "$stub" >&2; echo "reference.sh: the stub did not start" >&2; exit 1; }

docker run --rm --network "$net" --user "$(id -u):$(id -g)" -e HOME=/tmp -e NODE_EXTRA_CA_CERTS=/ca/ca.crt \
  -v "$work/ca:/ca:ro" -v "$work/project:/project" -w /project "$E2E_CLIENTS_NODE_IMAGE" \
  sh -c 'echo "npm $(npm --version) on Node $(node --version)" && npm install --package-lock-only --ignore-scripts --no-audit --no-fund --no-update-notifier --loglevel=error --cache /tmp/npm-cache --registry https://npmstub:8443/'

if [ "$mode" = "check" ]; then
  status=0
  for f in package.json package-lock.json; do
    diff -u "$ref/$f" "$work/project/$f" || status=1
  done
  [ "$status" -eq 0 ] || { echo "reference.sh: perf/npm/reference differs from a regeneration; run make npm-reference and commit the result" >&2; exit 1; }
  echo "perf/npm/reference is current"
else
  mkdir -p "$ref"
  cp "$work/project/package.json" "$work/project/package-lock.json" "$ref/"
  echo "wrote $ref/package.json and package-lock.json"
fi
