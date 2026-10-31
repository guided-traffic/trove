# Task runner for trove. GNU Make 3.81 or later (the version macOS ships).
#
# Every tool and image pin lives in tools/versions.env, which this file includes
# and CI appends to $GITHUB_ENV (E0.2). Go itself is pinned by the `go` line of go.mod;
# sqlc and oapi-codegen by the `tool` directives of go.mod; Node by web/.nvmrc.

SHELL := /bin/bash

include tools/versions.env
# Hand the pins to scripts (deploy/dev/*.sh, test/integration/*.sh).
export $(shell sed -n 's/^\([A-Z0-9_]*\)=.*/\1/p' tools/versions.env)

MODULE      := github.com/guided-traffic/trove
comma       := ,
space       := $(subst ,, )
GOVERSION   := $(shell go env GOVERSION)
GO_MOD_GO   := $(shell awk '$$1=="go"{print $$2}' go.mod)
NODE_VERSION := $(shell tr -d '[:space:]' < web/.nvmrc)
# The binary accepts only major.minor.patch or "dev" as its version: a commit hash would
# never become ready. A v0.* tag on HEAD gives its number; every other checkout is "dev".
VERSION     ?= $(or $(shell git describe --tags --exact-match 2>/dev/null | sed -n 's/^v\(0\..*\)$$/\1/p'),dev)
COMMIT      ?= $(shell git rev-parse --verify -q HEAD 2>/dev/null || echo unknown)
DATE        ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w \
	-X $(MODULE)/internal/buildinfo.version=$(VERSION) \
	-X $(MODULE)/internal/buildinfo.commit=$(COMMIT) \
	-X $(MODULE)/internal/buildinfo.date=$(DATE)

# The tools' binary names carry their pin and the Go toolchain that built them, so a change
# of either one rebuilds them and a stale binary is never reused.
LINT_BIN       := .bin/golangci-lint-$(GOLANGCI_LINT_VERSION)-$(GOVERSION)
GOVULNCHECK_BIN := .bin/govulncheck-$(GOVULNCHECK_VERSION)-$(GOVERSION)
GOCYCLO_BIN    := .bin/gocyclo-$(GOCYCLO_VERSION)-$(GOVERSION)

IMAGE       ?= guidedtraffic/trove:dev
IMAGE_ARCH  ?= $(shell go env GOARCH)

INTEGRATION_ENV := .dev/integration/env
COVERAGE_DIR := .out/coverage

# Cyclomatic complexity: no function of the module's own code above CYCLO_THRESHOLD. Tests and
# the generated code (sqlc in internal/meta/postgres/q, oapi-codegen in internal/api/v1/gen) are
# not the team's to simplify. The threshold is a ratchet, as in cogpit: it is the complexity of the
# most complex function today (config's validateEndpoints, 33), and a change that simplifies that
# function lowers it to the new maximum in the same commit. The 15 of valkey-operator is the goal:
# 40 functions are above it today (docs/developer/releasing.md, Cyclomatic complexity).
CYCLO_THRESHOLD ?= 33
CYCLO_IGNORE := _test\.go|/q/|/gen/|node_modules

# Go packages of the module. npm packages in web/node_modules and root node_modules can ship Go
# files; the `ignore` directive in go.mod keeps them out of `./...`.
PKGS        = ./...

.DEFAULT_GOAL := help

.PHONY: help
help: ## List the targets
	@awk 'BEGIN{FS=":.*## "} /^[a-zA-Z0-9_.-]+:.*## /{printf "  %-22s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

##@ Build

.PHONY: build
build: ## Build the static binary into bin/trove (CGO_ENABLED=0, -trimpath, version via ldflags)
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/trove ./cmd/trove

.PHONY: web-ci
web-ci: ## npm ci in web/ (Node from web/.nvmrc; fails when package.json and the lockfile disagree)
	cd web && npm ci

.PHONY: web-lint
web-lint: ## ESLint and the R-UI-046 rules in web/ (after web-ci)
	cd web && npm run lint

