#!/bin/sh
# PID 1 of the Docker container of an OCI clients pod: puts the dev CA where dockerd looks for a registry's CA, optionally
# names a registry mirror in daemon.json, starts the forwarder (test/e2e/clients/fwd) in the background and becomes dockerd.
# The pod shares one network namespace, so the forwarder's listener on 127.0.0.1:<edge port> is the registry the daemon, the
# CLI and Podman reach; `localhost:<edge port>` is the base URL of the setup snippets.
#
#   TROVE_CA_FILE         the dev CA, PEM (default /etc/trove-ca/ca.crt, a ConfigMap mount)
#   TROVE_REGISTRY_HOSTS  the registry hosts, host[:port] separated by blanks; each gets /etc/docker/certs.d/<host>/ca.crt
#   TROVE_ENGINE_MIRROR   https URL of the Docker Engine mirror (registry-mirrors of daemon.json); empty: none
#   arguments             the forwarder's: -listen, -to, -target-file
set -eu
ca="${TROVE_CA_FILE:-/etc/trove-ca/ca.crt}"
[ -s "$ca" ] || { echo "entrypoint: the dev CA $ca is missing or empty" >&2; exit 1; }
for h in ${TROVE_REGISTRY_HOSTS:?TROVE_REGISTRY_HOSTS is not set}; do
  mkdir -p "/etc/docker/certs.d/$h"
  cp "$ca" "/etc/docker/certs.d/$h/ca.crt"
done
# BuildKit inside dockerd fetches the token of `buildx build --push` with a client of its own, which reads the system trust and not certs.d.
cp "$ca" /usr/local/share/ca-certificates/trove-dev-ca.crt
update-ca-certificates >/dev/null
mkdir -p /etc/docker
if [ -n "${TROVE_ENGINE_MIRROR:-}" ]; then
  printf '{"registry-mirrors":["%s"]}\n' "$TROVE_ENGINE_MIRROR" >/etc/docker/daemon.json
else
  printf '{}\n' >/etc/docker/daemon.json
fi
/usr/local/bin/fwd "$@" &
# dockerd by the image's own entrypoint (cgroup nesting, iptables flavour, docker-init), with the unix socket only: with no
# arguments the image opens an unauthenticated TCP listener, which every pod of the cluster could reach.
exec dockerd-entrypoint.sh dockerd --host=unix:///var/run/docker.sock
