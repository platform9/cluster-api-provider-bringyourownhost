set -ex
echo 'alias shasum="sha512sum"' >> ~/.bashrc
source ~/.bashrc


export BUILD_ONLY=${BUILD_ONLY:-1}
export CONTAINERD_VERSION=${CONTAINERD_VERSION:-1.7.26}
export KUBERNETES_VERSION=${KUBERNETES_VERSION:-1.32.3-1.1}
export KUBERNETES_MAJOR_VERSION=${KUBERNETES_MAJOR_VERSION:-v1.32}
export BUNDLE_VERSION=${BUNDLE_VERSION:-v1.32.3}
export ARCH=${ARCH:-amd64}
export CRITOOL_VERSION=${CRITOOL_VERSION:-1.32.0-1.1}
export UBUNTU_VERSION=${UBUNTU_VERSION:-"22.04"} # Default to 22.04, can be overridden

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
docker run -e CRITOOL_VERSION -e BUILD_ONLY -e CONTAINERD_VERSION -e KUBERNETES_VERSION -e KUBERNETES_MAJOR_VERSION -e ARCH -e UBUNTU_VERSION --name byoh-bundle-container -i byoh-bundle /bin/bash

echo "creating bundle dir to push k8s packages"
mkdir -p ./bundle

echo "coping bundle from docker image"
docker cp byoh-bundle-container:/bundle/. ./bundle/

# Determine bundle name based on Ubuntu version
case "$UBUNTU_VERSION" in
    "20.04")
        BUNDLE_NAME="byoh-bundle-ubuntu_20.04.1_x86-64_k8s"
        ;;
    "22.04")
        BUNDLE_NAME="byoh-bundle-ubuntu_22.04_x86-64_k8s"
        ;;
    *)
        echo "Error: Unsupported Ubuntu version '$UBUNTU_VERSION'"
        echo "Supported versions are: 20.04, 22.04"
        docker rm byoh-bundle-container
        exit 1
        ;;
esac

if [ "$UBUNTU_VERSION" = "20.04" ]; then
    BUNDLE_NAME="byoh-bundle-ubuntu_20.04.1_x86-64_k8s"
else
    BUNDLE_NAME="byoh-bundle-ubuntu_22.04_x86-64_k8s"
fi

# Push bundle
echo "pushing oci bundle to quay.io/platform9/$BUNDLE_NAME"
./imgpkg push -f ./bundle -i quay.io/platform9/$BUNDLE_NAME:$BUNDLE_VERSION
