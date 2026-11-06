#!/usr/bin/env bash
# Builds trove from the working tree, applies the migrations and runs one replica of role all in the
# foreground against the services of deploy/ui-dev/up.sh. Ctrl-C stops it; run it again after a change of
# the Go code. docs/developer/web-ui.md#local-ui-development
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"

state="$root/.dev/ui-dev"
[ -f "$state/trove.yaml" ] || { echo "$state/trove.yaml is missing: run 'make ui-dev-up' first" >&2; exit 1; }

bin="$state/bin/trove"
mkdir -p "$state/bin"
CGO_ENABLED=0 go build -trimpath -o "$bin" ./cmd/trove

"$bin" migrate up --config "$state/trove.yaml"

cat <<EOF

  UI:        https://localhost:4200/ui/   (make ui-dev-web; the certificate is signed by $state/certs/ca.crt)
  sign in:   admin / $(cat "$state/secrets/break-glass-password")
  backend:   https://127.0.0.1:19080, ops https://127.0.0.1:19081

EOF
exec "$bin" serve --role all --config "$state/trove.yaml"
