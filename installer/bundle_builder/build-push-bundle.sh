#!/bin/bash

# Copyright 2021 VMware, Inc. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0

set -e
download.sh
build-bundle.sh $1 $2
push-bundle.sh 

