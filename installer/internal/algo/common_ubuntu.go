// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package algo

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
)

const (
	// ImgpkgVersion defines the imgpkg version that will be installed on host if imgpkg is not already installed
	ImgpkgVersion = "v0.36.4"
)

// BaseUbuntuInstaller provides common functionality for Ubuntu installers
type BaseUbuntuInstaller struct {
	install   string
	uninstall string
}

// Install will return k8s install script
func (s *BaseUbuntuInstaller) Install() string {
	return s.install
}

// Uninstall will return k8s uninstall script
func (s *BaseUbuntuInstaller) Uninstall() string {
	return s.uninstall
}

// NewBaseUbuntuInstaller creates a new base Ubuntu installer
func NewBaseUbuntuInstaller(ctx context.Context, arch, bundleAddrs string, containerdConfig string) (*BaseUbuntuInstaller, error) {
	data := map[string]string{
		"BundleAddrs":        bundleAddrs,
		"Arch":               arch,
		"ImgpkgVersion":      ImgpkgVersion,
		"BundleDownloadPath": "{{.BundleDownloadPath}}",
		"ContainerdConfig":   containerdConfig,
	}

	parseFn := func(script string) (string, error) {
		parser, err := template.New("parser").Parse(script)
		if err != nil {
			return "", fmt.Errorf("unable to parse install script")
		}
		var tpl bytes.Buffer
		if err = parser.Execute(&tpl, data); err != nil {
			return "", fmt.Errorf("unable to apply install parsed template to the data object")
		}
		return tpl.String(), nil
	}

	install, err := parseFn(commonUbuntuInstallTemplate)
	if err != nil {
		return nil, err
	}
	uninstall, err := parseFn(commonUbuntuUninstallTemplate)
	if err != nil {
		return nil, err
	}
	return &BaseUbuntuInstaller{
		install:   install,
		uninstall: uninstall,
	}, nil
}

// Common installation template for Ubuntu-based systems
var commonUbuntuInstallTemplate = `
set -euox pipefail

BUNDLE_DOWNLOAD_PATH={{.BundleDownloadPath}}
BUNDLE_ADDR={{.BundleAddrs}}
IMGPKG_VERSION={{.ImgpkgVersion}}
ARCH={{.Arch}}
BUNDLE_PATH=$BUNDLE_DOWNLOAD_PATH/$BUNDLE_ADDR

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
    mv /tmp/imgpkg /usr/local/bin/imgpkg
    chmod +x /usr/local/bin/imgpkg
fi

echo "downloading bundle"
mkdir -p $BUNDLE_PATH
imgpkg pull -i $BUNDLE_ADDR -o $BUNDLE_PATH

## disable swap
swapoff -a && sed -ri '/\sswap\s/s/^#?/#/' /etc/fstab

## disable firewall
if command -v ufw >>/dev/null; then
    ufw disable
fi

## load kernal modules
modprobe overlay && modprobe br_netfilter

## adding os configuration
tar -C / -xvf "$BUNDLE_PATH/conf.tar" && sysctl --system 

## installing deb packages
for pkg in cri-tools kubernetes-cni kubectl kubelet kubeadm; do
    dpkg --install "$BUNDLE_PATH/$pkg.deb" && apt-mark hold $pkg
done

## intalling containerd
tar -C / -xvf "$BUNDLE_PATH/containerd.tar"
mkdir -p /etc/containerd
containerd config default > /etc/containerd/config.toml
{{.ContainerdConfig}}

## starting containerd service
systemctl daemon-reload && systemctl enable containerd && systemctl start containerd

echo "Installation complete!"
`

// Common uninstallation template for Ubuntu-based systems
var commonUbuntuUninstallTemplate = `
set -euox pipefail

BUNDLE_DOWNLOAD_PATH={{.BundleDownloadPath}}
BUNDLE_ADDR={{.BundleAddrs}}
BUNDLE_PATH=$BUNDLE_DOWNLOAD_PATH/$BUNDLE_ADDR

## disabling containerd service
systemctl stop containerd && systemctl disable containerd && systemctl daemon-reload

## removing containerd configurations and cni plugins
rm -rf /opt/cni/ && rm -rf /opt/containerd/ &&  tar tf "$BUNDLE_PATH/containerd.tar" | xargs -n 1 echo '/' | sed 's/ //g'  | grep -e '[^/]$' | xargs rm -f

## removing deb packages
for pkg in kubeadm kubelet kubectl kubernetes-cni cri-tools; do
    dpkg --purge $pkg
done

## removing os configuration
tar tf "$BUNDLE_PATH/conf.tar" | xargs -n 1 echo '/' | sed 's/ //g' | grep -e "[^/]$" | xargs rm -f

## remove kernal modules
modprobe -rq overlay && modprobe -r br_netfilter

## enable firewall
if command -v ufw >>/dev/null; then
    ufw enable
fi

## enable swap
swapon -a && sed -ri '/\sswap\s/s/^#?//' /etc/fstab

rm -rf $BUNDLE_PATH
`
