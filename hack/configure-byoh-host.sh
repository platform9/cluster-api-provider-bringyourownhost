#!/usr/bin/env bash
#
# configure-byoh-host.sh — enable the kernel/network prerequisites kubeadm needs
# on a BYO host, applied over SSH against a target VM.
#
# When the workload cluster runs on a single machine, the privileged byoh/node
# containers mount /lib/modules:ro and share the host kernel, so the host must
# have the overlay + br_netfilter modules and the bridge-netfilter / ip_forward
# sysctls loaded. The same script preps a standalone BYO host VM.
#
# The commands that run on the target live in configure-byoh-host-remote.sh (a
# sibling file) so they can be linted independently; this script just pipes that
# file to the host over SSH.
#
# Usage:
#   hack/configure-byoh-host.sh <VM_IP>
#
# SSH user defaults to "ubuntu"; override with BYOH_SSH_USER.
# Assumes passwordless sudo on the target.

set -Eeuo pipefail
shopt -s nullglob

SSH_USER=${BYOH_SSH_USER:-ubuntu}

log() { printf '%s %s\n' "$(date -u +%FT%TZ)" "$*"; }

usage() {
  cat >&2 <<'EOF'
Usage: configure-byoh-host.sh <VM_IP>

Loads overlay + br_netfilter and sets the kubeadm-required sysctls on the
target host over SSH, persisting both across reboot. Idempotent.

SSH user defaults to "ubuntu" (override with BYOH_SSH_USER).
EOF
}

main() {
  if [[ $# -ne 1 || "$1" == "-h" || "$1" == "--help" ]]; then
    usage
    exit 1
  fi
  local vm_ip="$1"

  local script_dir remote_file
  script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
  remote_file="${script_dir}/configure-byoh-host-remote.sh"
  [[ -f "$remote_file" ]] || {
    log "remote script not found: ${remote_file}"
    exit 1
  }

  log "configuring byoh host prerequisites on ${SSH_USER}@${vm_ip}"
  ssh "${SSH_USER}@${vm_ip}" 'bash -s' <"$remote_file"
  log "done on ${vm_ip}: overlay/br_netfilter loaded, sysctls applied and persisted"
}

main "$@"
