#!/usr/bin/env bash
set -Eeuo pipefail

# Copyright 2026 Platform9, Inc. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0

# clean-e2e-runner-docker.sh - removes all docker state from a self-hosted
# e2e runner. Runners are dedicated to CI, so nothing else uses this docker
# daemon. E2E runs leave containers, images and volumes behind (byohost
# containers mount /var as an anonymous volume), which eventually fills the
# disk. Runs at the start and end of every e2e job.
#
# Usage: clean-e2e-runner-docker.sh
# No arguments, no required env.

log() { printf '%s %s\n' "$(date -u +%FT%TZ)" "$*"; }

main() {
  if ! command -v docker >/dev/null; then
    log "docker not installed, nothing to clean"
    return 0
  fi

  log "disk usage before cleanup"
  df -h /

  local containers
  containers=$(docker ps -aq)
  if [[ -n "$containers" ]]; then
    log "force-removing containers"
    # Word splitting is intended: one argument per container ID.
    # shellcheck disable=SC2086
    docker rm -f -v $containers
  else
    log "no containers to remove"
  fi

  log "pruning docker system, images and volumes"
  docker system prune -af --volumes
  docker volume prune -af

  log "disk usage after cleanup"
  df -h /
  docker system df
}

main "$@"
