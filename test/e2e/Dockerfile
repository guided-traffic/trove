# The image of the e2e driver (test/e2e/cmd/driver): a static binary on the release image's base. It
# is not the release image and is never pushed; deploy/dev/e2e.sh builds it and loads it into kind.
ARG RUNTIME_BASE_IMAGE
FROM ${RUNTIME_BASE_IMAGE}
ARG TARGETARCH
COPY driver-linux-${TARGETARCH} /driver
USER 65532:65532
ENTRYPOINT ["/driver"]
