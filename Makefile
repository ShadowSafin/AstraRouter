# CoreRouter developer entry points.
#
# Every target is a thin wrapper over a command the documentation also shows, so
# reading this file is a reasonable way to learn how the pieces are built. Nothing
# here is required: the gateway is `go build`, the workers are `python -m`, and the
# dashboard is `npm`.
#
# The Go toolchain is resolved through `go` on PATH. If Go is not installed,
# `make go` downloads the module's pinned toolchain through GOTOOLCHAIN.

SHELL := /bin/bash

MODULE      := github.com/shadowsafin/corerouter
CMD         := ./cmd/corerouter
BIN_DIR     := bin
GO          ?= go
PYTHON      ?= python3
NPM         ?= npm

# Stamped into the binary and exported as corerouter_build_info.
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT) \
	-X $(MODULE)/internal/version.BuildDate=$(BUILD_DATE)

.DEFAULT_GOAL := help
.PHONY: help build install run config migrate test test-go test-workers lint fmt vet \
        tidy dashboard-install dashboard-build dashboard-typecheck up down logs \
        ps smoke smoke-ps clean docker-build

help: ## Show this help.
	@awk 'BEGIN {FS = ":.*?## "; printf "\nCoreRouter\n\nUsage: make <target>\n\nTargets:\n"} \
	     /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@echo

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

build: ## Build the gateway binary into ./bin.
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/corerouter $(CMD)
	@echo "built $(BIN_DIR)/corerouter ($(VERSION))"

install: ## Install the gateway into $GOBIN (or $GOPATH/bin).
	$(GO) install -trimpath -ldflags "$(LDFLAGS)" $(CMD)

run: ## Run the gateway against the local config and datastores.
	$(GO) run $(CMD) serve

config: ## Print the resolved configuration with secrets redacted.
	$(GO) run $(CMD) config

migrate: ## Apply pending database migrations.
	$(GO) run $(CMD) migrate

docker-build: ## Build the gateway image.
	docker build -f deploy/docker/Dockerfile.gateway \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t corerouter-gateway:$(VERSION) .

# ---------------------------------------------------------------------------
# Quality
# ---------------------------------------------------------------------------

fmt: ## Format Go and Python sources, then report anything still unformatted.
	$(GO) fmt ./internal/... ./cmd/...
	@command -v gofmt >/dev/null && test -z "$$(gofmt -l ./internal ./cmd)" \
		|| (echo "gofmt: unformatted files" && gofmt -l ./internal ./cmd && exit 1)
	@command -v ruff >/dev/null && (cd workers && ruff format . && ruff check --fix .) \
		|| echo "ruff not installed; skipping Python formatting"

vet: ## Run go vet over every package.
	$(GO) vet ./...

tidy: ## Tidy the module graph.
	$(GO) mod tidy

lint: ## Run go vet plus the configured linters.
	$(GO) vet ./...
	@command -v golangci-lint >/dev/null && golangci-lint run ./... \
		|| echo "golangci-lint not installed; skipping"
	@command -v ruff >/dev/null && (cd workers && ruff check . && ruff format --check .) \
		|| echo "ruff not installed; skipping Python lint"
	@command -v mypy >/dev/null && (cd workers && mypy corerouter_workers) \
		|| echo "mypy not installed; skipping Python types"

test: test-go test-workers ## Run every test suite.

test-go: ## Run the Go test suite.
	$(GO) test ./...

test-workers: ## Run the Python worker test suite.
	cd workers && $(PYTHON) -m unittest discover -s tests -t . -v

# ---------------------------------------------------------------------------
# Dashboard
# ---------------------------------------------------------------------------

dashboard-install: ## Install dashboard dependencies from the lockfile.
	cd dashboard && $(NPM) ci

dashboard-build: ## Compile the dashboard for production.
	cd dashboard && $(NPM) run build

dashboard-typecheck: ## Typecheck the dashboard without emitting.
	cd dashboard && $(NPM) run typecheck

# ---------------------------------------------------------------------------
# Full stack (requires Docker)
# ---------------------------------------------------------------------------

up: ## Start the whole stack in the background.
	docker compose up -d --build

smoke: ## Verify the running stack end to end (needs the stack up; see scripts/smoke.sh).
	bash scripts/smoke.sh

smoke-ps: ## Verify the running stack from Windows PowerShell.
	powershell -ExecutionPolicy Bypass -File scripts/smoke.ps1

down: ## Stop the stack, keeping volumes.
	docker compose down

logs: ## Tail logs from the gateway and workers.
	docker compose logs -f gateway workers

ps: ## Show stack status.
	docker compose ps

clean: ## Remove build output and test artifacts.
	rm -rf $(BIN_DIR) coverage.out
	rm -rf dashboard/.next dashboard/out dashboard/tsconfig.tsbuildinfo
	find workers -type d -name __pycache__ -prune -exec rm -rf {} +
