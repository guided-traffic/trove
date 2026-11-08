#!/bin/sh
# PID 1 of the Podman container of an OCI clients pod: puts the dev CA where Podman looks for a registry's CA and waits. The tests
# run `podman` per `kubectl exec` (root, privileged pod). The forwarder is the Docker container's; the network namespace is shared.
#
#   TROVE_CA_FILE         the dev CA, PEM (default /etc/trove-ca/ca.crt, a ConfigMap mount)
#   TROVE_REGISTRY_HOSTS  the registry hosts, host[:port] separated by blanks; each gets /etc/containers/certs.d/<host>/ca.crt
set -eu
ca="${TROVE_CA_FILE:-/etc/trove-ca/ca.crt}"
[ -s "$ca" ] || { echo "entrypoint: the dev CA $ca is missing or empty" >&2; exit 1; }
for h in ${TROVE_REGISTRY_HOSTS:?TROVE_REGISTRY_HOSTS is not set}; do
  mkdir -p "/etc/containers/certs.d/$h"
  cp "$ca" "/etc/containers/certs.d/$h/ca.crt"
done
: >/tmp/ready
exec sleep infinity
