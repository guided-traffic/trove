#!/usr/bin/env bash
# A slow Valkey (R-HA-052 "Slow Valkey"): every command of every client of the primary waits for the pause for the whole time.
# The script holds a CLIENT PAUSE ... ALL on the primary and renews it the moment it ends, from one connection inside the pod:
# the connection's own next command is held until the pause ends and then runs ahead of every command that arrived later, so
# the pause is continuous and a client's command waits at most <ms> (replicas, which the pause never holds, and the Sentinels,
# which wait for their PING reply less than down-after-milliseconds, see a slow primary, not a dead one).
#
#   valkey-slow.sh 180s 2000     for 180 s every command waits up to 2000 ms (seconds like 180, 180s or minutes like 3m; default 60s and 2000)
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
rounds=$(( secs * 1000 / ms ))
log "holding CLIENT PAUSE ${ms} ms ALL on $pod for ${secs} s (${rounds} renewals)"
kn exec "$pod" -c valkey -- sh -c '
  n=$1; ms=$2; i=0
  while [ "$i" -lt "$n" ]; do echo "client pause $ms all"; i=$((i + 1)); done |
    valkey-cli --tls --cacert /tls/ca.crt -h 127.0.0.1 -p 16379 --no-auth-warning -a "$VALKEY_PASSWORD" >/dev/null
' sh "$rounds" "$ms"
sleep $(( (ms + 999) / 1000 ))
log "the pause on $pod has ended"
