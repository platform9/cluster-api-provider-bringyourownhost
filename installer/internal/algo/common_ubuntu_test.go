// SPDX-License-Identifier: Apache-2.0

package algo_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/installer/internal/algo"
)

func TestBaseUbuntuInstallerUninstallKernelModuleCleanup(t *testing.T) {
	testCases := []struct {
		name                    string
		skipKernelModuleCleanup bool
		wantModprobeLine        bool
	}{
		{
			name:                    "kernel modules unloaded when cleanup is not skipped",
			skipKernelModuleCleanup: false,
			wantModprobeLine:        true,
		},
		{
			name:                    "kernel modules left alone when cleanup is skipped",
			skipKernelModuleCleanup: true,
			wantModprobeLine:        false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			installer, err := algo.NewBaseUbuntuInstaller(context.Background(), "amd64", "test-bundle", "", tc.skipKernelModuleCleanup)
			require.NoError(t, err)

			uninstallScript := installer.Uninstall()

			hasModprobeLine := strings.Contains(uninstallScript, "modprobe -rq overlay")
			assert.Equal(t, tc.wantModprobeLine, hasModprobeLine)
		})
	}
}

// TestBaseUbuntuInstallerInstallsBundleContent guards against install.sh.tmpl drifting from what
// the bundle builder (installer/bundle_builder/build-bundle.sh) actually packages: kubeadm,
// kubelet and kubectl as raw binaries, crictl and CNI plugins as tarballs, runc as a raw binary,
// and containerd as a plain (non-deprecated) release tarball -- not apt .deb packages.
func TestBaseUbuntuInstallerInstallsBundleContent(t *testing.T) {
	installer, err := algo.NewBaseUbuntuInstaller(context.Background(), "amd64", "test-bundle", "", false)
	require.NoError(t, err)

	installScript := installer.Install()

	wantSubstrings := []string{
		`install -m 0755 "$BUNDLE_PATH/kubeadm" /usr/bin/kubeadm`,
		`install -m 0755 "$BUNDLE_PATH/kubelet" /usr/bin/kubelet`,
		`install -m 0755 "$BUNDLE_PATH/kubectl" /usr/bin/kubectl`,
		`tar -C /usr/local/bin -xvf "$BUNDLE_PATH/crictl.tar.gz"`,
		`tar -C /opt/cni/bin -xvf "$BUNDLE_PATH/cni-plugins.tgz"`,
		`install -m 0755 "$BUNDLE_PATH/runc" /usr/local/sbin/runc`,
		`tar -C /usr/local -xvf "$BUNDLE_PATH/containerd.tar.gz"`,
	}
	for _, want := range wantSubstrings {
		assert.Contains(t, installScript, want)
	}

	unwantedSubstrings := []string{"dpkg --install", ".deb"}
	for _, unwanted := range unwantedSubstrings {
		assert.NotContains(t, installScript, unwanted)
	}
}

// TestBaseUbuntuInstallerUninstallsBundleContent is the removal-side counterpart of
// TestBaseUbuntuInstallerInstallsBundleContent -- every path install.sh.tmpl creates must have a
// matching removal in uninstall.sh.tmpl.
func TestBaseUbuntuInstallerUninstallsBundleContent(t *testing.T) {
	installer, err := algo.NewBaseUbuntuInstaller(context.Background(), "amd64", "test-bundle", "", false)
	require.NoError(t, err)

	uninstallScript := installer.Uninstall()

	wantSubstrings := []string{
		"rm -f /usr/bin/kubeadm /usr/bin/kubelet /usr/bin/kubectl",
		"rm -f /usr/local/sbin/runc",
		`tar tzf "$BUNDLE_PATH/crictl.tar.gz"`,
		`tar tzf "$BUNDLE_PATH/containerd.tar.gz"`,
		"rm -rf /opt/cni/",
	}
	for _, want := range wantSubstrings {
		assert.Contains(t, uninstallScript, want)
	}

	unwantedSubstrings := []string{"dpkg --purge", "dpkg -l"}
	for _, unwanted := range unwantedSubstrings {
		assert.NotContains(t, uninstallScript, unwanted)
	}
}
