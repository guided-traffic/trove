#!/bin/sh
# Starts a data node. The configuration is written once: Sentinel rewrites it
# during a failover, and a restarted container must keep what Sentinel decided.
set -eu
if [ ! -f /data/valkey.conf ]; then
  cp /conf/users.acl /data/users.acl
  cp /conf/valkey-node.conf /data/valkey.conf
  {
    echo "tls-port ${NODE_PORT}"
    echo "replica-announce-port ${NODE_PORT}"
    [ -n "${REPLICAOF:-}" ] && echo "replicaof ${REPLICAOF}"
  } >> /data/valkey.conf
fi
exec valkey-server /data/valkey.conf