.PHONY: web-test
web-test: ## Angular unit tests in web/ (after web-ci)
	cd web && npm test

.PHONY: web-embed
web-embed: ## Build the Angular workspace and embed it into internal/webui/dist (after web-ci)
	cd web && npm run build:embed

.PHONY: web
web: web-ci web-embed ## web-ci, then web-embed (what a local build of the UI needs)

# The release image builds the Angular bundle itself (Containerfile, stage web); no Node.js is
# needed on the host. The build arguments read the same pins as the rest of the repository.
.PHONY: image
image: ## Build the release image (IMAGE, VERSION, IMAGE_ARCH) for linux/IMAGE_ARCH with the Containerfile, loaded into the local Docker
	docker buildx build --load --platform linux/$(IMAGE_ARCH) \
		--build-arg NODE_VERSION=$(NODE_VERSION) \
		--build-arg GO_VERSION=$(GO_MOD_GO) \
		--build-arg RUNTIME_BASE_IMAGE=$(RUNTIME_BASE_IMAGE) \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg DATE=$(DATE) \
		-f Containerfile -t $(IMAGE) .

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

$(GOVULNCHECK_BIN):
	@mkdir -p .bin
	@tmp="$$(mktemp -d)"; \
	  GOBIN="$$tmp" go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) && \
	  mv "$$tmp/govulncheck" $@ && rmdir "$$tmp"

$(GOCYCLO_BIN):
	@mkdir -p .bin
	@tmp="$$(mktemp -d)"; \
	  GOBIN="$$tmp" go install github.com/fzipp/gocyclo/cmd/gocyclo@$(GOCYCLO_VERSION) && \
	  mv "$$tmp/gocyclo" $@ && rmdir "$$tmp"

.PHONY: lint
lint: $(LINT_BIN) ## golangci-lint (pinned, rebuilt when the pin or the Go toolchain changes; gosec among its linters) and go vet
	$(LINT_BIN) run ./...
	go vet $(PKGS)

.PHONY: fmt
fmt: $(LINT_BIN) ## Format Go code with the formatters of .golangci.yml (gofumpt, goimports)
	$(LINT_BIN) fmt ./...

# gosec runs through golangci-lint, at the pinned golangci-lint version, with the gosec settings of
# .golangci.yml: the accepted findings carry their reason there or at the line (//nolint:gosec). One
# gosec version, the one golangci-lint bundles, so the repository has no second copy to disagree.
.PHONY: gosec
gosec: $(LINT_BIN) ## gosec over the module through the pinned golangci-lint (CI: GoSec Security Scan)
	$(LINT_BIN) run --enable-only=gosec ./...

.PHONY: vuln
vuln: $(GOVULNCHECK_BIN) ## govulncheck (pinned) over the module (CI: govulncheck)
	GOFLAGS="-buildvcs=false" $(GOVULNCHECK_BIN) ./...

.PHONY: cyclo
cyclo: $(GOCYCLO_BIN) ## gocyclo (pinned): fails on any function above CYCLO_THRESHOLD (CI: cyclomatic-complexity)
	@over="$$($(GOCYCLO_BIN) -over $(CYCLO_THRESHOLD) -ignore '$(CYCLO_IGNORE)' .)"; \
	if [ -n "$$over" ]; then \
	  echo "$$over"; \
	  echo "functions above the complexity threshold $(CYCLO_THRESHOLD) (CYCLO_THRESHOLD in the Makefile)"; \
	  exit 1; \
	fi; \
	echo "every function is at or below the complexity threshold $(CYCLO_THRESHOLD)"

.PHONY: cyclo-report
cyclo-report: $(GOCYCLO_BIN) ## The 20 most complex functions, tests included
	@$(GOCYCLO_BIN) -top 20 .

