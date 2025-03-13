ARG BASE_IMAGE=ubuntu:22.04
FROM $BASE_IMAGE as build

# Override to download other version

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends sudo

WORKDIR /bundle-builder
COPY download.sh .
RUN chmod a+x download.sh
COPY config
WORKDIR /ingredients
RUN /bundle-builder/download.sh

RUN mkdir config 

