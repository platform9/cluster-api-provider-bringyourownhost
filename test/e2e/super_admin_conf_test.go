// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: testpackage
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/yaml"
)

// superAdminConfPath is the path hardcoded in the templates. The test replaces
// it with a temporary path.
const superAdminConfPath = "/etc/kubernetes/super-admin.conf"

// templatesWithSuperAdminCommand lists the templates that ship the command,
// including the generated one.
var templatesWithSuperAdminCommand = []string{
	"data/infrastructure-provider-byoh/v1beta1/templates/e2e/cluster-with-kcp.yaml",
	"data/infrastructure-provider-byoh/v1beta1/templates/e2e/clusterclass-quickstart.yaml",
	"data/infrastructure-provider-byoh/v1beta1/templates/e2e/cluster-template.yaml",
}

// postKubeadmCommand returns the only postKubeadmCommands entry in a
// multi-document template, so the test runs the command that ships.
func postKubeadmCommand(t *testing.T, path string) string {
	t.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	for doc := range strings.SplitSeq(string(raw), "\n---\n") {
		if strings.TrimSpace(doc) == "" {
			continue
		}

		var parsed map[string]any
		if err := yaml.Unmarshal([]byte(doc), &parsed); err != nil {
			continue
		}

		cmds := findPostKubeadmCommands(parsed)
		if len(cmds) > 0 {
			require.Len(t, cmds, 1, "expected exactly one postKubeadmCommands entry in %s", path)
			return cmds[0]
		}
	}

	t.Fatalf("no postKubeadmCommands found in %s", path)
	return ""
}

// findPostKubeadmCommands returns the first postKubeadmCommands list in a
// decoded document. The key sits at a different depth in each template.
func findPostKubeadmCommands(node any) []string {
	switch typed := node.(type) {
	case map[string]any:
		if raw, ok := typed["postKubeadmCommands"]; ok {
			if list, ok := raw.([]any); ok {
				cmds := make([]string, 0, len(list))
				for _, item := range list {
					if s, ok := item.(string); ok {
						cmds = append(cmds, s)
					}
				}
				return cmds
			}
		}
		for _, v := range typed {
			if found := findPostKubeadmCommands(v); len(found) > 0 {
				return found
			}
		}
	case []any:
		for _, v := range typed {
			if found := findPostKubeadmCommands(v); len(found) > 0 {
				return found
			}
		}
	}
	return nil
}

// stubKubeadm returns a directory holding a fake kubeadm that writes a
// super-admin.conf into the directory given by --kubeconfig-dir.
func stubKubeadm(t *testing.T) string {
	t.Helper()

	binDir := t.TempDir()
	script := `#!/usr/bin/env bash
set -Eeuo pipefail
dir=""
while [[ $# -gt 0 ]]; do
  if [[ "$1" == "--kubeconfig-dir" ]]; then
    dir="$2"
    shift
  fi
  shift
done
printf 'apiVersion: v1\nkind: Config\n' > "$dir/super-admin.conf"
`
	path := filepath.Join(binDir, "kubeadm")
	err := os.WriteFile(path, []byte(script), 0o755)
	require.NoError(t, err)

	return binDir
}

// linkProbe hard-links path to a sibling and returns the sibling's path. The
// extra link keeps the original inode alive, so a command that unlinks and
// recreates the path cannot be handed the same inode number back.
func linkProbe(t *testing.T, path string) string {
	t.Helper()

	probe := path + ".probe"
	err := os.Link(path, probe)
	require.NoError(t, err)

	return probe
}

// TestPostKubeadmSuperAdminCommand runs the shipped command against each state
// the path can be in. The command must never unlink an existing file, because
// kube-vip bind-mounts it, and must leave a file only the owner can read.
func TestPostKubeadmSuperAdminCommand(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		// setup creates the initial state and reports whether it left a regular
		// file for the probe to link to.
		setup          func(t *testing.T, conf string) bool
		wantInPlace    bool
		wantRegenerate bool
	}{
		{
			name: "missing path is created",
			setup: func(_ *testing.T, _ string) bool {
				return false
			},
			wantRegenerate: true,
		},
		{
			name: "empty placeholder is filled in place",
			setup: func(t *testing.T, conf string) bool {
				t.Helper()
				err := os.WriteFile(conf, nil, 0o644)
				require.NoError(t, err)
				return true
			},
			wantInPlace:    true,
			wantRegenerate: true,
		},
		{
			name: "populated file is left alone",
			setup: func(t *testing.T, conf string) bool {
				t.Helper()
				err := os.WriteFile(conf, []byte("existing-kubeconfig\n"), 0o600)
				require.NoError(t, err)
				return true
			},
			wantInPlace:    true,
			wantRegenerate: false,
		},
		{
			name: "directory left by an older build is replaced",
			setup: func(t *testing.T, conf string) bool {
				t.Helper()
				err := os.Mkdir(conf, 0o755)
				require.NoError(t, err)
				return false
			},
			wantRegenerate: true,
		},
	}

	for _, template := range templatesWithSuperAdminCommand {
		command := postKubeadmCommand(t, template)

		for _, tc := range testCases {
			t.Run(filepath.Base(template)+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				dir := t.TempDir()
				conf := filepath.Join(dir, "super-admin.conf")
				hadFile := tc.setup(t, conf)

				var probe string
				if hadFile {
					probe = linkProbe(t, conf)
				}

				scoped := strings.ReplaceAll(command, superAdminConfPath, conf)
				cmd := exec.CommandContext(t.Context(), "/bin/bash", "-c", scoped)
				cmd.Env = append(os.Environ(), fmt.Sprintf("PATH=%s:%s", stubKubeadm(t), os.Getenv("PATH")))

				out, err := cmd.CombinedOutput()
				require.NoError(t, err, "command failed: %s", out)

				info, err := os.Stat(conf)
				require.NoError(t, err)
				assert.True(t, info.Mode().IsRegular(), "super-admin.conf must be a regular file")
				assert.NotZero(t, info.Size(), "super-admin.conf must not be empty")

				if tc.wantInPlace {
					probeInfo, probeErr := os.Stat(probe)
					require.NoError(t, probeErr)
					assert.True(t, os.SameFile(info, probeInfo),
						"the path was unlinked and recreated, which breaks kube-vip's bind mount")
				}

				contents, err := os.ReadFile(conf)
				require.NoError(t, err)
				if tc.wantRegenerate {
					assert.Contains(t, string(contents), "kind: Config", "expected a regenerated kubeconfig")
					assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "regenerated kubeconfig must be owner-only")
				} else {
					assert.Equal(t, "existing-kubeconfig\n", string(contents), "existing kubeconfig must be preserved")
				}
			})
		}
	}
}
