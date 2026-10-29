# Task runner for trove. GNU Make 3.81 or later (the version macOS ships).
#
# Every tool and image pin lives in tools/versions.env, which this file includes
# and CI appends to $GITHUB_ENV (E0.2). Go itself is pinned by the `go` line of
# go.mod; sqlc and oapi-codegen by the `tool` directives of go.mod.

SHELL := /bin/bash

include tools/versions.env
# Hand the pins to scripts (deploy/dev/*.sh, test/integration/*.sh).
export $(shell sed -n 's/^\([A-Z0-9_]*\)=.*/\1/p' tools/versions.env)

MODULE      := github.com/guided-traffic/trove
comma       := ,
space       := $(subst ,, )
GOVERSION   := $(shell go env GOVERSION)
GO_MOD_GO   := $(shell awk '$$1=="go"{print $$2}' go.mod)
# The binary accepts only major.minor.patch or "dev" as its version: a commit hash would
# never become ready. A v0.* tag on HEAD gives its number; every other checkout is "dev".
VERSION     ?= $(or $(shell git describe --tags --exact-match 2>/dev/null | sed -n 's/^v\(0\..*\)$$/\1/p'),dev)
COMMIT      ?= $(shell git rev-parse --verify -q HEAD 2>/dev/null || echo unknown)
DATE        ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w \
	-X $(MODULE)/internal/buildinfo.version=$(VERSION) \
	-X $(MODULE)/internal/buildinfo.commit=$(COMMIT) \
	-X $(MODULE)/internal/buildinfo.date=$(DATE)

# The linter binary's name carries its pin and the Go toolchain that built it, so
# a change of either one rebuilds it and a stale binary is never reused.
LINT_BIN    := .bin/golangci-lint-$(GOLANGCI_LINT_VERSION)-$(GOVERSION)

IMAGE       ?= trove:dev
IMAGE_ARCH  ?= $(shell go env GOARCH)

INTEGRATION_ENV := .dev/integration/env

# Go packages of the module. npm packages in web/node_modules can ship Go files;
# the `ignore` directive in go.mod keeps them out of `./...`.
PKGS        = ./...

.DEFAULT_GOAL := help

.PHONY: help
help: ## List the targets
	@awk 'BEGIN{FS=":.*## "} /^[a-zA-Z0-9_.-]+:.*## /{printf "  %-22s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

##@ Build

.PHONY: build
build: ## Build the static binary into bin/trove (CGO_ENABLED=0, -trimpath, version via ldflags)
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/trove ./cmd/trove

.PHONY: web
web: ## Build the Angular workspace into internal/webui/dist (needs Node.js, see web/.nvmrc)
	cd web && npm ci && npm run build:embed

.PHONY: image
image: web ## Build the release image (IMAGE=trove:dev) for linux/IMAGE_ARCH (host build, then a distroless wrap)
	CGO_ENABLED=0 GOOS=linux GOARCH=$(IMAGE_ARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o bin/trove-linux-$(IMAGE_ARCH) ./cmd/trove
	docker buildx build --load --platform linux/$(IMAGE_ARCH) \
		--build-arg RUNTIME_BASE_IMAGE=$(RUNTIME_BASE_IMAGE) -f Dockerfile -t $(IMAGE) bin

# Architectures of the published image; the Dockerfile copies bin/trove-linux-<arch>.
IMAGE_PLATFORMS ?= amd64 arm64

.PHONY: image-push
image-push: web ## Build the release image for IMAGE_PLATFORMS and push it with every tag in IMAGE_TAGS (CI: preview.yml)
	@test -n "$(IMAGE_TAGS)" || { echo "IMAGE_TAGS is empty: name at least one registry/repository:tag"; exit 1; }
	@mkdir -p .out
	@for arch in $(IMAGE_PLATFORMS); do \
	  echo "go build linux/$$arch"; \
	  CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o bin/trove-linux-$$arch ./cmd/trove || exit 1; \
	done
	docker buildx build --push \
		--platform $(subst $(space),$(comma),$(addprefix linux/,$(IMAGE_PLATFORMS))) \
		--build-arg RUNTIME_BASE_IMAGE=$(RUNTIME_BASE_IMAGE) \
		--label org.opencontainers.image.source=https://$(MODULE) \
		--label org.opencontainers.image.revision=$(COMMIT) \
		--label org.opencontainers.image.version=$(VERSION) \
		$(addprefix -t ,$(IMAGE_TAGS)) \
		--metadata-file .out/image-metadata.json \
		-f Dockerfile bin

##@ Code generation

.PHONY: generate
generate: ## Run every go:generate directive (config schema, sqlc, oapi-codegen)
	go generate $(PKGS)

.PHONY: generate-check
generate-check: generate ## Generate, then fail when the tree differs from the committed files (CI: E0.1)
	git diff --exit-code
	@untracked="$$(git ls-files --others --exclude-standard | grep -E '(^|/)(gen|q)/|schema\.json$$' || true)"; \
	  if [ -n "$$untracked" ]; then echo "generated files are not committed:"; echo "$$untracked"; exit 1; fi

##@ Quality

$(LINT_BIN):
	@mkdir -p .bin
	@tmp="$$(mktemp -d)"; \
	  GOBIN="$$tmp" go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) && \
	  mv "$$tmp/golangci-lint" $@ && rmdir "$$tmp"

