# Binary name
BINARY_NAME=byohctl

# Build directory
BUILD_DIR=$(shell pwd)/bin

# Go build flags
GOOS?=linux
GOARCH?=amd64
GO_BUILD_FLAGS=-o $(BUILD_DIR)/$(BINARY_NAME)

# Default target
.PHONY: all
all: build

# Create build directory
$(BUILD_DIR):
	mkdir -p $(BUILD_DIR)

# Build the binary
.PHONY: build
build: $(BUILD_DIR)
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(GO_BUILD_FLAGS)

# Clean build artifacts
.PHONY: clean
clean:
	rm -rf $(BUILD_DIR)

# Build for multiple platforms
.PHONY: build-all
build-all: $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 go build -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64
	GOOS=linux GOARCH=arm64 go build -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64
	GOOS=darwin GOARCH=amd64 go build -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64
	GOOS=darwin GOARCH=arm64 go build -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64

# Help target
.PHONY: help
help:
	@echo "Available targets:"
	@echo "  build       - Build for specified GOOS/GOARCH (default: linux/amd64)"
	@echo "  build-all   - Build for multiple platforms"
	@echo "  clean       - Remove build artifacts"
	@echo ""
	@echo "Usage:"
	@echo "  make build                    # Build for linux/amd64"
	@echo "  make build GOOS=darwin        # Build for macOS"
	@echo "  make build GOARCH=arm64       # Build for ARM64"
	@echo "  make build-all                # Build for all platforms"
	@echo ""
	@echo "Output directory: $(BUILD_DIR)/"
