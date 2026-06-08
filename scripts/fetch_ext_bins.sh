#!/usr/bin/env bash
# Copyright 2021 VMware, Inc. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0

set -o errexit
set -o nounset
set -o pipefail

# Enable tracing in this script off by setting the TRACE variable in your
# environment to any value:
#
# $ TRACE=1 test.sh
TRACE=${TRACE:-""}
if [[ -n "${TRACE}" ]]; then
  set -x
fi

k8s_version=1.25.0
# goarch=amd64
goarch="$(go version | awk '{print $NF}' | egrep -o '[^/]+$')"
goos="unknown"

if [[ "${OSTYPE}" == "linux"* ]]; then
  goos="linux"
elif [[ "${OSTYPE}" == "darwin"* ]]; then
  goos="darwin"
fi

if [[ "$goos" == "unknown" ]]; then
  echo "OS '$OSTYPE' not supported. Aborting." >&2
  exit 1
fi

# Turn colors in this script off by setting the NO_COLOR variable in your
# environment to any value:
#
# $ NO_COLOR=1 test.sh
NO_COLOR=${NO_COLOR:-""}
if [[ -z "${NO_COLOR}" ]]; then
  header=$'\e[1;33m'
  reset=$'\e[0m'
else
  header=''
  reset=''
fi

function header_text {
  echo "$header$*$reset"
}

tmp_root=/tmp

# Skip fetching and untaring the tools by setting the SKIP_FETCH_TOOLS variable
# in your environment to any value:
#
# $ SKIP_FETCH_TOOLS=1 ./fetch_ext_bins.sh
#
# If you skip fetching tools, this script will use the tools already on your
# machine.
SKIP_FETCH_TOOLS=${SKIP_FETCH_TOOLS:-""}

function fetch_tools {
  if [[ -n "$SKIP_FETCH_TOOLS" ]]; then
    return 0
  fi

  mkdir -p ${tmp_root}

  # use the pre-existing version in the temporary folder if it matches our k8s version
  if [[ -x "${tmp_root}/controller-tools/envtest/kube-apiserver" ]]; then
    version=$(${tmp_root}/controller-tools/envtest/kube-apiserver --version)
    if [[ $version == *"${k8s_version}"* ]]; then
      return 0
    fi
  fi

  header_text "fetching envtest@${k8s_version}"
  kb_tools_archive_name="envtest-v${k8s_version}-${goos}-${goarch}.tar.gz"
  kb_tools_download_url="https://github.com/kubernetes-sigs/controller-tools/releases/download/envtest-v${k8s_version}/${kb_tools_archive_name}"

  kb_tools_archive_path="${tmp_root}/${kb_tools_archive_name}"
  if [[ ! -f ${kb_tools_archive_path} ]]; then
    curl -fsL ${kb_tools_download_url} -o "${kb_tools_archive_path}"
  fi
  rm -rf "${tmp_root}/controller-tools"
  tar -zvxf "${kb_tools_archive_path}" -C "${tmp_root}/"
  rm "${kb_tools_archive_path}"
}

function setup_envs {
  header_text "setting up envtest@${k8s_version} env vars"
  export KUBEBUILDER_ASSETS="${tmp_root}/controller-tools/envtest"
}
