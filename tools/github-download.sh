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
# the second request fails, the first is repeated for a fresh redirect, up to three rounds and 900 s in all.
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
# Common options: no configuration file of the account, no glob expansion of the URL, https only (also on redirects).
common=(--globoff --proto '=https' --proto-redir '=https' --silent --show-error --connect-timeout 20 --speed-limit 51200 --speed-time 30)
# Unauthenticated: six retries, 20 s apart, and 600 s of retries in all (transient 502 to 504 and the stalls of the options above).
if [ -z "$token" ]; then
  curl -q "${common[@]}" --fail --location --retry 6 --retry-all-errors --retry-delay 20 --retry-max-time 600 \
    -o "$tmp/asset" "$url"
  mv "$tmp/asset" "$dest"
  exit 0
fi

rounds=3
deadline=$(( SECONDS + 900 ))
round=1
while :; do
  # The first request: the token on stdin, no redirects followed, retries of 300 s in all.
  code="$(printf 'header = "Authorization: token %s"\n' "$token" \
    | curl -q "${common[@]}" -K - --max-redirs 0 --retry 6 --retry-all-errors --retry-delay 20 --retry-max-time 300 \
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
      if curl -q "${common[@]}" --fail --location --retry 6 --retry-all-errors --retry-delay 20 --retry-max-time 240 \
           -o "$tmp/asset" "$location"; then
        mv "$tmp/asset" "$dest"
        exit 0
      fi
      if [ "$round" -ge "$rounds" ] || [ "$SECONDS" -ge "$deadline" ]; then
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
