#!/usr/bin/env bash

# publish-byohctl-s3.sh - CI script for publishing a released byohctl binary to S3.
#
# Downloads the byohctl artifact GitHub Actions already published to Quay, then uploads the
# linux/amd64 binary out of it to S3. Nothing is compiled here.
#
# Parameters:
# - BYOHCTL_VERSION               Required. Version of the published byohctl artifact, as printed
#                                 by `make tag` (git describe output, e.g. v0.5.0-281-ged501738).
#                                 Must resolve to a commit reachable from origin/main.
# - QUAY_USERNAME                 Required. Quay robot account name, mapped onto
#                                 IMGPKG_REGISTRY_USERNAME.
# - QUAY_TOKEN                    Required. Quay robot account token, mapped onto
#                                 IMGPKG_REGISTRY_PASSWORD.
# - AWS_ACCESS_KEY_ID             Required. Read as-is by the aws CLI. A caller holding the
#                                 credential under another name maps it before invoking this
#                                 script.
# - AWS_SECRET_ACCESS_KEY         Required. Same.
# - S3_BUCKET                     Required. Bucket to upload into.
# - S3_DOWNLOAD_URL_FILE          Required. Basename of the file the download URL is written to,
#                                 inside the build directory.
# - BYOHCTL_IMAGE                 Optional. Overrides the image to pull from.
# - TEAMCITY_BUILD_NUMBER         Optional. Recorded as an S3 object tag.
# - TEAMCITY_BUILD_ID             Optional. Recorded as an S3 object tag.
# - TEAMCITY_BUILD_BRANCH         Optional. Recorded as an S3 object tag.
# - BASH_DEBUG                    Optional. Set to any non-empty value to turn on `set -x`.
#
set -o nounset
set -o errexit
set -o pipefail

project_root=$(realpath "$(dirname "$0")/..")
bin_dir=${project_root}/cmd/byohctl/bin
byohctl_binary=${bin_dir}/byohctl
BYOHCTL_VERSION=${BYOHCTL_VERSION:-}
S3_BUCKET=${S3_BUCKET:-}
S3_DOWNLOAD_URL_FILE=${S3_DOWNLOAD_URL_FILE:-}
QUAY_USERNAME=${QUAY_USERNAME:-}
QUAY_TOKEN=${QUAY_TOKEN:-}
AWS_ACCESS_KEY_ID=${AWS_ACCESS_KEY_ID:-}
AWS_SECRET_ACCESS_KEY=${AWS_SECRET_ACCESS_KEY:-}
BYOHCTL_IMAGE=${BYOHCTL_IMAGE:-quay.io/platform9/cluster-api-provider-bringyourownhost/byohctl}

# The published artifact holds one binary per platform. Only linux/amd64 is published to S3,
# and this script runs the binary to read its version, so no other platform would work here.
ARTIFACT_PLATFORM="linux-amd64"

# imgpkg is what publishes the artifact (cmd/byohctl/Makefile's push-byohctl target), so
# it is also what reads it back. Pinned by SHA256 (not a sha256sums.txt fetched at runtime)
# so a compromised upstream release cannot swap the binary CI executes without a second,
# reviewable commit to this repo. Keep this pin in step with push-byohctl's.
IMGPKG_VERSION="v0.43.1"
IMGPKG_SHA256="d36ccfcc54143d2475cf09b0247c88bccf24a7aeb062bd9bb8cab7cb58135fcf"

tmpdir=""
cleanup() { rm -rf "${tmpdir}"; }
trap cleanup EXIT

main() {
  # Move to the project directory
  cd "${project_root}"

  if [ -n "${BASH_DEBUG:-}" ]; then
    set -x
    PS4='${BASH_SOURCE}.${LINENO} '
  fi

  # Everything is validated before the download, so a missing AWS key fails the build in a
  # second rather than after a pull from Quay.
  validate_version
  validate_destination
  validate_credentials
  check_version_on_main

  install_imgpkg
  pull_byohctl
  publish_to_s3
}