.PHONY: lint
lint: $(LINT_BIN) ## golangci-lint (pinned, rebuilt when the pin or the Go toolchain changes) and go vet
	$(LINT_BIN) run ./...
	go vet $(PKGS)

.PHONY: fmt
fmt: $(LINT_BIN) ## Format Go code with the formatters of .golangci.yml (gofumpt, goimports)
	$(LINT_BIN) fmt ./...

.PHONY: versions-check
versions-check: ## One version per tool (E0.2): go, sqlc, oapi-codegen, golangci-lint and the local dev tools against their pins
	@set -e; fail=0; \
	have="$$(go env GOVERSION | sed 's/^go//')"; \
	if [ "$$have" != "$(GO_MOD_GO)" ]; then echo "go: running $$have, go.mod pins $(GO_MOD_GO)"; fail=1; fi; \
	want="$$(go list -m -f '{{.Version}}' github.com/sqlc-dev/sqlc)"; have="$$(go tool sqlc version)"; \
	if [ "$$have" != "$$want" ]; then echo "sqlc: running $$have, go.mod pins $$want"; fail=1; fi; \
	want="$$(go list -m -f '{{.Version}}' github.com/oapi-codegen/oapi-codegen/v2)"; have="$$(go tool oapi-codegen --version | tail -n 1)"; \
	if [ "$$have" != "$$want" ]; then echo "oapi-codegen: running $$have, go.mod pins $$want"; fail=1; fi; \
	if [ -x "$(LINT_BIN)" ]; then \
	  have="$$($(LINT_BIN) --version | sed -n 's/.*version \([^ ]*\) built with \(go[^ ]*\).*/\1 \2/p')"; \
	  if [ "$$have" != "$(GOLANGCI_LINT_VERSION:v%=%) $(GOVERSION)" ]; then echo "golangci-lint: running '$$have', pins are $(GOLANGCI_LINT_VERSION) built with $(GOVERSION)"; fail=1; fi; \
	fi; \
	if command -v kind >/dev/null; then have="$$(kind version | awk '{print $$2}')"; \
	  if [ "$$have" != "$(KIND_VERSION)" ]; then echo "kind: installed $$have, pin is $(KIND_VERSION)"; fail=1; fi; fi; \
	if command -v kubectl >/dev/null; then have="$$(kubectl version --client -o json | sed -n 's/.*"gitVersion": "\([^"]*\)".*/\1/p' | head -n 1)"; \
	  if [ "$$have" != "$(KUBECTL_VERSION)" ]; then echo "kubectl: installed $$have, pin is $(KUBECTL_VERSION)"; fail=1; fi; fi; \
	if command -v helm >/dev/null; then have="$$(helm version --template '{{.Version}}')"; \
	  if [ "$$have" != "$(HELM_VERSION)" ]; then echo "helm: installed $$have, pin is $(HELM_VERSION)"; fail=1; fi; fi; \
	if command -v k6 >/dev/null; then have="$$(k6 version | sed -n 's/^k6 v\([^ ]*\).*/v\1/p')"; \
	  if [ "$$have" != "$(K6_VERSION)" ]; then echo "k6: installed $$have, pin is $(K6_VERSION)"; fail=1; fi; fi; \
	if [ $$fail -ne 0 ]; then exit 1; fi; \
	echo "versions ok: go $$(go env GOVERSION), sqlc, oapi-codegen match go.mod"

##@ Tests

.PHONY: test
test: ## Unit tests with the race detector; go test -json goes to .out/test-unit.json for the coverage generator
	@mkdir -p .out
	@set -o pipefail; go test -race -count=1 -json $(PKGS) | tee .out/test-unit.json | $(TESTLOG)

.PHONY: integration-up
integration-up: ## Start PostgreSQL, PgBouncer, Valkey with Sentinel (TLS) and SeaweedFS; write .dev/integration/env
	test/integration/up.sh

.PHONY: integration-down
integration-down: ## Stop the integration stack and remove its volumes
	test/integration/down.sh

.PHONY: test-integration
test-integration: ## Integration tests against the running stack (make integration-up); json goes to .out/test-integration.json
	@test -f $(INTEGRATION_ENV) || { echo "$(INTEGRATION_ENV) is missing: run 'make integration-up' first"; exit 1; }
	@mkdir -p .out
	@set -o pipefail; set -a; . $(INTEGRATION_ENV); set +a; \
	  go test -count=1 -p 1 -tags integration -json ./test/integration/... | tee .out/test-integration.json | $(TESTLOG)

.PHONY: coverage
coverage: ## Regenerate the requirement coverage checklist from the committed evidence record
	go run ./internal/tools/coverage

# Both Valkey lines run, because CI passes both integration reports to the
# generator in one call (docs/developer/ci.md, job coverage-evidence).
COVERAGE_LINES := 9.0 9.1

.PHONY: coverage-refresh
coverage-refresh: test chart-test ## Recompute docs/coverage/implemented.txt from unit, chart and both integration lines
	@for line in $(COVERAGE_LINES); do \
	  TROVE_TEST_VALKEY_LINE=$$line $(MAKE) integration-up && \
	  TROVE_TEST_VALKEY_LINE=$$line $(MAKE) test-integration && \
	  cp .out/test-integration.json .out/test-integration-$$line.json || { $(MAKE) integration-down; exit 1; }; \
	done
	$(MAKE) integration-down
	go run ./internal/tools/coverage -tests .out/test-unit.json .out/test-chart.json \
	  .out/test-integration-9.0.json .out/test-integration-9.1.json

# TESTLOG prints the human-readable text of a `go test -json` stream: package
# results, failures and their output. Subtest chatter is dropped.
TESTLOG := sed -n -E -e 's/.*"Output":"(([^"\\]|\\.)*)".*/\1/p' | sed -e 's/\\n/\n/g' -e 's/\\t/\t/g' -e 's/\\"/"/g' -e 's/\\\\/\\/g' | { grep -v -E '^(=== |--- (PASS|RUN)|\s+--- PASS|PASS$$|^$$)' || true; }

##@ Chart and dev cluster (scripts are provided by the chart and dev-profile work packages)

.PHONY: chart-lint
chart-lint: ## helm lint and helm template over every value set (test/chart/lint.sh)
	test/chart/lint.sh

.PHONY: chart-test
chart-test: ## Chart render assertions of E0.7 (test/chart/test.sh)
	test/chart/test.sh

.PHONY: dev-up
dev-up: ## Create the kind cluster and install the dev profile (deploy/dev/up.sh)
	deploy/dev/up.sh

.PHONY: dev-down
dev-down: ## Delete the dev cluster (deploy/dev/down.sh)
	deploy/dev/down.sh

.PHONY: dev-failover
dev-failover: ## PostgreSQL switchover under load (deploy/dev/failover.sh)
	deploy/dev/failover.sh

.PHONY: dev-valkey-failover
dev-valkey-failover: ## Valkey Sentinel failover under load (deploy/dev/valkey-failover.sh)
	deploy/dev/valkey-failover.sh

.PHONY: dev-valkey-kill
dev-valkey-kill: ## Kill the Valkey primary under load (deploy/dev/valkey-kill.sh)
	deploy/dev/valkey-kill.sh

.PHONY: dev-valkey-outage
dev-valkey-outage: ## Stop Valkey for DURATION (default 60s) under load (deploy/dev/valkey-outage.sh)
	deploy/dev/valkey-outage.sh $(or $(DURATION),60s)

.PHONY: dev-pg-kill
dev-pg-kill: ## Force-delete the PostgreSQL primary pod, wait for the recovery (deploy/dev/pg-kill.sh)
	deploy/dev/pg-kill.sh

.PHONY: dev-pg-stop
dev-pg-stop: ## Stop PostgreSQL for DURATION (default 30s) by fencing the CloudNativePG cluster (deploy/dev/pg-stop.sh)
	deploy/dev/pg-stop.sh $(or $(DURATION),30s)

.PHONY: dev-drain
dev-drain: ## Drain a node under k6 load with cursor-persisting jobs; fails on a failed request (deploy/dev/drain.sh, E0.14)
	deploy/dev/drain.sh

.PHONY: e2e
e2e: ## End-to-end tests on the dev cluster (deploy/dev/e2e.sh); SET=push|failover|full|drain|<go test -run regexp>, default: all but the profile switch
	deploy/dev/e2e.sh '$(SET)'
