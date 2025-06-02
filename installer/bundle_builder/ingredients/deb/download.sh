#!/bin/bash

# Copyright 2021 VMware, Inc. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0

set -e

echo  Update the apt package index and install packages needed to use the Kubernetes apt repository
 apt-get update
 apt-get install -y apt-transport-https ca-certificates curl

echo Download containerd
curl -LOJR https://github.com/containerd/containerd/releases/download/v${CONTAINERD_VERSION}/cri-containerd-cni-${CONTAINERD_VERSION}-linux-amd64.tar.gz 

echo Download the Google Cloud public signing key
 curl -fsSLo /usr/share/keyrings/kubernetes-archive-keyring.gpg https://dl.k8s.io/apt/doc/apt-key.gpg

echo Add the Kubernetes apt repository

echo "deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/${KUBERNETES_MAJOR_VERSION}/deb/ /" |  tee /etc/apt/sources.list.d/kubernetes.list

mkdir -p /etc/apt/keyrings
curl -fsSL https://pkgs.k8s.io/core:/stable:/${KUBERNETES_MAJOR_VERSION}/deb/Release.key |  gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg

echo "Downloading Kubernetes binaries directly from official repositories"
cd /ingredients

# Move containerd to ingredients directory
mv cri-containerd-cni-${CONTAINERD_VERSION}-linux-amd64.tar.gz /ingredients/

# Download kubectl directly from Kubernetes release server
echo "Downloading kubectl ${KUBERNETES_VERSION}..."
curl -L "https://dl.k8s.io/release/v${KUBERNETES_VERSION}/bin/linux/${ARCH}/kubectl" -o kubectl
chmod +x kubectl

# Download kubelet directly from Kubernetes release server
echo "Downloading kubelet ${KUBERNETES_VERSION}..."
curl -L "https://dl.k8s.io/release/v${KUBERNETES_VERSION}/bin/linux/${ARCH}/kubelet" -o kubelet
chmod +x kubelet

# Download kubeadm directly from Kubernetes release server
echo "Downloading kubeadm ${KUBERNETES_VERSION}..."
curl -L "https://dl.k8s.io/release/v${KUBERNETES_VERSION}/bin/linux/${ARCH}/kubeadm" -o kubeadm
chmod +x kubeadm

# Download crictl (cri-tools) from GitHub releases
echo "Downloading crictl ${CRITOOL_VERSION}..."
curl -L "https://github.com/kubernetes-sigs/cri-tools/releases/download/${CRITOOL_VERSION}/crictl-${CRITOOL_VERSION}-linux-${ARCH}.tar.gz" -o crictl.tar.gz
tar -xzf crictl.tar.gz
rm -f crictl.tar.gz

# Download CNI plugins from GitHub releases
CNI_PLUGINS_VERSION="v1.2.0"
echo "Downloading CNI plugins ${CNI_PLUGINS_VERSION}..."
curl -L "https://github.com/containernetworking/plugins/releases/download/${CNI_PLUGINS_VERSION}/cni-plugins-linux-${ARCH}-${CNI_PLUGINS_VERSION}.tgz" -o cni-plugins.tgz
