// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package algo

import (
	"context"
)

const (
	// systemdCgroupConfig contains containerd configuration fixes for Ubuntu 22.04.
	// 1. Enable systemd cgroup.
	// 2. Remove "cri" from disabled_plugins list so that the CRI plugin is enabled.
	// 3. Add a basic CRI plugin configuration block if it is missing.
	systemdCgroupConfig = `# Enable systemd cgroup in containerd
sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml
# Remove cri from disabled_plugins list if present
sed -i '/disabled_plugins/s/"cri",*//g' /etc/containerd/config.toml
# Add CRI plugin configuration block if not present
grep -q '\[plugins."io.containerd.grpc.v1.cri"\]' /etc/containerd/config.toml || cat <<'EOF' >> /etc/containerd/config.toml
[plugins."io.containerd.grpc.v1.cri"]
  sandbox_image = "registry.k8s.io/pause:3.9"
EOF`
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
