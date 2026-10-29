#!/usr/bin/env bash
# Creates the dev CA and one certificate for the Valkey and Sentinel host names (R-HA-051).
# Output: .dev/valkey/{ca.crt,ca.key,tls.crt,tls.key}. The directory is ignored by Git (it
# carries its own .gitignore). Existing files are reused until the certificate is about to
# expire; --force recreates them. openssl runs from OPENSSL_IMAGE (tools/versions.env), so no
# host openssl is used.
# TROVE_CERT_DIR overrides the output directory (default .dev/valkey).
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

force=0
[ "${1:-}" = "--force" ] && force=1

command -v docker >/dev/null 2>&1 || die "docker is required: openssl runs from the pinned image $OPENSSL_IMAGE"

out="${TROVE_CERT_DIR:-$STATE_DIR/valkey}"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
[ -n "${TROVE_CERT_DIR:-}" ] || printf '*\n' > "$STATE_DIR/.gitignore"

cd "$out"
if [ "$force" = 0 ] && [ -s tls.crt ] && [ -s ca.crt ] \
   && openssl_pinned x509 -checkend 86400 -noout -in tls.crt >/dev/null 2>&1; then
  log "reusing the Valkey certificate in $out"
  exit 0
fi

log "creating the dev CA and the Valkey certificate in $out"
umask 077
openssl_pinned ecparam -name prime256v1 -genkey -noout -out ca.key
openssl_pinned req -x509 -new -key ca.key -sha256 -days 825 \
  -subj "/CN=trove dev CA" -out ca.crt \
  -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign"

openssl_pinned ecparam -name prime256v1 -genkey -noout -out tls.key
cat > san.cnf <<CNF
[req]
distinguished_name = dn
prompt = no
[dn]
CN = trove-valkey
[ext]
basicConstraints = CA:FALSE
keyUsage = critical,digitalSignature
extendedKeyUsage = serverAuth,clientAuth
subjectAltName = @san
[san]
DNS.1 = trove-valkey
DNS.2 = trove-valkey.${NS}.svc
DNS.3 = *.trove-valkey.${NS}.svc
DNS.4 = trove-valkey-sentinel
DNS.5 = trove-valkey-sentinel.${NS}.svc
DNS.6 = *.trove-valkey-sentinel.${NS}.svc
DNS.7 = localhost
IP.1 = 127.0.0.1
CNF
openssl_pinned req -new -key tls.key -config san.cnf -out tls.csr
openssl_pinned x509 -req -in tls.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -days 825 -sha256 -extfile san.cnf -extensions ext -out tls.crt 2>/dev/null
rm -f tls.csr san.cnf ca.srl
chmod 0644 ca.crt tls.crt
