#!/usr/bin/env bash
# A slow Valkey (R-HA-052 "Slow Valkey"): the primary holds every command of its clients for up to <ms> at a time.
# The script holds a CLIENT PAUSE <ms> ALL on the primary, leaves 300 ms open and pauses again, from one connection inside the
# pod, for the whole time. A command that arrives in a pause waits for its end; the open 300 ms let the held commands run, and
# the Sentinels' PING gets its reply at the latest <ms> + 300 ms after it was sent, below down-after-milliseconds, so they see a
# slow primary, not a dead one. (A pause renewed with no gap starves every client, the Sentinels included: the first version
# of the script made them declare the primary down after 6 s.) Replicas are never held by a pause.
#
#   valkey-slow.sh 180s 2000     for 180 s the primary holds its clients' commands for up to 2000 ms at a time (seconds like 180, 180s or minutes like 3m; default 60s and 2000)
#
# The script returns when the last pause has ended.
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_cluster

arg="${1:-60s}"
ms="${2:-2000}"
case "$arg" in
  *m) secs=$(( ${arg%m} * 60 )) ;;
  *s) secs="${arg%s}" ;;
  *) secs="$arg" ;;
esac
case "$secs" in ''|*[!0-9]*) die "duration '$arg' is not seconds (180, 180s) or minutes (3m)" ;; esac
case "$ms" in ''|*[!0-9]*) die "delay '$ms' is not milliseconds" ;; esac
[ "$ms" -ge 100 ] && [ "$ms" -lt 5000 ] || die "the delay must be below the Sentinels' down-after-milliseconds (5000) and at least 100"

primary="$(valkey_primary)" || die "no Sentinel reports a primary"
pod="${primary%%.*}"
rounds=$(( secs * 1000 / (ms + 300) ))
gap="$(awk "BEGIN { print ($ms + 300) / 1000 }")"
log "holding CLIENT PAUSE ${ms} ms ALL on $pod for ${secs} s (${rounds} renewals)"
kn exec "$pod" -c valkey -- sh -c '
  n=$1; ms=$2; gap=$3; i=0
  while [ "$i" -lt "$n" ]; do echo "client pause $ms all"; sleep "$gap"; i=$((i + 1)); done |
    valkey-cli --tls --cacert /tls/ca.crt -h 127.0.0.1 -p 16379 --no-auth-warning -a "$VALKEY_PASSWORD" >/dev/null
' sh "$rounds" "$ms" "$gap"
sleep $(( (ms + 999) / 1000 ))
log "the pause on $pod has ended"
