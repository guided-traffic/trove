#!/usr/bin/env bash
# valkey-cli.sh <command...> runs valkey-cli as the default user against the current Valkey primary (found through
# the Sentinels), over TLS, with the password read inside the pod. The e2e tests use it for FLUSHALL (E1.11) and for
# CLIENT PAUSE, the latency injection of R-ARCH-034's circuit-breaker criterion (E1.8).
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_cluster
[ "$#" -gt 0 ] || die "usage: valkey-cli.sh <command...>"
primary="$(valkey_primary)" || die "no Sentinel reports a primary"
valkey_cli "${primary%%.*}" "$@"
