#!/usr/bin/env bash
# Downloads a GitHub release asset to a file (Q-ARCH-356): github-download.sh <asset URL> <destination>.
#
# The URL must be https://github.com/<owner>/<repo>/releases/download/<tag>/<asset>. With GITHUB_TOKEN (the job's token in
# CI) or GH_TOKEN set, the request to github.com carries it in an Authorization header. github.com does not validate the
# header on this route (measured on 2026-11-08 during an incident: a fake token and a bearer header were answered like the
# real one); the real token is sent anyway, as actions/setup-go sends it, so the request keeps working if GitHub starts to check.
#
# The request does not follow redirects. The redirect target (the storage host, a signed URL that lives 300 s) is fetched in a
# second request without the token, so the token reaches github.com and no other host, whatever curl does with redirects. If
# the second request fails, the first is repeated for a fresh redirect, up to three rounds.
#
# The whole download has a budget (900 s; TROVE_DOWNLOAD_BUDGET overrides it, for CI steps with less time and for the tests).
# Before each curl call the time left is computed, and the call's limits come from it:
#   - the first request (headers only) may take up to 120 s; its retries stop when the time left reserves the last attempt
#     (the 20 s retry delay counts), and the retry window is at most 300 s;
#   - the body request may take most of the time left: its retry window is at most 240 s (600 s unauthenticated) and is half
#     the time left at most, and its last attempt has the rest, so no attempt is capped below what the time left allows;
#   - a call with no retry window gets --retry 0, because curl reads --retry-max-time 0 as no limit.
# No call starts after the budget is spent, and no call runs past it.
#
# The token reaches curl on stdin (a pipe, not a file, not the command line). Every curl call starts with -q, so no .curlrc
# of the account (a `verbose` line there would print the header) is read. Without a token the asset is fetched unauthenticated,
# as before (local runs). The caller checks the SHA-256 of the file: this script decides only how the bytes are fetched,
# never whether they are right. The token is never printed and this script never runs with xtrace.
set +x
set -euo pipefail

url="${1:-}"
dest="${2:-}"
[ -n "$url" ] && [ -n "$dest" ] || { echo "usage: github-download.sh <asset URL> <destination>" >&2; exit 2; }
case "$url" in
  https://github.com/*/*/releases/download/*) ;;
  *) echo "github-download.sh: not a GitHub release asset URL: $url" >&2; exit 2 ;;
esac

token="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
budget="${TROVE_DOWNLOAD_BUDGET:-900}"
deadline=$(( SECONDS + budget ))
common=(--globoff --proto '=https' --proto-redir '=https' --silent --show-error --connect-timeout 20 --speed-limit 51200 --speed-time 30)
retry_delay=20

# check_time: stops the script when the budget is spent (it exits here, not in a subshell).
check_time() {
  if [ $(( deadline - SECONDS )) -le 0 ]; then
    echo "github-download.sh: the download of $url ran out of its $budget s budget" >&2
    exit 1
  fi
}

# set_retry WINDOW: the retry options for a window of WINDOW seconds; 0 means no retry at all.
set_retry() {
  if [ "$1" -gt 0 ]; then
    retry=(--retry 6 --retry-all-errors --retry-delay "$retry_delay" --retry-max-time "$1")
  else
    retry=(--retry 0)
  fi
}

# limits_first: the first request, whose answer is a few headers. One attempt takes at most 120 s, and the retries stop early
# enough that the last attempt, after its delay, ends within the time left.
limits_first() {
  check_time
  local left=$(( deadline - SECONDS )) window
  max_time=$(( left < 120 ? left : 120 ))
  window=$(( left - max_time - retry_delay ))
  [ "$window" -gt 300 ] && window=300
  set_retry "$(( window > 0 ? window : 0 ))"
}

# limits_body CAP: a request whose answer is the asset. Its retry window is the smaller of CAP and half the time left (after
# the retry delay), and its last attempt has the rest, so an attempt may take most of the time left.
limits_body() {
  check_time
  local left=$(( deadline - SECONDS )) window
  window=$(( (left - retry_delay) / 2 ))
  [ "$window" -gt "$1" ] && window="$1"
  if [ "$window" -gt 0 ]; then
    max_time=$(( left - retry_delay - window ))
  else
    max_time="$left"
    window=0
  fi
  set_retry "$window"
}

if [ -z "$token" ]; then
  limits_body 600
  curl -q "${common[@]}" --fail --location "${retry[@]}" --max-time "$max_time" -o "$tmp/asset" "$url"
  mv "$tmp/asset" "$dest"
  exit 0
fi

rounds=3
round=1
while :; do
  # The first request: the token on stdin, no redirects followed.
  limits_first
  code="$(printf 'header = "Authorization: token %s"\n' "$token" \
    | curl -q "${common[@]}" -K - --max-redirs 0 "${retry[@]}" --max-time "$max_time" \
        -D "$tmp/headers" -o "$tmp/first" -w '%{http_code}' "$url")" || code="000"
  case "$code" in
    200)
      mv "$tmp/first" "$dest"
      exit 0
      ;;
    301|302|303|307|308)
      location="$(tr -d '\r' < "$tmp/headers" | awk 'tolower($1) == "location:" { sub(/^[^:]*: */, ""); loc = $0 } END { print loc }')"
      case "$location" in
        https://*) ;;
        *) echo "github-download.sh: github.com redirected $url without an https location" >&2; exit 1 ;;
      esac
      # The second request has no token: its host is the storage host, which must not receive it. The signed URL is valid for
      # 300 s; if this request fails after its retries, the first request is repeated for a fresh one.
      limits_body 240
      if curl -q "${common[@]}" --fail --location "${retry[@]}" --max-time "$max_time" -o "$tmp/asset" "$location"; then
        mv "$tmp/asset" "$dest"
        exit 0
      fi
      if [ "$round" -ge "$rounds" ] || [ $(( deadline - SECONDS )) -le 0 ]; then
        echo "github-download.sh: the download of $url failed in $round round(s), the storage host did not answer in time" >&2
        exit 1
      fi
      echo "github-download.sh: round $round of $url failed on the storage host, requesting a new redirect" >&2
      round=$(( round + 1 ))
      ;;
    *)
      echo "github-download.sh: github.com answered HTTP $code for $url" >&2
      exit 1
      ;;
  esac
done
