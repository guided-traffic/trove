# check=skip=InvalidDefaultArgInFrom
# The release image of trove (R-ARCH-003, R-HA-033): the Angular bundle embedded into a static
# binary, on a distroless base that runs as UID 65532. Three stages:
#
#   web    the Angular build (build:embed) on the Node.js of web/.nvmrc, into internal/webui/dist
#   build  the Go binary, cross-compiled for TARGETOS/TARGETARCH on the build platform, so that no
#          emulation is needed for the other architecture (CGO_ENABLED=0)
#   trivy  the analyser of the image scanning (R-VULN-033): the release archive of tools/versions.env, checked against its SHA-256
#   final  the distroless base of RUNTIME_BASE_IMAGE (tools/versions.env), the binary, the analyser and nothing else
#
# The build arguments come from the pins: make image passes them, and build.yml reads the same
# files. NODE_VERSION is web/.nvmrc, GO_VERSION the go line of go.mod, RUNTIME_BASE_IMAGE
# tools/versions.env. VERSION, COMMIT and DATE set the version line the binary prints.
# No defaults on purpose: each version is read from its pin by make image and build.yml, so a missing
# build argument fails the build instead of picking a second copy of the pin.
# UI_BUNDLE_MARK=1 (make image-marked) builds the second UI bundle of the upgrade test across two bundles
# (R-HA-020, Q-HA-124): the same code under other hashed names. Release builds leave it empty.
ARG NODE_VERSION
ARG GO_VERSION
ARG RUNTIME_BASE_IMAGE
ARG TRIVY_VERSION
ARG TRIVY_SHA256_LINUX_AMD64
ARG TRIVY_SHA256_LINUX_ARM64

FROM --platform=$BUILDPLATFORM node:${NODE_VERSION}-alpine AS web
ARG UI_BUNDLE_MARK=
WORKDIR /src
# build:embed reads the target directory ../internal/webui/dist, which holds the committed placeholder.
COPY internal/webui/dist/ internal/webui/dist/
COPY web/ web/
WORKDIR /src/web
RUN npm ci && TROVE_UI_BUNDLE_MARK="$UI_BUNDLE_MARK" npm run build:embed

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist/ internal/webui/dist/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false \
      -ldflags "-s -w \
        -X github.com/guided-traffic/trove/internal/buildinfo.version=${VERSION} \
        -X github.com/guided-traffic/trove/internal/buildinfo.commit=${COMMIT} \
        -X github.com/guided-traffic/trove/internal/buildinfo.date=${DATE}" \
      -o /out/trove ./cmd/trove

FROM --platform=$BUILDPLATFORM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a AS trivy
ARG TARGETARCH
ARG TRIVY_VERSION
ARG TRIVY_SHA256_LINUX_AMD64
ARG TRIVY_SHA256_LINUX_ARM64
RUN set -eu; \
    case "$TARGETARCH" in \
      amd64) arch=64bit; want="$TRIVY_SHA256_LINUX_AMD64" ;; \
      arm64) arch=ARM64; want="$TRIVY_SHA256_LINUX_ARM64" ;; \
      *) echo "no pinned analyser for $TARGETARCH"; exit 1 ;; \
    esac; \
    name="trivy_${TRIVY_VERSION#v}_Linux-${arch}.tar.gz"; \
    wget -q -O "/tmp/$name" "https://github.com/aquasecurity/trivy/releases/download/${TRIVY_VERSION}/${name}"; \
    echo "$want  /tmp/$name" | sha256sum -c -; \
    mkdir -p /out; tar -xzf "/tmp/$name" -C /out trivy

FROM ${RUNTIME_BASE_IMAGE}
COPY --from=build /out/trove /usr/local/bin/trove
COPY --from=trivy /out/trivy /usr/local/bin/trivy
USER 65532:65532
EXPOSE 8080 8081
ENTRYPOINT ["/usr/local/bin/trove"]
