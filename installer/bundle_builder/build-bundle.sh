#!/bin/bash

# Copyright 2021 VMware, Inc. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0


INGREDIENTS_PATH=$1
CONFIG_PATH=$2

set -e

echo Building bundle...

echo Ingredients $INGREDIENTS_PATH
ls -l $INGREDIENTS_PATH

cd /bundle
echo "Copying binaries with well-known names"
# Mandatory
cp $INGREDIENTS_PATH/*containerd* containerd.tar
cp $INGREDIENTS_PATH/kubectl ./kubectl
cp $INGREDIENTS_PATH/kubelet ./kubelet
cp $INGREDIENTS_PATH/kubeadm ./kubeadm
cp $INGREDIENTS_PATH/crictl ./crictl
cp $INGREDIENTS_PATH/cni-plugins.tgz ./cni-plugins.tgz

# Create directories for binaries
mkdir -p bin
mv kubectl kubelet kubeadm crictl bin/

echo Configuration $CONFIG_PATH
ls -l $CONFIG_PATH

echo Add configuration under well-known name
(cd $CONFIG_PATH && tar -cvf conf.tar *)
cp $CONFIG_PATH/conf.tar .

echo Creating bundle tar
tar -cvf /bundle/bundle.tar *

echo Done
