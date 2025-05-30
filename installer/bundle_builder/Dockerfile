# Copyright 2021 VMware, Inc. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0

# Build and push (opt-in) a BYOH bundle to repository
#
# 1. Download bundle ingredients. See ingredients/deb/download.sh
# 2. Mount the bundle ingredients under /ingredients
# 3. Optional. Mount output bundle directory under /bundle
# 3. Optional. Mount additional configuration under /config
#	-v config/ubuntu/20_04/k8s/1_22:/config
#	Defaults to config/ubuntu/20_04/k8s/1_22
# Example
# // Build and push a BYOH bundle to repository
# docker run --rm -v <INGREDIENTS_HOST_ABS_PATH>:/ingredients --env BUILD_ONLY=0 <THIS_IMAGE> <REPO>/<BUNDLE IMAGE>
#
# // Build and store a BYOH bundle
# docker run --rm -v <INGREDIENTS_HOST_ABS_PATH>:/ingredients  -v <BUNDLE_OUTPUT_ABS_PATH>:/bundle --env <THIS_IMAGE>

FROM ubuntu:20.04 

# If set to 1 bundle is built and available as bundle/bundle.tar
# If set to 0 bundle is build and pushed to repo

RUN apt-get update && apt-get install -y gpg \
    curl \
    libdigest-sha-perl  

WORKDIR /bundle-builder
COPY *.sh ./
#Default config
COPY config/ubuntu/20_04/k8s/1_22 /config/
COPY ingredients/deb/download.sh download.sh

RUN chmod a+x *.sh
RUN mkdir /ingredients && mkdir /bundle

ENV PATH="/bundle-builder:${PATH}"

WORKDIR /bundle
ENTRYPOINT ["build-push-bundle.sh", "/ingredients", "/config"]
