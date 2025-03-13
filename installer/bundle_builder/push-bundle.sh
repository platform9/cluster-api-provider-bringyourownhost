#!/bin/bash

# Copyright 2021 VMware, Inc. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0

set -e

imgpkg push -f . -i snhpf9/byoh-bundle-ubuntu_20.04.1_x86-64_k8s:$KUBERNETES_MAJOR_VERSION

echo "bundle push done"
