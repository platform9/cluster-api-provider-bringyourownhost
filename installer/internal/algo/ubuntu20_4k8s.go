// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package algo

import (
	"context"
)

// Ubuntu20_04Installer represent the installer implementation for ubuntu20.04.* os distribution
type Ubuntu20_04Installer struct {
	*BaseUbuntuInstaller
}

// NewUbuntu20_04Installer will return new Ubuntu20_04Installer instance
func NewUbuntu20_04Installer(ctx context.Context, arch, bundleAddrs string) (*Ubuntu20_04Installer, error) {
	base, err := NewBaseUbuntuInstaller(ctx, arch, bundleAddrs, "") // No special containerd config needed for 20.04
	if err != nil {
		return nil, err
	}
	return &Ubuntu20_04Installer{
		BaseUbuntuInstaller: base,
	}, nil
}

// Install will return k8s install script
func (s *Ubuntu20_04Installer) Install() string {
	return s.BaseUbuntuInstaller.Install()
}

// Uninstall will return k8s uninstall script
func (s *Ubuntu20_04Installer) Uninstall() string {
	return s.BaseUbuntuInstaller.Uninstall()
}
