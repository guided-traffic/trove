#!/usr/bin/env bash
# Silo for the S3 conformance tests (R-STOR-035): the image of SILO_IMAGE (tools/versions.env), pinned by digest, served over
# TLS only (INV-20), with the buckets the tests use. Silo is the maintained fork of the MinIO server, with the same protocol
# and the same MINIO_* settings. The leg of .github/workflows/release.yml (job s3-conformance) runs these steps; `make silo-up`,
# `make s3-conformance` and `make silo-down` run them locally.
#
#   up    starts the container trove-silo on https://127.0.0.1:9000 (console 9001), checks that a plaintext request is refused,
#         seeds the buckets and writes the TROVE_TEST_S3_* variables to .dev/silo/env.
#   down  writes the server's log to .out/silo.log and stops the container, which takes its data with it.
#
# The root credentials are random per start and live only in .dev/silo/env (owner-only).
set -euo pipefail

usage() { echo "usage: silo.sh up|down" >&2; exit 64; }
[ "$#" -eq 1 ] || usage

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"
state=.dev/silo
env_file="$state/env"
name=trove-silo

down() {
  if docker inspect "$name" >/dev/null 2>&1; then
    mkdir -p .out
    docker logs "$name" > .out/silo.log 2>&1 || true
    docker stop "$name" >/dev/null
  fi
}

up() {
  if ! [[ "${SILO_IMAGE:-}" =~ @sha256:[0-9a-f]{64}$ ]]; then
    echo "SILO_IMAGE in tools/versions.env is missing or is not pinned by digest" >&2
    exit 1
  fi
  mkdir -p "$state"
  down

  # TLS on every connection: the CA and the server certificate come from the integration stack's script (openssl from the
  # pinned image); the SANs cover 127.0.0.1 and localhost. The server reads public.crt and private.key from --certs-dir. The key
  # is readable by the container's user: it is a throwaway key of this test server.
  pki="$state/pki"
  test/integration/certs.sh "$pki" >/dev/null
  mkdir -p "$state/certs"
  cp "$pki/s3.crt" "$state/certs/public.crt"
  cp "$pki/s3.key" "$state/certs/private.key"
  chmod 755 "$state/certs"
  chmod 644 "$state/certs/public.crt" "$state/certs/private.key"
  ca="$pki/ca.crt"

  user="trove-silo"
  pass="$(head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9')"
  docker run -d --rm --name "$name" \
    -p 127.0.0.1:9000:9000 -p 127.0.0.1:9001:9001 \
    -e MINIO_ROOT_USER="$user" -e MINIO_ROOT_PASSWORD="$pass" \
    -v "$root/$state/certs:/certs:ro" \
    --tmpfs /data:mode=1777 \
    "$SILO_IMAGE" server /data --certs-dir /certs --address :9000 --console-address :9001 >/dev/null

  for _ in $(seq 1 60); do
    curl -fsS --cacert "$ca" https://127.0.0.1:9000/minio/health/live >/dev/null 2>&1 && break
    sleep 1
  done
  curl -fsS --cacert "$ca" https://127.0.0.1:9000/minio/health/live >/dev/null || { docker logs "$name" 2>&1 | tail -20 >&2; exit 1; }
  # A plaintext request must not be served (INV-20).
  if curl -fsS --max-time 5 http://127.0.0.1:9000/minio/health/live >/dev/null 2>&1; then
    echo "Silo answered a plaintext request" >&2
    exit 1
  fi

  (
    umask 077
    {
      echo "TROVE_TEST_S3_ENDPOINT=https://127.0.0.1:9000"
      echo "TROVE_TEST_CA_FILE=$root/$ca"
      echo "TROVE_TEST_S3_REGION=us-east-1"
      echo "TROVE_TEST_S3_ACCESS_KEY=$user"
      echo "TROVE_TEST_S3_SECRET_KEY=$pass"
      echo "TROVE_TEST_S3_BUCKET_BLOBS=trove-blobs"
      echo "TROVE_TEST_S3_BUCKET_BACKUPS=trove-backups"
      echo "TROVE_TEST_S3_FORCE_PATH_STYLE=true"
    } > "$env_file"
  )
  set -a
  . "$env_file"
  set +a
  go run ./test/integration/cmd/seed
  echo "Silo serves https://127.0.0.1:9000; environment in $env_file"
}

case "$1" in
  up) up ;;
  down) down ;;
  *) usage ;;
esac
