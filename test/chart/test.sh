#!/usr/bin/env bash
# Chart render assertions (E0.7): the Go package in this directory, behind the build tag
# "chart". Needs helm at the pinned version, no cluster. The `go test -json` stream goes to
# .out/test-chart.json, where the coverage generator reads the requirement IDs from the test
# names.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
mkdir -p .out

status=0
go test -tags chart -count=1 -json ./test/chart/... > .out/test-chart.json || status=$?

# Human-readable result: failures with their output, then the package line.
sed -n -E -e 's/.*"Output":"(([^"\\]|\\.)*)".*/\1/p' .out/test-chart.json \
  | sed -e 's/\\n/\n/g' -e 's/\\t/\t/g' -e 's/\\"/"/g' -e 's/\\\\/\\/g' \
  | grep -v -E '^(=== |--- PASS|\s+--- PASS|PASS$|$)' || true
exit "$status"
