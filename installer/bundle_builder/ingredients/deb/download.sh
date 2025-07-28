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

echo Update apt package index, install kubelet, kubeadm and kubectl
apt-get update
chown -Rv _apt:root /bundle/
chown -R _apt:root /ingredients
mv cri-containerd-cni-${CONTAINERD_VERSION}-linux-amd64.tar.gz /ingredients/ 
cd /ingredients 
apt-get download {kubelet,kubeadm,kubectl}:$ARCH=$KUBERNETES_VERSION
apt-get download kubernetes-cni:$ARCH
apt-get download cri-tools:$ARCH=$CRITOOL_VERSION

SOCAT_URL="https://archive.ubuntu.com/ubuntu/pool/main/s/socat/socat_1.7.3.3-2_amd64.deb"
ETHTOOL_URL="https://archive.ubuntu.com/ubuntu/pool/main/e/ethtool/ethtool_5.4-1_amd64.deb"
EBTABLES_URL="https://archive.ubuntu.com/ubuntu/pool/main/e/ebtables/ebtables_2.0.10.4-3.4ubuntu1_amd64.deb"
CONNTRACK_URL="https://archive.ubuntu.com/ubuntu/pool/main/c/conntrack-tools/conntrack_1.4.5-2_amd64.deb"

curl -L -o socat.deb ${SOCAT_URL}
curl -L -o ethtool.deb ${ETHTOOL_URL}
curl -L -o ebtables.deb ${EBTABLES_URL}
curl -L -o conntrack.deb ${CONNTRACK_URL}
