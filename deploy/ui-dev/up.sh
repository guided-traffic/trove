#!/usr/bin/env bash
# Starts the backing services of the UI development profile (deploy/ui-dev/compose.yaml), creates the
# buckets, and writes everything trove needs into .dev/ui-dev: the certificates, the secret files, the key
# ring, the break-glass password and trove.yaml. Safe to run again: running containers, the data and every
# generated secret are kept. deploy/ui-dev/serve.sh starts trove against it.
# docs/developer/web-ui.md#local-ui-development
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"

set -a
# shellcheck disable=SC1091
. tools/versions.env
set +a

state="$root/.dev/ui-dev"
certs="$state/certs"
secrets="$state/secrets"
mkdir -p "$state" "$secrets/keys" "$state/spool"

compose=(docker compose -f deploy/ui-dev/compose.yaml)
export POSTGRES_IMAGE="$INTEGRATION_POSTGRES_IMAGE"
export VALKEY_IMAGE="$VALKEY_91_IMAGE"
export SEAWEEDFS_IMAGE
export CERT_DIR="$certs"

# The integration stack's certificate script, into this profile's own directory: its own CA, the same
# SANs (localhost, 127.0.0.1). New material means containers that hold the old certificates are recreated;
# the volumes stay.
certs_out="$(test/integration/certs.sh "$certs")"
if [ -n "$certs_out" ]; then
  echo "$certs_out"
  "${compose[@]}" down --remove-orphans 2>/dev/null || true
fi

# Secret files, written once. The values of the backing services are fixed in compose.yaml and
# test/integration/conf/s3.json; the key ring and the break-glass password are random per checkout.
put() { # put <file> <value>: write only when missing, readable by the caller only
  [ -f "$1" ] && return 0
  (umask 077 && printf '%s\n' "$2" > "$1")
}
put "$secrets/db-user" trove
put "$secrets/db-pass" trove-ui-dev
put "$secrets/valkey-pass" trove-ui-dev-valkey
put "$secrets/s3-id" trove-it-access
put "$secrets/s3-secret" trove-it-secret-0123456789abcdef
put "$secrets/keys/dev.key" "$(head -c 32 /dev/urandom | base64)"
put "$secrets/break-glass-password" "$(head -c 18 /dev/urandom | base64 | tr '+/' '-_')"
# The client secret trove and the profile's sign-in provider (test/devidp, started by serve.sh) share.
put "$secrets/idp-client-secret" "$(head -c 18 /dev/urandom | base64 | tr '+/' '-_')"

"${compose[@]}" up -d --wait --wait-timeout 240

# Buckets: the integration stack's seed command, pointed at this profile's S3 store.
TROVE_TEST_S3_ENDPOINT=https://127.0.0.1:28333 TROVE_TEST_S3_REGION=us-east-1 \
  TROVE_TEST_S3_ACCESS_KEY="$(cat "$secrets/s3-id")" TROVE_TEST_S3_SECRET_KEY="$(cat "$secrets/s3-secret")" \
  TROVE_TEST_S3_BUCKET_BLOBS=trove-blobs TROVE_TEST_S3_BUCKET_BACKUPS=trove-backups \
  TROVE_TEST_CA_FILE="$certs/ca.crt" go run ./test/integration/cmd/seed

# trove.yaml: rewritten on every run, so a change of this script reaches an existing checkout.
cat > "$state/trove.yaml.tmp" <<YAML
# Written by deploy/ui-dev/up.sh; rewritten on every run, so edit the script, not this file.
server:
  # The Angular dev server (make ui-dev-web) is the edge: the browser sees https://localhost:4200 only,
  # and the dev server's proxy sends everything outside /ui/ here.
  listen: 127.0.0.1:19080
  ops_listen: 127.0.0.1:19081
  base_url: https://localhost:4200
  trusted_proxies: ["127.0.0.1/32", "::1/128"]
  tls:
    certificates:
      - cert_file: "$certs/server.crt"
        key_file: "$certs/server.key"
database:
  host: 127.0.0.1
  port: 25432
  name: trove
  username_file: "$secrets/db-user"
  password_file: "$secrets/db-pass"
  ssl_mode: verify-full
  ssl_root_cert_file: "$certs/ca.crt"
  preflight:
    allow_superuser: true
valkey:
  mode: standalone
  address: 127.0.0.1:26390
  password_file: "$secrets/valkey-pass"
  tls:
    ca_file: "$certs/ca.crt"
storage:
  s3:
    bucket: trove-blobs
    endpoint: https://127.0.0.1:28333
    region: us-east-1
    force_path_style: true
    ca_file: "$certs/ca.crt"
    credentials:
      source: static
      access_key_id_file: "$secrets/s3-id"
      secret_access_key_file: "$secrets/s3-secret"
  spool:
    dir: "$state/spool"
secrets:
  encryption_key_files: ["$secrets/keys/dev.key"]
  active_key_id: dev
auth:
  # The profile's sign-in provider (test/devidp, https://localhost:19090, started by serve.sh): it signs in sam, priya
  # or ada without a password, the three kinds of user of the renders (docs/developer/web-ui.md#renders-of-every-screen).
  oidc:
    providers:
      ui-dev:
        display_name: UI development users
        issuer: https://localhost:19090
        client_id: trove-ui-dev
        client_secret_file: "$secrets/idp-client-secret"
  # The sign-in screen offers the provider and the break-glass form instead of leaving for the provider at once.
  login_page:
    auto_redirect: false
  break_glass:
    password_file: "$secrets/break-glass-password"
    show_on_login_page: true
    # Longer than the defaults (15m idle, 1h absolute), so a working session is not cut off every hour.
    idle_timeout: 8h
    absolute_timeout: 24h
proxy:
  outbound:
    # The provider is on this machine: its certificate is signed by the profile's CA, and the loopback addresses, which
    # the egress policy refuses by default (R-REPO-038), are allowed. This also lets a remote repository of this profile
    # reach a service on this machine.
    tls:
      ca_files: ["$certs/ca.crt"]
    address_policy:
      allow_cidrs: ["127.0.0.0/8", "::1/128"]
vulnerabilities:
  sources:
    # The advisory sources default to the public archives; a UI session does not download them. The npm advisories come from
    # the archive the render seed uploads into the mirror repository platform/advisory-mirror (web/e2e/renders/advisories.ts,
    # R-VULN-005), so that the screens show every grade of R-VULN-038; until it exists, a sync run fails and says so.
    osv:
      mode: repository
      repository: platform/advisory-mirror
      ecosystems: [npm]
    kev:
      mode: "off"
api:
  rate_limits:
    # make ui-renders opens three pages of one user at a time (UI_RENDERS_WORKERS): a full run reached 197 reads of one
    # user in 4 s against the default read budget of 200 per 4 s (50 a second, burst 200), and an earlier run got 429s.
    # One person in one browser stays far below the default; the renders are not one person.
    read:
      requests_per_second: 200
      burst: 800
YAML
mv "$state/trove.yaml.tmp" "$state/trove.yaml"

echo
echo "UI development services are up; configuration: $state/trove.yaml"
echo "next: make ui-dev-backend (trove on https://127.0.0.1:19080), then make ui-dev-web (https://localhost:4200/ui/)"
