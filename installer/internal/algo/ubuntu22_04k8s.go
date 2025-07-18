// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package algo

import (
	"context"
)

const (
	// systemdCgroupConfig is the command to enable systemd cgroup and ensure CRI plugin is enabled in containerd for Ubuntu 22.04
	systemdCgroupConfig = `sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml
# Remove any existing disabled_plugins line that contains cri
sed -i '/disabled_plugins.*cri/d' /etc/containerd/config.toml
# Add empty disabled_plugins array after version line if not present
if ! grep -q disabled_plugins /etc/containerd/config.toml; then
  sed -i '/version = 2/a disabled_plugins = []' /etc/containerd/config.toml
fi
# Add CRI plugin section if not present
if ! grep -q 'plugins.*io.containerd.grpc.v1.cri' /etc/containerd/config.toml; then
  printf '\n[plugins."io.containerd.grpc.v1.cri"]\n' >> /etc/containerd/config.toml
fi`
)

// Ubuntu22_04Installer represent the installer implementation for ubuntu22.04.* os distribution
type Ubuntu22_04Installer struct {
	*BaseUbuntuInstaller
}

// NewUbuntu22_04Installer will return new Ubuntu22_04Installer instance
func NewUbuntu22_04Installer(ctx context.Context, arch, bundleAddrs string) (*Ubuntu22_04Installer, error) {
	base, err := NewBaseUbuntuInstaller(ctx, arch, bundleAddrs, systemdCgroupConfig)
	if err != nil {
		return nil, err
	}
	return &Ubuntu22_04Installer{
		BaseUbuntuInstaller: base,
	}, nil
}

// Install will return k8s install script
func (s *Ubuntu22_04Installer) Install() string {
	return s.BaseUbuntuInstaller.Install()
}

// Uninstall will return k8s uninstall script
func (s *Ubuntu22_04Installer) Uninstall() string {
	return s.BaseUbuntuInstaller.Uninstall()
}
