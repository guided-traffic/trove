#!/usr/bin/env bash
# make ui-renders: every screen of the web UI as pictures, as the three kinds of user, in both colour schemes and at
# three widths, into .out/ui-renders with the contact sheet index.html (docs/developer/web-ui.md#renders-of-every-screen).
#
# Starts what is not running and stops again only what it started: the services of the UI development profile
# (up.sh, always: it is idempotent), trove with the profile's sign-in provider (serve.sh) and the Angular dev server.
# The seed and the pictures are web/e2e/playwright.renders.config.ts; arguments go to Playwright (--grep <screen>).
# Local only: no CI job runs it.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"
mkdir -p .out
backend_log="$root/.out/ui-renders.backend.log"
web_log="$root/.out/ui-renders.web.log"

deploy/ui-dev/up.sh

started=()
cleanup() {
  for pid in "${started[@]+"${started[@]}"}"; do kill -TERM "$pid" 2>/dev/null || true; done
  for pid in "${started[@]+"${started[@]}"}"; do wait "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT

wait_for() { # wait_for <what> <seconds> <url> <log> <pid>: until the URL answers; fails at once when the process ended
  local i
  for ((i = 0; i < $2; i++)); do
    curl -skf -o /dev/null "$3" && return 0
    if ! kill -0 "$5" 2>/dev/null; then
      echo "ui-renders: $1 ended before it answered at $3; the end of $4:" >&2
      tail -n 20 "$4" >&2
      return 1
    fi
    sleep 1
  done
  echo "ui-renders: $1 did not answer at $3 within $2 s (log: $4)" >&2
  return 1
}

if ! curl -skf -o /dev/null https://127.0.0.1:19081/readyz; then
  echo "ui-renders: starting trove and the sign-in provider (log: $backend_log)"
  deploy/ui-dev/serve.sh > "$backend_log" 2>&1 &
  started+=("$!")
  wait_for trove 600 https://127.0.0.1:19081/readyz "$backend_log" "$!"
fi

if ! curl -skf -o /dev/null https://localhost:4200/ui/; then
  echo "ui-renders: starting the dev server (log: $web_log)"
  # The ng process itself, not npm around it, so that the TERM of cleanup reaches it.
  (cd web && NODE_EXTRA_CA_CERTS=../.dev/ui-dev/certs/ca.crt exec node_modules/.bin/ng serve --configuration local) > "$web_log" 2>&1 &
  started+=("$!")
  wait_for "the dev server" 300 https://localhost:4200/ui/ "$web_log" "$!"
fi

cd web
# Without arguments every screen is rendered into an empty folder; with --grep the other pictures stay.
if [ "$#" -eq 0 ]; then export UI_RENDERS_CLEAN=1; fi
npx playwright test -c e2e/playwright.renders.config.ts "$@"
