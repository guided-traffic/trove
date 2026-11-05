#!/usr/bin/env bash
# Collects the pod logs of a failed e2e run into a directory, whole and compressed, for the artifact `e2e-cluster-state-<set>`
# (the step "Cluster state (failure only)" of .github/workflows). It runs after the tests, on the cluster that is still up.
#
#   cluster-logs.sh <dir>
#
# What it keeps, per pod of the namespaces trove (every trove pod, the Valkey, PostgreSQL and test pods included) and
# ingress-controller (the edge, HAProxy Ingress):
#   log-<ns>-<pod>.txt.gz                      every container, from the start of the container's current log, no tail limit,
#                                              every line timestamped by the kubelet (--timestamps), so a failure window in the
#                                              test output can be cut out of every log by its time
#   log-<ns>-<pod>.<container>.previous.txt.gz the previous instance of a container that restarted (--previous)
# The whole window is there because the cluster is created for the job and the kubelet keeps up to containerLogMaxSize per
# container before it rotates (deploy/dev/kind.yaml raises it from 10Mi); `kubectl logs` reads only the current file. The edge
# writes one access line per request with the termination state, which names the side that ended the connection
# (deploy/dev/haproxy-values.yaml). Every log is compressed: the artifact is bounded by the size of the logs, which compress
# about tenfold, and is kept 14 days like the rest of the cluster state.
#
# Environment: TROVE_DEV_CLUSTER (default trove), as in lib.sh; the script needs only kubectl and gzip.
set -u
dir="${1:?usage: cluster-logs.sh <dir>}"
ctx="kind-${TROVE_DEV_CLUSTER:-trove}"
mkdir -p "$dir"
for ns in trove ingress-controller; do
  for p in $(kubectl --context "$ctx" -n "$ns" get pods -o name 2>/dev/null); do
    name="${p#pod/}"
    kubectl --context "$ctx" -n "$ns" logs "$p" --all-containers --timestamps 2>&1 | gzip -c > "$dir/log-$ns-$name.txt.gz" || true
    # The previous instance, per container that restarted: --previous with --all-containers stops at the first container
    # that has none.
    for c in $(kubectl --context "$ctx" -n "$ns" get "$p" -o jsonpath='{range .status.containerStatuses[?(@.restartCount>0)]}{.name}{"\n"}{end}' 2>/dev/null); do
      kubectl --context "$ctx" -n "$ns" logs "$p" -c "$c" --timestamps --previous 2>&1 | gzip -c > "$dir/log-$ns-$name.$c.previous.txt.gz" || true
    done
  done
done
