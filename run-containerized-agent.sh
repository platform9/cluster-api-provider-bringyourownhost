#!/bin/bash

# Build the container image
docker build -t byoh-agent:latest -f Dockerfile.agent .

# Run the agent in a container with host access
docker run \
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
