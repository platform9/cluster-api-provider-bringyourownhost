// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package algo

import (
	"context"
)

const (
	// systemdCgroupConfig is the command to enable systemd cgroup in containerd for Ubuntu 24.04
	systemdCgroupConfig2404 = "sed -i s/SystemdCgroup\\ =\\ false/SystemdCgroup\\ =\\ true/ /etc/containerd/config.toml"
)

// Ubuntu24_04Installer represent the installer implementation for ubuntu24.04.* os distribution
type Ubuntu24_04Installer struct {
	*BaseUbuntuInstaller
}

// NewUbuntu24_04Installer will return new Ubuntu24_04Installer instance
func NewUbuntu24_04Installer(ctx context.Context, arch, bundleAddrs string) (*Ubuntu24_04Installer, error) {
	base, err := NewBaseUbuntuInstaller(ctx, arch, bundleAddrs, systemdCgroupConfig2404)
	if err != nil {
		return nil, err
	}
	return &Ubuntu24_04Installer{
		BaseUbuntuInstaller: base,
	}, nil
}

// Install will return k8s install script
func (s *Ubuntu24_04Installer) Install() string {
	return s.BaseUbuntuInstaller.Install()
}

// Uninstall will return k8s uninstall script
func (s *Ubuntu24_04Installer) Uninstall() string {
	return s.BaseUbuntuInstaller.Uninstall()
}
