#!/usr/bin/env bash
# Creates a throwaway CA and one server certificate for the integration stack in
# $1 (default .dev/integration/certs). The certificate covers the names the
# stack is reached by: localhost, 127.0.0.1, ::1 and the compose service names.
# It is idempotent: an existing certificate that is still valid for 30 days is
# kept. When it creates new material it prints "certs: generated" on stdout, so
# the caller can recreate containers that hold the old certificate. The key is world-readable on purpose: the containers run as other users
# and the material protects nothing but a local test stack.
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

dir="${1:-.dev/integration/certs}"
mkdir -p "$dir"
dir="$(cd "$dir" && pwd)"

cd "$dir"
if [ -f server.crt ] && [ -f ca.crt ] && openssl x509 -checkend $((30 * 86400)) -noout -in server.crt >/dev/null 2>&1; then
  exit 0
fi
openssl genrsa -out ca.key 2048 2>/dev/null
openssl req -x509 -new -key ca.key -sha256 -days 365 -subj "/CN=trove integration CA" -out ca.crt

cat > server.cnf <<CNF
[req]
distinguished_name = dn
req_extensions = ext
prompt = no
[dn]
CN = trove-integration
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

openssl genrsa -out server.key 2048 2>/dev/null
openssl req -new -key server.key -config server.cnf -out server.csr
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 365 -sha256 \
  -extfile server.cnf -extensions ext -out server.crt 2>/dev/null
rm -f server.csr server.cnf ca.srl
echo "certs: generated"
chmod 0644 ca.crt server.crt server.key
chmod 0600 ca.key
