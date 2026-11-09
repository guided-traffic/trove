# One APT client of the Debian e2e set (R-DEB-035, Q-DEB-27, docs/developer/e2e.md#the-apt-clients): the official image of Debian or
# Ubuntu at its pinned digest, plus what a test pod needs and the client under test does not provide: curl (the upload of the
# publish snippet and the fetch of the key in the configure snippet), a `sudo` that runs its arguments (the snippets write
# below /etc/apt with sudo, and the pod runs as root), and the forwarder that lets the client use the repository's base URL
# unchanged. It is a test image, never pushed, built by test/e2e/aptclients/build.sh and loaded into kind by deploy/dev/e2e.sh.
# Not baked in: the dev CA (the pod mounts it; entrypoint.sh points APT and curl at it) and any credential.
#
# The client under test stays what the digest says: apt and the verifiers (gpgv, sqv) are held before curl is installed, so a
# dependency of curl that would upgrade them fails the build instead of changing the client. curl is a helper of the test and is
# installed from the image's own archive without an exact version (Q-DEB-105).
#
#   BASE_IMAGE   one of the four E2E_APTCLIENTS_*_IMAGE pins of tools/versions.env
#   --build-context fwd=<dir>   a directory with the static forwarder fwd-linux-<arch> (build.sh builds it)
ARG BASE_IMAGE
FROM ${BASE_IMAGE}
ARG TARGETARCH
RUN set -eu; \
    for p in apt gpgv sqv; do if dpkg-query -W "$p" >/dev/null 2>&1; then apt-mark hold "$p"; fi; done; \
    apt-get update; \
    apt-get install --yes --no-install-recommends curl; \
    rm -rf /var/lib/apt/lists/*
COPY --from=fwd fwd-linux-${TARGETARCH} /usr/local/bin/fwd
COPY sudo.sh /usr/local/bin/sudo
COPY entrypoint.sh /usr/local/bin/trove-entrypoint.sh
ENTRYPOINT ["/usr/local/bin/trove-entrypoint.sh"]
