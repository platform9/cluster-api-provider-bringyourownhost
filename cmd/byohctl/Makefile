# Copyright 2026 Platform9 Systems, Inc.
#
# Usage:
# make              # builds the binary for current platform
# make build        # builds for linux/amd64
# make build-all    # builds for all supported platforms
# make clean        # removes build artifacts
# make test         # runs tests

SHELL := /usr/bin/env bash

# VERSION defaults to the root Makefile's `make tag` target – the single source
# of truth every agent-side artifact (byohctl, the agent bundle) derives its
# version from, so they always agree without either side hardcoding or
# independently recomputing anything.
REPO_ROOT := $(shell git rev-parse --show-toplevel)
VERSION ?= $(shell $(MAKE) --no-print-directory -C $(REPO_ROOT) tag)

# Build settings
BINARY_NAME := byohctl
BUILD_DIR := $(shell pwd)/bin
GOOS ?= linux
GOARCH ?= amd64
CGO_ENABLED ?= 0

# LDFLAGS for version information
LDFLAGS := -X main.buildVersion=$(VERSION) \
           -s -w

# Default target
.PHONY: all
all: build

# Create build directory
$(BUILD_DIR):
	mkdir -p $(BUILD_DIR)

# Build the binary
.PHONY: build
build: $(BUILD_DIR)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o $(BUILD_DIR)/$(BINARY_NAME) -ldflags "$(LDFLAGS)"

# Build for multiple platforms
.PHONY: build-all
build-all: $(BUILD_DIR)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=linux GOARCH=amd64 go build -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 -ldflags "$(LDFLAGS)"
	CGO_ENABLED=$(CGO_ENABLED) GOOS=linux GOARCH=arm64 go build -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 -ldflags "$(LDFLAGS)"
	CGO_ENABLED=$(CGO_ENABLED) GOOS=darwin GOARCH=amd64 go build -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 -ldflags "$(LDFLAGS)"
	CGO_ENABLED=$(CGO_ENABLED) GOOS=darwin GOARCH=arm64 go build -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 -ldflags "$(LDFLAGS)"

# imgpkg is used to publish built binaries to Quay as an OCI artifact.
# Pinned by SHA256 (not sha256sums.txt fetched at runtime) so a compromised
# upstream release can't swap the binary CI executes without a second,
# reviewable commit to this repo.
IMGPKG_VERSION ?= v0.43.1
IMGPKG_SHA256 ?= d36ccfcc54143d2475cf09b0247c88bccf24a7aeb062bd9bb8cab7cb58135fcf
BYOHCTL_IMAGE ?= quay.io/platform9/cluster-api-provider-bringyourownhost/byohctl

# Push built byohctl binaries to the registry via imgpkg. Requires build-all to have already run.
.PHONY: push-byohctl
push-byohctl:
	curl -fsSL -o /tmp/imgpkg-byohctl-download "https://github.com/carvel-dev/imgpkg/releases/download/$(IMGPKG_VERSION)/imgpkg-linux-amd64"
	echo "$(IMGPKG_SHA256)  /tmp/imgpkg-byohctl-download" | sha256sum -c -
	install -m 0755 /tmp/imgpkg-byohctl-download /tmp/imgpkg-byohctl
	/tmp/imgpkg-byohctl push -f $(BUILD_DIR) -i $(BYOHCTL_IMAGE):$(VERSION)

# Format code
.PHONY: format
format:
	gofmt -w -s *.go
	gofmt -w -s */*.go

# Run tests
.PHONY: test
test:
	go test -v ./...

# Clean build artifacts
.PHONY: clean
clean:
	rm -rf $(BUILD_DIR)

# Help target
.PHONY: help
help:
	@echo "Available targets:"
	@echo "  build       - Build for specified GOOS/GOARCH (default: linux/amd64)"
	@echo "  build-all   - Build for multiple platforms"
	@echo "  push-byohctl - Push built binaries to Quay via imgpkg (requires build-all)"
	@echo "  clean       - Remove build artifacts"
	@echo "  format      - Format Go code"
	@echo "  test        - Run tests"
	@echo ""
	@echo "Usage:"
	@echo "  make build                    # Build for linux/amd64"
	@echo "  make build GOOS=darwin        # Build for macOS"
	@echo "  make build-all                # Build for all platforms"
	@echo "  make build MAJOR=2 MINOR=1    # Build version 2.1.BUILD_NUMBER"
	@echo ""
	@echo "Version: $(VERSION)"
	@echo "Output directory: $(BUILD_DIR)/"
