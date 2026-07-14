// Copyright 2022 VMware, Inc. All Rights Reserved.
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
