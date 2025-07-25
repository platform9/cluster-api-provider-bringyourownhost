// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package algo

import (
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"strings"
)

const (
	// ImgpkgVersion defines the imgpkg version that will be installed on host if imgpkg is not already installed
	ImgpkgVersion = "v0.36.4"
)

//go:embed ubuntu-templates/install.sh.tmpl
var commonUbuntuInstallTemplate string

//go:embed ubuntu-templates/uninstall.sh.tmpl
var commonUbuntuUninstallTemplate string

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
func NewBaseUbuntuInstaller(ctx context.Context, arch, bundleAddrs, containerdConfig string) (*BaseUbuntuInstaller, error) {
	// Validate embedded templates
	if commonUbuntuInstallTemplate == "" {
		return nil, fmt.Errorf("install template is empty - template file may be missing")
	}
	if commonUbuntuUninstallTemplate == "" {
		return nil, fmt.Errorf("uninstall template is empty - template file may be missing")
	}

	data := map[string]string{
		"BundleAddrs":        bundleAddrs,
		"Arch":               arch,
		"ImgpkgVersion":      ImgpkgVersion,
		"ContainerdConfig":   containerdConfig,
		"BundleDownloadPath": "/var/lib/byoh/bundles",
	}

	// Parse and validate templates
	installTemplate, err := template.New("install").Parse(commonUbuntuInstallTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to parse install template: %v", err)
	}

	uninstallTemplate, err := template.New("uninstall").Parse(commonUbuntuUninstallTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to parse uninstall template: %v", err)
	}

	var install, uninstall string
	var buf strings.Builder

	if err := installTemplate.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("failed to execute install template: %v", err)
	}
	install = buf.String()

	buf.Reset()
	if err := uninstallTemplate.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("failed to execute uninstall template: %v", err)
	}
	uninstall = buf.String()

	return &BaseUbuntuInstaller{
		install:   install,
		uninstall: uninstall,
	}, nil
}
