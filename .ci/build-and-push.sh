#!/usr/bin/env bash
set -o nounset
set -o errexit
set -o pipefail

project_root=$(realpath "$(dirname $0)/..")
build_dir=${project_root}/build
CONTAINER_TAG=${CONTAINER_TAG:-${build_dir}/manager-container-tag}
CONTAINER_FULL_TAG=${CONTAINER_FULL_TAG:-${build_dir}/manager-container-full-tag}
GO_VERSION=${GO_VERSION:-1.20.0}

