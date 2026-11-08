#!/usr/bin/env bash
# Serves the SBOM generator of this directory to a BuildKit builder, for one build (build.yml, Q-HA-166).
#
#   registry.sh start             starts a throwaway registry container on the default bridge network and prints its
#                                 address on the last line: <container IP>:5000
#   registry.sh host              prints that address again
#   registry.sh buildkitd-config  prints the buildkitd configuration that allows plain HTTP for that address; the
#                                 builder must be created with it, after `start`
#   registry.sh up                builds the generator for linux/amd64 and linux/arm64, pushes it to the registry and
#                                 prints its reference by digest on the last line: <IP>:5000/sbom-generator@sha256:...
#   registry.sh down              removes the registry container
#
# The builder stays on Docker's default bridge network, like every builder of the release, so the RUN steps of the image
# build resolve names through the daemon's resolvers (a builder on a user-defined network gets Docker's embedded DNS at
# 127.0.0.11, which BuildKit's per-step network namespaces cannot use: release v0.25.1 failed there, Q-HA-166). The registry
# publishes no port and is reached by its container IP, which is why the address is not a name. The generator is referenced
# by digest, so whoever else on the bridge can reach the registry can replace nothing the builder runs. The registry speaks
# plain HTTP. Nothing is published: `down` removes everything, and a failed `up` removes the registry itself.
#
# Environment: TROVE_SBOM_RUN names the run (the workflow sets the run id and attempt; default `local`), so concurrent
# runs on one Docker host never share a container name. The builder is the current one or BUILDX_BUILDER. The pins come
# from tools/versions.env.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
set -a
# shellcheck source=tools/versions.env
. "$here/../versions.env"
set +a

run="${TROVE_SBOM_RUN:-local}"
reg="trove-sbom-registry-${run}"

address() {
  local ip
  ip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$reg" 2>/dev/null)" || ip=""
  [ -n "$ip" ] || { echo "error: the registry $reg is not running or has no address; run '$0 start'" >&2; return 1; }
  printf '%s:5000\n' "$ip"
}

case "${1:-}" in
  start)
    docker rm -f "$reg" >/dev/null 2>&1 || true
    docker run -d --rm --name "$reg" "$SBOM_GENERATOR_REGISTRY_IMAGE" >/dev/null
    ready=0
    for _ in $(seq 1 30); do
      if docker exec "$reg" wget -q -O /dev/null "http://127.0.0.1:5000/v2/" 2>/dev/null; then ready=1; break; fi
      docker inspect -f '{{.State.Running}}' "$reg" >/dev/null 2>&1 || { echo "error: the registry $reg stopped" >&2; exit 1; }
      sleep 1
    done
    [ "$ready" = 1 ] || { docker rm -f "$reg" >/dev/null 2>&1 || true; echo "error: the registry $reg does not answer" >&2; exit 1; }
    address
    ;;
  host)
    address
    ;;
  buildkitd-config)
    printf '[registry."%s"]\n  http = true\n' "$(address)"
    ;;
  up)
    host="$(address)"
    ok=0
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"; [ "$ok" = 1 ] || docker rm -f "$reg" >/dev/null 2>&1 || true' EXIT
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
    ;;
  *)
    echo "usage: $0 start|host|buildkitd-config|up|down" >&2
    exit 2
    ;;
esac
