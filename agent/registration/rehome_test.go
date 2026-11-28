// Copyright 2026 Platform9, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// nolint: nolintlint,testpackage
package registration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadRehomeIdentity(t *testing.T) {
	const hostID = "byoh:host:default:w1-b4jtc"

	write := func(t *testing.T, content string) string {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, RehomeIdentityFileName), []byte(content), 0o600))
		return dir
	}

	t.Run("no file means a normal onboarding", func(t *testing.T) {
		got, err := ReadRehomeIdentity(t.TempDir(), registrarNamespace, "w1")
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("returns this host's identity", func(t *testing.T) {
		got, err := ReadRehomeIdentity(write(t, hostID+"\n"), registrarNamespace, "w1")
		require.NoError(t, err)
		assert.Equal(t, hostID, got)
	})

	for name, tc := range map[string]struct{ content, tenant, host string }{
		"another host's identity":   {hostID, registrarNamespace, "w2"},
		"another tenant's identity": {hostID, "other", "w1"},
		"not an identity":           {"kubernetes-admin", registrarNamespace, "w1"},
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			_, err := ReadRehomeIdentity(write(t, tc.content), tc.tenant, tc.host)
			assert.Error(t, err)
		})
	}

	t.Run("remove deletes the file and tolerates a missing one", func(t *testing.T) {
		dir := write(t, hostID)
		require.NoError(t, RemoveRehomeIdentity(dir))
		assert.NoFileExists(t, filepath.Join(dir, RehomeIdentityFileName))
		require.NoError(t, RemoveRehomeIdentity(dir))
	})
}
