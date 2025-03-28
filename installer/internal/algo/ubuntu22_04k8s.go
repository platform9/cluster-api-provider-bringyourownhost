// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package algo

import (
	"context"
)

const (
	// systemdCgroupConfig is the command to enable systemd cgroup in containerd for Ubuntu 22.04
	systemdCgroupConfig = "sed -i s/SystemdCgroup\\ =\\ false/SystemdCgroup\\ =\\ true/ /etc/containerd/config.toml"
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