validate_version() {
  if [ -z "${BYOHCTL_VERSION}" ]; then
    fatal "BYOHCTL_VERSION is required, e.g. v0.5.0-281-ged501738"
  fi

  # A version that carries make output or shell quoting is never a real artifact tag.
  if [[ "${BYOHCTL_VERSION}" =~ [[:space:]] ]]; then
    fatal "BYOHCTL_VERSION contains whitespace: '${BYOHCTL_VERSION}'"
  fi

  # git describe appends these when the worktree it described was not clean. Such a build
  # was never published, and the string does not resolve to a commit.
  if [[ "${BYOHCTL_VERSION}" == *-dirty || "${BYOHCTL_VERSION}" == *-broken ]]; then
    fatal "BYOHCTL_VERSION '${BYOHCTL_VERSION}' is from an unclean worktree and was never published"
  fi
}

validate_destination() {
  if [ -z "${S3_BUCKET}" ]; then
    fatal "S3_BUCKET is required"
  fi
  if [ -z "${S3_DOWNLOAD_URL_FILE}" ]; then
    fatal "S3_DOWNLOAD_URL_FILE is required"
  fi
  # It is written inside the build directory, so a path would put it somewhere unexpected.
  if [[ "${S3_DOWNLOAD_URL_FILE}" == */* ]]; then
    fatal "S3_DOWNLOAD_URL_FILE must be a filename, not a path: '${S3_DOWNLOAD_URL_FILE}'"
  fi
}

validate_credentials() {
  if [ -z "${QUAY_USERNAME}" ]; then
    fatal "QUAY_USERNAME is required to pull from quay.io"
  fi
  if [ -z "${QUAY_TOKEN}" ]; then
    fatal "QUAY_TOKEN is required to pull from quay.io"
  fi
  if [ -z "${AWS_ACCESS_KEY_ID}" ]; then
    fatal "AWS_ACCESS_KEY_ID is required to upload to s3://${S3_BUCKET}"
  fi
  if [ -z "${AWS_SECRET_ACCESS_KEY}" ]; then
    fatal "AWS_SECRET_ACCESS_KEY is required to upload to s3://${S3_BUCKET}"
  fi
}

# The version is caller-supplied, so confirm it names a commit that is on main before
# anything is downloaded or uploaded.
check_version_on_main() {
  local rev status

  if ! git rev-parse --verify --quiet "origin/main" >/dev/null; then
    fatal "origin/main does not resolve in this checkout, cannot verify the version is on main"
  fi

  if ! rev=$(git rev-parse --verify --quiet "${BYOHCTL_VERSION}^{commit}"); then
    fatal "BYOHCTL_VERSION '${BYOHCTL_VERSION}' does not resolve to a commit in this checkout"
  fi

  # --is-ancestor exits 1 for "not reachable" and something else for a real failure.
  # errexit would kill the script on either, so capture the status instead of branching
  # on the command directly.
  status=0
  git merge-base --is-ancestor "${rev}" origin/main || status=$?
  case ${status} in
  0) : ;;
  1) fatal "BYOHCTL_VERSION '${BYOHCTL_VERSION}' (${rev}) is not reachable from origin/main" ;;
  *) fatal "ancestry check for ${rev} against origin/main failed with exit ${status}" ;;
  esac

  info "Version ${BYOHCTL_VERSION} resolves to ${rev} on main"
}

install_imgpkg() {
  info "Installing imgpkg ${IMGPKG_VERSION}"
  curl -fsSL -o /tmp/imgpkg-byohctl-download \
    "https://github.com/carvel-dev/imgpkg/releases/download/${IMGPKG_VERSION}/imgpkg-linux-amd64"
  echo "${IMGPKG_SHA256}  /tmp/imgpkg-byohctl-download" | sha256sum -c -
  install -m 0755 /tmp/imgpkg-byohctl-download /tmp/imgpkg-byohctl
}

# push-byohctl publishes with `imgpkg push -i`, so the artifact is a plain OCI image and
# not a Carvel bundle. Pull it the same way.
pull_byohctl() {
  tmpdir=$(mktemp -d)

  # imgpkg's per-registry keychain needs IMGPKG_REGISTRY_HOSTNAME for the username and
  # password to apply at all. Any IMGPKG_REGISTRY_* variable outside the five imgpkg knows
  # is a hard error, so do not add others here. Do not set the host-agnostic
  # IMGPKG_USERNAME/IMGPKG_PASSWORD either: they suppress the docker-config fallback.
  export IMGPKG_REGISTRY_HOSTNAME="quay.io"
  export IMGPKG_REGISTRY_USERNAME="${QUAY_USERNAME}"
  export IMGPKG_REGISTRY_PASSWORD="${QUAY_TOKEN}"

  info "Pulling ${BYOHCTL_IMAGE}:${BYOHCTL_VERSION}"
  /tmp/imgpkg-byohctl pull -i "${BYOHCTL_IMAGE}:${BYOHCTL_VERSION}" -o "${tmpdir}"

  # Land it as plain byohctl, which is the name the job's artifact rules and the S3 keys
  # already use.
  mkdir -p "${bin_dir}"
  install -m 0755 "${tmpdir}/byohctl-${ARTIFACT_PLATFORM}" "${byohctl_binary}"
  info "Downloaded ${byohctl_binary}"
}

publish_to_s3() {
  local byohctl_version
  # byohctl prints an "installed elsewhere" warning on stdout ahead of the version, so keep
  # only the last line. Fail loud if what is left still does not look like a single token,
  # rather than uploading an object under a garbage key.
  byohctl_version=$("${byohctl_binary}" version | tail -n 1)
  if [[ -z "${byohctl_version}" || "${byohctl_version}" =~ [[:space:]] ]]; then
    fatal "could not read a usable version from byohctl version: '${byohctl_version}'"
  fi

  # The checked-out commit has no relationship to the downloaded binary, so record the
  # requested artifact version rather than the commit this script runs from.
  local tags_json
  tags_json=$(jq -n \
    --arg creation_date "$(date '+%d.%b.%Y')" \
    --arg artifact_version "${BYOHCTL_VERSION}" \
    --arg build_number "${TEAMCITY_BUILD_NUMBER:-}" \
    --arg build_id "${TEAMCITY_BUILD_ID:-}" \
    --arg build_branch "${TEAMCITY_BUILD_BRANCH:-}" \
    --arg byohctl_version "${byohctl_version}" \
    '{TagSet: [
      {Key: "CreationDate", Value: $creation_date},
      {Key: "ArtifactVersion", Value: $artifact_version},
      {Key: "TEAMCITY_BUILD_NUMBER", Value: $build_number},
      {Key: "TEAMCITY_BUILD_ID", Value: $build_id},
      {Key: "TEAMCITY_BUILD_BRANCH", Value: $build_branch},
      {Key: "ByohctlVersion", Value: $byohctl_version}
    ]}')

  local versioned_key="byohctl-v${byohctl_version}"

  info "Uploading byohctl to s3://${S3_BUCKET}"
  aws s3api put-object --bucket "${S3_BUCKET}" --key "byohctl" --body "${byohctl_binary}"
  aws s3api put-object --bucket "${S3_BUCKET}" --key "${versioned_key}" --body "${byohctl_binary}"

  info "Tagging uploaded objects"
  aws s3api put-object-tagging --bucket "${S3_BUCKET}" --key "byohctl" --tagging "${tags_json}"
  aws s3api put-object-tagging --bucket "${S3_BUCKET}" --key "${versioned_key}" --tagging "${tags_json}"

  # Kept in bin_dir, where the job's artifact rules already pick it up.
  local download_url_file="${bin_dir}/${S3_DOWNLOAD_URL_FILE}"
  echo "https://${S3_BUCKET}.s3.us-west-2.amazonaws.com/${versioned_key}" >"${download_url_file}"
  info "Wrote download URL to ${download_url_file}"
}

RED='\033[1;31m'
YELLOW='\033[1;33m'
NC='\033[0m'
info() { echo -e "${YELLOW}[INFO] $*${NC}" >&2; }
fatal() {
  echo -e "${RED}[FATAL] $*${NC}" >&2
  exit 1
}

# shellcheck disable=SC2068
main $@
