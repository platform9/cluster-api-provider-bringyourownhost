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
echo Strip version to well-known names

cp "$INGREDIENTS_PATH"/kubeadm .
cp "$INGREDIENTS_PATH"/kubelet .
cp "$INGREDIENTS_PATH"/kubectl .

cp "$INGREDIENTS_PATH"/cri-containerd-cni-*.tar.gz containerd.tar.gz
cp "$INGREDIENTS_PATH"/crictl-*.tar.gz crictl.tar.gz
cp "$INGREDIENTS_PATH"/cni-plugins-*.tgz cni-plugins.tgz


echo Configuration $CONFIG_PATH
ls -l $CONFIG_PATH

echo Add configuration under well-known name
(cd "$CONFIG_PATH" && tar -cvf conf.tar .)
cp "$CONFIG_PATH"/conf.tar .

echo "Creating BYOH bundle tar..."
tar -cvf /bundle/bundle.tar *

echo "Done"
