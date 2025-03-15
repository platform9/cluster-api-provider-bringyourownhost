#!/bin/bash
set -e

# This script builds and runs the BYOH agent using containerd (via nerdctl) instead of Docker

# Check if nerdctl is installed (needed for convenient containerd interaction)
if ! command -v nerdctl &> /dev/null; then
    echo "nerdctl not found, attempting to install it..."
    
    # Create temp directory
    TEMP_DIR=$(mktemp -d)
    pushd $TEMP_DIR
    
    # Download and install nerdctl (this is a simplified version, adjust as needed)
    NERDCTL_VERSION=1.5.0
    curl -L https://github.com/containerd/nerdctl/releases/download/v${NERDCTL_VERSION}/nerdctl-${NERDCTL_VERSION}-linux-amd64.tar.gz -o nerdctl.tar.gz
    sudo tar -C /usr/local/bin -xzf nerdctl.tar.gz
    
    popd
    rm -rf $TEMP_DIR
    
    # Verify installation
    if ! command -v nerdctl &> /dev/null; then
        echo "Failed to install nerdctl. Please install manually and try again."
        echo "Visit: https://github.com/containerd/nerdctl#install"
        exit 1
    fi
fi

# Build the container image with nerdctl
echo "Building BYOH agent container image with containerd..."
sudo nerdctl build -t byoh-agent:latest -f Dockerfile.agent .

# Run the agent in a container with host access using nerdctl
echo "Running BYOH agent container with containerd..."
sudo nerdctl run \
  --privileged \
  --network host \
  --pid host \
  -v /:/host \
  -v /var/run:/var/run \
  -v /var/lib/byoh:/var/lib/byoh \
  -v /run/systemd:/run/systemd \
  -v /sys/fs/cgroup:/sys/fs/cgroup \
  -e RUNNING_IN_CONTAINER=true \
  -e HOST_PREFIX=/host \
  byoh-agent:latest \
  --namespace "${1:-default}" "${@:2}"
