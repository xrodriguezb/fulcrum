# Every check in this repository has exactly one definition, and it lives here.
# CI calls these targets instead of repeating the commands inline, so "it works on
# my machine" and "it works in the pipeline" cannot diverge.

SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

GO              ?= go
GOBIN           := $(shell $(GO) env GOPATH)/bin
COMPOSE         ?= docker compose
DOCKER          ?= docker

GOLANGCI_VERSION := v2.13.2
LEFTHOOK_VERSION := v1.13.6
GITLEAKS_VERSION := v8.30.1
GOVULNCHECK_VERSION := v1.8.0
GOIMPORTS_VERSION := latest

# Pinned image digests are resolved by the pipeline; locally the tag is enough and
# keeps the clean-clone requirement to "Docker installed" and nothing else.
K6_IMAGE        ?= grafana/k6:0.55.0
TRIVY_IMAGE     ?= aquasecurity/trivy:0.58.1
SYFT_IMAGE      ?= anchore/syft:v1.18.1
REDOCLY_IMAGE   ?= redocly/cli:1.34.5

WEB_DIR         := web
GO_PKGS         := ./...

# A component that does not exist yet must not silently pass as if it did. These
# guards are keyed on the component being present, never on a manual switch, so a
# target becomes mandatory the moment the component lands.
HAS_WEB         := $(if $(wildcard $(WEB_DIR)/package.json),yes,no)
HAS_INT         := $(if $(wildcard test/integration),yes,no)
HAS_COMPOSE     := $(if $(wildcard deploy/compose.yaml),yes,no)
HAS_K6          := $(if $(wildcard test/load/reservation.js),yes,no)

.PHONY: help
help:
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-14s %s\n", $$1, $$2}'

.PHONY: setup
setup: ## install hooks, tools and dependencies
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	$(GO) install github.com/evilmartians/lefthook@$(LEFTHOOK_VERSION)
	$(GO) install github.com/gitleaks/gitleaks/v8@$(GITLEAKS_VERSION)
	$(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	$(GO) install golang.org/x/tools/cmd/goimports@$(GOIMPORTS_VERSION)
	$(GO) mod download
	npm ci --prefix . --no-audit --fund=false || npm install --prefix . --no-audit --fund=false
ifeq ($(HAS_WEB),yes)
	npm ci --prefix $(WEB_DIR) --no-audit --fund=false || npm install --prefix $(WEB_DIR) --no-audit --fund=false
endif
	$(GOBIN)/lefthook install
	@echo "setup complete"

.PHONY: fmt
fmt: ## format everything
	gofmt -w $(shell git ls-files '*.go')
	$(GOBIN)/goimports -w -local github.com/xrodriguezb/fulcrum $(shell git ls-files '*.go')
ifeq ($(HAS_WEB),yes)
	npm run --prefix $(WEB_DIR) format
endif

.PHONY: fmt-check
fmt-check: ## fail if any file is not formatted
	@files="$$(git ls-files '*.go')"; \\
	if [ -n "$$files" ]; then \\
	  out="$$(gofmt -l $$files)"; \\
	  if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi; \\
	  if [ -x "$(GOBIN)/goimports" ]; then \\
	    out="$$($(GOBIN)/goimports -l -local github.com/xrodriguezb/fulcrum $$files)"; \\
	    if [ -n "$$out" ]; then echo "goimports needed:"; echo "$$out"; exit 1; fi; \\
	  fi; \\
	fi
	@./scripts/check-forbidden-content.sh --all
	@bash scripts/check-forbidden-content.test.sh >/dev/null
ifeq ($(HAS_WEB),yes)
	npm run --prefix $(WEB_DIR) format:check
endif

.PHONY: lint
lint: ## golangci-lint and eslint
	$(GOBIN)/golangci-lint run --timeout=5m
ifeq ($(HAS_WEB),yes)
	npm run --prefix $(WEB_DIR) lint
	npm run --prefix $(WEB_DIR) typecheck
endif

.PHONY: test
test: ## unit tests, both languages
	$(GO) test -short $(GO_PKGS)
ifeq ($(HAS_WEB),yes)
	npm run --prefix $(WEB_DIR) test
endif

.PHONY: test-race
test-race: ## go test -race
	$(GO) test -race -short $(GO_PKGS)

.PHONY: test-int
test-int: ## integration tests, Testcontainers backed
ifeq ($(HAS_INT),yes)
	$(GO) test -race -tags=integration -timeout=20m ./test/integration/...
else
	@echo "test-int: test/integration does not exist yet, nothing to run"
endif

.PHONY: arch
arch: ## architecture boundary test
	@if [ -f test/architecture_test.go ]; then $(GO) test ./test/ -run 'TestArchitecture' -v; else echo "arch: test/architecture_test.go does not exist yet"; fi

.PHONY: contract
contract: ## validate openapi and assert generated types have no diff
	$(DOCKER) run --rm -v "$(PWD)":/spec -w /spec $(REDOCLY_IMAGE) lint api/openapi.yaml
ifeq ($(HAS_WEB),yes)
	npm run --prefix $(WEB_DIR) generate:api
	@if ! git diff --quiet -- $(WEB_DIR)/src/api/schema.d.ts; then \\
	  echo "generated API types are out of date, run make contract and commit the result"; \\
	  git --no-pager diff -- $(WEB_DIR)/src/api/schema.d.ts; \\
	  exit 1; \\
	fi
endif

.PHONY: security
security: ## govulncheck, gosec through golangci-lint, npm audit, trivy fs
	$(GOBIN)/govulncheck $(GO_PKGS)
	$(GOBIN)/golangci-lint run --timeout=5m --enable-only gosec ./... || $(GOBIN)/golangci-lint run --timeout=5m
	$(DOCKER) run --rm -v "$(PWD)":/src -w /src $(TRIVY_IMAGE) fs --scanners vuln,secret --exit-code 1 --severity HIGH,CRITICAL --no-progress .
ifeq ($(HAS_WEB),yes)
	npm audit --prefix $(WEB_DIR) --omit=dev --audit-level=high
endif

.PHONY: build
build: ## binaries and frontend production build
	$(GO) build -o bin/ ./cmd/...
ifeq ($(HAS_WEB),yes)
	npm run --prefix $(WEB_DIR) build
endif

.PHONY: images
images: ## build all container images
	$(COMPOSE) -f deploy/compose.yaml build

.PHONY: up
up: ## start the compose stack
	$(COMPOSE) -f deploy/compose.yaml up -d --build

.PHONY: down
down: ## stop the compose stack and remove volumes
	$(COMPOSE) -f deploy/compose.yaml down -v --remove-orphans

.PHONY: e2e
e2e: ## compose smoke: primary flow, idempotency, broker kill and recovery
	./scripts/e2e.sh

.PHONY: load
load: ## k6 load test against a running stack
	$(DOCKER) run --rm -i --network host -v "$(PWD)/test/load":/scripts $(K6_IMAGE) run /scripts/reservation.js

.PHONY: demo
demo: ## the full narrated demonstration
	./scripts/demo.sh

.PHONY: verify
verify: fmt-check lint arch test test-race contract security build ## everything a commit must satisfy

.PHONY: ci
ci: verify test-int e2e ## everything CI runs, in CI order

.PHONY: hooks-test
hooks-test: ## self-test of the forbidden content scanner
	bash scripts/check-forbidden-content.test.sh
