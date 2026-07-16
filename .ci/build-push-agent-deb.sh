set -ex
export BYOH_DEB_VERSION=${BYOH_DEB_VERSION:-$(git describe --dirty --tags --match='v*' 2>/dev/null || echo "v0.0.0-$(git rev-parse --short HEAD)")}

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

echo "pushing deb bundle to quay.io/platform9/cluster-api-provider-bringyourownhost/agent:$BYOH_DEB_VERSION"
./imgpkg push -f build/pf9-byohost/debsrc/ -i quay.io/platform9/cluster-api-provider-bringyourownhost/agent:$BYOH_DEB_VERSION

