#!/bin/sh
# Starts a Sentinel. Its configuration is written once; Sentinel rewrites it.
set -eu
if [ ! -f /data/sentinel.conf ]; then
  cp /conf/sentinel.conf /data/sentinel.conf
  {
    echo "tls-port ${SENTINEL_PORT}"
    echo "sentinel announce-port ${SENTINEL_PORT}"
  } >> /data/sentinel.conf
fi
exec valkey-server /data/sentinel.conf --sentinel
