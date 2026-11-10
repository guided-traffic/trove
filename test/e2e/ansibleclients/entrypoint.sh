#!/bin/sh
# Entrypoint of an Ansible client pod (test/e2e/ansibleclients/Dockerfile): the forwarder carries 127.0.0.1:<edge port> to the in-cluster edge,
# so that the base URL of the setup snippets (https://localhost:<edge port>/api/ansible/...) works unchanged and its certificate is verified
# for `localhost`. The client trusts the dev CA through SSL_CERT_FILE, which the pod manifest sets in the container's environment (the
# commands the tests exec inherit it). The arguments are the forwarder's.
set -eu
: "${SSL_CERT_FILE:?}"
test -s "$SSL_CERT_FILE"
: >/tmp/ready
exec /usr/local/bin/fwd "$@"