# ClamAV over the source tree, as valkey-operator's malware-scan job runs it. The build outputs
# (.out, .bin, .dev, bin) and node_modules are not source. The report is kept in .out.
.PHONY: malware-scan
malware-scan: ## ClamAV over the source tree; fails on any infected file or when the scan does not finish (CI: malware-scan; needs clamscan)
	@command -v clamscan >/dev/null || { echo "clamscan is not on PATH: install ClamAV (CI installs the clamav package)"; exit 1; }
	@mkdir -p .out
	@set -o pipefail; clamscan --recursive --infected \
	  --exclude-dir='(^|/)(\.git|node_modules|\.out|\.bin|\.dev|bin)$$' . | tee .out/clamav-report.txt
	@grep -q 'Infected files: 0' .out/clamav-report.txt || { echo "malware scan found files or did not finish: see .out/clamav-report.txt"; exit 1; }
	@echo "no malware in the source tree"

.PHONY: test-release-tooling
test-release-tooling: ## Release tooling: npm ci, npm audit signatures, and the release notes of .releaserc.json rendered (CI: release-tooling; needs node and npm)
	npm ci --no-fund
	npm audit signatures
	node hack/verify-release-tooling.mjs

# One version per tool (E0.2): every pin of a tool that is installed on PATH is compared with the
# pin, so a copy of another version can never be the one a check ran with.
.PHONY: versions-check
versions-check: ## One version per tool (E0.2): go, Node, sqlc, oapi-codegen, the linters, the CI tools and the npm client pins against their pins
	@set -e; fail=0; \
	have="$$(go env GOVERSION | sed 's/^go//')"; \
	if [ "$$have" != "$(GO_MOD_GO)" ]; then echo "go: running $$have, go.mod pins $(GO_MOD_GO)"; fail=1; fi; \
	have="$$(node --version | sed 's/^v//')"; \
	if [ "$$have" != "$(NODE_VERSION)" ]; then echo "node: running $$have, web/.nvmrc pins $(NODE_VERSION)"; fail=1; fi; \
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
	if docker buildx version >/dev/null 2>&1; then have="$$(docker buildx version | awk '{print $$2}')"; \
	  if [ "$$have" != "$(BUILDX_VERSION)" ]; then echo "docker buildx: installed $$have, pin is $(BUILDX_VERSION)"; fail=1; fi; fi; \
	node test/e2e/clients/check-pins.mjs "$(NODE_VERSION)" "$(E2E_CLIENTS_NODE_IMAGE)" || fail=1; \
	if [ $$fail -ne 0 ]; then exit 1; fi; \
	echo "versions ok: go $$(go env GOVERSION), node $(NODE_VERSION), sqlc, oapi-codegen match their pins"

##@ Tests

# The unit tests run under the race detector and write two things for the run's reports:
# .out/test-unit.json (go test -json, the requirement IDs of the test names) and the coverage
# profile of the unit tier (merged by `make coverage-report`).
.PHONY: test
test: ## Unit tests with the race detector; .out/test-unit.json for the coverage generator, .out/coverage/unit.out for the report
	@mkdir -p .out $(COVERAGE_DIR)
	@set -o pipefail; go test -race -count=1 -covermode=atomic -coverpkg=./... \
	  -coverprofile=$(COVERAGE_DIR)/unit.out -json $(PKGS) | tee .out/test-unit.json | $(TESTLOG)

.PHONY: integration-up
integration-up: ## Start PostgreSQL, PgBouncer, Valkey with Sentinel (TLS) and SeaweedFS; write .dev/integration/env
	test/integration/up.sh

.PHONY: integration-down
integration-down: ## Stop the integration stack and remove its volumes
	test/integration/down.sh

