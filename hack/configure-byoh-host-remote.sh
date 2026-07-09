#!/usr/bin/env bash
#
# Runs ON a BYO host — piped in over SSH by configure-byoh-host.sh. Loads the
# kernel modules and sets the sysctls kubeadm requires, persisting both across
# reboot. Idempotent. Uses sudo per-command; assumes passwordless sudo.

set -Eeuo pipefail

modules_conf=/etc/modules-load.d/byoh.conf
sysctl_conf=/etc/sysctl.d/99-byoh.conf

# Load the required modules now and persist them for boot.
printf '%s\n' overlay br_netfilter | sudo tee "$modules_conf" >/dev/null
for mod in overlay br_netfilter; do
  sudo modprobe "$mod"
done

# Persist and apply the kubeadm-required sysctls.
#
# net.netfilter.nf_conntrack_max is global, not per-netns: kube-proxy inside
# a privileged byoh host container can read it but gets "permission denied"
# trying to raise it, so the host must already meet or exceed whatever value
# kube-proxy computes (observed 524288; set well above it for headroom).
sudo tee "$sysctl_conf" >/dev/null <<'SYSCTL'
net.bridge.bridge-nf-call-iptables  = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward                 = 1
net.netfilter.nf_conntrack_max      = 1048576
SYSCTL
sudo sysctl --system >/dev/null

# Report final state (|| true: display only; the modprobe above is the assertion).
echo "modules:"
lsmod | grep -E '^overlay|^br_netfilter' || true
echo "sysctls:"
sudo sysctl net.bridge.bridge-nf-call-iptables net.bridge.bridge-nf-call-ip6tables net.ipv4.ip_forward net.netfilter.nf_conntrack_max
