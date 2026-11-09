#!/usr/bin/env bash
# Fetches the `go test -json` reports of the end-to-end legs (and of the nightly latency gates) that did not run in this workflow
# run (Q-ROAD-11):
#
#   fetch-e2e-reports.sh <reports of this run> <destination>
#
# The coverage evidence job reads the reports of every test tier. The kind legs of Test and Release are path-filtered (a push may
# run none of them) and the long legs run in the nightly workflow, so for every leg whose report artifact is not among the
# reports of this run, the newest report of that leg on main is taken, whichever workflow wrote it: the last completed runs of
# release.yml and performance.yml are listed, and each artifact named like `e2e[-<group>]-report[-<set>]` is a candidate. Of
# several candidates with one name the newest wins. Each report lands in <destination>/<artifact name>/.
#
# A leg that has no unexpired report (artifacts live 30 days) is simply not fetched: the requirements only that leg proves are then
# not proven, and the evidence job says so. This script never decides what a report proves.
#
# Environment: GITHUB_TOKEN (permission actions: read), GITHUB_REPOSITORY, GITHUB_RUN_ID, GITHUB_API_URL (default https://api.github.com).
# TROVE_E2E_REPORT_RUNS_RELEASE (default 40) and TROVE_E2E_REPORT_RUNS_NIGHTLY (default 14) bound how many runs are searched.
set +x
set -euo pipefail

here="${1:-}"
dest="${2:-}"
[ -n "$here" ] && [ -n "$dest" ] || { echo "usage: fetch-e2e-reports.sh <reports of this run> <destination>" >&2; exit 2; }
: "${GITHUB_TOKEN:?GITHUB_TOKEN is not set}"
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is not set}"
: "${GITHUB_RUN_ID:?GITHUB_RUN_ID is not set}"
api="${GITHUB_API_URL:-https://api.github.com}"
# The reports of the kind legs, and the one of the latency gates of the nightly (make test-performance).
name_re='^(e2e(-[a-z]+)*-report(-.+)?|test-performance)$'

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# The token goes to curl on stdin as a config, not on the command line.
call() {
  printf 'header = "Authorization: Bearer %s"\n' "$GITHUB_TOKEN" |
    curl -q -fsSL --retry 3 --retry-delay 5 --config - -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2022-11-28' "$@"
}

: > "$tmp/candidates.jsonl"
for entry in "release.yml:${TROVE_E2E_REPORT_RUNS_RELEASE:-40}" "performance.yml:${TROVE_E2E_REPORT_RUNS_NIGHTLY:-14}"; do
  workflow="${entry%%:*}"
  count="${entry##*:}"
  call "$api/repos/$GITHUB_REPOSITORY/actions/workflows/$workflow/runs?branch=main&status=completed&per_page=$count" |
    jq -r --arg self "$GITHUB_RUN_ID" '.workflow_runs[] | select((.id | tostring) != $self) | .id' > "$tmp/runs.txt"
  while read -r run; do
    page=1
    while :; do
      call "$api/repos/$GITHUB_REPOSITORY/actions/runs/$run/artifacts?per_page=100&page=$page" > "$tmp/page.json"
      jq -c --arg re "$name_re" --arg run "$run" \
        '.artifacts[] | select(.expired | not) | select(.name | test($re)) | {name, id, created_at, run: $run}' "$tmp/page.json" >> "$tmp/candidates.jsonl"
      [ "$(jq '.artifacts | length' "$tmp/page.json")" -eq 100 ] || break
      page=$((page + 1))
    done
  done < "$tmp/runs.txt"
done

# The newest candidate of each name.
jq -s -c 'group_by(.name) | map(max_by(.created_at)) | .[]' "$tmp/candidates.jsonl" > "$tmp/newest.jsonl"

mkdir -p "$dest"
fetched=0
while read -r c; do
  name="$(jq -r .name <<<"$c")"
  if [ -d "$here/$name" ]; then
    continue
  fi
  id="$(jq -r .id <<<"$c")"
  run="$(jq -r .run <<<"$c")"
  call -o "$tmp/artifact.zip" "$api/repos/$GITHUB_REPOSITORY/actions/artifacts/$id/zip"
  mkdir -p "$dest/$name"
  unzip -q -o "$tmp/artifact.zip" -d "$dest/$name"
  echo "carried: $name from run $run ($(jq -r .created_at <<<"$c"))"
  fetched=$((fetched + 1))
done < "$tmp/newest.jsonl"
echo "carried reports: $fetched"
