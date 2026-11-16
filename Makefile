# Kadr build, test and deploy targets. See docs/OPERATIONS.md §6.

SHELL   := /bin/bash
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)
BIN     := bin/kadr

# Remote layout (OPERATIONS.md §3).
REMOTE_ROOT ?= /opt/kadr
SSH         ?= ssh
SCP         ?= scp

.PHONY: help dev test test-race lint web build build-local release deploy rollback clean

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

dev: ## Run locally with dev defaults (./var/, generated master key)
	KADR_DEV=1 go run ./cmd/kadr migrate up
	KADR_DEV=1 go run ./cmd/kadr serve

test: ## Run Go tests
	go test ./...

test-race: ## Run Go tests with the race detector
	go test -race ./...

lint: ## go vet + staticcheck (if installed)
	go vet ./...
	@if command -v staticcheck >/dev/null 2>&1; then staticcheck ./...; \
	else echo "staticcheck not installed (go install honnef.co/go/tools/cmd/staticcheck@latest); skipping"; fi

web: ## Build the admin UI into web/dist
	cd web && pnpm install --frozen-lockfile && pnpm build

build: web ## Build the linux/amd64 release binary (with embedded UI) into bin/kadr
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/kadr
	@echo "built $(BIN) $(VERSION) ($(COMMIT))"

build-local: ## Build for the host OS without rebuilding the UI
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/kadr

release: ## Build a versioned artifact: make release VERSION=0.3.0
	@if [ "$(origin VERSION)" != "command line" ]; then echo "usage: make release VERSION=x.y.z"; exit 2; fi
	$(MAKE) build VERSION=$(VERSION)
	mkdir -p dist
	cp $(BIN) dist/kadr-$(VERSION)-linux-amd64
	cd dist && shasum -a 256 kadr-$(VERSION)-linux-amd64 > kadr-$(VERSION)-linux-amd64.sha256
	@echo "release artifacts in dist/"

deploy: ## Deploy dist/kadr-$(VERSION)-linux-amd64: make deploy HOST=kadr.internal VERSION=0.3.0
	@if [ -z "$(HOST)" ]; then echo "usage: make deploy HOST=host VERSION=x.y.z"; exit 2; fi
	@test -f dist/kadr-$(VERSION)-linux-amd64 || { echo "dist/kadr-$(VERSION)-linux-amd64 not found; run make release VERSION=$(VERSION)"; exit 1; }
	$(SCP) dist/kadr-$(VERSION)-linux-amd64 $(HOST):/tmp/kadr-$(VERSION)
	$(SSH) $(HOST) 'sudo bash -s -- deploy $(VERSION) $(REMOTE_ROOT)' < scripts/remote-release.sh

rollback: ## Switch to the previous release: make rollback HOST=kadr.internal
	@if [ -z "$(HOST)" ]; then echo "usage: make rollback HOST=host"; exit 2; fi
	$(SSH) $(HOST) 'sudo bash -s -- rollback - $(REMOTE_ROOT)' < scripts/remote-release.sh

clean: ## Remove build outputs
	rm -rf bin dist
