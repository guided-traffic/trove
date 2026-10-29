#!/bin/sh
# Entrypoint of the PostgreSQL container. PostgreSQL refuses a private key that is
# group- or world-readable or not owned by its own user, and the bind-mounted key
# belongs to the host user, so the material is copied into a directory owned by
# postgres first. rotate-cert.sh calls this script with "refresh" to copy a re-issued
# certificate again before it reloads the server.
set -eu
tls=/var/lib/postgresql/tls
install -d -o postgres -g postgres -m 0700 "$tls"
install -o postgres -g postgres -m 0644 /certs/postgres.crt "$tls/server.crt"
install -o postgres -g postgres -m 0600 /certs/postgres.key "$tls/server.key"
[ "${1:-}" = "refresh" ] && exit 0
exec docker-entrypoint.sh "$@"
