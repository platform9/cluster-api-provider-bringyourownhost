#!/bin/bash

# Copyright 2021 VMware, Inc. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0

set -e

echo "Starting BYOH bundle ingredient download..."

:"${ARCH:?ARCH must be set}"
:"${OS:?OS must be set}"
:"${CONTAINERD_VERSION:?CONTAINERD_VERSION must be set}"
:"${KUBERNETES_VERSION:?KUBERNETES_VERSION must be set}"
:"${CRITOOL_VERSION:?CRITOOL_VERSION must be set}"
: "${CNI_VERSION:?CNI_VERSION must be set}"

K8S_VERSION="v${KUBERNETES_VERSION%%-*}"
CRICTL_VERSION="v${CRITOOL_VERSION}"
CNI_VERSION="v${CNI_VERSION}"

mkdir -p /ingredients

echo "Downloading containerd..."
curl -LO "https://github.com/containerd/containerd/releases/download/v${CONTAINERD_VERSION}/cri-containerd-cni-${CONTAINERD_VERSION}-${OS}-${ARCH}.tar.gz"
mv "cri-containerd-cni-${CONTAINERD_VERSION}-${OS}-${ARCH}.tar.gz" /ingredients/

echo "Downloading crictl..."
curl -LO "https://github.com/kubernetes-sigs/cri-tools/releases/download/${CRICTL_VERSION}/crictl-${CRICTL_VERSION}-${OS}-${ARCH}.tar.gz"
mv "crictl-${CRICTL_VERSION}-${OS}-${ARCH}.tar.gz" /ingredients/

echo "Downloading kubeadm..."
curl -LO "https://dl.k8s.io/release/${K8S_VERSION}/bin/${OS}/${ARCH}/kubeadm"
chmod +x kubeadm
mv kubeadm /ingredients/

echo "Downloading kubelet..."
curl -LO "https://dl.k8s.io/release/${K8S_VERSION}/bin/${OS}/${ARCH}/kubelet"
chmod +x kubelet
mv kubelet /ingredients/

echo "Downloading kubectl..."
curl -LO "https://dl.k8s.io/release/${K8S_VERSION}/bin/${OS}/${ARCH}/kubectl"
chmod +x kubectl
mv kubectl /ingredients/

echo "Downloading CNI plugins..."
curl -LO "https://github.com/containernetworking/plugins/releases/download/${CNI_VERSION}/cni-plugins-${OS}-${ARCH}-${CNI_VERSION}.tgz"
mv "cni-plugins-${OS}-${ARCH}-${CNI_VERSION}.tgz" /ingredients/

echo "All ingredients downloaded and stored in /ingredients"