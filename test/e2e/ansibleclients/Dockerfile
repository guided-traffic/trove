# One ansible-galaxy client of the Ansible e2e set (R-ANS-032, Q-ANS-23, Q-ANS-49): the official Python image at its pinned digest, the exact
# ansible-core release under test with every dependency at the exact version of its constraints file, plus what a test pod needs and the
# client under test does not provide: curl (the upload through /files/), and the forwarder that lets the client use the repository's base
# URL unchanged. It is a test image, never pushed, built by test/e2e/ansibleclients/build.sh and loaded into kind by deploy/dev/e2e.sh.
# Not baked in: the dev CA (the pod mounts it; the pod's environment points the client's TLS at it) and any credential.
#
# The Python is the one each release supports: 2.17 runs on 3.10 to 3.12 (3.11 here), 2.21 on 3.12 to 3.14 (3.13 here).
#
#   BASE_IMAGE            one of the E2E_ANSIBLECLIENTS_*_IMAGE pins of tools/versions.env
#   ANSIBLE_CORE_VERSION  the E2E_ANSIBLECLIENTS_*_VERSION pin of the same client
#   ANSIBLE_SERIES        the series of the release (2.17, 2.21): the constraints file of the image is constraints-<series>.txt
#   --build-context fwd=<dir>   a directory with the static forwarder fwd-linux-<arch> (build.sh builds it)
ARG BASE_IMAGE
FROM ${BASE_IMAGE}
ARG TARGETARCH
ARG ANSIBLE_CORE_VERSION
ARG ANSIBLE_SERIES
ENV PYTHONDONTWRITEBYTECODE=1 ANSIBLE_NOCOLOR=1 PIP_DISABLE_PIP_VERSION_CHECK=1
COPY constraints-${ANSIBLE_SERIES}.txt /tmp/constraints.txt
RUN set -eu; \
    pip install --no-cache-dir --constraint /tmp/constraints.txt "ansible-core==${ANSIBLE_CORE_VERSION}"; \
    rm /tmp/constraints.txt; \
    apt-get update; \
    apt-get install --yes --no-install-recommends curl; \
    rm -rf /var/lib/apt/lists/*
COPY --from=fwd fwd-linux-${TARGETARCH} /usr/local/bin/fwd
COPY clean.sh /usr/local/bin/trove-clean
COPY entrypoint.sh /usr/local/bin/trove-entrypoint.sh
ENTRYPOINT ["/usr/local/bin/trove-entrypoint.sh"]
