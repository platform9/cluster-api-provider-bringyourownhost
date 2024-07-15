#!/bin/bash

# Copyright 2021 VMware, Inc. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0

set -e

echo  Update the apt package index and install packages needed to use the Kubernetes apt repository
sudo apt-get update
sudo apt-get install -y apt-transport-https ca-certificates curl

mkdir -p /etc/apt/keyrings/

echo Download containerd
curl -LOJR https://github.com/containerd/containerd/releases/download/v${CONTAINERD_VERSION}/cri-containerd-cni-${CONTAINERD_VERSION}-linux-amd64.tar.gz

echo Download the Google Cloud public signing key
curl -fsSL https://pkgs.k8s.io/core:/stable:/${KUBERENETES_MAJOR_VERSION}/deb/Release.key | sudo gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg

echo Add the Kubernetes apt repository
echo "deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/${KUBERENETES_MAJOR_VERSION}/deb/ /" | sudo tee /etc/apt/sources.list.d/kubernetes.list

echo Update apt package index, install kubelet, kubeadm and kubectl
sudo apt-get update
sudo apt-get download {kubelet,kubeadm,kubectl}:$ARCH=$KUBERNETES_VERSION
sudo apt-get download kubernetes-cni:$ARCH=1.1.1-00
sudo apt-get download cri-tools:$ARCH=1.25.0-00
