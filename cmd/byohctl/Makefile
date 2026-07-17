# Copyright 2023 Platform9 Systems, Inc.
#
# Usage:
# make              # builds the binary for current platform
# make build        # builds for linux/amd64
# make build-all    # builds for all supported platforms
# make clean        # removes build artifacts
# make test         # runs tests

SHELL := /usr/bin/env bash

# Version information
MAJOR ?= 1
MINOR ?= 0
BUILD_NUMBER ?= 1
VERSION := $(MAJOR).$(MINOR).$(BUILD_NUMBER)

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
