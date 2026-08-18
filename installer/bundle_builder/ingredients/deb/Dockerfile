# Copyright 2021 VMware, Inc. All Rights Reserved.
# Copyright 2026 Platform9, Inc. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0

# Downloads bundle ingredients: containerd, runc, crictl and CNI plugins as
# release tarballs/binaries, and kubeadm, kubelet, kubectl as raw binaries
# from dl.k8s.io.
#
# Usage:
# 1. Mount a host path as /ingredients
# 2. Run the image
#

ARG BASE_IMAGE=ubuntu:20.04
FROM $BASE_IMAGE as build

# Override to download other version
ENV CONTAINERD_VERSION=1.7.26
ENV RUNC_VERSION=1.2.5
ENV KUBERNETES_VERSION=1.32.2
ENV ARCH=amd64
ENV OS=linux
ENV CNI_VERSION=1.3.0

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends sudo

WORKDIR /bundle-builder
COPY download.sh .
RUN chmod a+x download.sh
WORKDIR /ingredients

ENTRYPOINT ["/bundle-builder/download.sh"]
