#!/bin/bash

set -e

if [ ! -z "${DEBUG:-}" ]; then
  set -x
  PS4='${BASH_SOURCE}:${LINENO} '
fi

RED='\033[1;31m'
YELLOW='\033[1;33m'
BLUE='\033[1;34m'
NC='\033[0m'
script=$(realpath "$0")
scriptpath=$(dirname "$script")
REPO="$scriptpath/.."
BIN_PATH="$REPO/build/bin"
WORKLOAD_CHART="byoh-chart"
log::info() { echo -e "${BLUE}[INFO] $*${NC}" >&2; }
log::warn() { echo -e "${YELLOW}[WARN] $*${NC}" >&2; }
log::fatal() { echo -e "${RED}[FATAL] $*${NC}" >&2 && exit 1; }
log::error() { echo -e "${RED}[ERROR] $*${NC}" >&2; }

usage() {
  echo "$0 <-r release-version> <-e byoh-image>"
  echo "Environment variables:"
  echo "INSTALL_BINARIES=1 : If this environment variable is set then this script will **attempt** to install relevant binaries"
  echo "DEBUG=1            : To enable debug"
  exit 1
}

install_yq() {
  if [ ${INSTALL_BINARIES:-0} -ne 1 ]; then
    exit 1
  fi
  log::warn "Attempting to install 'yq' binary"
  mkdir -p $BIN_PATH
  url=""
  if [[ "$OSTYPE" == "darwin"* ]]; then
    case $(uname -m) in
      x86_64)
        url="https://github.com/mikefarah/yq/releases/latest/download/yq_darwin_amd64"
      ;;
      arm64)
        url="https://github.com/mikefarah/yq/releases/latest/download/yq_darwin_arm64"
      ;;
    esac
  else
    url="https://github.com/mikefarah/yq/releases/latest/download/yq_linux_amd64"
  fi
  wget $url -O $BIN_PATH/yq && chmod +x $BIN_PATH/yq

}

install_kustomize() {
  if [ ${INSTALL_BINARIES:-0} -ne 1 ]; then
    exit 1
  fi
  log::warn "Attempting to install 'kustomize' binary"
  mkdir -p $BIN_PATH
  curl -sL --fail "https://raw.githubusercontent.com/kubernetes-sigs/kustomize/master/hack/install_kustomize.sh" | bash
  mv ./kustomize "${BIN_PATH}/kustomize"
  $BIN_PATH/kustomize version
}

install_helm() {
  if [ ${INSTALL_BINARIES:-0} -ne 1 ]; then
    exit 1
  fi
  log::warn "Attempting to install 'helm' v3.12.3 binary"
  mkdir -p $BIN_PATH
  #Checking OS type and cpu arch for downloading correct binary
  url=""
  bin_dir=""
  if [[ "$OSTYPE" == "darwin"* ]]; then
    case $(uname -m) in
      x86_64)
        url="https://get.helm.sh/helm-v3.12.3-darwin-amd64.tar.gz"
        bin_dir="darwin-amd64"
      ;;
      arm64)
        url="https://get.helm.sh/helm-v3.12.3-darwin-arm64.tar.gz"
        bin_dir="darwin-arm64"
      ;;
    esac
  else
    # Supporting only linux amd64 for now.
    url="https://get.helm.sh/helm-v3.12.3-linux-amd64.tar.gz"
    bin_dir="linux-amd64"
  fi
  wget $url -O - | tar -xz -C $BIN_PATH
  mv $BIN_PATH/$bin_dir/helm $BIN_PATH/.
  $BIN_PATH/helm version
}

# Check and attempt to install pre-requisite commands
prereqs() {
  which realpath >/dev/null || (echo >&2 "error: missing required command 'realpath'" && exit 1)
  which dirname >/dev/null || (echo >&2 "error: missing required command 'dirname'" && exit 1)
  script=$(realpath "$0")
  scriptpath=$(dirname "$script")
  REPO="$scriptpath/.."
  BIN_PATH="$REPO/build/bin"
  which helm >/dev/null || (echo >&2 "error: missing required command 'helm'" && install_helm)
  which kustomize >/dev/null || (echo >&2 "error: missing required command 'kustomize'" && install_kustomize)
  which git >/dev/null || (echo >&2 "error: missing required command 'git'" && exit 1)
  which yq >/dev/null || (echo >&2 "error: missing required command 'yq'" && install_yq)
  log::info "Checking if the right flavor of yq is installed (there too many incompatible ones)"
  yq --version | grep 'https://github.com/mikefarah/yq/' || (echo >&2 "error: invalid version of yq installed. This script requires https://github.com/mikefarah/yq. Use 'brew install yq' or download it directly." && install_yq)
}

drop_namespace_from_yaml() {
  local yaml=$1
  yq -s '"file_" + $index' $yaml
  rm -rf $yaml
  rm -rf $(grep 'kind: Namespace' file_*.yml -l)
  for f in $(ls -S file_*.yml);
  do
    cat $f >> $yaml
  done
  rm -rf file_*.yml
}
stage_byoh_template() {
  version=$1
  if [ ! -z "$(ls -A $BIN_PATH)" ]; then
    export PATH="$BIN_PATH/:$PATH"
  fi

  workload_chart_dir="$REPO/$WORKLOAD_CHART"
  helm create $workload_chart_dir
  
  rm -rf $workload_chart_dir/templates/*.yaml $workload_chart_dir/templates/NOTES.txt $workload_chart_dir/templates/tests

  kustomize build --enable-helm $REPO/chart-generator/byoh-chart > $workload_chart_dir/templates/workload.yaml

  # Copy extra templates
  # cp $REPO/chart-generator/templates/* $workload_chart_dir/templates/.

  yq -i '.name="'$WORKLOAD_CHART'"' $workload_chart_dir/Chart.yaml
  yq -i '.description="A helm chart for deploying Byoh Manager"' $workload_chart_dir/Chart.yaml
  yq -i '.version="'$version'"' $workload_chart_dir/Chart.yaml
  yq -i '.appVersion="'$version'"' $workload_chart_dir/Chart.yaml

  pushd $workload_chart_dir/templates
  drop_namespace_from_yaml workload.yaml
  popd
}

main() {
  major_minor_version=${BYOH_VERSION:-0.1}
  build_number=${BUILD_NUMBER:-0}
  release_version="$major_minor_version.$build_number"
  byoh_image="byoh-controller-manager:local"

  while getopts ":h:e:o:" opt; do
  case $opt in
    h)
      usage
      ;;
    e)
      byoh_image="$OPTARG"
      ;;
    *)
      usage
      ;;
  esac
  done
  
  prereqs
  log::info "Generating charts with version: release_version"
  stage_byoh_template $release_version
  log::info "Generating chart values.yaml"

  sed -e "s|__CONTROLLER_IMAGE__|${byoh_image}|g"  $REPO/chart-generator/sample-values.yaml > $REPO/$WORKLOAD_CHART/values.yaml

  log::info "Publishing helm version"
  echo -n "${release_version}" > "${REPO}/helm-chart-version"
  cat "${REPO}/helm-chart-version"

}

main $@
