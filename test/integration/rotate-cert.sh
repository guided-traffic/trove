#!/usr/bin/env bash
# Re-issues one service's server certificate of the running integration stack in place and makes
# the service serve it, without recreating the container. For tests of certificate rotation.
#
#   rotate-cert.sh <service> [days]
#
# service is one of: postgres, pgbouncer, valkey, s3
#   postgres   copies the new pair into the container and reloads the server (pg_reload_conf)
#   pgbouncer  signals the process to reload (SIGHUP re-reads the TLS files)
#   valkey     re-issues server.crt/server.key and re-applies them on all five processes (CONFIG SET)
#   s3         re-issues s3.crt/s3.key and restarts the gateway container: SeaweedFS reads the pair
#              only at start, so this one is not a hot reload and the restart takes several seconds
# days is the validity of the new certificate (default 365); a short value makes expiry observable.
# The CA does not change, so a client that trusts it keeps verifying. The new certificate has a new
# key and serial; the previous files are overwritten. Needs a stack from `make integration-up`.
set -euo pipefail

svc="${1:?usage: rotate-cert.sh <postgres|pgbouncer|valkey|s3> [days]}"
days="${2:-365}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
certs="$root/.dev/integration/certs"
[ -f "$certs/ca.crt" ] || { echo "rotate-cert.sh: no stack certificates in $certs; run make integration-up" >&2; exit 1; }

case "$svc" in
  postgres)
    "$here/certs.sh" --issue postgres "$certs" "$days"
    docker exec trove-it-postgres /conf/postgres.sh refresh
    docker exec trove-it-postgres psql -U trove -d trove -qtAc "select pg_reload_conf()" >/dev/null
    ;;
  pgbouncer)
    "$here/certs.sh" --issue pgbouncer "$certs" "$days"
    docker kill --signal=HUP trove-it-pgbouncer >/dev/null
    ;;
  valkey)
    "$here/certs.sh" --issue server "$certs" "$days"
    for port in 16379 16380 26381 26382 26383; do
      auth=(--user ops --pass ops-valkey-it)
      case "$port" in 263*) auth=(-a sentinel-valkey-it) ;; esac
      docker exec trove-it-valkey-primary valkey-cli --tls --cacert /certs/ca.crt -h 127.0.0.1 -p "$port" "${auth[@]}" --no-auth-warning \
        CONFIG SET tls-cert-file /certs/server.crt tls-key-file /certs/server.key >/dev/null
    done
    ;;
  s3)
    "$here/certs.sh" --issue s3 "$certs" "$days"
    docker restart trove-it-seaweedfs >/dev/null
    for _ in $(seq 1 60); do
      [ "$(docker inspect -f '{{.State.Health.Status}}' trove-it-seaweedfs)" = healthy ] && exit 0
      sleep 2
    done
    echo "rotate-cert.sh: the S3 gateway did not become healthy after the restart" >&2
    exit 1
    ;;
  *) echo "rotate-cert.sh: unknown service '$svc' (postgres, pgbouncer, valkey, s3)" >&2; exit 2 ;;
esac
echo "rotate-cert: $svc now serves a new certificate (valid $days days)"
