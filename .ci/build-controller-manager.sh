#!/usr/bin/env bash

# build-controller-manager.sh - CI script for building the byoh controller manager Docker image.
#
# Parameters:
# - IMAGE_REGISTRY  Registry to tag the Docker image for. By default 'quay.io/platform9/cluster-api-provider-bringyourownhost' is used.
# - IMAGE_NAME      Name to use for this image. By default 'controller-manager' is used.
# - IMAGE_TAG       Tag to use for the image. By default the output of `make tag` (git describe) is used.
# - CONTAINER_TAG   Location of the container_tag file (used as an artifact in TeamCity)
#
# Examples:
# - `USE_SYSTEM_GO=1 IMAGE_REGISTRY=quay.io IMAGE_NAME=platform9/cluster-api-provider-bringyourownhost/controller-manager IMAGE_TAG=latest ./build-controller-manager.sh`: To test the script locally without gimme

set -o nounset
set -o errexit
set -o pipefail

project_root=$(realpath "$(dirname "$0")/..")
build_dir=${project_root}/build
CONTAINER_TAG=${CONTAINER_TAG:-${build_dir}/manager-container-tag}
CONTAINER_FULL_TAG=${CONTAINER_FULL_TAG:-${build_dir}/manager-container-full-tag}
GO_VERSION=${GO_VERSION:-1.22.5}

IMAGE_REGISTRY=${IMAGE_REGISTRY:-"quay.io/platform9/cluster-api-provider-bringyourownhost"}
IMAGE_NAME=${IMAGE_NAME:-"controller-manager"}
IMAGE_TAG=${IMAGE_TAG:-$(make --no-print-directory -C "${project_root}" tag)}
IMAGE_NAME_TAG=${IMAGE_NAME}:${IMAGE_TAG}
IMAGE_REGISTRY_NAME_TAG=${IMAGE_REGISTRY}/${IMAGE_NAME_TAG}

# make -C implicitly enables --print-directory on some GNU Make versions
# (confirmed: not on this repo's dev-Mac Make 3.81, but yes on the Ubuntu
# CI runner's newer Make) -- without --no-print-directory above, the
# "Entering directory" chatter leaks into IMAGE_TAG via command
# substitution and corrupts the docker -t argument. Fail loud if it ever
# recurs instead of silently building/pushing a mistagged image.
if [[ "${IMAGE_TAG}" =~ [[:space:]] ]]; then
  echo "ERROR: IMAGE_TAG contains whitespace, likely make output leaked into the tag: '${IMAGE_TAG}'" >&2
  exit 1
fi

main() {
  # Move to the project directory
  pushd "${project_root}"
  trap on_exit EXIT

  if [ -n "${BASH_DEBUG:-}" ]; then
    set -x
    PS4='${BASH_SOURCE}.${LINENO} '
  fi

  info "Verifying prerequisites"
  #which aws > /dev/null || (echo "error: missing required command 'aws'" && exit 1)
  which docker >/dev/null || (echo "error: missing required command 'docker'" && exit 1)
  # note: go and/or gimme are checked in configure_go

  info "Preparing build environment"
  mkdir -p "${build_dir}"

  info "Configure go"
  configure_go

  # ensure vendor directory is present
  go mod vendor

  info "Build Docker image"
  # Do not build the image with the registry prefix, because docker will think it is part of the name.
  make docker-build IMG="${IMAGE_REGISTRY_NAME_TAG}"

  info "Publish artifacts"
  mkdir -p "$(dirname "${CONTAINER_TAG}")" "$(dirname "${CONTAINER_FULL_TAG}")"
  echo -n "${IMAGE_TAG}" >"${CONTAINER_TAG}"
  echo -n "${IMAGE_REGISTRY_NAME_TAG}" >"${CONTAINER_FULL_TAG}"
  echo "Stored image tag in ${CONTAINER_TAG}:"
  cat "${CONTAINER_TAG}" && echo ""
  echo "Stored image full tag in ${CONTAINER_FULL_TAG}:"
  cat "${CONTAINER_FULL_TAG}" && echo ""
}

on_exit() {
  ret=$?
  info "-------cleanup--------"
  if [ -z "${SKIP_CLEANUP:-}" ]; then
    make docker-clean IMG="${IMAGE_REGISTRY_NAME_TAG}" || true
  fi
  popd
  exit ${ret}
}

configure_go() {
  if [ -n "${USE_SYSTEM_GO:-}" ]; then
    echo "\$USE_SYSTEM_GO set, using system go instead of gimme"
    return 0
  else
    which gimme >/dev/null || (echo "error: missing required command 'gimme'" && exit 1)
    eval "$(GIMME_GO_VERSION=${GO_VERSION} gimme)"
  fi
  which go
  go version
}

RED='\033[1;31m'
YELLOW='\033[1;33m'
NC='\033[0m'
info() { echo -e "${YELLOW}[INFO] $*${NC}" >&2; }
fatal() {
  echo >&2 "${RED}[FATAL] $*${NC}"
  exit 1
}

# shellcheck disable=SC2068
main $@
