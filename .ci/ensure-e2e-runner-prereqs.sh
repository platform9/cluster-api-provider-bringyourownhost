#!/usr/bin/env bash
set -Eeuo pipefail

# ensure-e2e-runner-prereqs.sh - installs the e2e job's tooling on a
# self-hosted runner VM if it isn't already present. GitHub-hosted runners
# ship gh/make/docker preinstalled; a bare self-hosted VM doesn't, so this
# runs at the start of every e2e job and is a no-op once a VM already has
# everything.
#
# Usage: ensure-e2e-runner-prereqs.sh
# No arguments, no required env.

ensure_gh() {
  if command -v gh >/dev/null; then
    return 0
  fi

  curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg |
    sudo tee /usr/share/keyrings/githubcli-archive-keyring.gpg >/dev/null
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" |
    sudo tee /etc/apt/sources.list.d/github-cli.list >/dev/null
  sudo apt-get update
  sudo apt-get install -y gh
}

ensure_make() {
  if command -v make >/dev/null; then
    return 0
  fi

  sudo apt-get update
  sudo apt-get install -y build-essential
}

ensure_docker() {
  if ! command -v docker >/dev/null; then
    curl -fsSL https://get.docker.com | sudo sh
    sudo usermod -aG docker "$USER"
  fi

  # A docker group membership added just above only takes effect for new
  # login sessions, not for this already-running runner process, so open
  # the socket directly to unblock the current job.
  sudo chmod 666 /var/run/docker.sock
}

main() {
  ensure_gh
  ensure_make
  ensure_docker
}

main "$@"
