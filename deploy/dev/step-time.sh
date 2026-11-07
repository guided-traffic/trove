#!/usr/bin/env bash
# step-time.sh <label> <command...>: runs the command and appends "<label><TAB><seconds><TAB><exit code>" to
# .out/release-smoke/times.tsv, whether it succeeded or not, and exits with the command's status. The post-release check
# (build.yml, job smoke-published) wraps its long steps in it for the wall times of the job summary; the GitHub API
# would give them too, but its job listing needs a token permission the job does not hold.
set -uo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
label="${1:?usage: step-time.sh <label> <command...>}"; shift
mkdir -p "$root/.out/release-smoke"
start="$(date +%s)"
"$@"
status=$?
printf '%s\t%s\t%s\n' "$label" "$(( $(date +%s) - start ))" "$status" >> "$root/.out/release-smoke/times.tsv"
exit "$status"
