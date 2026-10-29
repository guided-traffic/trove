# The release image wraps a binary that `make image` built on the host with the
# Angular build embedded (R-ARCH-003). It holds nothing else: a static binary on
# a distroless base that runs as UID 65532 (R-HA-033).
ARG RUNTIME_BASE_IMAGE
FROM ${RUNTIME_BASE_IMAGE}
ARG TARGETARCH
COPY trove-linux-${TARGETARCH} /usr/local/bin/trove
USER 65532:65532
EXPOSE 8080 8081
ENTRYPOINT ["/usr/local/bin/trove"]
