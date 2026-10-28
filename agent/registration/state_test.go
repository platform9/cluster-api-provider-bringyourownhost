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

func TestState_SaveAndLoad(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "nested", ".byoh")
	want := &state{
		Version:          stateVersion,
		Phase:            phaseIssued,
		BootstrapTokenID: "abcdef",
		Identity:         "byoh:host:example-worker-1:q4m9z",
		CSRName:          "byoh-csr-example-worker-1-k3v8d",
		CSRUID:           "4b6c",
	}

	err := want.save(dir)
	require.NoError(t, err)

	got, err := loadState(dir)
	require.NoError(t, err)
	assert.Equal(t, want, got)

	info, err := os.Stat(filepath.Join(dir, StateFileName))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// An atomic write renames its temporary file, so only the state file is left.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	assert.Equal(t, []string{StateFileName}, names)
}

func TestState_SaveReplacesExistingFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first := &state{Version: stateVersion, Phase: phasePending, BootstrapTokenID: "abcdef", Identity: "first"}
	second := &state{Version: stateVersion, Phase: phasePending, BootstrapTokenID: "abcdef", Identity: "second"}

	require.NoError(t, first.save(dir))
	require.NoError(t, second.save(dir))

	got, err := loadState(dir)
	require.NoError(t, err)
	assert.Equal(t, second, got)
}

func TestLoadState_Errors(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		contents *string
	}{
		{
			name:     "missing file",
			contents: nil,
		},
		{
			name:     "invalid JSON",
			contents: new(`{"version": 1,`),
		},
		{
			name:     "unknown version",
			contents: new(`{"version": 2, "bootstrapTokenID": "abcdef"}`),
		},
		{
			name:     "no version",
			contents: new(`{"bootstrapTokenID": "abcdef"}`),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			if tc.contents != nil {
				err := os.WriteFile(filepath.Join(dir, StateFileName), []byte(*tc.contents), 0o600)
				require.NoError(t, err)
			}

			got, err := loadState(dir)
			require.Error(t, err)
			assert.Nil(t, got)
		})
	}
}

func TestRemoveOnboardingFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	config := filepath.Join(dir, "config")
	for _, name := range []string{StateFileName, PrivateKeyFileName, "config"} {
		err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600)
		require.NoError(t, err)
	}

	err := removeOnboardingFiles(dir)
	require.NoError(t, err)

	assert.NoFileExists(t, filepath.Join(dir, StateFileName))
	assert.NoFileExists(t, filepath.Join(dir, PrivateKeyFileName))
	// The final kubeconfig is not part of the onboarding files.
	assert.FileExists(t, config)

	// Files that are already gone are not an error.
	err = removeOnboardingFiles(dir)
	assert.NoError(t, err)
}

func TestLoadState_Phase(t *testing.T) {
	t.Parallel()

	for _, phase := range []phase{phasePending, phaseIssued} {
		t.Run(string(phase)+" is accepted", func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			want := &state{Version: stateVersion, Phase: phase, BootstrapTokenID: "abcdef"}
			require.NoError(t, want.save(dir))

			got, err := loadState(dir)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}

	testCases := []struct {
		name     string
		contents string
	}{
		{
			name:     "empty phase is rejected",
			contents: `{"version": 1, "phase": "", "bootstrapTokenID": "abcdef"}`,
		},
		{
			name:     "unknown phase is rejected",
			contents: `{"version": 1, "phase": "done", "bootstrapTokenID": "abcdef"}`,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			err := os.WriteFile(filepath.Join(dir, StateFileName), []byte(tc.contents), 0o600)
			require.NoError(t, err)

			got, err := loadState(dir)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "unknown phase")
			assert.Nil(t, got)
		})
	}
}

func TestState_MarkIssued(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	st := &state{
		Version:          stateVersion,
		Phase:            phasePending,
		BootstrapTokenID: "abcdef",
		Identity:         "byoh:host:example-worker-1:q4m9z",
		CSRName:          "byoh-csr-example-worker-1-k3v8d",
		CSRUID:           "4b6c",
	}

	err := st.markIssued(dir)
	require.NoError(t, err)

	// The CSR the agent created stays named, even though it may be gone from the cluster.
	want := &state{
		Version:          stateVersion,
		Phase:            phaseIssued,
		BootstrapTokenID: "abcdef",
		Identity:         "byoh:host:example-worker-1:q4m9z",
		CSRName:          "byoh-csr-example-worker-1-k3v8d",
		CSRUID:           "4b6c",
	}
	assert.Equal(t, want, st)
	reloaded, err := loadState(dir)
	require.NoError(t, err)
	assert.Equal(t, want, reloaded)
}
