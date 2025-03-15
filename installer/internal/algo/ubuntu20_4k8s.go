// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package algo

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"os"
)

const (
	// ImgpkgVersion defines the imgpkg version that will be installed on host if imgpkg is not already installed
	ImgpkgVersion = "v0.36.4"
)

// isRunningInContainer detects if we're running in a container environment
func isRunningInContainer() bool {
	_, exists := os.LookupEnv("RUNNING_IN_CONTAINER")
	return exists
}

// getHostPrefix returns the host filesystem prefix when running in a container
func getHostPrefix() string {
	if isRunningInContainer() {
		return os.Getenv("HOST_PREFIX")
	}
	return ""
}

// Ubuntu20_04Installer represent the installer implementation for ubunto20.04.* os distribution
type Ubuntu20_04Installer struct {
	install   string
	uninstall string
}

// NewUbuntu20_04Installer will return new Ubuntu20_04Installer instance
func NewUbuntu20_04Installer(ctx context.Context, arch, bundleAddrs string) (*Ubuntu20_04Installer, error) {
	parseFn := func(script string) (string, error) {
		parser, err := template.New("parser").Parse(script)
		if err != nil {
			return "", fmt.Errorf("unable to parse install script")
		}
		var tpl bytes.Buffer
		if err = parser.Execute(&tpl, map[string]string{
			"BundleAddrs":        bundleAddrs,
			"Arch":               arch,
			"ImgpkgVersion":      ImgpkgVersion,
			"BundleDownloadPath": "{{.BundleDownloadPath}}",
		}); err != nil {
			return "", fmt.Errorf("unable to apply install parsed template to the data object")
		}
		return tpl.String(), nil
	}

	install, err := parseFn(DoUbuntu20_4K8s1_22)
	if err != nil {
		return nil, err
	}
	uninstall, err := parseFn(UndoUbuntu20_4K8s1_22)
	if err != nil {
		return nil, err
	}
	return &Ubuntu20_04Installer{
		install:   install,
		uninstall: uninstall,
	}, nil
}

// Install will return k8s install script
func (s *Ubuntu20_04Installer) Install() string {
	return s.install
}

// Uninstall will return k8s uninstall script
func (s *Ubuntu20_04Installer) Uninstall() string {
	return s.uninstall
}

