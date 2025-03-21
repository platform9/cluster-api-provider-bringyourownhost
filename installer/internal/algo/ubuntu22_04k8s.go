// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package algo

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
)

// Ubuntu22_04Installer represent the installer implementation for ubuntu22.04.* os distribution
type Ubuntu22_04Installer struct {
	install   string
	uninstall string
}

// NewUbuntu22_04Installer will return new Ubuntu22_04Installer instance
func NewUbuntu22_04Installer(ctx context.Context, arch, bundleAddrs string) (*Ubuntu22_04Installer, error) {
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

	install, err := parseFn(DoUbuntu22_04K8s)
	if err != nil {
		return nil, err
	}
	uninstall, err := parseFn(UndoUbuntu22_04K8s)
	if err != nil {
		return nil, err
	}
	return &Ubuntu22_04Installer{
		install:   install,
		uninstall: uninstall,
	}, nil
}

// Install will return k8s install script
func (s *Ubuntu22_04Installer) Install() string {
	return s.install
}

// Uninstall will return k8s uninstall script
func (s *Ubuntu22_04Installer) Uninstall() string {
	return s.uninstall
}

// contains the installation and uninstallation steps for Ubuntu 22.04 and k8s
var (
	DoUbuntu22_04K8s = `
set -euox pipefail

BUNDLE_DOWNLOAD_PATH={{.BundleDownloadPath}}
BUNDLE_ADDR={{.BundleAddrs}}
IMGPKG_VERSION={{.ImgpkgVersion}}
ARCH={{.Arch}}
BUNDLE_PATH=$BUNDLE_DOWNLOAD_PATH/$BUNDLE_ADDR

# Ensure we're using legacy iptables for compatibility
echo "Configuring iptables..."
update-alternatives --set iptables /usr/sbin/iptables-legacy
update-alternatives --set ip6tables /usr/sbin/ip6tables-legacy

if ! command -v imgpkg >>/dev/null; then
	echo "installing imgpkg"	
	
	if command -v wget >>/dev/null; then
		dl_bin="wget -nv -O-"
	elif command -v curl >>/dev/null; then
		dl_bin="curl -s -L"
	else
		echo "installing curl"
		apt-get update && apt-get install -y curl
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

## disable firewall and ensure iptables is not blocked
if command -v ufw >>/dev/null; then
	ufw disable
fi

## load kernel modules and configure sysctl
echo "Configuring kernel modules and parameters..."
modprobe overlay
modprobe br_netfilter

cat > /etc/sysctl.d/99-kubernetes.conf <<EOF
net.bridge.bridge-nf-call-iptables = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward = 1
net.ipv4.conf.all.forwarding = 1
EOF

sysctl --system

## adding os configuration
tar -C / -xvf "$BUNDLE_PATH/conf.tar"

## installing deb packages
for pkg in cri-tools kubernetes-cni kubectl kubelet kubeadm; do
	dpkg --install "$BUNDLE_PATH/$pkg.deb" && apt-mark hold $pkg
done

## installing containerd with proper configuration
echo "Installing and configuring containerd..."
tar -C / -xvf "$BUNDLE_PATH/containerd.tar"
mkdir -p /etc/containerd
containerd config default > /etc/containerd/config.toml
sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml

## starting containerd service
systemctl daemon-reload 
systemctl enable containerd 
systemctl restart containerd

## Update CNI config to use node IP instead of service IP
echo "Configuring CNI..."
NODE_IP=$(hostname -I | awk '{print $1}')
API_PORT=6443

# Update all kubeconfig files that might be using the service IP
find /etc/cni/net.d/ -type f -name "*.conflist" -o -name "*.conf" -o -name "*kubeconfig*" | while read -r file; do
    if [ -f "$file" ]; then
        echo "Updating CNI config: $file"
        sed -i "s|server: https://10.96.0.1:443|server: https://${NODE_IP}:${API_PORT}|g" "$file"
    fi
done

## Clean up any stale CNI resources
rm -f /var/lib/cni/networks/k8s-pod-network/* || true
ip link show | grep -o 'cali[^@]*' | while read -r interface; do
    ip link delete "$interface" || true
done

echo "Installation complete!"
`

	UndoUbuntu22_04K8s = `
set -euox pipefail

BUNDLE_DOWNLOAD_PATH={{.BundleDownloadPath}}
BUNDLE_ADDR={{.BundleAddrs}}
BUNDLE_PATH=$BUNDLE_DOWNLOAD_PATH/$BUNDLE_ADDR

## disabling containerd service
systemctl stop containerd && systemctl disable containerd && systemctl daemon-reload

## removing containerd configurations and cni plugins
rm -rf /opt/cni/ && rm -rf /opt/containerd/ && rm -f /etc/containerd/config.toml
tar tf "$BUNDLE_PATH/containerd.tar" | xargs -n 1 echo '/' | sed 's/ //g'  | grep -e '[^/]$' | xargs rm -f

## removing deb packages
for pkg in kubeadm kubelet kubectl kubernetes-cni cri-tools; do
	dpkg --purge $pkg
done

## removing os configuration
tar tf "$BUNDLE_PATH/conf.tar" | xargs -n 1 echo '/' | sed 's/ //g' | grep -e "[^/]$" | xargs rm -f
rm -f /etc/sysctl.d/99-kubernetes.conf

## remove kernel modules
modprobe -rq overlay && modprobe -r br_netfilter

## enable firewall
if command -v ufw >>/dev/null; then
	ufw enable
fi

## enable swap
swapon -a && sed -ri '/\sswap\s/s/^#?//' /etc/fstab

## Reset iptables to system default if needed
if command -v update-alternatives >>/dev/null; then
    update-alternatives --auto iptables
    update-alternatives --auto ip6tables
fi

rm -rf $BUNDLE_PATH`
)
