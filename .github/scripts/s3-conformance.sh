#!/usr/bin/env bash
# Runs the S3 conformance tests (R-STOR-035) against the store that the TROVE_TEST_S3_*
# variables describe, and fails unless at least one Conformance test passed.
#
# Usage: s3-conformance.sh <label>
#   label   names the store in the job summary, for example "MinIO 7aac2a2c5b7c"
#
# The tests live in test/integration/blob (build tag integration, selected with
# -run Conformance). Two cases fail on purpose: no package matches the pattern (the tests
# are not in the repository yet), and go test reports success for a run in which nothing
# matched. Counting the passed Conformance tests closes that gap.
set -euo pipefail

label="${1:?usage: s3-conformance.sh <label>}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"
mkdir -p .out
report=".out/conformance.json"

if [ -z "$(go list -tags integration ./test/integration/blob/... 2>/dev/null)" ]; then
  echo "::error::no package matches ./test/integration/blob/...: the S3 conformance tests of R-STOR-035 are not in the repository yet"
  exit 1
fi

status=0
TROVE_TEST_SCOPE=s3 go test -count=1 -tags integration -run Conformance -json ./test/integration/blob/... > "$report" || status=$?

passed="$(jq -Rn '[inputs | fromjson? | select(.Action == "pass" and ((.Test // "") | test("Conformance")))] | length' "$report")"
failed="$(jq -Rn '[inputs | fromjson? | select(.Action == "fail")] | length' "$report")"

{
  echo "## S3 conformance: $label"
  echo ""
  echo "- Conformance tests passed: $passed"
  echo "- Tests failed: $failed (go test exit status $status)"
} >> "${GITHUB_STEP_SUMMARY:-/dev/null}"

if [ "$status" -ne 0 ]; then
  echo "::error::S3 conformance failed for $label; the test output follows"
  jq -Rr 'fromjson? | select(.Action == "output") | .Output' "$report" | tail -n 400
  exit "$status"
fi
if [ "$passed" -eq 0 ]; then
  echo "::error::no Conformance test passed for $label"
  exit 1
fi
echo "S3 conformance passed for $label: $passed test(s)"
