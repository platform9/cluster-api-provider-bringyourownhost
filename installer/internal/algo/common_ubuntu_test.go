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

// TestBaseUbuntuInstallerInstallDoesNotSwallowFailuresViaAnd guards against a
// regression where a step is written as "cmd-that-can-fail && next-cmd": under
// `set -e`, a command on the left-hand side of && is treated as a conditional,
// not a statement, so its failure is silently swallowed and the script keeps
// running.
func TestBaseUbuntuInstallerInstallDoesNotSwallowFailuresViaAnd(t *testing.T) {
	installer, err := algo.NewBaseUbuntuInstaller(context.Background(), "amd64", "test-bundle", "", false)
	require.NoError(t, err)

	installScript := installer.Install()

	// Denylist of known-fixed chains, not a blanket "no &&" regex: && and ||
	// are fine when the left side's failure is meant to be non-fatal (e.g.
	// "cmd || true") or it's the last command in the chain.
	swallowedFailurePatterns := []string{
		"swapoff -a &&",
		"modprobe overlay &&",
		`tar -C / -xvf "$BUNDLE_PATH/conf.tar" &&`,
		`dpkg --install "$BUNDLE_PATH/$pkg.deb" &&`,
		"systemctl daemon-reload &&",
	}
	for _, pattern := range swallowedFailurePatterns {
		assert.NotContains(t, installScript, pattern)
	}

	assert.Contains(t, installScript, "dpkg --install \"$BUNDLE_PATH/$pkg.deb\"\n    apt-mark hold $pkg")
}
