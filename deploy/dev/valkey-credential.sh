#!/usr/bin/env bash
# A Valkey credential mismatch (R-HA-007 "Valkey rejects trove's credentials"): the password of the default user on the Valkey
# primary is changed to a value trove does not hold, and the connections of the trove pods are closed so that every
# replica meets WRONGPASS on its next handshake. This is the rotation mistake, a Secret that no longer matches the server,
# from the server's side, so that the Valkey operator (which reads the Secret) has nothing to follow. The connections of
# the Sentinels and the replica are not closed: they hold the old password and stay authenticated, so no failover starts.
#
#   valkey-credential.sh break    changes the password on both Valkey pods (the replica too, in case a failover moves the primary)
#                                  and closes the connections of the trove pods on the primary
#   valkey-credential.sh fix      sets the password of the Secret back on both pods; safe to run when nothing is broken
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_cluster

WRONG="e2e-mismatch-not-the-secret"
mode="${1:-}"
case "$mode" in break|fix) ;; *) die "usage: valkey-credential.sh break|fix" ;; esac

# acl_set <pod> <auth password> <new password> sets the default user's password; it prints nothing and succeeds only when the pod accepted the old one.
acl_set() {
  kn exec "$1" -c valkey -- sh -c '
    auth=$1; new=$2
    [ "$auth" != "-" ] || auth="$VALKEY_PASSWORD"
    [ "$new" != "-" ] || new="$VALKEY_PASSWORD"
    exec valkey-cli --tls --cacert /tls/ca.crt -h 127.0.0.1 -p 16379 --no-auth-warning -a "$auth" acl setuser default resetpass ">$new"
  ' sh "$2" "$3" >/dev/null
}

case "$mode" in
  break)
    primary="$(valkey_primary)" || die "no Sentinel reports a primary"
    ppod="${primary%%.*}"
    ips="$(kn get pods -l 'app.kubernetes.io/instance=trove,trove/serve=true' -o jsonpath='{range .items[*]}{.status.podIP}{"\n"}{end}' | tr '\n' ' ')"
    [ -n "$ips" ] || die "no trove pod has an IP"
    log "changing the password of the default user on the Valkey pods and closing the connections of the trove pods ($ips)"
    # The replica first: its replication link to the primary holds the old password and stays up.
    for pod in trove-valkey-0 trove-valkey-1; do
      [ "$pod" = "$ppod" ] || acl_set "$pod" - "$WRONG" || die "could not change the password on $pod"
    done
    # The primary: change the password, then close the trove connections over the connection that is still authenticated.
    kn exec "$ppod" -c valkey -- sh -c '
      ips=" $1 "
      cli() { valkey-cli --tls --cacert /tls/ca.crt -h 127.0.0.1 -p 16379 --no-auth-warning -a "$VALKEY_PASSWORD" "$@"; }
      ids="$(cli client list | while read -r line; do
        id="${line#id=}"; id="${id%% *}"
        addr="${line#*addr=}"; addr="${addr%% *}"; ip="${addr%:*}"
        case "$ips" in *" $ip "*) echo "$id" ;; esac
      done)"
      {
        echo "acl setuser default resetpass >'"$WRONG"'"
        for id in $ids; do echo "client kill id $id"; done
      } | cli >/dev/null
      echo "closed $(echo "$ids" | wc -w | tr -d " ") connection(s) of trove pods"
    ' sh "$ips" >&2
    ;;
  fix)
    for pod in trove-valkey-0 trove-valkey-1; do
      # the pod holds either password: the wrong one after break, the Secret's when nothing was changed
      acl_set "$pod" "$WRONG" - 2>/dev/null || acl_set "$pod" - - 2>/dev/null || log "$pod: neither password was accepted (a pod that is not there is fine)"
    done
    log "the password of the Secret is set on the Valkey pods again"
    ;;
esac
