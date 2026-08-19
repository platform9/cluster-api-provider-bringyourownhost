#!/bin/bash

# Copyright 2021 VMware, Inc. All Rights Reserved.
# Copyright 2026 Platform9, Inc. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0

set -e

echo "Starting BYOH bundle ingredient download..."

: "${ARCH:?ARCH must be set}"
: "${OS:?OS must be set}"
: "${CONTAINERD_VERSION:?CONTAINERD_VERSION must be set}"
: "${RUNC_VERSION:?RUNC_VERSION must be set}"
: "${KUBERNETES_VERSION:?KUBERNETES_VERSION must be set}"
: "${CRITOOL_VERSION:?CRITOOL_VERSION must be set}"
: "${CNI_VERSION:?CNI_VERSION must be set}"

# Strip any trailing Debian-revision suffix (e.g. "1.32.2-1.1") left over from
# the old apt-based versioning scheme; upstream GitHub/dl.k8s.io release tags
# are plain semver.
K8S_VERSION="v${KUBERNETES_VERSION%%-*}"
CRICTL_VERSION="v${CRITOOL_VERSION%%-*}"
RUNC_VERSION="v${RUNC_VERSION%%-*}"
CNI_VERSION="v${CNI_VERSION%%-*}"

mkdir -p /ingredients

echo "Downloading containerd..."
curl -LO "https://github.com/containerd/containerd/releases/download/v${CONTAINERD_VERSION}/containerd-${CONTAINERD_VERSION}-${OS}-${ARCH}.tar.gz"
mv "containerd-${CONTAINERD_VERSION}-${OS}-${ARCH}.tar.gz" /ingredients/

echo "Downloading runc..."
curl -LO "https://github.com/opencontainers/runc/releases/download/${RUNC_VERSION}/runc.${ARCH}"
chmod +x "runc.${ARCH}"
mv "runc.${ARCH}" /ingredients/

echo "Downloading crictl..."
curl -LO "https://github.com/kubernetes-sigs/cri-tools/releases/download/${CRICTL_VERSION}/crictl-${CRICTL_VERSION}-${OS}-${ARCH}.tar.gz"
mv "crictl-${CRICTL_VERSION}-${OS}-${ARCH}.tar.gz" /ingredients/

echo "Downloading kubeadm..."
curl -LO "https://dl.k8s.io/release/${K8S_VERSION}/bin/${OS}/${ARCH}/kubeadm"
curl -LO "https://dl.k8s.io/release/${K8S_VERSION}/bin/${OS}/${ARCH}/kubeadm.sha256"
echo "$(cat kubeadm.sha256)  kubeadm" | sha256sum --check
chmod +x kubeadm
mv kubeadm /ingredients/

echo "Downloading kubelet..."
curl -LO "https://dl.k8s.io/release/${K8S_VERSION}/bin/${OS}/${ARCH}/kubelet"
curl -LO "https://dl.k8s.io/release/${K8S_VERSION}/bin/${OS}/${ARCH}/kubelet.sha256"
echo "$(cat kubelet.sha256)  kubelet" | sha256sum --check
chmod +x kubelet
mv kubelet /ingredients/

echo "Downloading kubectl..."
curl -LO "https://dl.k8s.io/release/${K8S_VERSION}/bin/${OS}/${ARCH}/kubectl"
curl -LO "https://dl.k8s.io/release/${K8S_VERSION}/bin/${OS}/${ARCH}/kubectl.sha256"
echo "$(cat kubectl.sha256)  kubectl" | sha256sum --check
chmod +x kubectl
mv kubectl /ingredients/

echo "Downloading CNI plugins..."
curl -LO "https://github.com/containernetworking/plugins/releases/download/${CNI_VERSION}/cni-plugins-${OS}-${ARCH}-${CNI_VERSION}.tgz"
mv "cni-plugins-${OS}-${ARCH}-${CNI_VERSION}.tgz" /ingredients/

echo "All ingredients downloaded and stored in /ingredients"
