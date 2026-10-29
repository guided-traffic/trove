#!/usr/bin/env bash
# Creates the throwaway CA and the server certificates of the integration stack.
#
#   certs.sh [dir]                      create the CA and every certificate (default dir .dev/integration/certs)
#   certs.sh --issue <service> [dir] [days]
#                                       re-issue one service's certificate in place, signed by the existing CA
#                                       (used by rotate-cert.sh); days defaults to 365
#
# Layout of the directory (one CA, one certificate per service, all with the same SANs):
#   ca.crt  ca.key                      the CA; ca.crt is what TROVE_TEST_CA_FILE points to
#   server.crt  server.key              Valkey data nodes and Sentinels
#   postgres.crt  postgres.key          PostgreSQL
#   pgbouncer.crt  pgbouncer.key        PgBouncer (client side)
#   s3.crt  s3.key                      the SeaweedFS S3 gateway
# The SANs cover localhost, 127.0.0.1, ::1 and the compose service names, so the same certificate
# verifies from the host (127.0.0.1) and between containers (service name).
#
# Creating is idempotent: an existing set that is complete and still valid for 30 days is kept. When
# it creates new material it prints "certs: generated" on stdout, so the caller can recreate
# containers that hold the old certificates. The keys are world-readable on purpose: the containers
# run as other users and the material protects nothing but a local test stack.
set -euo pipefail

# openssl is never the host's: it runs from OPENSSL_IMAGE (tools/versions.env), in the certificate
# directory, as the calling user so the files belong to the caller.
repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
set -a
# shellcheck source=/dev/null
. "$repo/tools/versions.env"
set +a
command -v docker >/dev/null 2>&1 || { echo "certs.sh: docker is required (openssl runs from $OPENSSL_IMAGE)" >&2; exit 1; }
openssl() { docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:$PWD" -w "$PWD" "$OPENSSL_IMAGE" "$@"; }

services="server postgres pgbouncer s3"

issue() { # issue <service> <days>
  local name="$1" days="$2"
  cat > "$name.cnf" <<CNF
[req]
distinguished_name = dn
req_extensions = ext
prompt = no
[dn]
CN = trove-integration-$name
[ext]
subjectAltName = @alt
extendedKeyUsage = serverAuth, clientAuth
keyUsage = digitalSignature, keyEncipherment
[alt]
DNS.1 = localhost
DNS.2 = postgres
DNS.3 = pgbouncer
DNS.4 = valkey
DNS.5 = valkey-primary
DNS.6 = valkey-replica
DNS.7 = valkey-sentinel-1
DNS.8 = valkey-sentinel-2
DNS.9 = valkey-sentinel-3
DNS.10 = seaweedfs
IP.1 = 127.0.0.1
IP.2 = ::1
CNF
  openssl genrsa -out "$name.key.new" 2048 2>/dev/null
  openssl req -new -key "$name.key.new" -config "$name.cnf" -out "$name.csr"
  openssl x509 -req -in "$name.csr" -CA ca.crt -CAkey ca.key -CAcreateserial -days "$days" -sha256 \
    -extfile "$name.cnf" -extensions ext -out "$name.crt.new" 2>/dev/null
  # key first, then certificate, each by rename: a reader never sees a half-written file
  chmod 0644 "$name.key.new" "$name.crt.new"
  mv -f "$name.key.new" "$name.key"
  mv -f "$name.crt.new" "$name.crt"
  rm -f "$name.csr" "$name.cnf" ca.srl
}

if [ "${1:-}" = "--issue" ]; then
  svc="${2:?usage: certs.sh --issue <service> [dir] [days]}"
  case " $services " in *" $svc "*) ;; *) echo "certs.sh: unknown service '$svc' (one of: $services)" >&2; exit 2 ;; esac
  dir="${3:-.dev/integration/certs}"
  days="${4:-365}"
  dir="$(cd "$dir" && pwd)"
  cd "$dir"
  [ -f ca.crt ] && [ -f ca.key ] || { echo "certs.sh: no CA in $dir; run certs.sh first" >&2; exit 1; }
  issue "$svc" "$days"
  exit 0
fi

dir="${1:-.dev/integration/certs}"
mkdir -p "$dir"
dir="$(cd "$dir" && pwd)"
cd "$dir"

complete=1
for s in $services; do
  [ -f "$s.crt" ] && [ -f "$s.key" ] || complete=0
done
if [ "$complete" = 1 ] && [ -f ca.crt ]; then
  valid=1
  for s in $services; do
    openssl x509 -checkend $((30 * 86400)) -noout -in "$s.crt" >/dev/null 2>&1 || valid=0
  done
  if [ "$valid" = 1 ]; then
    exit 0
  fi
fi

openssl genrsa -out ca.key 2048 2>/dev/null
openssl req -x509 -new -key ca.key -sha256 -days 365 -subj "/CN=trove integration CA" -out ca.crt
for s in $services; do
  issue "$s" 365
done
echo "certs: generated"
chmod 0644 ca.crt
chmod 0600 ca.key