// contains the installation and uninstallation steps for the supported os and k8s
var (
	DoUbuntu20_4K8s1_22 = `
set -euox pipefail

# Container environment detection
HOST_PREFIX=${HOST_PREFIX:-""}
if [ -n "$HOST_PREFIX" ]; then
    # We're in a container - use nsenter for host operations
    SYSTEMCTL="nsenter -t 1 -m -u -n -i systemctl"
    MODPROBE="nsenter -t 1 -m -u -n -i modprobe"
else
    # Direct host operations
    SYSTEMCTL="systemctl"
    MODPROBE="modprobe"
fi

BUNDLE_DOWNLOAD_PATH={{.BundleDownloadPath}}
BUNDLE_ADDR={{.BundleAddrs}}
IMGPKG_VERSION={{.ImgpkgVersion}}
ARCH={{.Arch}}
BUNDLE_PATH=$BUNDLE_DOWNLOAD_PATH/$BUNDLE_ADDR

# Adjust paths if in container
if [ -n "$HOST_PREFIX" ]; then
    REAL_BUNDLE_PATH="${HOST_PREFIX}${BUNDLE_PATH}"
else
    REAL_BUNDLE_PATH="$BUNDLE_PATH"
fi

if ! command -v imgpkg >>/dev/null; then
	echo "installing imgpkg"	
	
	if command -v wget >>/dev/null; then
		dl_bin="wget -nv -O-"
	elif command -v curl >>/dev/null; then
		dl_bin="curl -s -L"
	else
		echo "installing curl"
		apt-get install -y curl
		dl_bin="curl -s -L"
	fi
	
	$dl_bin github.com/vmware-tanzu/carvel-imgpkg/releases/download/$IMGPKG_VERSION/imgpkg-linux-$ARCH > /tmp/imgpkg
	
    # Install to host or container path
    if [ -n "$HOST_PREFIX" ]; then
        mv /tmp/imgpkg ${HOST_PREFIX}/usr/local/bin/imgpkg
        chmod +x ${HOST_PREFIX}/usr/local/bin/imgpkg
    else
        mv /tmp/imgpkg /usr/local/bin/imgpkg
        chmod +x /usr/local/bin/imgpkg
    fi
fi

echo "downloading bundle"
mkdir -p $REAL_BUNDLE_PATH
imgpkg pull -i $BUNDLE_ADDR -o $REAL_BUNDLE_PATH

## disable swap
if [ -n "$HOST_PREFIX" ]; then
    nsenter -t 1 -m -u -n -i bash -c "swapoff -a && sed -ri '/\sswap\s/s/^#?/#/' /etc/fstab"
else
    swapoff -a && sed -ri '/\sswap\s/s/^#?/#/' /etc/fstab
fi

## disable firewall
if command -v ufw >>/dev/null; then
    if [ -n "$HOST_PREFIX" ]; then
        nsenter -t 1 -m -u -n -i ufw disable
    else
        ufw disable
    fi
fi

## load kernel modules
$MODPROBE overlay && $MODPROBE br_netfilter

## adding os configuration
if [ -n "$HOST_PREFIX" ]; then
    tar -C ${HOST_PREFIX}/ -xvf "$REAL_BUNDLE_PATH/conf.tar" && nsenter -t 1 -m -u -n -i sysctl --system
else
    tar -C / -xvf "$BUNDLE_PATH/conf.tar" && sysctl --system
fi

## installing deb packages
for pkg in cri-tools kubernetes-cni kubectl kubelet kubeadm; do
    if [ -n "$HOST_PREFIX" ]; then
        # Install on host using chroot or nsenter
        nsenter -t 1 -m -u -n -i dpkg --install "$REAL_BUNDLE_PATH/$pkg.deb" && nsenter -t 1 -m -u -n -i apt-mark hold $pkg
    else
        dpkg --install "$BUNDLE_PATH/$pkg.deb" && apt-mark hold $pkg
    fi
done

## intalling containerd
if [ -n "$HOST_PREFIX" ]; then
    tar -C ${HOST_PREFIX}/ -xvf "$REAL_BUNDLE_PATH/containerd.tar"
else
    tar -C / -xvf "$BUNDLE_PATH/containerd.tar"
fi

## starting containerd service
$SYSTEMCTL daemon-reload && $SYSTEMCTL enable containerd && $SYSTEMCTL start containerd`

	UndoUbuntu20_4K8s1_22 = `
set -euox pipefail

# Container environment detection
HOST_PREFIX=${HOST_PREFIX:-""}
if [ -n "$HOST_PREFIX" ]; then
    # We're in a container - use nsenter for host operations
    SYSTEMCTL="nsenter -t 1 -m -u -n -i systemctl"
    MODPROBE="nsenter -t 1 -m -u -n -i modprobe"
else
    # Direct host operations
    SYSTEMCTL="systemctl"
    MODPROBE="modprobe"
fi

BUNDLE_DOWNLOAD_PATH={{.BundleDownloadPath}}
BUNDLE_ADDR={{.BundleAddrs}}
BUNDLE_PATH=$BUNDLE_DOWNLOAD_PATH/$BUNDLE_ADDR

# Adjust paths if in container
if [ -n "$HOST_PREFIX" ]; then
    REAL_BUNDLE_PATH="${HOST_PREFIX}${BUNDLE_PATH}"
else
    REAL_BUNDLE_PATH="$BUNDLE_PATH"
fi

## disabling containerd service
$SYSTEMCTL stop containerd && $SYSTEMCTL disable containerd && $SYSTEMCTL daemon-reload

## removing containerd configurations and cni plugins
if [ -n "$HOST_PREFIX" ]; then
    rm -rf ${HOST_PREFIX}/opt/cni/ && rm -rf ${HOST_PREFIX}/opt/containerd/ && tar tf "$REAL_BUNDLE_PATH/containerd.tar" | xargs -n 1 echo '/' | sed 's/ //g' | grep -e '[^/]$' | sed "s|^|${HOST_PREFIX}|" | xargs rm -f
else
    rm -rf /opt/cni/ && rm -rf /opt/containerd/ && tar tf "$BUNDLE_PATH/containerd.tar" | xargs -n 1 echo '/' | sed 's/ //g' | grep -e '[^/]$' | xargs rm -f
fi

## removing deb packages
for pkg in kubeadm kubelet kubectl kubernetes-cni cri-tools; do
    if [ -n "$HOST_PREFIX" ]; then
        nsenter -t 1 -m -u -n -i dpkg --purge $pkg
    else
        dpkg --purge $pkg
    fi
done

## removing os configuration
if [ -n "$HOST_PREFIX" ]; then
    tar tf "$REAL_BUNDLE_PATH/conf.tar" | xargs -n 1 echo '/' | sed 's/ //g' | grep -e "[^/]$" | sed "s|^|${HOST_PREFIX}|" | xargs rm -f
else
    tar tf "$BUNDLE_PATH/conf.tar" | xargs -n 1 echo '/' | sed 's/ //g' | grep -e "[^/]$" | xargs rm -f
fi

## remove kernel modules
$MODPROBE -rq overlay && $MODPROBE -r br_netfilter

## enable firewall
if command -v ufw >>/dev/null; then
    if [ -n "$HOST_PREFIX" ]; then
        nsenter -t 1 -m -u -n -i ufw enable
    else
        ufw enable
    fi
fi

## enable swap
if [ -n "$HOST_PREFIX" ]; then
    nsenter -t 1 -m -u -n -i bash -c "swapon -a && sed -ri '/\sswap\s/s/^#?//' /etc/fstab"
else
    swapon -a && sed -ri '/\sswap\s/s/^#?//' /etc/fstab
fi

rm -rf $REAL_BUNDLE_PATH`
)
