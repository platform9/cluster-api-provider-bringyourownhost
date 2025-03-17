set -ex
echo 'alias shasum="sha512sum"' >> ~/.bashrc
source ~/.bashrc

export BUILD_ONLY
export CONTAINERD_VERSION 
export KUBERNETES_VERSION
export KUBERNETES_MAJOR_VERSION
export ARCH

#alias shasum="sha512sum"
echo "installing imgpkg"
curl -LO https://github.com/carvel-dev/imgpkg/releases/download/v0.43.1/imgpkg-linux-amd64
mv imgpkg-linux-amd64 installer/bundle_builder/imgpkg
chmod +x installer/bundle_builder/imgpkg

cd installer/bundle_builder

echo "building docker image to create byoh-bundle"
docker build -t byoh-bundle .
docker rm -f byoh-bundle-container

echo "executing docker image"
docker run -e BUILD_ONLY -e CONTAINERD_VERSION -e KUBERNETES_VERSION -e KUBERNETES_MAJOR_VERSION -e ARCH --name byoh-bundle-container -i byoh-bundle /bin/bash

echo "creating bundle dir to push k8s packages"
mkdir -p ./bundle

echo "coping bundle from docker image"
docker cp byoh-bundle-container:/bundle ./bundle

echo "pushing oci bundle to quay.io/platform9/byoh-bundle-ubuntu_20.04.1_x86-64_k8s"
./imgpkg push -f ./bundle -i quay.io/platform9/byoh-bundle-ubuntu_20.04.1_x86-64_k8s:$KUBERNETES_VERSION

