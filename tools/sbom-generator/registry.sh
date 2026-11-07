#!/usr/bin/env bash
# Serves the SBOM generator of this directory to a BuildKit builder, for one build (build.yml, Q-HA-166).
#
#   registry.sh network  creates the docker network of this run (the builder is created on it, before `up`)
#   registry.sh host     prints the registry's address on that network: trove-sbom-registry-<run>:5000
#   registry.sh up       starts a throwaway registry on the network, builds the generator for linux/amd64 and
#                        linux/arm64, pushes it there and prints its reference by digest on the last line:
#                        trove-sbom-registry-<run>:5000/sbom-generator@sha256:...
#   registry.sh down     removes the registry container and the network
#
# The registry listens on the run's own network only: it publishes no port, and the builder is a container on the same
# network (docker buildx create --driver docker-container --driver-opt network=<network>), so the build steps of the
# image never share the runner's network namespace. The registry speaks plain HTTP, which the builder's buildkitd
# configuration must allow for exactly that host (`registry.sh buildkitd-config`). Nothing is published: `down` removes
# everything, and a failed `up` removes the registry itself.
#
# Environment: TROVE_SBOM_RUN names the run (the workflow sets the run id and attempt; default `local`), so concurrent
# runs on one Docker host never share a name. The builder is the current one or BUILDX_BUILDER. The pins come from
# tools/versions.env.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
set -a
# shellcheck source=tools/versions.env
. "$here/../versions.env"
set +a

run="${TROVE_SBOM_RUN:-local}"
net="trove-sbom-${run}"
reg="trove-sbom-registry-${run}"
host="${reg}:5000"

case "${1:-}" in
  network)
    docker network inspect "$net" >/dev/null 2>&1 || docker network create "$net" >/dev/null
    printf '%s\n' "$net"
    ;;
  host)
    printf '%s\n' "$host"
    ;;
  buildkitd-config)
    printf '[registry."%s"]\n  http = true\n' "$host"
    ;;
  up)
    docker network inspect "$net" >/dev/null 2>&1 || { echo "error: the network $net does not exist; run '$0 network' and create the builder on it first" >&2; exit 1; }
    ok=0
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"; [ "$ok" = 1 ] || docker rm -f "$reg" >/dev/null 2>&1 || true' EXIT
    docker rm -f "$reg" >/dev/null 2>&1 || true
    docker run -d --rm --name "$reg" --network "$net" "$SBOM_GENERATOR_REGISTRY_IMAGE" >/dev/null
    for _ in $(seq 1 30); do
      docker exec "$reg" wget -q -O /dev/null "http://127.0.0.1:5000/v2/" 2>/dev/null && break
      docker inspect -f '{{.State.Running}}' "$reg" >/dev/null 2>&1 || { echo "error: the registry $reg stopped" >&2; exit 1; }
      sleep 1
    done
    docker exec "$reg" wget -q -O /dev/null "http://127.0.0.1:5000/v2/" || { echo "error: the registry $reg does not answer" >&2; exit 1; }
    docker buildx build --platform linux/amd64,linux/arm64 \
      --build-arg "SYFT_IMAGE=${SYFT_IMAGE}" --build-arg "SBOM_GENERATOR_SHELL_IMAGE=${SBOM_GENERATOR_SHELL_IMAGE}" \
      -f "$here/Containerfile" -t "${host}/sbom-generator:${SYFT_VERSION}" --metadata-file "$tmp/meta.json" --push "$here" >&2
    digest="$(sed -n 's/.*"containerimage.digest": *"\(sha256:[0-9a-f]\{64\}\)".*/\1/p' "$tmp/meta.json" | head -n 1)"
    [ -n "$digest" ] || { echo "error: the build wrote no containerimage.digest" >&2; exit 1; }
    ok=1
    printf '%s/sbom-generator@%s\n' "$host" "$digest"
    ;;
  down)
    docker rm -f "$reg" >/dev/null 2>&1 || true
    docker network rm "$net" >/dev/null 2>&1 || true
    ;;
  *)
    echo "usage: $0 network|host|buildkitd-config|up|down" >&2
    exit 2
    ;;
esac
