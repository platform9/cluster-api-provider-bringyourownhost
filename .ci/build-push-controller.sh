#!/usr/bin/env bash

# build-and-push.sh - CI script for building and publishing the byoh controller manager Docker image.
#
# Parameters:
# - IMAGE_REGISTRY  Registry to publish the Docker image. By default 'quay.io/platform9/byoh-controller-manager' is used.
# - IMAGE_NAME      Name to use for this image. By default 'byoh-controller-manager' is used.
# - IMAGE_TAG       Tag to use for the image. By default '$BYOHCM_VERSION-$BUILD_NUMBER' is used.
# - IMAGE_REGISTRY      URL (without scheme) pointing to quay
# - DRY_RUN         If non-empty, no Docker image will be published.
# - CONTAINER_TAG   Location of the container_tag file (used as an artifact in TeamCity)
# - DOCKER_USERNAME Username to login to quay.io.
# - DOCKER_PASSWORD Password to login to quay.io.
#
# Examples:
# - `USE_SYSTEM_GO=1 IMAGE_REGISTRY=quay.io IMAGE_NAME=platform9/byoh-controller-manager IMAGE_TAG=latest ./build-and-push.sh`: To test the script locally without gimme and push to Docker

set -o nounset
set -o errexit
set -o pipefail

project_root=$(realpath "$(dirname $0)/..")
build_dir=${project_root}/build
CONTAINER_TAG=${CONTAINER_TAG:-${build_dir}/manager-container-tag}
CONTAINER_FULL_TAG=${CONTAINER_FULL_TAG:-${build_dir}/manager-container-full-tag}
GO_VERSION=${GO_VERSION:-1.23.1}

BUILD_NUMBER=${BUILD_NUMBER:-0}
BYOHCM_VERSION=${BYOHCM_VERSION:-0.1}

IMAGE_REGISTRY=${IMAGE_REGISTRY:-"quay.io/platform9"}
IMAGE_NAME=${IMAGE_NAME:-"byoh-controller-manager"}
IMAGE_TAG=${IMAGE_TAG:-${BYOHCM_VERSION}.${BUILD_NUMBER}}
IMAGE_NAME_TAG=${IMAGE_NAME}:${IMAGE_TAG}
IMAGE_REGISTRY_NAME_TAG=${IMAGE_REGISTRY}/${IMAGE_NAME_TAG}


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
  which docker > /dev/null || (echo "error: missing required command 'docker'" && exit 1)
  # note: go and/or gimme are checked in configure_go

  info "Preparing build environment"
  mkdir -p "${build_dir}"

  info "Configure Docker registry and create image repository if not present"
  configure_docker_registry "${IMAGE_NAME}"

  info "Configure go"
  configure_go

  # ensure vendor directory is present
  go mod vendor

  info "Build Docker image"
  # Do not build the image with the registry prefix, because docker will think it is part of the name.
  make docker-build IMG="${IMAGE_REGISTRY_NAME_TAG}"

  info "Pushing Docker image to ${IMAGE_REGISTRY_NAME_TAG}"
  if [ -z "${DRY_RUN:-}" ] ; then
    make docker-push IMG="${IMAGE_REGISTRY_NAME_TAG}"
  else
    info "DRY_RUN is set; not publishing the image"
  fi

  info "Publish artifacts"
  mkdir -p "$(dirname "${CONTAINER_TAG}")" "$(dirname "${CONTAINER_FULL_TAG}")"
  echo -n "${IMAGE_TAG}" > "${CONTAINER_TAG}"
  echo -n "${IMAGE_REGISTRY_NAME_TAG}" > "${CONTAINER_FULL_TAG}"
  echo "Stored image tag in ${CONTAINER_TAG}:"
  cat "${CONTAINER_TAG}" && echo ""
  echo "Stored image full tag in ${CONTAINER_FULL_TAG}:"
  cat "${CONTAINER_FULL_TAG}" && echo ""
}

on_exit() {
  ret=$?
  info "-------cleanup--------"
  if [ -z "${SKIP_CLEANUP:-}" ] ; then
    make docker-clean IMG="${IMAGE_REGISTRY_NAME_TAG}" || true
  fi
  popd
  exit ${ret}
}

configure_docker_registry() {
  repository=$1
  if [ "${IMAGE_REGISTRY}" = "quay.io/platform9" ]; then
    if [ -n "${DOCKER_PASSWORD:-}" ] ; then
      echo -n "${DOCKER_PASSWORD}" | docker login --username "${DOCKER_USERNAME}" --password-stdin "${IMAGE_REGISTRY}"
    else
      echo "Using default docker registry"
    fi
  fi
  echo "Configured registry '${IMAGE_REGISTRY}' for '${repository}'"
}

configure_go() {
  if [ -n "${USE_SYSTEM_GO:-}" ] ; then
    echo "\$USE_SYSTEM_GO set, using system go instead of gimme"
    return 0
  else
    which gimme > /dev/null || (echo "error: missing required command 'gimme'" && exit 1)
    eval "$(GIMME_GO_VERSION=${GO_VERSION} gimme)"
  fi
  which go
  go version
}

RED='\033[1;31m'
YELLOW='\033[1;33m'
NC='\033[0m'
info() { echo -e >&2 "${YELLOW}[INFO] $@${NC}" ; }
fatal() { echo >&2 "${RED}[FATAL] $@${NC}" ; exit 1 ; }

# shellcheck disable=SC2068
main $@