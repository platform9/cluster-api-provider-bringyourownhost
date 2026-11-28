// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/platform9/cluster-api-provider-bringyourownhost/agent/registration"
)

func TestRehome(t *testing.T) {
	const hostIdentity = "byoh:host:default:w1-b4jtc"

	setup := func(t *testing.T) (byohDir, confDir, kubeconfig string) {
		byohDir, confDir = t.TempDir(), t.TempDir()
		state := `{"phase":"issued","bootstrapTokenID":"abc123","identity":"` + hostIdentity + `"}`
		require.NoError(t, os.WriteFile(filepath.Join(byohDir, registration.StateFileName), []byte(state), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(byohDir, registration.PrivateKeyFileName), []byte("old key"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(byohDir, "config"), []byte("not a kubeconfig"), 0o600))
		kubeconfig = filepath.Join(t.TempDir(), "kubeconfig.yaml")
		require.NoError(t, os.WriteFile(kubeconfig, []byte("new bootstrap kubeconfig"), 0o600))
		return byohDir, confDir, kubeconfig
	}

	t.Run("keeps the identity and swaps the credential", func(t *testing.T) {
		byohDir, confDir, kubeconfig := setup(t)
		var actions []string

		got, err := rehome(byohDir, confDir, kubeconfig, func(a string) error { actions = append(actions, a); return nil })

		require.NoError(t, err)
		assert.Equal(t, hostIdentity, got)
		assert.Equal(t, []string{"stop", "start"}, actions)

		rehomeFile, err := os.ReadFile(filepath.Join(byohDir, registration.RehomeIdentityFileName))
		require.NoError(t, err)
		assert.Equal(t, hostIdentity+"\n", string(rehomeFile))

		conf, err := os.ReadFile(filepath.Join(confDir, "bootstrap-kubeconfig.yaml"))
		require.NoError(t, err)
		assert.Equal(t, "new bootstrap kubeconfig", string(conf))

		for _, name := range []string{"config", registration.PrivateKeyFileName, registration.StateFileName} {
			assert.NoFileExists(t, filepath.Join(byohDir, name))
		}
	})

	t.Run("refuses a host with no identity and leaves the agent alone", func(t *testing.T) {
		byohDir, confDir := t.TempDir(), t.TempDir()
		called := false

		_, err := rehome(byohDir, confDir, "unused", func(string) error { called = true; return nil })

		assert.Error(t, err)
		assert.False(t, called)
	})
}
