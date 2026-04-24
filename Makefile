# ClickTrics — build, test, package.
#
# Usage:
#   make              # build for current platform
#   make build-linux  # cross-compile linux/amd64
#   make deb          # build the .deb (requires nfpm)
#   make help         # list all targets

SHELL := /bin/bash

BIN  := clicktrics
PKG  := ./cmd/clicktrics
DIST := dist

# Version precedence: environment override → `git describe` → v0.0.0-dev.
# Tags use vX.Y.Z; the binary reports exactly what `git describe` emits so
# `clicktrics version` matches the GitHub tag. For the .deb `Version:`
# field we strip the leading 'v' below (dpkg requires a leading digit).
VERSION     ?= $(shell (git describe --tags --always --dirty 2>/dev/null) || echo v0.0.0-dev)
DEB_VERSION := $(VERSION:v%=%)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X main.versionStr=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.buildDate=$(BUILD_DATE)

GO       := go
GOFLAGS  := -trimpath
CGO      := CGO_ENABLED=0

# Auto-download a matching toolchain if go.mod requires one the host doesn't
# have. Users on pinned Go versions can override with GOTOOLCHAIN=local.
export GOTOOLCHAIN ?= auto

.DEFAULT_GOAL := build

# --- Help --------------------------------------------------------------------

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n\nTargets:\n"} /^[a-zA-Z0-9_-]+:.*##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

# --- Build -------------------------------------------------------------------

.PHONY: build
build: ## Build binary for the current platform
	mkdir -p $(DIST)
	$(CGO) $(GO) build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(DIST)/$(BIN) $(PKG)

.PHONY: build-linux
build-linux: build-linux-amd64 ## Cross-compile for linux/amd64

.PHONY: build-linux-amd64
build-linux-amd64: $(DIST)/$(BIN)-linux-amd64 ## Cross-compile for linux/amd64

$(DIST)/$(BIN)-linux-amd64:
	mkdir -p $(DIST)
	$(CGO) GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $@ $(PKG)

# --- Test & lint -------------------------------------------------------------

.PHONY: test
test: ## Run unit tests with race detector
	$(GO) test -race -count=1 ./...

.PHONY: vet
vet: ## go vet
	$(GO) vet ./...

.PHONY: fmt
fmt: ## Apply gofmt to the tree
	gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if any file needs gofmt
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "$$out"; exit 1; fi

# --- Clean -------------------------------------------------------------------

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(DIST)

# --- Packaging (.deb) --------------------------------------------------------

NFPM := nfpm

.PHONY: deb
deb: deb-amd64 ## Build the linux/amd64 .deb package

.PHONY: deb-amd64
deb-amd64: build-linux-amd64 $(DIST)/nfpm.amd64.yaml ## Build amd64 .deb
	@command -v $(NFPM) >/dev/null 2>&1 || { \
	  echo "nfpm not installed. Get it from https://nfpm.goreleaser.com/install/"; exit 1; }
	$(NFPM) pkg -f $(DIST)/nfpm.amd64.yaml -p deb -t $(DIST)/

# nfpm's env-var expansion works on top-level scalars but not on nested
# `contents[].src` — pre-render the config with sed per target arch.
$(DIST)/nfpm.%.yaml: packaging/nfpm.yaml
	mkdir -p $(DIST)
	sed -e 's|$${VERSION}|$(DEB_VERSION)|g' -e 's|$${NFPM_ARCH}|$*|g' $< > $@

# --- Install (dev convenience) -----------------------------------------------

PREFIX ?= /usr/local

.PHONY: install
install: build ## Install binary + systemd unit + config template (requires sudo)
	sudo install -D -m 0755 $(DIST)/$(BIN) $(PREFIX)/bin/$(BIN)
	sudo install -D -m 0644 deploy/systemd/clicktrics.service /lib/systemd/system/clicktrics.service
	sudo install -D -m 0644 clicktrics.example.yaml /etc/clicktrics/config.yaml.example
	@if [ ! -e /etc/clicktrics/config.yaml ]; then \
		sudo cp /etc/clicktrics/config.yaml.example /etc/clicktrics/config.yaml; \
		echo "Seeded /etc/clicktrics/config.yaml from template."; \
	else \
		echo "/etc/clicktrics/config.yaml already present — left untouched."; \
	fi
	sudo systemctl daemon-reload

.PHONY: uninstall
uninstall: ## Remove binary and unit file (requires sudo)
	sudo systemctl disable --now clicktrics || true
	sudo rm -f $(PREFIX)/bin/$(BIN) /lib/systemd/system/clicktrics.service
	sudo systemctl daemon-reload
