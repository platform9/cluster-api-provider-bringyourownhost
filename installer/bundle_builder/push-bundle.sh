#!/bin/bash

# Copyright 2021 VMware, Inc. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0

set -e
echo install imgpkg
curl -L https://carvel.dev/install.sh | bash
export PATH=$PATH:/usr/local/bin
imgpkg version

imgpkg push -f . -i quay.io/platform9/byoh-bundle-ubuntu_20.04.1_x86-64_k8s:latest

echo "bundle push done"
