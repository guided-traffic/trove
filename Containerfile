# check=skip=InvalidDefaultArgInFrom
# The release image of trove (R-ARCH-003, R-HA-033): the Angular bundle embedded into a static
# binary, on a distroless base that runs as UID 65532. Three stages:
#
#   web    the Angular build (build:embed) on the Node.js of web/.nvmrc, into internal/webui/dist
#   build  the Go binary, cross-compiled for TARGETOS/TARGETARCH on the build platform, so that no
#          emulation is needed for the other architecture (CGO_ENABLED=0)
#   final  the distroless base of RUNTIME_BASE_IMAGE (tools/versions.env), the binary and nothing else
#
# The build arguments come from the pins: make image passes them, and build.yml reads the same
# files. NODE_VERSION is web/.nvmrc, GO_VERSION the go line of go.mod, RUNTIME_BASE_IMAGE
# tools/versions.env. VERSION, COMMIT and DATE set the version line the binary prints.
# No defaults on purpose: each version is read from its pin by make image and build.yml, so a missing
# build argument fails the build instead of picking a second copy of the pin.
ARG NODE_VERSION
ARG GO_VERSION
ARG RUNTIME_BASE_IMAGE

FROM --platform=$BUILDPLATFORM node:${NODE_VERSION}-alpine AS web
WORKDIR /src
# build:embed reads the target directory ../internal/webui/dist, which holds the committed placeholder.
COPY internal/webui/dist/ internal/webui/dist/
COPY web/ web/
WORKDIR /src/web
RUN npm ci && npm run build:embed

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

FROM ${RUNTIME_BASE_IMAGE}
COPY --from=build /out/trove /usr/local/bin/trove
USER 65532:65532
EXPOSE 8080 8081
ENTRYPOINT ["/usr/local/bin/trove"]
