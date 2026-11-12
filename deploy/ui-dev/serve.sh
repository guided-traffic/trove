#!/usr/bin/env bash
# Builds trove and the profile's sign-in provider (test/devidp) from the working tree, applies the migrations,
# starts the provider in the background and runs one replica of role all in the foreground against the services of
# deploy/ui-dev/up.sh. Ctrl-C stops both; run it again after a change of the Go code.
# docs/developer/web-ui.md#local-ui-development
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"

state="$root/.dev/ui-dev"
[ -f "$state/trove.yaml" ] || { echo "$state/trove.yaml is missing: run 'make ui-dev-up' first" >&2; exit 1; }

bin="$state/bin/trove"
idp="$state/bin/devidp"
mkdir -p "$state/bin"
CGO_ENABLED=0 go build -trimpath -o "$bin" ./cmd/trove
CGO_ENABLED=0 go build -trimpath -o "$idp" ./test/devidp/cmd/devidp

"$bin" migrate up --config "$state/trove.yaml"

# The sign-in provider of the profile, on the loopback address; it ends with this script.
"$idp" -listen 127.0.0.1:19090 -issuer https://localhost:19090 \
  -tls-cert "$state/certs/server.crt" -tls-key "$state/certs/server.key" \
  -client-id trove-ui-dev -client-secret-file "$state/secrets/idp-client-secret" -client-base https://localhost:4200 &
idp_pid=$!
trap 'kill "$idp_pid" 2>/dev/null || true' EXIT

cat <<EOF

  UI:        https://localhost:4200/ui/   (make ui-dev-web; the certificate is signed by $state/certs/ca.crt)
  sign in:   admin / $(cat "$state/secrets/break-glass-password"), or as sam, priya or ada through "UI development users"
  backend:   https://127.0.0.1:19080, ops https://127.0.0.1:19081

EOF
# In the background and waited for, so that Ctrl-C or a TERM (make ui-renders stops a backend it started) reaches
# trove, and the provider ends with it.
"$bin" serve --role all --config "$state/trove.yaml" &
trove_pid=$!
stop() { kill -TERM "$trove_pid" 2>/dev/null || true; wait "$trove_pid" 2>/dev/null || true; }
trap stop INT TERM
wait "$trove_pid"