# The coverage profile of the line is named by the Valkey line it ran against, so the two lines
# of the matrix merge into one report.
.PHONY: test-integration
test-integration: ## Integration tests against the running stack (make integration-up); .out/test-integration.json and .out/coverage/integration-<line>.out
	@test -f $(INTEGRATION_ENV) || { echo "$(INTEGRATION_ENV) is missing: run 'make integration-up' first"; exit 1; }
	@mkdir -p .out $(COVERAGE_DIR)
	@set -o pipefail; set -a; . $(INTEGRATION_ENV); set +a; \
	  go test -count=1 -p 1 -tags integration -covermode=atomic -coverpkg=./... \
	    -coverprofile=$(COVERAGE_DIR)/integration-$${TROVE_TEST_VALKEY_LINE:-9.1}.out \
	    -json ./test/integration/... | tee .out/test-integration.json | $(TESTLOG)

# The latency bounds of P-13 and P-14 with Valkey, tail included (Q-ARCH-220): the per-change tier asserts
# delivery and the p50; the p99 bound is enforced here, by the nightly Performance workflow.
.PHONY: test-performance
test-performance: ## P-13 and P-14 latency tests with the p99 bounds enforced (make integration-up); .out/test-performance.json
	@test -f $(INTEGRATION_ENV) || { echo "$(INTEGRATION_ENV) is missing: run 'make integration-up' first"; exit 1; }
	@mkdir -p .out
	@set -o pipefail; set -a; . $(INTEGRATION_ENV); set +a; \
	  TROVE_PERF_GATE=1 go test -count=1 -p 1 -tags integration -run 'TestChangePropagation|TestJobPickup' \
	    -json ./test/integration/coordination/... | tee .out/test-performance.json | $(TESTLOG)

# S3 conformance against MinIO, the leg that used to run nightly: MinIO is built from source at
# MINIO_COMMIT and served over TLS only (INV-20). The tests are the same as against any store.
.PHONY: minio-up
minio-up: ## Build MinIO at MINIO_COMMIT, serve it over TLS and create the buckets; writes .dev/minio/env
	test/integration/minio.sh up

.PHONY: minio-down
minio-down: ## Stop the MinIO started by minio-up
	test/integration/minio.sh down

.PHONY: s3-conformance
s3-conformance: ## S3 conformance tests (R-STOR-035) against the MinIO of minio-up (CI: s3-conformance)
	@test -f .dev/minio/env || { echo ".dev/minio/env is missing: run 'make minio-up' first"; exit 1; }
	@set -a; . .dev/minio/env; set +a; \
	  .github/scripts/s3-conformance.sh "MinIO at $$(echo $(MINIO_COMMIT) | cut -c1-12)"

.PHONY: coverage
coverage: ## Regenerate the requirement coverage checklist from the committed evidence record
	go run ./internal/tools/coverage

.PHONY: coverage-refresh
coverage-refresh: test chart-test ## Recompute docs/coverage/implemented.txt from the unit, chart and integration reports
	$(MAKE) integration-up
	$(MAKE) test-integration || { $(MAKE) integration-down; exit 1; }
	$(MAKE) integration-down
	go run ./internal/tools/coverage -tests .out/test-unit.json .out/test-chart.json .out/test-integration.json

# Merges the profiles of one run (unit and each integration line) into the combined coverage of
# the repository: coverage.txt, summary.md for the job summary, and coverage.json, the badge.
.PHONY: coverage-report
coverage-report: ## Merge .out/coverage/unit.out and integration-*.out: coverage.txt, summary.md, coverage.json (the badge)
	test/coverage/report.sh $(COVERAGE_DIR)

# TESTLOG prints the human-readable text of a `go test -json` stream: package
# results, failures and their output. Subtest chatter is dropped.
TESTLOG := sed -n -E -e 's/.*"Output":"(([^"\\]|\\.)*)".*/\1/p' | sed -e 's/\\n/\n/g' -e 's/\\t/\t/g' -e 's/\\"/"/g' -e 's/\\\\/\\/g' | { grep -v -E '^(=== |--- (PASS|RUN)|\s+--- PASS|PASS$$|^$$)' || true; }

##@ Chart and dev cluster (scripts are provided by the chart and dev-profile work packages)

.PHONY: chart-lint
chart-lint: ## helm lint and helm template over every value set (test/chart/lint.sh)
	test/chart/lint.sh

