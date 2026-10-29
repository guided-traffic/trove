#!/usr/bin/env bash
# MinIO for the S3 conformance tests (R-STOR-035): built from source at MINIO_COMMIT
# (tools/versions.env), served over TLS only (INV-20), with the buckets the tests use. The leg of
# .github/workflows/release.yml (job s3-conformance) runs these steps; `make minio-up`,
# `make s3-conformance` and `make minio-down` run them locally.
#
#   up    builds MinIO into .dev/minio/bin, starts it on https://127.0.0.1:9000 (console 9001),
#         checks that a plaintext request is refused, seeds the buckets and writes the
#         TROVE_TEST_S3_* variables to .dev/minio/env. The server's log is .out/minio.log.
#   down  stops the server started by up.
#
# The root credentials are random per start and live only in .dev/minio/env (owner-only).
set -euo pipefail

usage() { echo "usage: minio.sh up|down" >&2; exit 64; }
[ "$#" -eq 1 ] || usage

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"
state=.dev/minio
pid_file="$state/pid"
env_file="$state/env"
MINIO_REPO=https://github.com/minio/minio

down() {
  if [ -f "$pid_file" ]; then
    kill "$(cat "$pid_file")" 2>/dev/null || true
    rm -f "$pid_file"
  fi
}

up() {
  if ! [[ "${MINIO_COMMIT:-}" =~ ^[0-9a-f]{40}$ ]]; then
    echo "MINIO_COMMIT in tools/versions.env is missing or is not a full 40-character commit SHA" >&2
    exit 1
  fi
  mkdir -p "$state" .out
  down

  # The source is fetched at the pin and checked out detached; HEAD must equal the pin.
  src="$state/src"
  if [ ! -d "$src/.git" ]; then
    rm -rf "$src"
    mkdir -p "$src"
    git -C "$src" init -q
    git -C "$src" remote add origin "$MINIO_REPO"
  fi
  if [ "$(git -C "$src" rev-parse -q --verify HEAD 2>/dev/null || true)" != "$MINIO_COMMIT" ]; then
    git -C "$src" fetch -q --depth 1 origin "$MINIO_COMMIT"
    git -C "$src" checkout -q --detach FETCH_HEAD
  fi
  test "$(git -C "$src" rev-parse HEAD)" = "$MINIO_COMMIT"
  mkdir -p "$state/bin"
  (cd "$src" && go build -trimpath -o "$root/$state/bin/minio" .)
  "$state/bin/minio" --version

  # TLS on every connection: the CA and the server certificate come from the integration stack's
  # script (openssl from the pinned image); the SANs cover 127.0.0.1 and localhost. MinIO reads
  # public.crt and private.key from --certs-dir.
  pki="$state/pki"
  test/integration/certs.sh "$pki" >/dev/null
  mkdir -p "$state/certs/minio" "$state/data"
  cp "$pki/s3.crt" "$state/certs/minio/public.crt"
  cp "$pki/s3.key" "$state/certs/minio/private.key"
  ca="$pki/ca.crt"

  user="trove-minio"
  pass="$(head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9')"
  MINIO_ROOT_USER="$user" MINIO_ROOT_PASSWORD="$pass" \
    "$state/bin/minio" server "$state/data" --certs-dir "$state/certs/minio" \
    --address 127.0.0.1:9000 --console-address 127.0.0.1:9001 \
    > .out/minio.log 2>&1 &
  echo $! > "$pid_file"

  for _ in $(seq 1 60); do
    curl -fsS --cacert "$ca" https://127.0.0.1:9000/minio/health/live >/dev/null 2>&1 && break
    sleep 1
  done
  curl -fsS --cacert "$ca" https://127.0.0.1:9000/minio/health/live >/dev/null
  # A plaintext request must not be served (INV-20).
  if curl -fsS --max-time 5 http://127.0.0.1:9000/minio/health/live >/dev/null 2>&1; then
    echo "MinIO answered a plaintext request" >&2
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
  echo "MinIO at ${MINIO_COMMIT:0:12} serves https://127.0.0.1:9000; environment in $env_file"
}

case "$1" in
  up) up ;;
  down) down ;;
  *) usage ;;
esac
