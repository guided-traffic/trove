# The package-manager clients of the npm acceptance matrix (R-NPM-045): one exact version per gate row,
# installed from package-lock.json, and the forwarder that lets them use the registry's base URL
# unchanged. It is a test image: never pushed, built by test/e2e/clients/build.sh, loaded into kind by
# deploy/dev/e2e.sh. Not baked in: the dev CA (the pod mounts it, NODE_EXTRA_CA_CERTS names it) and any
# credential.
#
#   NODE_IMAGE  E2E_CLIENTS_NODE_IMAGE of tools/versions.env (the tag is web/.nvmrc; make versions-check)
#   --build-context fwd=<dir>   a directory with the static forwarder fwd-linux-<arch> (build.sh builds it)
ARG NODE_IMAGE

FROM ${NODE_IMAGE} AS install
WORKDIR /clients
COPY package.json package-lock.json ./
# --ignore-scripts keeps every package's scripts from running, then pnpm 12 alone is rebuilt: its install
# script replaces the Node launcher with the native binary of its platform package. Without that step the
# launcher may download a binary when it first runs.
RUN npm ci --ignore-scripts --no-audit --no-fund && npm rebuild pnpm12 --no-audit --no-fund

FROM ${NODE_IMAGE}
ARG TARGETARCH
# The npm of the Node image is a second npm 11; the clients are the rows of package.json and nothing
# else, so the image has no `npm`, `npx` or `corepack` on its PATH.
RUN rm -rf /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/corepack \
      /usr/local/bin/npm /usr/local/bin/npx /usr/local/bin/corepack \
 && mkdir /work && chown 1000:1000 /work
COPY --from=install /clients/node_modules /clients/node_modules
COPY bin/ /usr/local/bin/
# The one `npm` there is, on pnpm 10's PATH only (bin/pnpm10): pnpm 10 shells out to npm for whoami, view and publish.
COPY shim/ /clients/shim/
COPY --from=fwd fwd-linux-${TARGETARCH} /usr/local/bin/fwd
USER 1000:1000
WORKDIR /work
ENV HOME=/work
ENTRYPOINT ["/usr/local/bin/fwd"]