.PHONY: chart-test
chart-test: ## Chart render assertions of E0.7 (test/chart/test.sh); .out/test-chart.json
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

.PHONY: dev-valkey-stop
dev-valkey-stop: ## Stop Valkey and the Sentinels until dev-valkey-start (deploy/dev/valkey-stop.sh)
	deploy/dev/valkey-stop.sh

.PHONY: dev-valkey-start
dev-valkey-start: ## End the outage of dev-valkey-stop (deploy/dev/valkey-start.sh)
	deploy/dev/valkey-start.sh

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
e2e: ## End-to-end tests on the dev cluster (deploy/dev/e2e.sh); SET=push|failover|access|full|drain|npm|npm-closing|npm-ha|npm-ha-closing|<go test -run regexp>, default: all but the profile switch
	deploy/dev/e2e.sh '$(SET)'

# >>> M2 npm-clients
.PHONY: npm-clients-image
npm-clients-image: ## Build the image of the npm client matrix (R-NPM-045: npm 10-12, pnpm 10-12, Yarn 4, Yarn 1) into the local Docker; NPM_CLIENTS_IMAGE, default trove-e2e-clients:dev
	test/e2e/clients/build.sh $(or $(NPM_CLIENTS_IMAGE),trove-e2e-clients:dev)
# <<< M2 npm-clients

# >>> M2 npm-fuzz
# The fuzz run of the closing phase (roadmap M2, E2.6): npm's Match and Inspect with the R-ARCH-028 limits checked on
# every input. The workflow runs three legs (FUZZ=FuzzR_ARCH_028_Match, FuzzR_ARCH_028_Inspect, FuzzMatch) for 30 minutes
# each when it is dispatched with closing=true (Q-NPM-46). Locally: make fuzz-npm FUZZ=FuzzMatch FUZZTIME=1m.
FUZZ ?= FuzzR_ARCH_028_Match
FUZZTIME ?= 30m

.PHONY: fuzz-npm
fuzz-npm: ## Fuzz one npm target (FUZZ, default FuzzR_ARCH_028_Match) for FUZZTIME (default 30m); a failing input stays in internal/formats/npm/testdata/fuzz
	go test ./internal/formats/npm -run '^$$' -fuzz '^$(FUZZ)$$' -fuzztime $(FUZZTIME)
# <<< M2 npm-fuzz

# >>> M2 npm-stub
# The counting test upstream of the npm tests (test/npmstub, docs/developer/e2e.md) and the reference
# lockfile resolved against it (perf/npm, perf/README.md). Edit only between the markers.
NPM_STUB_IMAGE ?= trove-e2e-npmstub:dev

.PHONY: npm-stub-image
npm-stub-image: ## Build the counting npm test upstream image (NPM_STUB_IMAGE, IMAGE_ARCH) on the release base image
	@mkdir -p .out/npmstub
	CGO_ENABLED=0 GOOS=linux GOARCH=$(IMAGE_ARCH) go build -trimpath -ldflags '-s -w' -o .out/npmstub/npmstub-linux-$(IMAGE_ARCH) ./test/npmstub/cmd/npmstub
	docker buildx build --load --platform linux/$(IMAGE_ARCH) --build-arg RUNTIME_BASE_IMAGE=$(RUNTIME_BASE_IMAGE) \
	  -f test/npmstub/Containerfile -t $(NPM_STUB_IMAGE) .out/npmstub

.PHONY: npm-reference
npm-reference: npm-stub-image ## Regenerate perf/npm/reference (package.json, package-lock.json) with the pinned Node image's npm against the stub
	NPM_STUB_IMAGE=$(NPM_STUB_IMAGE) perf/npm/reference.sh

.PHONY: npm-reference-check
npm-reference-check: npm-stub-image ## Fail when perf/npm/reference differs from a regeneration (the corpus is deterministic)
	NPM_STUB_IMAGE=$(NPM_STUB_IMAGE) perf/npm/reference.sh --check
# <<< M2 npm-stub
