set -ex
export BUILD_NUMBER
export MAJOR_MINOR_VERSION=0.1
export BYOH_DEB_VERSION="latest"

echo 'alias shasum="sha512sum"' >> ~/.bashrc
source ~/.bashrc

echo "removing build/ if already present"
rm -rf build/
echo "started building byoh-agent binary"
make build-host-agent-binary

echo "started building deb package for byoh-agent"
make build-host-agent-deb

echo "created deb package under build/pf9-byohost/debsrc/ "

echo "installing imgpkg"
curl -LO https://github.com/carvel-dev/imgpkg/releases/download/v0.43.1/imgpkg-linux-amd64
mv imgpkg-linux-amd64 imgpkg
chmod +x imgpkg

echo "pushing deb bundle to quay.io/platform9/byoh-deb:$BYOH_DEB_VERSION"
./imgpkg push -f build/pf9-byohost/debsrc/ -i quay.io/platform9/byoh-agent-deb:$BYOH_DEB_VERSION

