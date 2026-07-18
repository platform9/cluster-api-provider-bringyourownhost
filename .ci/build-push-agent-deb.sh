#!/usr/bin/env bash
set -Eeuo pipefail

main() {
  export BYOH_DEB_VERSION=${BYOH_DEB_VERSION:-$(make tag)}

  echo 'alias shasum="sha512sum"' >>~/.bashrc
  # shellcheck disable=SC1090 # sourcing the user's own ~/.bashrc, not a repo file shellcheck can resolve
  source ~/.bashrc

  echo "removing build/ if already present"
  rm -rf build/
  echo "started building byoh-agent binary"
  make build-host-agent-binary

  echo "started building deb package for byoh-agent"
  make build-host-agent-deb

  echo "created deb package under build/pf9-byohost/debsrc/ "
}

main "$@"
